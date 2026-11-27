package main

// The cache as a whole: CacheManager, which looks a request up in memory
// and then on disk, and stores a response in both as it streams to the
// client. The tiers are in cache_memory.go and cache_disk.go, and the
// RFC 9111 rules that decide what may be stored or reused in
// cache_policy.go.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// CacheManager manages memory and disk caches for go-polipo.
type CacheManager struct {
	config   *Config
	mu       sync.RWMutex // guards disk cache file I/O only; memCache is self-synchronized
	memCache *lru.Cache[string, *memEntry]

	// diskSize is the disk cache's total size, once diskSizeKnown; trimming
	// is set while trimDisk runs in the background. See cache_size.go.
	diskSize      atomic.Int64
	diskSizeKnown atomic.Bool
	trimming      atomic.Bool
}

// NewCacheManager creates a CacheManager instance.
func NewCacheManager(config *Config) *CacheManager {
	cm := &CacheManager{config: config}
	if config.MemoryCacheEntries > 0 {
		// New only errors for a non-positive size, already excluded above.
		cm.memCache, _ = lru.New[string, *memEntry](config.MemoryCacheEntries)
	}
	return cm
}

// Invalidate removes whatever is cached for rawURL, from memory and disk.
func (cm *CacheManager) Invalidate(rawURL string) {
	if cm.memCache != nil {
		cm.memCache.Remove(rawURL)
	}
	if cm.isDiskCacheDisabled() {
		return
	}
	path, err := cm.CachePath(rawURL)
	if err != nil {
		return
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	size := fileSize(path)
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf(cm.config.LogLevel, LogWarn, "Cache invalidate warning: %s: couldn't remove %s: %v", rawURL, path, err)
		}
		return
	}
	cm.diskSize.Add(-size)
}

// Lookup retrieves a cached response for the request if it exists and is fresh.
func (cm *CacheManager) Lookup(req *http.Request) *http.Response {
	resp, stale := cm.lookupEntry(req)
	if resp == nil {
		return nil
	}
	if stale {
		// A stale entry is a miss as far as Lookup is concerned; the caller
		// (ProxyHandler's rewrite/ModifyResponse) is what turns this into a
		// conditional GET and merges a 304 back into the entry - see
		// addConditionalHeaders and reviveFromCache in proxy.go.
		_ = resp.Body.Close()
		return nil
	}
	return resp
}

// LookupStale retrieves a cached response regardless of freshness, reporting
// whether it is expired. It backs relaxTransparency and proxyOffline, which
// serve a stale entry rather than contacting (or, for proxyOffline, ever
// attempting to contact) the origin. resp is nil when there is no usable
// entry at all, in which case stale is meaningless.
func (cm *CacheManager) LookupStale(req *http.Request) (resp *http.Response, stale bool) {
	return cm.lookupEntry(req)
}

// lookupEntry does the actual cache read shared by Lookup and LookupStale: the
// memory cache first, then disk.
func (cm *CacheManager) lookupEntry(req *http.Request) (resp *http.Response, stale bool) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil, false
	}

	// Cache-Control checks
	if cc := parseCacheControl(req.Header); cc.has("no-cache") || cc.has("no-store") || hasPragmaNoCache(req.Header) {
		return nil, false
	}

	key := req.URL.String()

	// Checked first, and independently of isDiskCacheDisabled: MemoryCacheEntries
	// is meant to work the same whether or not the disk cache is enabled.
	if cm.memCache != nil {
		if entry, ok := cm.memCache.Get(key); ok {
			cm.noteMemoryAccess(key, entry)
			resp := entry.response(req)
			stale := cm.isStaleFor(req, resp, entry.birth)
			setAge(resp, entry.birth)
			return resp, stale
		}
	}

	if cm.isDiskCacheDisabled() {
		return nil, false
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	resp, birth := cm.lookupDisk(req, key)
	if resp == nil {
		return nil, false
	}

	// A response carrying Vary is always reported stale, regardless of its
	// actual HTTP freshness: the cache key is the URL alone, so this entry
	// cannot be matched against the current request's headers, and only a
	// round-trip to the origin (a revalidation, or a full re-fetch when
	// there is no usable validator - see addConditionalHeaders) can settle
	// which variant is correct. See responseVaries. A no-cache response is
	// always stale for the same reason: only the origin may approve its
	// reuse. See responseHasNoCache.
	stale = cm.isStaleFor(req, resp, birth)

	// Copy the entry into the memory cache, so that later hits skip the
	// disk - otherwise the memory cache would only ever hold what was
	// fetched since the last restart. Nothing needs demoting when the LRU
	// evicts it again: the disk copy stays where it is. A HEAD response
	// carries no body to copy.
	if req.Method == http.MethodGet {
		cm.promote(key, resp, birth)
	}

	setAge(resp, birth)
	return resp, stale
}

// Store arranges for a cacheable response to be written to the disk and/or
// memory cache. It does not read the body itself: it replaces resp.Body with a
// reader that passes the body through to the caller unchanged while copying
// it into the cache, so the client receives each byte as soon as the origin
// sends it rather than after the whole body has arrived - as Polipo did. The
// entry is committed only when the body has been read to EOF; if reading
// stops early (the client went away, or the origin's connection broke), the
// partial copy is discarded. Non-cacheable responses are left untouched.
func (cm *CacheManager) Store(req *http.Request, resp *http.Response) {
	if req.Method != http.MethodGet || resp.StatusCode != http.StatusOK {
		return
	}

	// Cache-Control checks
	cc := parseCacheControl(resp.Header)
	if cc.has("no-store") || cc.has("private") {
		return
	}
	// A request's no-store forbids storing the response to it too (RFC 9111
	// section 5.2.1.5).
	if parseCacheControl(req.Header).has("no-store") {
		return
	}

	// go-polipo's cache key is the URL alone, so it cannot store more than one
	// variant per URL or pick between them - a Vary response is still written
	// (matching Polipo), but lookupEntry always reports it stale, so it is
	// never handed to a later client without first revalidating (or, absent a
	// usable validator, fully re-fetching) with the origin. See responseVaries.

	// A Set-Cookie response is meant for a single client; a cached copy would be
	// replayed to everyone who later requests the same URL. Polipo cached these,
	// so this is opt-out via dontCacheCookies.
	if cm.config.DontCacheCookies && len(resp.Header[headerSetCookie]) > 0 {
		return
	}

	// RFC 9111 section 3.5 (RFC 2616 section 14.8): a shared cache must not
	// reuse a response to a request carrying Authorization unless the response
	// explicitly permits it.
	if isRequestAuthenticated(req) && !cc.allowsAuthenticated() {
		return
	}

	diskEnabled := !cm.isDiskCacheDisabled()
	memEnabled := cm.memCache != nil
	if !diskEnabled && !memEnabled {
		return
	}

	// maxDiskCacheEntrySize / memoryCacheMaxEntrySize: reject known-oversized
	// responses up front - matching Polipo's makeDiskEntry, which makes this
	// same check against the declared Content-Length (its object->length)
	// before deciding to store. The response is still forwarded to the client
	// untouched either way; only caching is skipped. The two destinations have
	// independent ceilings, so a response can be too big for one and still fit
	// the other. A body that turns out larger than declared (or had no
	// Content-Length) is caught while it streams, in cacheBody.Read.
	maxDiskSize := cm.effectiveMaxDiskEntrySize()
	diskWants := diskEnabled && (maxDiskSize < 0 || resp.ContentLength < 0 || resp.ContentLength <= maxDiskSize)
	maxMemSize := cm.config.MemoryCacheMaxEntrySize
	memWants := memEnabled && (maxMemSize < 0 || resp.ContentLength < 0 || resp.ContentLength <= maxMemSize)
	if !diskWants && !memWants {
		return
	}

	requestTime, ok := req.Context().Value(requestTimeKey{}).(time.Time)
	if !ok {
		requestTime = time.Now()
	}
	birth := responseBirth(resp.Header, requestTime)

	b := &cacheBody{cm: cm, src: resp.Body, key: req.URL.String()}
	if diskWants {
		b.disk = cm.startDiskWrite(req, resp, birth)
	}
	if memWants {
		// Snapshot the headers now: the caller may change resp.Header while
		// the body is still streaming.
		b.mem = newMemEntry(resp, nil, birth)
		b.memBuf = new(bytes.Buffer)
	}
	if b.disk == nil && b.mem == nil {
		return
	}
	resp.Body = b
}

var (
	errCacheWriteStopped = fmt.Errorf("cache file writer stopped")
	errCacheBodyAborted  = fmt.Errorf("response body not read to the end")
	errCacheTooLarge     = fmt.Errorf("response body larger than maxDiskCacheEntrySize")
)

// cacheBody is the response body Store hands back: reads pass through from
// src, and each chunk is also copied to the disk and/or memory cache entry
// being built. The entry is committed when src reports EOF.
type cacheBody struct {
	cm  *CacheManager
	src io.ReadCloser
	key string

	disk   *diskWrite    // nil once dropped or finished
	mem    *memEntry     // nil once dropped or finished
	memBuf *bytes.Buffer // the body so far, for mem
	size   int64

	once sync.Once
}

var _ io.ReadCloser = (*cacheBody)(nil)

// Read is not called from go-polipo's own code. Store installs the cacheBody
// as resp.Body inside the ReverseProxy's ModifyResponse hook (cacheResponse),
// and httputil.ReverseProxy then reads it as a plain io.ReadCloser while
// copying the body to the client (copyResponse, which loops in copyBuffer).
// Each call hands the chunk it read back to that copy and also copies it into
// the disk and/or memory entry; reaching EOF commits the entry. Tests call it
// directly, for example through io.ReadAll.
func (b *cacheBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
	if n > 0 {
		b.size += int64(n)
		if b.disk != nil {
			if max := b.cm.effectiveMaxDiskEntrySize(); max >= 0 && b.size > max {
				b.dropDisk(errCacheTooLarge)
			} else if _, werr := b.disk.pw.Write(p[:n]); werr != nil {
				b.dropDisk(werr)
			}
		}
		if b.mem != nil {
			if max := b.cm.config.MemoryCacheMaxEntrySize; max >= 0 && b.size > max {
				b.mem, b.memBuf = nil, nil
			} else {
				b.memBuf.Write(p[:n])
			}
		}
	}
	switch {
	case err == io.EOF:
		b.once.Do(b.commit)
	case err != nil:
		b.once.Do(b.abort)
	}
	return n, err
}

// Close discards whatever was not committed: a body closed before EOF is
// incomplete and must not be cached. Like Read, it is called by
// httputil.ReverseProxy, which closes the response body once the copy to
// the client ends, whether it finished or failed.
func (b *cacheBody) Close() error {
	b.once.Do(b.abort)
	return b.src.Close()
}

// commit finishes the disk write and moves the file into place, and adds the
// memory entry.
func (b *cacheBody) commit() {
	if w := b.disk; w != nil {
		b.disk = nil
		_ = w.pw.Close()
		if err := <-w.done; err != nil {
			logf(b.cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't write cache file %s: %v", b.key, w.path, err)
			os.Remove(w.tmp)
		} else {
			b.cm.mu.Lock()
			added := fileSize(w.tmp) - fileSize(w.path) // an entry replaced no longer counts
			err := os.Rename(w.tmp, w.path)
			b.cm.mu.Unlock()
			if err != nil {
				logf(b.cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't move %s into place: %v", b.key, w.tmp, err)
				os.Remove(w.tmp)
			} else {
				b.cm.diskSize.Add(added)
				b.cm.maybeTrim()
			}
		}
	}
	if b.mem != nil {
		// memCache is set: Store gives b.mem a value only when memWants,
		// which needs the memory cache.
		b.mem.body = b.memBuf.Bytes()
		b.cm.memCache.Add(b.key, b.mem)
		b.mem, b.memBuf = nil, nil
	}
}

// abort discards the partial entry.
func (b *cacheBody) abort() {
	b.dropDisk(errCacheBodyAborted)
	b.mem, b.memBuf = nil, nil
}

// dropDisk stops the disk write and removes its temporary file.
func (b *cacheBody) dropDisk(reason error) {
	w := b.disk
	if w == nil {
		return
	}
	b.disk = nil
	w.pw.CloseWithError(reason)
	<-w.done //nolint:errcheck // wait for the writer to stop; its outcome no longer matters
	os.Remove(w.tmp)
}

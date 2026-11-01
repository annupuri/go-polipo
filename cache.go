package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

var b64fss = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-").WithPadding(base64.StdPadding)

// CacheManager manages memory and disk caches for go-polipo.
type CacheManager struct {
	config   *Config
	mu       sync.RWMutex // guards disk cache file I/O only; memCache is self-synchronized
	memCache *lru.Cache[string, *memEntry]
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

// memEntry is a single in-memory cache entry: a self-contained snapshot of a
// cacheable response. Unlike the disk format it has no body-offset gap and no
// X-Polipo-* metadata - none of that is needed here. There is no hash
// collision to guard against (the map key is the request URL itself, not a
// hash of it), and "last access" bookkeeping is unnecessary: the LRU tracks
// recency internally, and the entry never outlives the process anyway.
type memEntry struct {
	status     string
	statusCode int
	proto      string
	protoMajor int
	protoMinor int
	header     http.Header
	body       []byte
}

// newMemEntry snapshots resp (whose body has already been read into
// bodyBytes) for storage in the memory cache.
func newMemEntry(resp *http.Response, bodyBytes []byte) *memEntry {
	return &memEntry{
		status:     resp.Status,
		statusCode: resp.StatusCode,
		proto:      resp.Proto,
		protoMajor: resp.ProtoMajor,
		protoMinor: resp.ProtoMinor,
		header:     resp.Header.Clone(),
		body:       bodyBytes,
	}
}

// response reconstructs an *http.Response from e. The header is cloned on
// every call: callers (censoredHeaders stripping, the X-Polipo header
// cleanup in lookupEntry, and so on) routinely mutate the header map of
// whatever Lookup/LookupStale hands back, and the same *memEntry is reused
// for every future hit until it is evicted. The body bytes themselves are
// never mutated by a reader, so they are shared as-is, with no copy.
func (e *memEntry) response(req *http.Request) *http.Response {
	return &http.Response{
		Status:        e.status,
		StatusCode:    e.statusCode,
		Proto:         e.proto,
		ProtoMajor:    e.protoMajor,
		ProtoMinor:    e.protoMinor,
		Header:        e.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

// diskCacheDisabled reports whether the on-disk cache is turned off, either
// explicitly (noDiskCache) or by having no root to read from or write into.
func (cm *CacheManager) diskCacheDisabled() bool {
	return cm.config.NoDiskCache || cm.config.DiskCacheRoot == ""
}

// requestIsAuthenticated reports whether req carries HTTP credentials that make
// its response unsafe for a shared cache to reuse for other clients.
//
// This checks only Authorization, not Proxy-Authorization: Authorization is
// end-to-end (forwarded to, and meaningful to, the origin server), so RFC 9111
// section 3.5 is right to treat it as marking the response as tied to that
// one client. Proxy-Authorization is hop-by-hop - it authenticates the client
// to go-polipo itself (see isAuthorized in proxy.go), is stripped before the
// request is forwarded upstream, and says nothing about who the origin's
// response may be shared with. Polipo's own cache_control.flags never set
// CACHE_AUTHORIZATION for Proxy-Authorization (see http_parse.c); once
// authCredentials is configured, every legitimate request carries
// Proxy-Authorization, so treating it the same here would make almost
// nothing cacheable.
func requestIsAuthenticated(req *http.Request) bool {
	return req.Header.Get("Authorization") != ""
}

// responseVaries reports whether resp's body depends on request headers (a
// non-empty Vary). go-polipo keys the cache on the URL alone, so it cannot tell
// which stored variant matches a later request. Matching Polipo's default
// (mindlesslyCacheVary=false): such a response is still stored, but is never
// served as a fresh hit - lookupEntry always reports it stale, forcing a
// revalidation (or, absent a usable validator, a full re-fetch) on every
// access, so it is the origin, not a stale local guess, that ends up
// resolving which variant is correct for the current request. (RFC 9111
// section 4.1 requires a cache to match the nominated request headers before
// reusing a Vary response; forced revalidation is how Polipo satisfies that
// without actually keying the cache on those headers.)
func responseVaries(resp *http.Response) bool {
	return strings.TrimSpace(resp.Header.Get("Vary")) != ""
}

// responseNoCache reports whether resp carries a Cache-Control: no-cache
// directive. Such a response may be stored, but RFC 9111 section 5.2.2.4
// forbids reusing it without first revalidating with the origin, however fresh
// its max-age says it is - the same rule as Polipo's objectMustRevalidate
// (object.c), which checks CACHE_NO. Like responseVaries, lookupEntry
// therefore always reports it stale. The qualified form (no-cache="field")
// is treated as unqualified, as RFC 9111 allows. Directive names are
// case-insensitive, and Cache-Control may span several header lines.
func responseNoCache(resp *http.Response) bool {
	cc := strings.ToLower(strings.Join(resp.Header.Values("Cache-Control"), ","))
	return strings.Contains(cc, "no-cache")
}

// cacheControlAllowsAuthenticated reports whether a response Cache-Control value
// carries a directive that, per RFC 9111 section 3.5 (RFC 2616 section 14.8),
// lets a shared cache store the response to an authenticated request.
func cacheControlAllowsAuthenticated(cacheControl string) bool {
	cc := strings.ToLower(cacheControl)
	return strings.Contains(cc, "public") ||
		strings.Contains(cc, "must-revalidate") ||
		strings.Contains(cc, "s-maxage")
}

// getDirname gets the directory path for the host.
func (cm *CacheManager) getDirname(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" {
		return "", fmt.Errorf("only http is supported for disk cache")
	}

	host := u.Host
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	var sb strings.Builder
	for i := 0; i < len(host); i++ {
		c := host[i]
		if c == '.' && i != len(host)-1 && host[i+1] == '.' {
			return "", fmt.Errorf("invalid dot sequence in host")
		}
		// Polipo fssafe check: a-z, A-Z, 0-9, ., -, _
		isSafe := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_'

		if c == '%' || !isSafe {
			sb.WriteByte('%')
			sb.WriteString(fmt.Sprintf("%02X", c))
		} else {
			sb.WriteByte(c)
		}
	}

	return filepath.Join(cm.config.DiskCacheRoot, sb.String()), nil
}

// GetCachePath generates the cache file path for the given URL.
func (cm *CacheManager) GetCachePath(rawURL string) (string, error) {
	dir, err := cm.getDirname(rawURL)
	if err != nil {
		return "", err
	}

	h := md5.New()
	h.Write([]byte(rawURL))
	md5sum := h.Sum(nil)

	encoded := b64fss.EncodeToString(md5sum)
	return filepath.Join(dir, encoded), nil
}

// DiscardMemory empties the memory cache, leaving the disk cache untouched -
// the counterpart of Polipo's discardObjects on SIGUSR2. Nothing is lost
// beyond memory: every entry that fit on disk was written there by Store
// at the same time.
func (cm *CacheManager) DiscardMemory() {
	if cm.memCache != nil {
		cm.memCache.Purge()
	}
}

// Lookup retrieves a cached response for the request if it exists and is fresh.
func (cm *CacheManager) Lookup(req *http.Request) *http.Response {
	resp, stale := cm.lookupEntry(req)
	if resp == nil {
		return nil
	}
	if stale {
		// A stale entry is a miss as far as Lookup is concerned; the caller
		// (ProxyHandler's Director/ModifyResponse) is what turns this into a
		// conditional GET and merges a 304 back into the entry - see
		// addConditionalHeaders and reviveFromCache in proxy.go.
		resp.Body.Close()
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
	cc := req.Header.Get("Cache-Control")
	if strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store") {
		return nil, false
	}

	key := req.URL.String()

	// Checked first, and independently of diskCacheDisabled: MemoryCacheEntries
	// is meant to work the same whether or not the disk cache is enabled.
	if cm.memCache != nil {
		if entry, ok := cm.memCache.Get(key); ok {
			resp := entry.response(req)
			return resp, cm.isExpired(resp) || responseVaries(resp) || responseNoCache(resp)
		}
	}

	if cm.diskCacheDisabled() {
		return nil, false
	}

	path, err := cm.GetCachePath(key)
	if err != nil {
		return nil, false
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}

	// Parse response from file
	resp, err = http.ReadResponse(bufio.NewReader(file), req)
	if err != nil {
		logf(cm.config.LogLevel, LogWarn, "Cache lookup warning: %s: corrupt entry %s: %v", req.URL, path, err)
		file.Close()
		return nil, false
	}

	// Verify X-Polipo-Location to prevent hash collisions
	loc := resp.Header.Get("X-Polipo-Location")
	if loc != req.URL.String() {
		logf(cm.config.LogLevel, LogWarn, "Cache lookup warning: %s: hash collision at %s (stored for %s)", req.URL, path, loc)
		resp.Body.Close()
		file.Close()
		return nil, false
	}

	// resp.Body reads through a bufio.Reader, so closing it would leave the
	// file itself open until the garbage collector got to it.
	resp.Body = fileBody{ReadCloser: resp.Body, file: file}

	// A response carrying Vary is always reported stale, regardless of its
	// actual HTTP freshness: the cache key is the URL alone, so this entry
	// cannot be matched against the current request's headers, and only a
	// round-trip to the origin (a revalidation, or a full re-fetch when
	// there is no usable validator - see addConditionalHeaders) can settle
	// which variant is correct. See responseVaries. A no-cache response is
	// always stale for the same reason: only the origin may approve its
	// reuse. See responseNoCache.
	stale = cm.isExpired(resp) || responseVaries(resp) || responseNoCache(resp)

	// Clean up X-Polipo headers before returning
	resp.Header.Del("X-Polipo-Location")
	resp.Header.Del("X-Polipo-Date")
	resp.Header.Del("X-Polipo-Access")
	resp.Header.Del("X-Polipo-Body-Offset")

	// Copy the entry into the memory cache, so that later hits skip the
	// disk - otherwise the memory cache would only ever hold what was
	// fetched since the last restart. Nothing needs demoting when the LRU
	// evicts it again: the disk copy stays where it is. A HEAD response
	// carries no body to copy.
	if cm.memCache != nil && req.Method == http.MethodGet {
		cm.promote(key, resp)
	}

	return resp, stale
}

// promote copies a disk hit into the memory cache, subject to the same
// memoryCacheMaxEntrySize limit as Store. A body over the limit is not
// copied and keeps streaming from the file, with whatever was already read
// put back in front of the rest.
func (cm *CacheManager) promote(key string, resp *http.Response) {
	limit := cm.config.MemoryCacheMaxEntrySize
	if limit >= 0 && resp.ContentLength > limit {
		return
	}
	var r io.Reader = resp.Body
	if limit >= 0 {
		r = io.LimitReader(resp.Body, limit+1)
	}
	body, err := io.ReadAll(r)
	if err != nil || (limit >= 0 && int64(len(body)) > limit) {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	cm.memCache.Add(key, newMemEntry(resp, body))
}

// fileBody is a disk cache entry's body: closing it also closes the cache
// file it is read from.
type fileBody struct {
	io.ReadCloser
	file *os.File
}

func (b fileBody) Close() error {
	b.ReadCloser.Close()
	return b.file.Close()
}

// Heuristic freshness parameters (RFC 9111 section 4.2.2). Polipo exposed the
// equivalents as the maxAgeFraction (default 0.1) and maxAge (default 1 day)
// config variables; go-polipo hard-codes them on purpose. The fraction and cap
// are effectively RFC-blessed constants rather than operator policy, and a
// permissive value here would re-open the same over-serving of header-less
// responses that this heuristic exists to close. If a real need turns up, one
// "cap, or 0 to disable" duration is the knob to add - not the full Polipo set.
const (
	heuristicFraction    = 0.1
	heuristicMaxLifetime = 24 * time.Hour // cap, and the value used with no Last-Modified
)

// responseDate returns the response's Date, or the current time when the header
// is missing or unparseable.
func responseDate(resp *http.Response) time.Time {
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		return d
	}
	return time.Now()
}

// ccFreshnessLifetime reads an explicit freshness lifetime out of a
// Cache-Control value. s-maxage (shared-cache specific) wins over max-age, per
// RFC 9111 section 5.2.2.10. ok is false when neither directive is present and
// parseable.
func ccFreshnessLifetime(cc string) (life time.Duration, ok bool) {
	var maxAge, sMaxAge time.Duration
	var haveMaxAge, haveSMaxAge bool
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "s-maxage="):
			if n, err := strconv.Atoi(strings.TrimPrefix(part, "s-maxage=")); err == nil {
				sMaxAge, haveSMaxAge = time.Duration(n)*time.Second, true
			}
		case strings.HasPrefix(part, "max-age="):
			if n, err := strconv.Atoi(strings.TrimPrefix(part, "max-age=")); err == nil {
				maxAge, haveMaxAge = time.Duration(n)*time.Second, true
			}
		}
	}
	if haveSMaxAge {
		return sMaxAge, true
	}
	if haveMaxAge {
		return maxAge, true
	}
	return 0, false
}

// heuristicLifetime derives a freshness lifetime for a response with no explicit
// expiration. With a Last-Modified it is a fraction of the document's age at
// fetch time, capped; without one it is a flat default. This keeps a header-less
// 200 from being served as fresh until diskCacheUnlinkTime (32 days).
func heuristicLifetime(resp *http.Response, date time.Time) time.Duration {
	lm, err := http.ParseTime(resp.Header.Get("Last-Modified"))
	if err != nil {
		return heuristicMaxLifetime
	}
	age := date.Sub(lm)
	if age <= 0 {
		return 0 // changed at or after the response date: no basis to call it fresh
	}
	if life := time.Duration(float64(age) * heuristicFraction); life < heuristicMaxLifetime {
		return life
	}
	return heuristicMaxLifetime
}

// isExpired determines if the cached response is expired.
func (cm *CacheManager) isExpired(resp *http.Response) bool {
	now := time.Now()
	hasExplicit := false

	// Expires header.
	if expiresStr := resp.Header.Get("Expires"); expiresStr != "" {
		hasExplicit = true
		t, err := http.ParseTime(expiresStr)
		if err != nil {
			return true // an invalid Expires (e.g. "0") means "already expired" (RFC 9111 5.3)
		}
		if now.After(t) {
			return true
		}
	}

	// Cache-Control: max-age / s-maxage.
	if life, ok := ccFreshnessLifetime(resp.Header.Get("Cache-Control")); ok {
		hasExplicit = true
		if now.After(responseDate(resp).Add(life)) {
			return true
		}
	}

	if hasExplicit {
		return false
	}

	// No explicit expiration: fall back to a heuristic lifetime rather than
	// treating the entry as fresh indefinitely.
	date := responseDate(resp)
	return now.After(date.Add(heuristicLifetime(resp, date)))
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
	cc := resp.Header.Get("Cache-Control")
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "private") {
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
	if cm.config.DontCacheCookies && len(resp.Header["Set-Cookie"]) > 0 {
		return
	}

	// RFC 9111 section 3.5 (RFC 2616 section 14.8): a shared cache must not
	// reuse a response to a request carrying Authorization unless the response
	// explicitly permits it.
	if requestIsAuthenticated(req) && !cacheControlAllowsAuthenticated(cc) {
		return
	}

	diskEnabled := !cm.diskCacheDisabled()
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
	maxDiskSize := cm.config.MaxDiskCacheEntrySize
	diskWants := diskEnabled && (maxDiskSize < 0 || resp.ContentLength < 0 || resp.ContentLength <= maxDiskSize)
	maxMemSize := cm.config.MemoryCacheMaxEntrySize
	memWants := memEnabled && (maxMemSize < 0 || resp.ContentLength < 0 || resp.ContentLength <= maxMemSize)
	if !diskWants && !memWants {
		return
	}

	b := &cacheBody{cm: cm, src: resp.Body, key: req.URL.String()}
	if diskWants {
		b.disk = cm.startDiskWrite(req, resp)
	}
	if memWants {
		// Snapshot the headers now: the caller may change resp.Header while
		// the body is still streaming.
		b.mem = newMemEntry(resp, nil)
		b.memBuf = new(bytes.Buffer)
	}
	if b.disk == nil && b.mem == nil {
		return
	}
	resp.Body = b
}

// cacheTempPrefix starts the name of a disk cache entry that is still being
// written. Cache file names are custom-base64 (b64fss), which never contains
// a '.', so a temporary file can never be mistaken for an entry.
const cacheTempPrefix = ".tmp-"

// cacheTempMaxAge is how long Expire leaves a temporary file alone. One that
// has not been written to for this long was left behind by a crash.
const cacheTempMaxAge = time.Hour

// diskWrite is a disk cache entry being written in the background. The body
// arrives through pw; a goroutine serializes the response, Polipo metadata
// headers included, into a temporary file next to the final path, and sends
// the outcome on done once pw is closed.
type diskWrite struct {
	path string // final cache file
	tmp  string // temporary file being written
	pw   *io.PipeWriter
	done chan error
}

// startDiskWrite starts writing resp to a temporary file, or returns nil (after
// logging why) if that cannot be set up.
func (cm *CacheManager) startDiskWrite(req *http.Request, resp *http.Response) *diskWrite {
	path, err := cm.GetCachePath(req.URL.String())
	if err != nil {
		logf(cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't compute cache path: %v", req.URL, err)
		return nil
	}

	// Under the lock, so that Expire's pruneEmptyDirs cannot remove the
	// directory between MkdirAll and CreateTemp.
	cm.mu.Lock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		cm.mu.Unlock()
		logf(cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't create cache directory %s: %v", req.URL, dir, err)
		return nil
	}
	file, err := os.CreateTemp(dir, cacheTempPrefix+"*")
	cm.mu.Unlock()
	if err != nil {
		logf(cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't create a temporary file in %s: %v", req.URL, dir, err)
		return nil
	}

	pr, pw := io.Pipe()
	clone := *resp
	clone.Body = pr
	clone.Header = resp.Header.Clone()
	clone.Header.Set("X-Polipo-Location", req.URL.String())
	clone.Header.Set("X-Polipo-Date", time.Now().Format(http.TimeFormat))
	clone.Header.Set("X-Polipo-Access", time.Now().Format(http.TimeFormat))

	w := &diskWrite{path: path, tmp: file.Name(), pw: pw, done: make(chan error, 1)}
	go func() {
		err := clone.Write(file)
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		// If Write stopped early (a disk error), unblock and fail any further
		// writes to pw instead of leaving them waiting for a reader.
		pr.CloseWithError(errCacheWriteStopped)
		w.done <- err
	}()
	return w
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

func (b *cacheBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
	if n > 0 {
		b.size += int64(n)
		if b.disk != nil {
			if max := b.cm.config.MaxDiskCacheEntrySize; max >= 0 && b.size > max {
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
// incomplete and must not be cached.
func (b *cacheBody) Close() error {
	b.once.Do(b.abort)
	return b.src.Close()
}

// commit finishes the disk write and moves the file into place, and adds the
// memory entry.
func (b *cacheBody) commit() {
	if w := b.disk; w != nil {
		b.disk = nil
		w.pw.Close()
		if err := <-w.done; err != nil {
			logf(b.cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't write cache file %s: %v", b.key, w.path, err)
			os.Remove(w.tmp)
		} else {
			b.cm.mu.Lock()
			err := os.Rename(w.tmp, w.path)
			b.cm.mu.Unlock()
			if err != nil {
				logf(b.cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't move %s into place: %v", b.key, w.tmp, err)
				os.Remove(w.tmp)
			}
		}
	}
	if b.mem != nil {
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
	<-w.done
	os.Remove(w.tmp)
}

// ExpireStats reports the outcome of an Expire run.
type ExpireStats struct {
	Scanned int   // cache files examined
	Removed int   // cache files deleted
	Freed   int64 // bytes reclaimed

	// RemovedForSize counts entries removed early by the
	// DiskCacheTruncateTime/DiskCacheTruncateSize policy (large, stale
	// entries that predate the general DiskCacheUnlinkTime cutoff) rather
	// than for being corrupt, HTTP-stale, or past DiskCacheUnlinkTime. It is
	// a subset of Removed, broken out because it is go-polipo's deliberate
	// substitute for Polipo's in-place truncation (see DiskCacheTruncateTime
	// on Config) - worth being able to tell apart from ordinary housekeeping.
	RemovedForSize int
}

// Expire walks the disk cache and removes entries that are no longer usable:
// corrupt files, responses that are stale by HTTP freshness rules, files
// whose last access is older than Config.DiskCacheUnlinkTime, and large
// files that have gone unused past DiskCacheTruncateTime (see
// expireReason). Empty host directories left behind are pruned. It
// implements the `-x` command.
func (cm *CacheManager) Expire() (ExpireStats, error) {
	var stats ExpireStats

	root := cm.config.DiskCacheRoot
	if root == "" {
		return stats, fmt.Errorf("diskCacheRoot is not configured")
	}
	if info, err := os.Stat(root); err != nil {
		return stats, err
	} else if !info.IsDir() {
		return stats, fmt.Errorf("diskCacheRoot %q is not a directory", root)
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // vanished between listing and stat; ignore
		}
		if strings.HasPrefix(d.Name(), cacheTempPrefix) && time.Since(info.ModTime()) < cacheTempMaxAge {
			return nil // an entry still being written by Store
		}
		stats.Scanned++
		reason := cm.expireReason(path, info)
		if reason == "" {
			return nil
		}
		if err := os.Remove(path); err != nil {
			logf(cm.config.LogLevel, LogWarn, "Cache expire warning: couldn't remove %s (%s): %v", path, reason, err)
			return nil
		}
		stats.Removed++
		stats.Freed += info.Size()
		if reason == "size" {
			stats.RemovedForSize++
			logf(cm.config.LogLevel, LogInfo, "Cache expire: removed %s (%d bytes, unused since %s): "+
				"larger than diskCacheTruncateSize and older than diskCacheTruncateTime",
				path, info.Size(), info.ModTime().Format(http.TimeFormat))
		}
		return nil
	})
	if walkErr != nil {
		return stats, walkErr
	}

	cm.pruneEmptyDirs(root)
	return stats, nil
}

// expireReason reports why a single cache file should be deleted ("" if it
// should be kept). The specific non-empty values are for Expire's own
// bookkeeping/logging; callers other than Expire only need to know whether
// the result is empty.
func (cm *CacheManager) expireReason(path string, info fs.FileInfo) string {
	if strings.HasPrefix(info.Name(), cacheTempPrefix) {
		return "abandoned" // a partial write left behind by a crash
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	resp, err := http.ReadResponse(bufio.NewReader(file), nil)
	if err != nil {
		return "corrupt" // unparseable cache file: junk, drop it
	}
	defer resp.Body.Close()

	if cm.isExpired(resp) {
		return "http-stale"
	}

	// Absolute age cap based on last recorded access (fall back to mtime).
	lastAccess := info.ModTime()
	if a := resp.Header.Get("X-Polipo-Access"); a != "" {
		if t, err := http.ParseTime(a); err == nil {
			lastAccess = t
		}
	}
	if cm.config.DiskCacheUnlinkTime > 0 &&
		time.Since(lastAccess) > cm.config.DiskCacheUnlinkTime {
		return "unlink-time"
	}

	// Polipo would truncate a large, long-unused entry down to
	// diskCacheTruncateSize rather than remove it, then serve the remainder
	// via partial instantiation. go-polipo has no partial-instance support
	// (§10 of PARITY_AUDIT): truncating the body in place here would leave
	// the stored Content-Length pointing past the end of the file, so a
	// later Lookup would hand a client a response that claims to be
	// complete but silently cuts off short. Deleting the entry outright
	// reclaims the same space without ever serving that broken response.
	if cm.config.DiskCacheTruncateSize > 0 && info.Size() > cm.config.DiskCacheTruncateSize &&
		time.Since(lastAccess) > cm.config.DiskCacheTruncateTime {
		return "size"
	}

	return ""
}

// pruneEmptyDirs removes empty directories under root, deepest first, keeping
// root itself.
func (cm *CacheManager) pruneEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Deepest paths first so parents become empty after children are removed.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) == 0 {
			os.Remove(dir)
		}
	}
}

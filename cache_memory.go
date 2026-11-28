package main

// The in-memory tier of the cache: an LRU of whole responses in front of
// the disk cache, filled by Store and by copying disk hits (promote).

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// memEntry is a single in-memory cache entry: a self-contained snapshot of a
// cacheable response. Unlike the disk format it has no body-offset gap and no
// X-Polipo-* metadata - none of that is needed here. There is no hash
// collision to guard against (the map key is the request URL itself, not a
// hash of it). The LRU tracks recency in memory; touched only exists to
// record a memory hit's use on the disk copy, for -x.
type memEntry struct {
	status     string
	statusCode int
	proto      string
	protoMajor int
	protoMinor int
	header     http.Header
	body       []byte
	birth      time.Time // see responseBirth; a disk entry keeps it in X-Polipo-Date

	// touched is when this entry's disk copy last had its use recorded
	// (UnixNano; see noteAccess). A hit served from memory never opens the
	// file, so this is how it knows when to record the use again.
	touched atomic.Int64
}

// newMemEntry snapshots resp (whose body has already been read into
// bodyBytes) for storage in the memory cache.
func newMemEntry(resp *http.Response, bodyBytes []byte, birth time.Time) *memEntry {
	e := &memEntry{
		birth:      birth,
		status:     resp.Status,
		statusCode: resp.StatusCode,
		proto:      resp.Proto,
		protoMajor: resp.ProtoMajor,
		protoMinor: resp.ProtoMinor,
		header:     resp.Header.Clone(),
		body:       bodyBytes,
	}
	// Made from a response just stored or just read from disk, whose use
	// is already recorded.
	e.touched.Store(time.Now().UnixNano())
	return e
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
		Body:          memBody{bytes.NewReader(e.body)},
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

// MemoryEntries reports how many entries the in-memory cache holds (0 when
// it is disabled).
func (cm *CacheManager) MemoryEntries() int {
	if cm.memCache == nil {
		return 0
	}
	return cm.memCache.Len()
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

// noteMemoryAccess records a memory hit's use on the entry's disk copy, if
// there is one; see noteAccess.
func (cm *CacheManager) noteMemoryAccess(key string, entry *memEntry) {
	if cm.isDiskCacheDisabled() {
		return
	}
	if time.Since(time.Unix(0, entry.touched.Load())) < accessRefreshInterval {
		return
	}
	path, err := cm.CachePath(key)
	if err != nil {
		return
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	// The memory entry's own clock is what throttles this, so pass a lastUse
	// old enough that noteAccess writes.
	if noteAccess(path, time.Time{}) {
		entry.touched.Store(time.Now().UnixNano())
	}
}

// promote copies a disk hit into the memory cache, subject to the same
// memoryCacheMaxEntrySize limit as Store. A body over the limit is not
// copied and keeps streaming from the file, with whatever was already read
// put back in front of the rest.
// It does nothing when the memory cache is off.
func (cm *CacheManager) promote(key string, resp *http.Response, birth time.Time) {
	if cm.memCache == nil {
		return
	}
	limit := cm.config().MemoryCacheMaxEntrySize
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
	_ = resp.Body.Close()
	resp.Body = memBody{bytes.NewReader(body)}
	resp.ContentLength = int64(len(body))
	cm.memCache.Add(key, newMemEntry(resp, body, birth))
}

// memBody is a memory cache entry's body. It shares the entry's bytes, so
// writeRange can slice it without a copy.
type memBody struct {
	*bytes.Reader
}

func (memBody) Close() error { return nil }

func (b memBody) bodyAt() (io.ReaderAt, int64, bool) {
	return b.Reader, b.Size(), true
}

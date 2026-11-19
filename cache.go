package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"errors"
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
	"sync/atomic"
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

// accessRefreshInterval is how often a cache hit records its entry's use on
// disk. diskCacheUnlinkTime is counted in days, so an hour is fine enough,
// and an entry served many times a minute does not touch its file each time.
const accessRefreshInterval = time.Hour

// noteAccess records that the disk entry at path was used, by setting the
// file's mtime to now, unless lastUse (when the use was last recorded) is
// within accessRefreshInterval. Polipo kept an object's last use in
// object->atime, set whenever it was served (client.c), and wrote it out to
// X-Polipo-Access; judgeExpire counts diskCacheUnlinkTime from that. go-polipo
// does not rewrite the entry's header for this: changing the file's
// timestamp leaves its content alone, so a reader cannot see a half-written
// header. The mtime then means "last used" rather than "last modified", and
// judgeExpire takes the later of it and X-Polipo-Access. A failure (say the
// entry was just removed) only means the use goes unrecorded.
func noteAccess(path string, lastUse time.Time) bool {
	now := time.Now()
	if now.Sub(lastUse) < accessRefreshInterval {
		return false
	}
	return os.Chtimes(path, now, now) == nil
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

// isDiskCacheDisabled reports whether the on-disk cache is turned off, either
// explicitly (noDiskCache) or by having no root to read from or write into.
func (cm *CacheManager) isDiskCacheDisabled() bool {
	return cm.config.NoDiskCache || cm.config.DiskCacheRoot == ""
}

// isRequestAuthenticated reports whether req carries HTTP credentials that make
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
func isRequestAuthenticated(req *http.Request) bool {
	return req.Header.Get(headerAuthorization) != ""
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
	return strings.TrimSpace(resp.Header.Get(headerVary)) != ""
}

// responseHasNoCache reports whether resp carries a Cache-Control: no-cache
// directive. Such a response may be stored, but RFC 9111 section 5.2.2.4
// forbids reusing it without first revalidating with the origin, however fresh
// its max-age says it is - the same rule as Polipo's objectMustRevalidate
// (object.c), which checks CACHE_NO. Like responseVaries, lookupEntry
// therefore always reports it stale. The qualified form (no-cache="field")
// is treated as unqualified, as RFC 9111 allows. Directive names are
// case-insensitive, and Cache-Control may span several header lines.
// Without any Cache-Control, Pragma: no-cache counts too (see hasPragmaNoCache).
func responseHasNoCache(resp *http.Response) bool {
	return parseCacheControl(resp.Header).has("no-cache") || hasPragmaNoCache(resp.Header)
}

// hasPragmaNoCache reports whether h carries Pragma: no-cache and no
// Cache-Control at all; when Cache-Control is present, it alone decides. In
// a request, RFC 7234 section 5.4 required treating that as Cache-Control:
// no-cache; RFC 9111 section 5.4 deprecates Pragma and drops the rule, but
// an HTTP/1.0 client may still send it alone, and Polipo (http_parse.c)
// honoured it, so go-polipo keeps doing so. In a response its meaning was
// never specified; Polipo honoured it there as well, and so does go-polipo.
func hasPragmaNoCache(h http.Header) bool {
	if len(h.Values(headerCacheControl)) > 0 {
		return false
	}
	pragma := strings.ToLower(strings.Join(h.Values(headerPragma), ","))
	return strings.Contains(pragma, "no-cache")
}

// cacheControl holds the directives of a message's Cache-Control, keyed by
// lower-cased name, with their values unquoted ("" for a directive without
// one).
type cacheControl map[string]string

// parseCacheControl reads every Cache-Control line in h.
func parseCacheControl(h http.Header) cacheControl {
	cc := make(cacheControl)
	for _, line := range h.Values(headerCacheControl) {
		for _, part := range strings.Split(line, ",") {
			name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				cc[name] = strings.Trim(strings.TrimSpace(value), `"`)
			}
		}
	}
	return cc
}

// has reports whether the directive name (lower case) is present.
func (cc cacheControl) has(name string) bool {
	_, ok := cc[name]
	return ok
}

// seconds reads a delta-seconds directive value, reporting false when it is
// missing or not a non-negative integer.
func (cc cacheControl) seconds(name string) (time.Duration, bool) {
	v, ok := cc[name]
	if !ok {
		return 0, false
	}
	return parseDeltaSeconds(v)
}

// parseDeltaSeconds parses a delta-seconds value (RFC 9111 section 1.2.2),
// reporting false when v is not a non-negative integer. A value too large
// to represent is capped at maxAgeValue, as the RFC asks.
func parseDeltaSeconds(v string) (time.Duration, bool) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(min(n, maxAgeValue)) * time.Second, true
}

// requestHasOnlyIfCached reports whether req carries Cache-Control:
// only-if-cached, under which the client wants nothing fetched from the
// origin (RFC 9111 section 5.2.1.7).
func requestHasOnlyIfCached(req *http.Request) bool {
	return parseCacheControl(req.Header).has("only-if-cached")
}

// responseForbidsStale reports whether resp may not be served stale even when
// the request's max-stale would accept it: must-revalidate (RFC 9111 section
// 5.2.2.2), and, since this is a shared cache, proxy-revalidate and s-maxage
// (sections 5.2.2.8 and 5.2.2.10).
func responseForbidsStale(resp *http.Response) bool {
	cc := parseCacheControl(resp.Header)
	return cc.has("must-revalidate") || cc.has("proxy-revalidate") || cc.has("s-maxage")
}

// canServeStale reports whether resp may be served stale at all, as
// relaxTransparency and proxyOffline do. Not when it carries
// must-revalidate, proxy-revalidate or s-maxage (see responseForbidsStale),
// nor no-cache (see responseHasNoCache): RFC 9111 sections 4.2.4 and 5.2.2
// require a successful validation with the origin before any reuse of
// those once they are stale, even by a disconnected cache. Polipo served
// them stale anyway.
func canServeStale(resp *http.Response) bool {
	return !responseForbidsStale(resp) && !responseHasNoCache(resp)
}

// allowsAuthenticated reports whether a response's Cache-Control carries a
// directive that, per RFC 9111 section 3.5 (RFC 2616 section 14.8), lets a
// shared cache store the response to an authenticated request.
func (cc cacheControl) allowsAuthenticated() bool {
	return cc.has("public") || cc.has("must-revalidate") || cc.has("s-maxage")
}

// cacheDir returns the directory that holds the cache files for rawURL's
// host.
func (cm *CacheManager) cacheDir(rawURL string) (string, error) {
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
			fmt.Fprintf(&sb, "%02X", c)
		} else {
			sb.WriteByte(c)
		}
	}

	return filepath.Join(cm.config.DiskCacheRoot, sb.String()), nil
}

// CachePath returns the cache file path for rawURL.
func (cm *CacheManager) CachePath(rawURL string) (string, error) {
	dir, err := cm.cacheDir(rawURL)
	if err != nil {
		return "", err
	}

	h := md5.New()
	h.Write([]byte(rawURL))
	md5sum := h.Sum(nil)

	encoded := b64fss.EncodeToString(md5sum)
	return filepath.Join(dir, encoded), nil
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
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logf(cm.config.LogLevel, LogWarn, "Cache invalidate warning: %s: couldn't remove %s: %v", rawURL, path, err)
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
		// (ProxyHandler's rewrite/ModifyResponse) is what turns this into a
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

	path, err := cm.CachePath(key)
	if err != nil {
		return nil, false
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// Record the use before opening the file: on Windows os.Chtimes opens
	// the file without sharing it for reading, so it fails while this
	// function holds the file open. (Another request reading the same entry
	// can still make it fail there; the next hit then records the use.)
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	noteAccess(path, info.ModTime())

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
	loc := resp.Header.Get(headerPolipoLocation)
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
	// reuse. See responseHasNoCache.
	birth := storedBirth(resp)
	stale = cm.isStaleFor(req, resp, birth)

	// Clean up X-Polipo headers before returning
	resp.Header.Del(headerPolipoLocation)
	resp.Header.Del(headerPolipoDate)
	resp.Header.Del(headerPolipoAccess)
	resp.Header.Del(headerPolipoBodyOffset)

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

// setAge replaces whatever Age resp was stored with by its current age, which
// RFC 9111 section 4 requires on every response served from the cache.
// Polipo (client.c) sent it too, though only once it reached a second.
func setAge(resp *http.Response, birth time.Time) {
	age := max(int64(time.Since(birth)/time.Second), 0)
	resp.Header.Set(headerAge, strconv.FormatInt(age, 10))
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
	cm.memCache.Add(key, newMemEntry(resp, body, birth))
}

// fileBody is a disk cache entry's body: closing it also closes the cache
// file it is read from.
type fileBody struct {
	io.ReadCloser
	file *os.File
}

var _ io.ReadCloser = fileBody{}

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
// A response with no Last-Modified gets no heuristic lifetime at all; see
// heuristicFreshnessLifetime.
const (
	heuristicFraction    = 0.1
	heuristicMaxLifetime = 24 * time.Hour
)

// responseDate returns the response's Date, or the current time when the header
// is missing or unparseable.
func responseDate(resp *http.Response) time.Time {
	if d, err := http.ParseTime(resp.Header.Get(headerDate)); err == nil {
		return d
	}
	return time.Now()
}

// maxAgeValue caps a parsed delta-seconds value (Age, and the max-age and
// other Cache-Control durations), as RFC 9111 section 1.2.2 asks of any
// value that overflows.
const maxAgeValue = 1 << 31

// responseBirth returns the instant a response just received from upstream
// was generated, so that its current age (RFC 9111 section 4.2.3) is simply
// the time elapsed since. It is the earlier of the response's Date and
// requestTime less any Age reported by a cache further upstream - the RFC's
// corrected_initial_age, which counts the round trip as time the response
// spent ageing. Polipo kept the same instant in object->age (server.c) and
// stored it on disk as X-Polipo-Date.
func responseBirth(h http.Header, requestTime time.Time) time.Time {
	birth := requestTime
	if age, ok := parseDeltaSeconds(strings.TrimSpace(h.Get(headerAge))); ok {
		birth = birth.Add(-age)
	}
	if d, err := http.ParseTime(h.Get(headerDate)); err == nil && d.Before(birth) {
		birth = d
	}
	return birth
}

// storedBirth reads back the responseBirth of a disk cache entry from its
// X-Polipo-Date. Polipo omitted X-Polipo-Date when it equalled Date, and
// before go-polipo stored the birth there it stored the time of writing, so
// Date, when earlier, still wins.
func storedBirth(resp *http.Response) time.Time {
	date := responseDate(resp)
	if t, err := http.ParseTime(resp.Header.Get(headerPolipoDate)); err == nil && t.Before(date) {
		return t
	}
	return date
}

// freshnessDirective reads an explicit freshness lifetime out of a response's
// Cache-Control. s-maxage (shared-cache specific) wins over max-age, per RFC
// 9111 section 5.2.2.10. ok is false when neither directive is present. One
// that is present but not a non-negative integer gives a zero lifetime: RFC
// 9111 section 4.2.1 encourages treating such a response as stale.
func (cc cacheControl) freshnessDirective() (life time.Duration, ok bool) {
	for _, name := range []string{"s-maxage", "max-age"} {
		if cc.has(name) {
			life, _ := cc.seconds(name)
			return life, true
		}
	}
	return 0, false
}

// heuristicFreshnessLifetime derives a freshness lifetime for a response
// with no explicit expiration (RFC 9111 section 4.2.2, which gives it this
// name). With a Last-Modified it is a fraction of the document's age at
// fetch time, capped. Without one there is nothing to base a guess on, and
// it is zero: the response is stored but revalidated (or re-fetched) on
// every use, as browsers and Squid's default refresh_pattern do. Polipo
// gave it maxNoModifiedAge, 23 minutes, which left pages generated afresh
// on every request served stale for that long.
func heuristicFreshnessLifetime(resp *http.Response, date time.Time) time.Duration {
	lm, err := http.ParseTime(resp.Header.Get(headerLastModified))
	if err != nil {
		return 0
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

// freshnessLifetime returns how long resp stays fresh after its birth (RFC
// 9111 section 4.2.1): s-maxage or max-age, which override Expires (section
// 5.3); otherwise Expires less Date; otherwise a heuristic lifetime rather
// than treating the entry as fresh indefinitely. Without a Date, birth stands
// in for it.
func freshnessLifetime(resp *http.Response, birth time.Time) time.Duration {
	if life, ok := parseCacheControl(resp.Header).freshnessDirective(); ok {
		return life
	}
	date := birth
	if d, err := http.ParseTime(resp.Header.Get(headerDate)); err == nil {
		date = d
	}
	if expiresStr := resp.Header.Get(headerExpires); expiresStr != "" {
		t, err := http.ParseTime(expiresStr)
		if err != nil {
			return 0 // an invalid Expires (e.g. "0") means "already expired" (RFC 9111 5.3)
		}
		return t.Sub(date)
	}
	return heuristicFreshnessLifetime(resp, date)
}

// isExpired reports whether resp, generated at birth (see responseBirth), is
// no longer fresh: RFC 9111 section 4.2 calls it fresh only while its
// freshness lifetime exceeds its current age.
func (cm *CacheManager) isExpired(resp *http.Response, birth time.Time) bool {
	return time.Since(birth) >= freshnessLifetime(resp, birth)
}

// isStaleFor reports whether resp, generated at birth, may not be used for req
// without going to the origin. Beyond resp's own freshness, req's
// Cache-Control narrows or widens what it accepts (RFC 9111 section 5.2.1),
// as Polipo's objectIsStale did: max-age caps the age (a browser reload
// sends max-age=0 to have the cache check with the origin), min-fresh asks
// for that much freshness still left, and max-stale accepts that much
// staleness, or any without a value (an invalid value is ignored) - unless
// resp forbids serving it stale.
// When a request has both min-fresh and max-stale, min-fresh wins, as in
// Polipo. A Vary or no-cache response is always stale, whatever req says;
// see responseVaries and responseHasNoCache.
func (cm *CacheManager) isStaleFor(req *http.Request, resp *http.Response, birth time.Time) bool {
	if responseVaries(resp) || responseHasNoCache(resp) {
		return true
	}
	cc := parseCacheControl(req.Header)
	age := time.Since(birth)
	if maxAge, ok := cc.seconds("max-age"); ok && age > maxAge {
		return true
	}
	deadline := freshnessLifetime(resp, birth)
	if minFresh, ok := cc.seconds("min-fresh"); ok {
		deadline -= minFresh
	} else if v, ok := cc["max-stale"]; ok && !responseForbidsStale(resp) {
		// max-stale has three cases, and the empty one must be checked
		// first, because parseDeltaSeconds rejects "" just as it rejects
		// "abc" or "-1":
		//   - no value: any staleness is accepted (RFC 9111 section
		//     5.2.1.2);
		//   - delta-seconds: that much staleness is accepted;
		//   - anything else is invalid and ignored, as if the request had
		//     no max-stale, like an invalid max-age. Treating it as "any
		//     staleness" would let a malformed value unlock stale entries.
		if v == "" {
			return false
		}
		if maxStale, ok := parseDeltaSeconds(v); ok {
			deadline += maxStale
		}
	}
	return age >= deadline
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
	maxDiskSize := cm.config.MaxDiskCacheEntrySize
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
func (cm *CacheManager) startDiskWrite(req *http.Request, resp *http.Response, birth time.Time) *diskWrite {
	path, err := cm.CachePath(req.URL.String())
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
	clone.Header.Set(headerPolipoLocation, req.URL.String())
	clone.Header.Set(headerPolipoDate, birth.UTC().Format(http.TimeFormat))
	clone.Header.Set(headerPolipoAccess, time.Now().UTC().Format(http.TimeFormat))

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
// judgeExpire). Empty host directories left behind are pruned. It
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
		reason := cm.judgeExpire(path, info)
		if reason == expireKeep {
			return nil
		}
		if err := os.Remove(path); err != nil {
			logf(cm.config.LogLevel, LogWarn, "Cache expire warning: couldn't remove %s (%s): %v", path, reason, err)
			return nil
		}
		stats.Removed++
		stats.Freed += info.Size()
		if reason == expireSize {
			stats.RemovedForSize++
			logf(cm.config.LogLevel, LogInfo, "Cache expire: removed %s (%d bytes, unused since %s): "+
				"larger than diskCacheTruncateSize and older than diskCacheTruncateTime",
				path, info.Size(), info.ModTime().UTC().Format(http.TimeFormat))
		}
		return nil
	})
	if walkErr != nil {
		return stats, walkErr
	}

	cm.pruneEmptyDirs(root)
	return stats, nil
}

// expireReason is why Expire removes a cache file, or expireKeep when it
// keeps it. The values appear in Expire's log lines.
type expireReason string

const (
	expireKeep       expireReason = ""
	expireAbandoned  expireReason = "abandoned"   // a partial write left behind by a crash
	expireCorrupt    expireReason = "corrupt"     // unparseable cache file: junk
	expireHTTPStale  expireReason = "http-stale"  // stale by HTTP freshness rules
	expireUnlinkTime expireReason = "unlink-time" // unused for diskCacheUnlinkTime
	expireSize       expireReason = "size"        // large and unused for diskCacheTruncateTime
)

// judgeExpire decides whether Expire should remove a single cache file, and
// why. Expire counts expireSize removals separately (RemovedForSize).
func (cm *CacheManager) judgeExpire(path string, info fs.FileInfo) expireReason {
	if strings.HasPrefix(info.Name(), cacheTempPrefix) {
		return expireAbandoned
	}
	file, err := os.Open(path)
	if err != nil {
		return expireKeep
	}
	defer file.Close()

	resp, err := http.ReadResponse(bufio.NewReader(file), nil)
	if err != nil {
		return expireCorrupt
	}
	defer resp.Body.Close()

	if cm.isExpired(resp, storedBirth(resp)) {
		return expireHTTPStale
	}

	// Absolute age cap based on the last recorded use: the later of
	// X-Polipo-Access (written when the entry was stored, or by Polipo when
	// it served the entry) and the file's mtime (set by noteAccess when
	// go-polipo serves it).
	lastAccess := info.ModTime()
	if a := resp.Header.Get(headerPolipoAccess); a != "" {
		if t, err := http.ParseTime(a); err == nil && t.After(lastAccess) {
			lastAccess = t
		}
	}
	if cm.config.DiskCacheUnlinkTime > 0 &&
		time.Since(lastAccess) > cm.config.DiskCacheUnlinkTime {
		return expireUnlinkTime
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
		return expireSize
	}

	return expireKeep
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

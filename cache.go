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
)

var b64fss = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-").WithPadding(base64.StdPadding)

// CacheManager manages memory and disk caches for go-polipo.
type CacheManager struct {
	config *Config
	mu     sync.RWMutex
}

// NewCacheManager creates a CacheManager instance.
func NewCacheManager(config *Config) *CacheManager {
	return &CacheManager{
		config: config,
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
// which stored variant matches a later request; such responses are neither
// written to the cache nor served from it. (RFC 9111 section 4.1 requires a
// cache to match the nominated request headers before reusing a Vary response.)
func responseVaries(resp *http.Response) bool {
	return strings.TrimSpace(resp.Header.Get("Vary")) != ""
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

// Lookup retrieves a cached response for the request if it exists and is fresh.
func (cm *CacheManager) Lookup(req *http.Request) *http.Response {
	resp, stale := cm.lookupEntry(req)
	if resp == nil {
		return nil
	}
	if stale {
		// We could support conditional validation here, but for simplicity
		// we treat expired cache as a miss.
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

// lookupEntry does the actual disk read shared by Lookup and LookupStale.
func (cm *CacheManager) lookupEntry(req *http.Request) (resp *http.Response, stale bool) {
	if cm.diskCacheDisabled() {
		return nil, false
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil, false
	}

	// Cache-Control checks
	cc := req.Header.Get("Cache-Control")
	if strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store") {
		return nil, false
	}

	path, err := cm.GetCachePath(req.URL.String())
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
		file.Close()
		return nil, false
	}

	// Verify X-Polipo-Location to prevent hash collisions
	loc := resp.Header.Get("X-Polipo-Location")
	if loc != req.URL.String() {
		resp.Body.Close()
		file.Close()
		return nil, false
	}

	// A stored response carrying Vary cannot be matched against this request's
	// headers (the cache key is the URL alone), so serving it risks handing back
	// the wrong variant. Store skips these, but a cache tree written by Polipo or
	// an older go-polipo may still contain them.
	if responseVaries(resp) {
		resp.Body.Close()
		file.Close()
		return nil, false
	}

	stale = cm.isExpired(resp)

	// Clean up X-Polipo headers before returning
	resp.Header.Del("X-Polipo-Location")
	resp.Header.Del("X-Polipo-Date")
	resp.Header.Del("X-Polipo-Access")
	resp.Header.Del("X-Polipo-Body-Offset")

	return resp, stale
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

// Store writes a cacheable response to the disk cache. It consumes resp.Body
// and replaces it with an equivalent in-memory reader, so the caller can still
// stream the response afterwards. Non-cacheable responses are left untouched.
// An error is returned only when the response body cannot be read.
func (cm *CacheManager) Store(req *http.Request, resp *http.Response) error {
	if cm.diskCacheDisabled() {
		return nil
	}
	if req.Method != http.MethodGet || resp.StatusCode != http.StatusOK {
		return nil
	}

	// Cache-Control checks
	cc := resp.Header.Get("Cache-Control")
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "private") {
		return nil
	}

	// go-polipo's cache key is the URL alone, so it cannot store more than one
	// variant per URL or pick between them. Caching a Vary response would let a
	// later client be served a variant selected for someone else (a gzipped body
	// to a client that did not offer gzip, a French page to an English one). Skip
	// them; they are still forwarded, just not cached.
	if responseVaries(resp) {
		return nil
	}

	// A Set-Cookie response is meant for a single client; a cached copy would be
	// replayed to everyone who later requests the same URL. Polipo cached these,
	// so this is opt-out via dontCacheCookies.
	if cm.config.DontCacheCookies && len(resp.Header["Set-Cookie"]) > 0 {
		return nil
	}

	// RFC 9111 section 3.5 (RFC 2616 section 14.8): a shared cache must not
	// reuse a response to a request carrying Authorization unless the response
	// explicitly permits it.
	if requestIsAuthenticated(req) && !cacheControlAllowsAuthenticated(cc) {
		return nil
	}

	// Read the body into memory so it can be both written to disk and replayed.
	bodyBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}

	// Restore the response body for the caller.
	resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	path, err := cm.GetCachePath(req.URL.String())
	if err != nil {
		return nil
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil
	}
	defer file.Close()

	// Create clone of response to write
	cloneResp := *resp
	cloneResp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	cloneResp.Header = resp.Header.Clone()

	// Add Polipo metadata headers
	cloneResp.Header.Set("X-Polipo-Location", req.URL.String())
	cloneResp.Header.Set("X-Polipo-Date", time.Now().Format(http.TimeFormat))
	cloneResp.Header.Set("X-Polipo-Access", time.Now().Format(http.TimeFormat))

	// Write to disk in HTTP format
	if err := cloneResp.Write(file); err != nil {
		os.Remove(path) // Delete incomplete cache file
	}

	return nil
}

// ExpireStats reports the outcome of an Expire run.
type ExpireStats struct {
	Scanned int   // cache files examined
	Removed int   // cache files deleted
	Freed   int64 // bytes reclaimed
}

// Expire walks the disk cache and removes entries that are no longer usable:
// corrupt files, responses that are stale by HTTP freshness rules, and files
// whose last access is older than Config.DiskCacheUnlinkTime. Empty host
// directories left behind are pruned. It implements the `-x` command.
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
		stats.Scanned++
		if !cm.shouldExpire(path, info) {
			return nil
		}
		if err := os.Remove(path); err == nil {
			stats.Removed++
			stats.Freed += info.Size()
		}
		return nil
	})
	if walkErr != nil {
		return stats, walkErr
	}

	cm.pruneEmptyDirs(root)
	return stats, nil
}

// shouldExpire reports whether a single cache file is safe to delete.
func (cm *CacheManager) shouldExpire(path string, info fs.FileInfo) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	resp, err := http.ReadResponse(bufio.NewReader(file), nil)
	if err != nil {
		return true // unparseable cache file: junk, drop it
	}
	defer resp.Body.Close()

	if cm.isExpired(resp) {
		return true
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
		return true
	}

	return false
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

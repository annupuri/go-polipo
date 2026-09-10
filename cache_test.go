package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCacheEntry writes a single cache file for rawURL directly on disk, with
// caller-controlled Cache-Control/Date/X-Polipo-Access headers and an optional
// mtime override. It returns the file path. Empty header strings are omitted.
func writeCacheEntry(t *testing.T, cm *CacheManager, rawURL, cacheControl, date, access string, mtime time.Time) string {
	t.Helper()
	p, err := cm.GetCachePath(rawURL)
	if err != nil {
		t.Fatalf("GetCachePath(%q): %v", rawURL, err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}

	body := "cached body"
	h := make(http.Header)
	h.Set("Content-Type", "text/plain")
	h.Set("X-Polipo-Location", rawURL)
	if cacheControl != "" {
		h.Set("Cache-Control", cacheControl)
	}
	if date != "" {
		h.Set("Date", date)
	}
	if access != "" {
		h.Set("X-Polipo-Access", access)
	}
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}

	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Write(f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestCachePathGeneration(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = "/var/cache/polipo/"
	cm := NewCacheManager(cfg)

	tests := []struct {
		urlStr       string
		expectedDir  string
		expectedFile string // prefix or part
	}{
		{"http://example.com/", "/var/cache/polipo/example.com/", "pr8XV--wV-JmtpffnPF2-Q=="},
		{"http://sub-domain.com/path?query=1", "/var/cache/polipo/sub-domain.com/", ""},
		{"http://user:pass@host.com/path", "/var/cache/polipo/host.com/", ""},
	}

	for _, tt := range tests {
		path, err := cm.GetCachePath(tt.urlStr)
		if err != nil {
			t.Errorf("GetCachePath(%s) unexpected error: %v", tt.urlStr, err)
			continue
		}

		if !strings.HasPrefix(path, tt.expectedDir) {
			t.Errorf("expected path %q to have prefix %q", path, tt.expectedDir)
		}

		if tt.expectedFile != "" && !strings.HasSuffix(path, tt.expectedFile) {
			t.Errorf("expected path %q to end with %q", path, tt.expectedFile)
		}
	}
}

func TestCacheStoreAndLookup(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)

	targetURL := "http://example.com/api/data"
	req := httptest.NewRequest(http.MethodGet, targetURL, nil)

	// Create a mock response
	bodyText := "Hello, Cache World!"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(bodyText)),
	}
	resp.Header.Set("Content-Type", "text/plain")
	resp.Header.Set("Cache-Control", "max-age=3600")
	resp.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))

	// 1. Store the response
	if err := cm.Store(req, resp); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Verify Store left a replayable body in place for the caller
	storedBody, _ := io.ReadAll(resp.Body)
	if string(storedBody) != bodyText {
		t.Errorf("expected body %q, got %q", bodyText, string(storedBody))
	}

	// Verify file was created
	path, _ := cm.GetCachePath(targetURL)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected cache file %s to exist, error: %v", path, err)
	}

	// 2. Lookup the response
	cachedResp := cm.Lookup(req)
	if cachedResp == nil {
		t.Fatal("Lookup failed: returned nil")
	}
	defer cachedResp.Body.Close()

	if cachedResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", cachedResp.StatusCode)
	}

	cachedBody, _ := io.ReadAll(cachedResp.Body)
	if string(cachedBody) != bodyText {
		t.Errorf("expected cached body %q, got %q", bodyText, string(cachedBody))
	}

	// Verify X-Polipo headers are removed on lookup
	if cachedResp.Header.Get("X-Polipo-Location") != "" {
		t.Error("X-Polipo-Location was not removed on Lookup")
	}
}

func TestCacheIsExpired(t *testing.T) {
	cfg := NewDefaultConfig()
	cm := NewCacheManager(cfg)

	// 1. Check Expires header in the past
	respPast := &http.Response{
		Header: make(http.Header),
	}
	respPast.Header.Set("Expires", time.Now().UTC().Add(-5*time.Minute).Format(http.TimeFormat))
	if !cm.isExpired(respPast) {
		t.Error("expected response with past Expires to be expired")
	}

	// 2. Check Expires header in the future
	respFuture := &http.Response{
		Header: make(http.Header),
	}
	respFuture.Header.Set("Expires", time.Now().UTC().Add(5*time.Minute).Format(http.TimeFormat))
	if cm.isExpired(respFuture) {
		t.Error("expected response with future Expires to be fresh")
	}

	// 3. Check Cache-Control: max-age (expired)
	respCCExpired := &http.Response{
		Header: make(http.Header),
	}
	respCCExpired.Header.Set("Cache-Control", "max-age=60")
	respCCExpired.Header.Set("Date", time.Now().UTC().Add(-2*time.Minute).Format(http.TimeFormat))
	if !cm.isExpired(respCCExpired) {
		t.Error("expected response with exceeded max-age to be expired")
	}

	// 4. Check Cache-Control: max-age (fresh)
	respCCFresh := &http.Response{
		Header: make(http.Header),
	}
	respCCFresh.Header.Set("Cache-Control", "max-age=300")
	respCCFresh.Header.Set("Date", time.Now().UTC().Add(-2*time.Minute).Format(http.TimeFormat))
	// 5. Check Cache-Control with other values
	respCCFreshWithOpts := &http.Response{
		Header: make(http.Header),
	}
	respCCFreshWithOpts.Header.Set("Cache-Control", "public, max-age=300")
	respCCFreshWithOpts.Header.Set("Date", time.Now().UTC().Add(-2*time.Minute).Format(http.TimeFormat))
	if cm.isExpired(respCCFreshWithOpts) {
		t.Error("expected response within max-age (with public option) to be fresh")
	}
}

func TestCacheInvalidAndNonGetCases(t *testing.T) {
	cfg := NewDefaultConfig()
	cm := NewCacheManager(cfg)

	// 1. Invalid schemes or URLs
	_, err := cm.GetCachePath("https://example.com")
	if err == nil {
		t.Error("expected error for non-http cache path generation")
	}

	_, err = cm.GetCachePath("http://%4ghst") // invalid escape URL
	if err == nil {
		t.Error("expected error for invalid URL path generation")
	}

	// 2. Non-GET lookup
	reqPost := httptest.NewRequest(http.MethodPost, "http://example.com/", nil)
	if cm.Lookup(reqPost) != nil {
		t.Error("expected nil lookup for non-GET request")
	}

	// 3. Cache-control no-cache/no-store lookup
	reqNoStore := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	reqNoStore.Header.Set("Cache-Control", "no-store")
	if cm.Lookup(reqNoStore) != nil {
		t.Error("expected nil lookup for request with Cache-Control: no-store")
	}

	// 4. Non-GET store: left untouched, no cache file written.
	respMock := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString("test")),
	}
	if err := cm.Store(reqPost, respMock); err != nil {
		t.Errorf("unexpected error on non-GET Store: %v", err)
	}
	if body, _ := io.ReadAll(respMock.Body); string(body) != "test" {
		t.Errorf("non-GET Store should not disturb the body, got %q", body)
	}

	// 5. Cache-control no-store store: left untouched.
	respNoStore := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString("test")),
	}
	respNoStore.Header.Set("Cache-Control", "no-store")
	reqGet := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)

	if err := cm.Store(reqGet, respNoStore); err != nil {
		t.Errorf("unexpected error on no-store Store: %v", err)
	}
	if body, _ := io.ReadAll(respNoStore.Body); string(body) != "test" {
		t.Errorf("no-store Store should not disturb the body, got %q", body)
	}
}

// mkResp builds a 200 response with the given body and headers (key,value pairs).
func mkResp(body string, hdr ...string) *http.Response {
	h := make(http.Header)
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Add(hdr[i], hdr[i+1])
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h,
		Body:   io.NopCloser(bytes.NewBufferString(body)),
	}
}

func fileExistsForURL(t *testing.T, cm *CacheManager, rawURL string) bool {
	t.Helper()
	p, err := cm.GetCachePath(rawURL)
	if err != nil {
		t.Fatalf("GetCachePath(%q): %v", rawURL, err)
	}
	_, err = os.Stat(p)
	return err == nil
}

func TestStoreSkipsSetCookie(t *testing.T) {
	url := "http://example.com/with-cookie"
	req := httptest.NewRequest(http.MethodGet, url, nil)

	// Default config: dontCacheCookies is true -> not written, body preserved.
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)

	resp := mkResp("body", "Cache-Control", "max-age=3600", "Set-Cookie", "sid=abc")
	if err := cm.Store(req, resp); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got, _ := io.ReadAll(resp.Body); string(got) != "body" {
		t.Errorf("body not preserved for skipped Store, got %q", got)
	}
	if fileExistsForURL(t, cm, url) {
		t.Error("Set-Cookie response should not have been cached")
	}

	// dontCacheCookies=false restores the historical behaviour.
	cfg2 := NewDefaultConfig()
	cfg2.DiskCacheRoot = t.TempDir()
	cfg2.DontCacheCookies = false
	cm2 := NewCacheManager(cfg2)
	if err := cm2.Store(req, mkResp("body", "Cache-Control", "max-age=3600", "Set-Cookie", "sid=abc")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !fileExistsForURL(t, cm2, url) {
		t.Error("with dontCacheCookies=false the Set-Cookie response should be cached")
	}
}

func TestStoreSkipsAuthenticated(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)

	url := "http://example.com/private-area"

	// Authorization present, response not explicitly shareable -> not cached.
	reqAuth := httptest.NewRequest(http.MethodGet, url, nil)
	reqAuth.Header.Set("Authorization", "Bearer t0ken")
	if err := cm.Store(reqAuth, mkResp("secret", "Cache-Control", "max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if fileExistsForURL(t, cm, url) {
		t.Error("authenticated request response should not be cached without an explicit directive")
	}

	// Same, but Cache-Control: public -> RFC 9111 3.5 permits caching.
	url2 := "http://example.com/public-but-authed"
	reqAuth2 := httptest.NewRequest(http.MethodGet, url2, nil)
	reqAuth2.Header.Set("Authorization", "Bearer t0ken")
	if err := cm.Store(reqAuth2, mkResp("ok", "Cache-Control", "public, max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !fileExistsForURL(t, cm, url2) {
		t.Error("Cache-Control: public should allow caching an authenticated response")
	}

	// Proxy-Authorization is treated the same way.
	url3 := "http://example.com/proxy-authed"
	reqProxyAuth := httptest.NewRequest(http.MethodGet, url3, nil)
	reqProxyAuth.Header.Set("Proxy-Authorization", "Basic Zm9vOmJhcg==")
	if err := cm.Store(reqProxyAuth, mkResp("secret", "Cache-Control", "max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if fileExistsForURL(t, cm, url3) {
		t.Error("Proxy-Authorization request response should not be cached")
	}
}

func TestDiskCacheDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		tune func(*Config)
	}{
		{"noDiskCache", func(c *Config) { c.NoDiskCache = true }},
		{"emptyRoot", func(c *Config) { c.DiskCacheRoot = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewDefaultConfig()
			cfg.DiskCacheRoot = t.TempDir()
			tc.tune(cfg)
			cm := NewCacheManager(cfg)

			url := "http://example.com/x"
			req := httptest.NewRequest(http.MethodGet, url, nil)
			resp := mkResp("body", "Cache-Control", "max-age=3600")
			if err := cm.Store(req, resp); err != nil {
				t.Fatalf("Store: %v", err)
			}
			if got, _ := io.ReadAll(resp.Body); string(got) != "body" {
				t.Errorf("body not preserved, got %q", got)
			}
			if cm.Lookup(req) != nil {
				t.Error("Lookup should return nil when the disk cache is disabled")
			}
		})
	}
}

func TestExpire(t *testing.T) {
	root := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = root
	cfg.DiskCacheUnlinkTime = 32 * 24 * time.Hour
	cm := NewCacheManager(cfg)

	now := time.Now()
	httpDate := func(d time.Duration) string {
		return now.Add(d).UTC().Format(http.TimeFormat)
	}

	// Fresh: recent Date, long max-age, recent access -> must survive.
	fresh := writeCacheEntry(t, cm, "http://fresh.example.com/a",
		"max-age=3600", httpDate(0), httpDate(0), time.Time{})

	// Stale by HTTP freshness: old Date, short max-age -> removed.
	stale := writeCacheEntry(t, cm, "http://stale.example.com/b",
		"max-age=60", httpDate(-time.Hour), httpDate(0), time.Time{})

	// Fresh by HTTP rules but last touched 40 days ago and no X-Polipo-Access,
	// so the absolute age cap (mtime fallback) removes it.
	old := writeCacheEntry(t, cm, "http://old.example.com/c",
		"max-age=999999", httpDate(0), "", now.Add(-40*24*time.Hour))

	// Corrupt file: unparseable as an HTTP response -> removed.
	corruptDir := filepath.Join(root, "corrupt.example.com")
	if err := os.MkdirAll(corruptDir, 0700); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(corruptDir, "garbage")
	if err := os.WriteFile(corrupt, []byte("this is not an HTTP response"), 0600); err != nil {
		t.Fatal(err)
	}

	stats, err := cm.Expire()
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if stats.Scanned != 4 {
		t.Errorf("Scanned = %d, want 4", stats.Scanned)
	}
	if stats.Removed != 3 {
		t.Errorf("Removed = %d, want 3", stats.Removed)
	}
	if stats.Freed <= 0 {
		t.Errorf("Freed = %d, want > 0", stats.Freed)
	}

	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh entry was removed: %v", err)
	}
	for _, p := range []string{stale, old, corrupt} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", p, err)
		}
	}

	// Emptied host directories are pruned; populated ones remain; root stays.
	if _, err := os.Stat(filepath.Join(root, "stale.example.com")); !os.IsNotExist(err) {
		t.Error("empty host directory should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(root, "fresh.example.com")); err != nil {
		t.Error("non-empty host directory should remain")
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("cache root should not be removed: %v", err)
	}
}

func TestCacheLookupRejections(t *testing.T) {
	root := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = root
	cm := NewCacheManager(cfg)

	// 1. X-Polipo-Location mismatch (hash collision guard): the file lives at
	// the path for urlA but records urlB as its origin.
	urlA := "http://collision.example.com/a"
	pA, err := cm.GetCachePath(urlA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pA), 0700); err != nil {
		t.Fatal(err)
	}
	body := "x"
	mism := &http.Response{
		StatusCode: 200, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	mism.Header.Set("X-Polipo-Location", "http://collision.example.com/DIFFERENT")
	f, err := os.Create(pA)
	if err != nil {
		t.Fatal(err)
	}
	mism.Write(f)
	f.Close()

	if got := cm.Lookup(httptest.NewRequest(http.MethodGet, urlA, nil)); got != nil {
		got.Body.Close()
		t.Error("Lookup should reject an entry whose X-Polipo-Location differs")
	}

	// 2. Expired entry (old Date, tiny max-age).
	urlB := "http://expired.example.com/b"
	writeCacheEntry(t, cm, urlB, "max-age=1",
		time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat),
		time.Now().UTC().Format(http.TimeFormat), time.Time{})
	if got := cm.Lookup(httptest.NewRequest(http.MethodGet, urlB, nil)); got != nil {
		got.Body.Close()
		t.Error("Lookup should reject an expired entry")
	}

	// 3. Corrupt file.
	urlC := "http://corrupt.example.com/c"
	pC, _ := cm.GetCachePath(urlC)
	os.MkdirAll(filepath.Dir(pC), 0700)
	os.WriteFile(pC, []byte("garbage"), 0600)
	if got := cm.Lookup(httptest.NewRequest(http.MethodGet, urlC, nil)); got != nil {
		got.Body.Close()
		t.Error("Lookup should reject a corrupt cache file")
	}

	// 4. Miss: no file at all.
	if got := cm.Lookup(httptest.NewRequest(http.MethodGet, "http://absent.example.com/", nil)); got != nil {
		got.Body.Close()
		t.Error("Lookup should return nil on a cache miss")
	}
}

func TestExpireErrors(t *testing.T) {
	// Unconfigured root.
	cmEmpty := NewCacheManager(&Config{})
	if _, err := cmEmpty.Expire(); err == nil {
		t.Error("expected error for empty diskCacheRoot")
	}

	// Root that does not exist.
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = filepath.Join(t.TempDir(), "missing")
	if _, err := NewCacheManager(cfg).Expire(); err == nil {
		t.Error("expected error for missing diskCacheRoot")
	}

	// Root that is a file, not a directory.
	fp := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(fp, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg2 := NewDefaultConfig()
	cfg2.DiskCacheRoot = fp
	if _, err := NewCacheManager(cfg2).Expire(); err == nil {
		t.Error("expected error when diskCacheRoot is a file")
	}
}

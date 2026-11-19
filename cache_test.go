package main

import (
	"bufio"
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// writeCacheEntry writes a single cache file for rawURL directly on disk, with
// caller-controlled Cache-Control/Date/X-Polipo-Access headers and an optional
// mtime override. It returns the file path. Empty header strings are omitted.
func writeCacheEntry(t *testing.T, cm *CacheManager, rawURL, cacheControl, date, access string, mtime time.Time) string {
	t.Helper()
	p, err := cm.CachePath(rawURL)
	if err != nil {
		t.Fatalf("CachePath(%q): %v", rawURL, err)
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
		path, err := cm.CachePath(tt.urlStr)
		if err != nil {
			t.Errorf("CachePath(%s) unexpected error: %v", tt.urlStr, err)
			continue
		}

		// filepath.Join gives the OS's separator.
		if dir := filepath.FromSlash(tt.expectedDir); !strings.HasPrefix(path, dir) {
			t.Errorf("expected path %q to have prefix %q", path, dir)
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
	if err := storeNow(cm, req, resp); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Verify Store left a replayable body in place for the caller
	storedBody, _ := io.ReadAll(resp.Body)
	if string(storedBody) != bodyText {
		t.Errorf("expected body %q, got %q", bodyText, string(storedBody))
	}

	// Verify file was created
	path, _ := cm.CachePath(targetURL)
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
	expired := func(r *http.Response) bool { return cm.isExpired(r, storedBirth(r)) }

	// 1. Check Expires header in the past
	respPast := &http.Response{
		Header: make(http.Header),
	}
	respPast.Header.Set("Expires", time.Now().UTC().Add(-5*time.Minute).Format(http.TimeFormat))
	if !expired(respPast) {
		t.Error("expected response with past Expires to be expired")
	}

	// 2. Check Expires header in the future
	respFuture := &http.Response{
		Header: make(http.Header),
	}
	respFuture.Header.Set("Expires", time.Now().UTC().Add(5*time.Minute).Format(http.TimeFormat))
	if expired(respFuture) {
		t.Error("expected response with future Expires to be fresh")
	}

	// 3. Check Cache-Control: max-age (expired)
	respCCExpired := &http.Response{
		Header: make(http.Header),
	}
	respCCExpired.Header.Set("Cache-Control", "max-age=60")
	respCCExpired.Header.Set("Date", time.Now().UTC().Add(-2*time.Minute).Format(http.TimeFormat))
	if !expired(respCCExpired) {
		t.Error("expected response with exceeded max-age to be expired")
	}

	// 4. Check Cache-Control: max-age (fresh)
	respCCFresh := &http.Response{
		Header: make(http.Header),
	}
	respCCFresh.Header.Set("Cache-Control", "max-age=300")
	respCCFresh.Header.Set("Date", time.Now().UTC().Add(-2*time.Minute).Format(http.TimeFormat))
	if expired(respCCFresh) {
		t.Error("expected response within max-age to be fresh")
	}

	// 5. Check Cache-Control with other values
	respCCFreshWithOpts := &http.Response{
		Header: make(http.Header),
	}
	respCCFreshWithOpts.Header.Set("Cache-Control", "public, max-age=300")
	respCCFreshWithOpts.Header.Set("Date", time.Now().UTC().Add(-2*time.Minute).Format(http.TimeFormat))
	if expired(respCCFreshWithOpts) {
		t.Error("expected response within max-age (with public option) to be fresh")
	}

	mkR := func(hdr ...string) *http.Response {
		h := make(http.Header)
		for i := 0; i+1 < len(hdr); i += 2 {
			h.Set(hdr[i], hdr[i+1])
		}
		return &http.Response{Header: h}
	}
	httpTime := func(d time.Duration) string {
		return time.Now().UTC().Add(d).Format(http.TimeFormat)
	}

	// 6. s-maxage takes precedence over max-age for a shared cache.
	if !expired(mkR(
		"Cache-Control", "max-age=100000, s-maxage=10",
		"Date", httpTime(-time.Minute),
	)) {
		t.Error("s-maxage=10 with a 1-minute-old Date should be expired despite max-age=100000")
	}

	// 7. Invalid Expires ("0") is treated as already expired.
	if !expired(mkR("Expires", "0", "Date", httpTime(0))) {
		t.Error(`Expires: "0" should be treated as expired`)
	}

	// 8. Heuristic: no Expires/max-age, Last-Modified far in the past.
	// Lifetime = min(10% of age, 24h) = 24h; a 2-day-old Date is stale.
	if !expired(mkR(
		"Last-Modified", httpTime(-100*24*time.Hour),
		"Date", httpTime(-2*24*time.Hour),
	)) {
		t.Error("header-less response 2 days old (24h heuristic cap) should be expired")
	}
	// Same document fetched just now is still fresh.
	if expired(mkR(
		"Last-Modified", httpTime(-100*24*time.Hour),
		"Date", httpTime(-time.Minute),
	)) {
		t.Error("header-less response fetched a minute ago should be fresh under the heuristic")
	}

	// 9. Heuristic with a recent Last-Modified: age at fetch = 1h, lifetime = 6m.
	// A Date 30 minutes ago is well past that.
	if !expired(mkR(
		"Last-Modified", httpTime(-90*time.Minute),
		"Date", httpTime(-30*time.Minute),
	)) {
		t.Error("header-less response older than 10% of its Last-Modified age should be expired")
	}
	// Fetched 2 minutes ago, still inside the 6m lifetime.
	if expired(mkR(
		"Last-Modified", httpTime(-62*time.Minute),
		"Date", httpTime(-2*time.Minute),
	)) {
		t.Error("header-less response inside 10% of its Last-Modified age should be fresh")
	}

	// 10. No Last-Modified either: no heuristic lifetime, stale at once.
	if !expired(mkR("Date", httpTime(0))) {
		t.Error("header-less response with no Last-Modified should be stale at once")
	}
	if !expired(mkR("Date", httpTime(-time.Minute), "ETag", `"v1"`)) {
		t.Error("an ETag alone should not give a header-less response a heuristic lifetime")
	}

	// 11. max-age overrides Expires (RFC 9111 section 5.3), even a past one.
	if expired(mkR(
		"Cache-Control", "max-age=600",
		"Expires", httpTime(-time.Minute),
		"Date", httpTime(-time.Minute),
	)) {
		t.Error("max-age=600 on a 1-minute-old response should win over a past Expires")
	}

	// 12. Expires is measured from Date, not against the local clock: a
	// 60-second lifetime received 2 minutes ago has run out, even though the
	// local clock has not reached Expires, because the origin's clock is 5
	// minutes fast.
	if !expired(mkR(
		"Expires", httpTime(5*time.Minute-2*time.Minute+time.Minute),
		"Date", httpTime(5*time.Minute-2*time.Minute),
		"X-Polipo-Date", httpTime(-2*time.Minute),
	)) {
		t.Error("Expires 60s after Date should be expired 2 minutes after receipt")
	}
}

// TestStoreCountsAge checks that a response's Age - how long it had already
// sat in a cache upstream - counts towards its current age (RFC 9111 section
// 4.2.3), in both the disk and the memory cache, as Polipo's object->age did.
func TestStoreCountsAge(t *testing.T) {
	for _, memEntries := range []int{0, 1024} {
		for _, tc := range []struct {
			name, cc, age string
			fresh         bool
		}{
			{"Age beyond max-age", "max-age=60", "100", false},
			{"Age within max-age", "max-age=600", "100", true},
			{"no Age", "max-age=60", "", true},
			{"unparseable Age is ignored", "max-age=60", "soon", true},
		} {
			cfg := NewDefaultConfig()
			cfg.DiskCacheRoot = t.TempDir()
			cfg.MemoryCacheEntries = memEntries
			cm := NewCacheManager(cfg)

			url := "http://example.com/aged"
			req := httptest.NewRequest(http.MethodGet, url, nil)
			resp := mkResp("body", "Cache-Control", tc.cc, "Date", time.Now().UTC().Format(http.TimeFormat))
			if tc.age != "" {
				resp.Header.Set("Age", tc.age)
			}
			if err := storeNow(cm, req, resp); err != nil {
				t.Fatalf("%s (memoryCacheEntries=%d): Store: %v", tc.name, memEntries, err)
			}

			// Both from wherever Lookup finds it first, and from disk alone.
			for _, fromDisk := range []bool{false, true} {
				if fromDisk {
					cm.DiscardMemory()
				}
				got := cm.Lookup(req)
				if got != nil {
					got.Body.Close()
				}
				if (got != nil) != tc.fresh {
					t.Errorf("%s (memoryCacheEntries=%d, fromDisk=%v): fresh = %v, want %v",
						tc.name, memEntries, fromDisk, got != nil, tc.fresh)
				}
			}
		}
	}
}

// testT0 is a fixed instant for the table tests of the age and freshness
// helpers below, so that their results do not depend on when they run.
var testT0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// at formats testT0+d as an HTTP date.
func at(d time.Duration) string { return testT0.Add(d).Format(http.TimeFormat) }

func TestResponseBirth(t *testing.T) {
	tests := []struct {
		name string
		hdr  []string
		want time.Time
	}{
		{"no Age or Date", nil, testT0},
		{"Age is subtracted", []string{"Age", "100"}, testT0.Add(-100 * time.Second)},
		{"Age with blanks", []string{"Age", " 100 "}, testT0.Add(-100 * time.Second)},
		{"zero Age", []string{"Age", "0"}, testT0},
		{"unparseable Age is ignored", []string{"Age", "soon"}, testT0},
		{"negative Age is ignored", []string{"Age", "-5"}, testT0},
		// Without the cap, the multiplication by time.Second would overflow.
		{"huge Age is capped", []string{"Age", "9223372036854775807"}, testT0.Add(-maxAgeValue * time.Second)},
		{"earlier Date wins", []string{"Age", "20", "Date", at(-30 * time.Second)}, testT0.Add(-30 * time.Second)},
		{"Date later than request time less Age", []string{"Age", "100", "Date", at(-50 * time.Second)}, testT0.Add(-100 * time.Second)},
		{"future Date is ignored", []string{"Date", at(time.Hour)}, testT0},
		{"unparseable Date is ignored", []string{"Date", "yesterday"}, testT0},
	}
	for _, tt := range tests {
		if got := responseBirth(mkResp("", tt.hdr...).Header, testT0); !got.Equal(tt.want) {
			t.Errorf("%s: responseBirth = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestStoredBirth(t *testing.T) {
	tests := []struct {
		name string
		hdr  []string
		want time.Time
	}{
		{"X-Polipo-Date earlier than Date", []string{"Date", at(0), "X-Polipo-Date", at(-time.Minute)}, testT0.Add(-time.Minute)},
		// Before go-polipo stored the birth, X-Polipo-Date held the time of writing.
		{"X-Polipo-Date later than Date", []string{"Date", at(0), "X-Polipo-Date", at(time.Minute)}, testT0},
		{"X-Polipo-Date equal to Date", []string{"Date", at(0), "X-Polipo-Date", at(0)}, testT0},
		// Polipo omitted X-Polipo-Date when it equalled Date.
		{"no X-Polipo-Date", []string{"Date", at(0)}, testT0},
		{"unparseable X-Polipo-Date", []string{"Date", at(0), "X-Polipo-Date", "soon"}, testT0},
		{"no Date", []string{"X-Polipo-Date", at(0)}, testT0},
	}
	for _, tt := range tests {
		if got := storedBirth(mkResp("", tt.hdr...)); !got.Equal(tt.want) {
			t.Errorf("%s: storedBirth = %v, want %v", tt.name, got, tt.want)
		}
	}

	// With neither header, the entry counts as born now.
	before := time.Now()
	got := storedBirth(mkResp(""))
	if got.Before(before) || got.After(time.Now()) {
		t.Errorf("no Date or X-Polipo-Date: storedBirth = %v, want the current time", got)
	}
}

func TestHeuristicFreshnessLifetime(t *testing.T) {
	tests := []struct {
		name string
		lm   string // Last-Modified; "" for none
		want time.Duration
	}{
		{"no Last-Modified", "", 0},
		{"unparseable Last-Modified", "long ago", 0},
		{"Last-Modified equal to Date", at(0), 0},
		{"Last-Modified after Date", at(time.Minute), 0},
		{"10% of the age", at(-time.Hour), 6 * time.Minute},
		{"10% of a short age", at(-10 * time.Second), time.Second},
		{"10% reaching the cap exactly", at(-240 * time.Hour), heuristicMaxLifetime},
		{"capped", at(-1000 * time.Hour), heuristicMaxLifetime},
	}
	for _, tt := range tests {
		var hdr []string
		if tt.lm != "" {
			hdr = []string{"Last-Modified", tt.lm}
		}
		if got := heuristicFreshnessLifetime(mkResp("", hdr...), testT0); got != tt.want {
			t.Errorf("%s: heuristicFreshnessLifetime = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFreshnessLifetime(t *testing.T) {
	tests := []struct {
		name string
		hdr  []string
		want time.Duration
	}{
		{"max-age", []string{"Cache-Control", "max-age=600"}, 600 * time.Second},
		{"s-maxage beats max-age", []string{"Cache-Control", "max-age=600, s-maxage=60"}, 60 * time.Second},
		{"max-age beats Expires", []string{"Cache-Control", "max-age=600", "Date", at(0), "Expires", at(time.Hour)}, 600 * time.Second},
		{"Expires less Date", []string{"Date", at(0), "Expires", at(time.Hour)}, time.Hour},
		// The origin's clock is 5 minutes fast; only the difference counts.
		{"Expires measured from Date, not birth", []string{"Date", at(5 * time.Minute), "Expires", at(6 * time.Minute)}, time.Minute},
		{"Expires before Date", []string{"Date", at(0), "Expires", at(-time.Minute)}, -time.Minute},
		{"invalid Expires", []string{"Date", at(0), "Expires", "0"}, 0},
		{"Expires without Date is measured from birth", []string{"Expires", at(time.Hour)}, time.Hour},
		{"heuristic from Date", []string{"Date", at(0), "Last-Modified", at(-time.Hour)}, 6 * time.Minute},
		{"heuristic without Date uses birth", []string{"Last-Modified", at(-time.Hour)}, 6 * time.Minute},
		{"nothing to go on", []string{"Date", at(0)}, 0},
	}
	for _, tt := range tests {
		if got := freshnessLifetime(mkResp("", tt.hdr...), testT0); got != tt.want {
			t.Errorf("%s: freshnessLifetime = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestRequestCacheControlBoundsFreshness: a request's max-age, min-fresh and
// max-stale narrow or widen which stored response it accepts (RFC 9111
// section 5.2.1), as in Polipo's objectIsStale - in both the disk and the
// memory cache. Every entry here is 100 seconds old.
func TestRequestCacheControlBoundsFreshness(t *testing.T) {
	for _, tc := range []struct {
		respCC, reqCC string
		fresh         bool
	}{
		{"max-age=600", "", true},
		{"max-age=600", "max-age=0", false}, // a browser reload
		{"max-age=600", "MAX-AGE=0", false},
		{"max-age=600", "max-age=50", false},
		{"max-age=600", "max-age=200", true},
		{"max-age=600", `max-age="50"`, false},
		{"max-age=600", "max-age=-1", true}, // invalid, ignored
		{"max-age=600", "min-fresh=600", false},
		{"max-age=600", "min-fresh=100", true},
		{"max-age=60", "", false},
		{"max-age=60", "max-stale", true},
		{"max-age=60", "max-stale=60", true},
		{"max-age=60", "max-stale=10", false},
		{"max-age=60", "max-stale=abc", false}, // invalid, ignored
		{"max-age=60", "max-stale=-1", false},  // invalid, ignored
		{"max-age=60", "max-stale, max-age=50", false},
		{"max-age=60", "max-stale, min-fresh=0", false}, // min-fresh wins
		{"max-age=60, must-revalidate", "max-stale", false},
		{"max-age=60, proxy-revalidate", "max-stale", false},
		{"s-maxage=60", "max-stale", false},
		{"max-age=600, no-cache", "max-stale", false},
	} {
		for _, memEntries := range []int{0, 1024} {
			cfg := NewDefaultConfig()
			cfg.DiskCacheRoot = t.TempDir()
			cfg.MemoryCacheEntries = memEntries
			cm := NewCacheManager(cfg)

			url := "http://example.com/bounded"
			resp := mkResp("body", "Cache-Control", tc.respCC, "Date", time.Now().UTC().Add(-100*time.Second).Format(http.TimeFormat))
			if err := storeNow(cm, httptest.NewRequest(http.MethodGet, url, nil), resp); err != nil {
				t.Fatalf("Store: %v", err)
			}

			for _, fromDisk := range []bool{false, true} {
				if fromDisk {
					cm.DiscardMemory()
				}
				req := httptest.NewRequest(http.MethodGet, url, nil)
				if tc.reqCC != "" {
					req.Header.Set("Cache-Control", tc.reqCC)
				}
				got := cm.Lookup(req)
				if got != nil {
					got.Body.Close()
				}
				if (got != nil) != tc.fresh {
					t.Errorf("response %q, request %q (memoryCacheEntries=%d, fromDisk=%v): fresh = %v, want %v",
						tc.respCC, tc.reqCC, memEntries, fromDisk, got != nil, tc.fresh)
				}
			}
		}
	}
}

// TestDiskEntryTimesAreUTC: the times Store writes into a disk entry are
// real GMT, as http.TimeFormat says, whatever the local time zone. In a zone
// ahead of UTC, a local clock reading labelled GMT puts X-Polipo-Access in
// the future, and `polipo -x` then keeps unused entries that much longer.
func TestDiskEntryTimesAreUTC(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = saved })

	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)

	url := "http://example.com/zoned"
	resp := mkResp("body", "Cache-Control", "max-age=600", "Date", time.Now().UTC().Format(http.TimeFormat))
	if err := storeNow(cm, httptest.NewRequest(http.MethodGet, url, nil), resp); err != nil {
		t.Fatalf("Store: %v", err)
	}
	p, err := cm.CachePath(url)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"X-Polipo-Date", "X-Polipo-Access"} {
		got, err := http.ParseTime(stored.Header.Get(name))
		if err != nil {
			t.Errorf("%s = %q: %v", name, stored.Header.Get(name), err)
			continue
		}
		if d := time.Since(got); d < -time.Minute || d > time.Minute {
			t.Errorf("%s = %q, %s away from now; want the current time in GMT", name, stored.Header.Get(name), -d)
		}
	}
}

// TestExpireCountsAge: `polipo -x` judges a disk entry's freshness by the
// same age as Lookup, so an entry whose upstream Age already used up its
// max-age is removed.
func TestExpireCountsAge(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)

	url := "http://example.com/aged"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp := mkResp("body", "Cache-Control", "max-age=60", "Age", "100", "Date", time.Now().UTC().Format(http.TimeFormat))
	if err := storeNow(cm, req, resp); err != nil {
		t.Fatalf("Store: %v", err)
	}
	stats, err := cm.Expire()
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if stats.Removed != 1 || fileExistsForURL(t, cm, url) {
		t.Errorf("Expire removed %d entries; want the aged-out entry removed", stats.Removed)
	}
}

func TestCacheInvalidAndNonGetCases(t *testing.T) {
	cfg := NewDefaultConfig()
	cm := NewCacheManager(cfg)

	// 1. Invalid schemes or URLs
	_, err := cm.CachePath("https://example.com")
	if err == nil {
		t.Error("expected error for non-http cache path generation")
	}

	_, err = cm.CachePath("http://%4ghst") // invalid escape URL
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
	if err := storeNow(cm, reqPost, respMock); err != nil {
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

	if err := storeNow(cm, reqGet, respNoStore); err != nil {
		t.Errorf("unexpected error on no-store Store: %v", err)
	}
	if body, _ := io.ReadAll(respNoStore.Body); string(body) != "test" {
		t.Errorf("no-store Store should not disturb the body, got %q", body)
	}
}

// mkResp builds a 200 response with the given body and headers (key,value pairs).
// storeNow calls Store and then reads the response body to EOF, as a client
// would, so the entry is committed before storeNow returns. resp.Body is
// replaced with a fresh reader over the same bytes, so a test can still read
// what the client received.
func storeNow(cm *CacheManager, req *http.Request, resp *http.Response) error {
	cm.Store(req, resp)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return err
}

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
	p, err := cm.CachePath(rawURL)
	if err != nil {
		t.Fatalf("CachePath(%q): %v", rawURL, err)
	}
	_, err = os.Stat(p)
	return err == nil
}

// TestStoreSkipsRequestNoStore: a request carrying Cache-Control: no-store
// forbids storing the response to it (RFC 9111 section 5.2.1.5), in memory
// and on disk; the client still gets the whole body.
func TestStoreSkipsRequestNoStore(t *testing.T) {
	for _, cc := range []string{"no-store", "max-age=0, No-Store"} {
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = t.TempDir()
		cm := NewCacheManager(cfg)

		url := "http://example.com/private"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Cache-Control", cc)
		resp := mkResp("body", "Cache-Control", "max-age=600", "Date", time.Now().UTC().Format(http.TimeFormat))
		if err := storeNow(cm, req, resp); err != nil {
			t.Fatalf("%q: Store: %v", cc, err)
		}
		if body, _ := io.ReadAll(resp.Body); string(body) != "body" {
			t.Errorf("%q: client got %q, want the whole body", cc, body)
		}
		if cm.memCache.Contains(url) || fileExistsForURL(t, cm, url) {
			t.Errorf("%q: the response was stored", cc)
		}
	}
}

// Cache-Control is read from every header line (RFC 9110 section 5.3) and
// with case-insensitive directive names (RFC 9111 section 5.2), as Polipo's
// http_parse.c did. Before, several checks read only the first line with
// Header.Get and matched case-sensitively, so a private response could be
// stored in the shared cache.
func TestStoreReadsEveryCacheControlLine(t *testing.T) {
	date := time.Now().UTC().Format(http.TimeFormat)
	tests := []struct {
		name       string
		auth       bool     // request carries Authorization
		cc         []string // response Cache-Control lines
		wantStored bool
	}{
		{"private on second line", false, []string{"public, max-age=600", "private"}, false},
		{"Private in capitals", false, []string{"Private, max-age=600"}, false},
		{"no-store on second line", false, []string{"max-age=600", "no-store"}, false},
		{"No-Store in capitals", false, []string{"No-Store, max-age=600"}, false},
		{"authenticated, public on second line", true, []string{"max-age=600", "public"}, true},
		{"authenticated, nothing allowing it", true, []string{"max-age=600"}, false},
	}
	for _, tt := range tests {
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = t.TempDir()
		cm := NewCacheManager(cfg)

		url := "http://example.com/page"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if tt.auth {
			req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		}
		hdr := []string{"Date", date}
		for _, line := range tt.cc {
			hdr = append(hdr, "Cache-Control", line)
		}
		resp := mkResp("body", hdr...)
		if err := storeNow(cm, req, resp); err != nil {
			t.Fatalf("%s: Store: %v", tt.name, err)
		}
		stored := cm.memCache.Contains(url) || fileExistsForURL(t, cm, url)
		if stored != tt.wantStored {
			t.Errorf("%s: stored = %t, want %t", tt.name, stored, tt.wantStored)
		}
	}
}

// The freshness lifetime is read from every Cache-Control line and
// case-insensitively; s-maxage wins over max-age wherever it appears; and a
// present but invalid value makes the response stale (RFC 9111 section
// 4.2.1) instead of being skipped in favour of Expires.
func TestFreshnessLifetimeReadsEveryCacheControlLine(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour).Format(http.TimeFormat)
	tests := []struct {
		name string
		hdr  []string
		want time.Duration
	}{
		{"max-age on second line", []string{"Cache-Control", "public", "Cache-Control", "max-age=600"}, 600 * time.Second},
		{"Max-Age in capitals", []string{"Cache-Control", "Max-Age=600"}, 600 * time.Second},
		{"s-maxage on second line wins", []string{"Cache-Control", "max-age=600", "Cache-Control", "s-maxage=60"}, 60 * time.Second},
		{"invalid max-age is stale, not Expires", []string{"Cache-Control", "max-age=soon", "Expires", future}, 0},
		{"negative max-age is stale", []string{"Cache-Control", "max-age=-1", "Expires", future}, 0},
	}
	for _, tt := range tests {
		resp := mkResp("", append([]string{"Date", now.Format(http.TimeFormat)}, tt.hdr...)...)
		if got := freshnessLifetime(resp, now); got != tt.want {
			t.Errorf("%s: freshnessLifetime = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// A request's no-cache or no-store bypasses the cache whatever its case and
// whichever Cache-Control line carries it.
func TestLookupReadsEveryRequestCacheControlLine(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	url := "http://example.com/fresh"
	resp := mkResp("body", "Cache-Control", "max-age=600", "Date", time.Now().UTC().Format(http.TimeFormat))
	if err := storeNow(cm, httptest.NewRequest(http.MethodGet, url, nil), resp); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if cm.Lookup(httptest.NewRequest(http.MethodGet, url, nil)) == nil {
		t.Fatal("a plain request missed the fresh entry")
	}
	for _, lines := range [][]string{{"No-Cache"}, {"NO-STORE"}, {"max-stale=60", "no-cache"}} {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		for _, line := range lines {
			req.Header.Add("Cache-Control", line)
		}
		if got := cm.Lookup(req); got != nil {
			got.Body.Close()
			t.Errorf("Cache-Control %q: served from the cache, want a bypass", lines)
		}
	}
}

func TestStoreSkipsSetCookie(t *testing.T) {
	url := "http://example.com/with-cookie"
	req := httptest.NewRequest(http.MethodGet, url, nil)

	// Default config: dontCacheCookies is true -> not written, body preserved.
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)

	resp := mkResp("body", "Cache-Control", "max-age=3600", "Set-Cookie", "sid=abc")
	if err := storeNow(cm, req, resp); err != nil {
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
	if err := storeNow(cm2, req, mkResp("body", "Cache-Control", "max-age=3600", "Set-Cookie", "sid=abc")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !fileExistsForURL(t, cm2, url) {
		t.Error("with dontCacheCookies=false the Set-Cookie response should be cached")
	}
}

func TestStoreCachesVaryButAlwaysStale(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)

	// A non-empty Vary is written to disk like any other response (matching
	// Polipo's mindlesslyCacheVary=false default: negotiated instances are
	// still cached, just always revalidated), but must never come back from
	// a fresh, no-round-trip Lookup - only LookupStale, and marked stale.
	for _, vary := range []string{"Accept-Encoding", "Accept-Language", "*", "  User-Agent  "} {
		url := "http://example.com/vary/" + strings.ReplaceAll(strings.TrimSpace(vary), " ", "_")
		req := httptest.NewRequest(http.MethodGet, url, nil)
		resp := mkResp("variant", "Cache-Control", "max-age=3600", "ETag", `"v1"`, "Vary", vary)
		if err := storeNow(cm, req, resp); err != nil {
			t.Fatalf("Store(Vary: %q): %v", vary, err)
		}
		if got, _ := io.ReadAll(resp.Body); string(got) != "variant" {
			t.Errorf("Vary: %q: body not preserved, got %q", vary, got)
		}
		if !fileExistsForURL(t, cm, url) {
			t.Errorf("Vary: %q response should still be cached", vary)
		}

		lookupReq := httptest.NewRequest(http.MethodGet, url, nil)
		if cachedResp := cm.Lookup(lookupReq); cachedResp != nil {
			cachedResp.Body.Close()
			t.Errorf("Vary: %q: Lookup should never report a Vary'd entry fresh", vary)
		}
		// Not even for a request that accepts any staleness.
		maxStaleReq := httptest.NewRequest(http.MethodGet, url, nil)
		maxStaleReq.Header.Set("Cache-Control", "max-stale")
		if cachedResp := cm.Lookup(maxStaleReq); cachedResp != nil {
			cachedResp.Body.Close()
			t.Errorf("Vary: %q: Lookup with max-stale should not report a Vary'd entry fresh", vary)
		}
		staleResp, stale := cm.LookupStale(lookupReq)
		if staleResp == nil {
			t.Errorf("Vary: %q: LookupStale should still find the entry", vary)
		} else {
			staleResp.Body.Close()
		}
		if !stale {
			t.Errorf("Vary: %q: LookupStale should report the entry stale", vary)
		}
	}

	// An empty Vary value is treated as absent: cached and servable fresh,
	// same as a response with no Vary at all.
	url := "http://example.com/vary/empty"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if err := storeNow(cm, req, mkResp("body", "Cache-Control", "max-age=3600", "Vary", "")); err != nil {
		t.Fatalf("Store(empty Vary): %v", err)
	}
	if !fileExistsForURL(t, cm, url) {
		t.Error("a response with an empty Vary should still be cached")
	}
	lookupReq := httptest.NewRequest(http.MethodGet, url, nil)
	if cachedResp := cm.Lookup(lookupReq); cachedResp == nil {
		t.Error("a response with an empty Vary should be servable fresh")
	} else {
		cachedResp.Body.Close()
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
	if err := storeNow(cm, reqAuth, mkResp("secret", "Cache-Control", "max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if fileExistsForURL(t, cm, url) {
		t.Error("authenticated request response should not be cached without an explicit directive")
	}

	// Same, but Cache-Control: public -> RFC 9111 3.5 permits caching.
	url2 := "http://example.com/public-but-authed"
	reqAuth2 := httptest.NewRequest(http.MethodGet, url2, nil)
	reqAuth2.Header.Set("Authorization", "Bearer t0ken")
	if err := storeNow(cm, reqAuth2, mkResp("ok", "Cache-Control", "public, max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !fileExistsForURL(t, cm, url2) {
		t.Error("Cache-Control: public should allow caching an authenticated response")
	}

	// Proxy-Authorization is unlike Authorization: it authenticates the
	// client to go-polipo itself (see ProxyHandler.isAuthorized), is stripped
	// before the request goes upstream, and says nothing about whether the
	// origin's response may be shared - so it must not block caching. Once
	// authCredentials is configured, every legitimate request carries
	// Proxy-Authorization; treating it like Authorization here would make
	// almost nothing cacheable. Polipo agrees: its CACHE_AUTHORIZATION flag
	// is only ever set for Authorization (http_parse.c).
	url3 := "http://example.com/proxy-authed"
	reqProxyAuth := httptest.NewRequest(http.MethodGet, url3, nil)
	reqProxyAuth.Header.Set("Proxy-Authorization", "Basic Zm9vOmJhcg==")
	if err := storeNow(cm, reqProxyAuth, mkResp("secret", "Cache-Control", "max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !fileExistsForURL(t, cm, url3) {
		t.Error("Proxy-Authorization request response should still be cached")
	}
}

func TestStoreSkipsOversizedEntries(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.MaxDiskCacheEntrySize = 5
	cm := NewCacheManager(cfg)

	// Declared (Content-Length) size over the limit: rejected before the
	// body is even read - resp.Body must come back completely untouched.
	t.Run("oversized Content-Length", func(t *testing.T) {
		url := "http://example.com/big-declared"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		resp := mkResp("this body is way over 5 bytes", "Cache-Control", "max-age=3600")
		resp.ContentLength = int64(len("this body is way over 5 bytes"))

		if err := storeNow(cm, req, resp); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if fileExistsForURL(t, cm, url) {
			t.Error("oversized entry should not have been cached")
		}
		got, err := io.ReadAll(resp.Body)
		if err != nil || string(got) != "this body is way over 5 bytes" {
			t.Errorf("body not preserved for a caller that never gets a cache hit: %q, %v", got, err)
		}
	})

	// No declared size (as when Content-Length is absent/chunked, mkResp's
	// default): the fallback check on the actual bytes read must still
	// catch it.
	t.Run("oversized with no declared Content-Length", func(t *testing.T) {
		url := "http://example.com/big-undeclared"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		resp := mkResp("also way over the limit", "Cache-Control", "max-age=3600")

		if err := storeNow(cm, req, resp); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if fileExistsForURL(t, cm, url) {
			t.Error("oversized entry should not have been cached")
		}
		got, err := io.ReadAll(resp.Body)
		if err != nil || string(got) != "also way over the limit" {
			t.Errorf("body not preserved: %q, %v", got, err)
		}
	})

	// Under the limit: cached as usual.
	t.Run("under the limit is cached", func(t *testing.T) {
		url := "http://example.com/small"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		resp := mkResp("ok", "Cache-Control", "max-age=3600")

		if err := storeNow(cm, req, resp); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if !fileExistsForURL(t, cm, url) {
			t.Error("entry under the limit should have been cached")
		}
	})

	// -1 (the default) means unlimited: nothing is rejected on size.
	t.Run("-1 disables the limit", func(t *testing.T) {
		cfg2 := NewDefaultConfig()
		cfg2.DiskCacheRoot = t.TempDir()
		cm2 := NewCacheManager(cfg2)
		url := "http://example.com/unlimited"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		resp := mkResp("this body is way over 5 bytes", "Cache-Control", "max-age=3600")
		resp.ContentLength = int64(len("this body is way over 5 bytes"))

		if err := storeNow(cm2, req, resp); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if !fileExistsForURL(t, cm2, url) {
			t.Error("with maxDiskCacheEntrySize=-1 nothing should be rejected on size")
		}
	})
}

// TestDiskCacheDisabled covers both halves of MemoryCacheEntries's
// independence from the disk cache (see Config.MemoryCacheEntries): turning
// off disk caching alone still lets the memory cache serve a hit, and only
// turning off both yields a true miss.
func TestDiskCacheDisabled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tune    func(*Config)
		wantHit bool
	}{
		{"noDiskCache", func(c *Config) { c.NoDiskCache = true; c.MemoryCacheEntries = 0 }, false},
		{"emptyRoot", func(c *Config) { c.DiskCacheRoot = ""; c.MemoryCacheEntries = 0 }, false},
		{"noDiskCacheMemoryStillServes", func(c *Config) { c.NoDiskCache = true }, true},
		{"emptyRootMemoryStillServes", func(c *Config) { c.DiskCacheRoot = "" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewDefaultConfig()
			cfg.DiskCacheRoot = t.TempDir()
			tc.tune(cfg)
			cm := NewCacheManager(cfg)

			url := "http://example.com/x"
			req := httptest.NewRequest(http.MethodGet, url, nil)
			resp := mkResp("body", "Cache-Control", "max-age=3600")
			if err := storeNow(cm, req, resp); err != nil {
				t.Fatalf("Store: %v", err)
			}
			if got, _ := io.ReadAll(resp.Body); string(got) != "body" {
				t.Errorf("body not preserved, got %q", got)
			}
			hit := cm.Lookup(req) != nil
			if hit != tc.wantHit {
				t.Errorf("Lookup hit = %v, want %v (memoryCacheEntries=%d)", hit, tc.wantHit, cfg.MemoryCacheEntries)
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
	pA, err := cm.CachePath(urlA)
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
	pC, _ := cm.CachePath(urlC)
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

	// 5. Entry carrying Vary (e.g. written by Polipo or an older go-polipo):
	// cannot be matched to the request, so it must not be served.
	urlV := "http://vary.example.com/v"
	pV, _ := cm.CachePath(urlV)
	os.MkdirAll(filepath.Dir(pV), 0700)
	varied := &http.Response{
		StatusCode: 200, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader("v")),
		ContentLength: 1,
	}
	varied.Header.Set("X-Polipo-Location", urlV)
	varied.Header.Set("Cache-Control", "max-age=3600")
	varied.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	varied.Header.Set("Vary", "Accept-Encoding")
	fV, err := os.Create(pV)
	if err != nil {
		t.Fatal(err)
	}
	varied.Write(fV)
	fV.Close()
	if got := cm.Lookup(httptest.NewRequest(http.MethodGet, urlV, nil)); got != nil {
		got.Body.Close()
		t.Error("Lookup should reject a cached entry that carries Vary")
	}
}

func TestExpireRemovesLargeStaleEntries(t *testing.T) {
	now := time.Now()
	httpDate := func(d time.Duration) string { return now.Add(d).UTC().Format(http.TimeFormat) }

	// "cached body" (writeCacheEntry's fixed body) is 11 bytes. Unused for
	// 48h: past DiskCacheTruncateTime, nowhere near the 32-day
	// DiskCacheUnlinkTime default, and fresh by HTTP rules (max-age high,
	// Date now) - the only thing that can remove it is the size policy.
	mtime := now.Add(-48 * time.Hour)

	t.Run("larger than diskCacheTruncateSize is removed early", func(t *testing.T) {
		root := t.TempDir()
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = root
		cfg.DiskCacheTruncateTime = 24 * time.Hour
		cfg.DiskCacheTruncateSize = 5 // smaller than the 11-byte body
		cm := NewCacheManager(cfg)

		p := writeCacheEntry(t, cm, "http://large.example.com/a",
			"max-age=999999", httpDate(0), "", mtime)

		stats, err := cm.Expire()
		if err != nil {
			t.Fatalf("Expire: %v", err)
		}
		if stats.Removed != 1 || stats.RemovedForSize != 1 {
			t.Errorf("stats = %+v, want Removed=1 RemovedForSize=1", stats)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected large stale entry to be removed, stat err = %v", err)
		}
	})

	t.Run("smaller than diskCacheTruncateSize survives", func(t *testing.T) {
		root := t.TempDir()
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = root
		cfg.DiskCacheTruncateTime = 24 * time.Hour
		// info.Size() is the whole on-disk file (status line + headers +
		// body), not just the 11-byte body, so the threshold needs enough
		// headroom above that to land on the "small" side.
		cfg.DiskCacheTruncateSize = 10000
		cm := NewCacheManager(cfg)

		p := writeCacheEntry(t, cm, "http://small.example.com/a",
			"max-age=999999", httpDate(0), "", mtime)

		stats, err := cm.Expire()
		if err != nil {
			t.Fatalf("Expire: %v", err)
		}
		if stats.Removed != 0 {
			t.Errorf("stats = %+v, want Removed=0", stats)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected small entry to survive: %v", err)
		}
	})

	t.Run("diskCacheTruncateSize=0 disables the policy", func(t *testing.T) {
		root := t.TempDir()
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = root
		cfg.DiskCacheTruncateTime = 24 * time.Hour
		cfg.DiskCacheTruncateSize = 0
		cm := NewCacheManager(cfg)

		p := writeCacheEntry(t, cm, "http://large.example.com/a",
			"max-age=999999", httpDate(0), "", mtime)

		stats, err := cm.Expire()
		if err != nil {
			t.Fatalf("Expire: %v", err)
		}
		if stats.Removed != 0 {
			t.Errorf("stats = %+v, want Removed=0", stats)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected entry to survive with the policy disabled: %v", err)
		}
	})
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

// TestMemoryCacheServesWithoutDisk proves a hit actually comes from the
// memory cache, not disk: the on-disk copy is deleted before the Lookup that
// is expected to still succeed.
func TestMemoryCacheServesWithoutDisk(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)

	url := "http://example.com/memhit"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp := mkResp("memory body", "Cache-Control", "max-age=3600")
	if err := storeNow(cm, req, resp); err != nil {
		t.Fatalf("Store: %v", err)
	}

	path, err := cm.CachePath(url)
	if err != nil {
		t.Fatalf("CachePath: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing disk copy: %v", err)
	}

	got := cm.Lookup(req)
	if got == nil {
		t.Fatal("Lookup returned nil; memory cache did not serve the hit")
	}
	defer got.Body.Close()
	body, _ := io.ReadAll(got.Body)
	if string(body) != "memory body" {
		t.Errorf("body = %q, want %q", body, "memory body")
	}
}

// TestMemoryCacheIndependentOfNoDiskCache locks in the design decision that
// MemoryCacheEntries works whether or not the disk cache is enabled.
func TestMemoryCacheIndependentOfNoDiskCache(t *testing.T) {
	for _, tc := range []struct {
		name string
		tune func(*Config)
	}{
		{"noDiskCache", func(c *Config) { c.NoDiskCache = true }},
		{"emptyDiskCacheRoot", func(c *Config) { c.DiskCacheRoot = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewDefaultConfig()
			cfg.DiskCacheRoot = t.TempDir()
			tc.tune(cfg)
			cm := NewCacheManager(cfg)

			url := "http://example.com/x"
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if err := storeNow(cm, req, mkResp("body", "Cache-Control", "max-age=3600")); err != nil {
				t.Fatalf("Store: %v", err)
			}
			if !cm.memCache.Contains(url) {
				t.Fatal("response was not admitted to the memory cache")
			}
			if got := cm.Lookup(req); got == nil {
				t.Error("Lookup returned nil; memory cache should serve regardless of the disk cache setting")
			} else {
				got.Body.Close()
			}
		})
	}
}

// TestMemoryCacheMaxEntrySize checks that memoryCacheMaxEntrySize and
// maxDiskCacheEntrySize gate their respective destinations independently: a
// response can be too big for one and still fit the other.
func TestMemoryCacheMaxEntrySize(t *testing.T) {
	body := strings.Repeat("x", 100)

	t.Run("too big for memory, fits disk", func(t *testing.T) {
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = t.TempDir()
		cfg.MemoryCacheMaxEntrySize = 10
		cfg.MaxDiskCacheEntrySize = -1
		cm := NewCacheManager(cfg)

		url := "http://example.com/toobig-for-mem"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if err := storeNow(cm, req, mkResp(body, "Cache-Control", "max-age=3600")); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if cm.memCache.Contains(url) {
			t.Error("response over memoryCacheMaxEntrySize was admitted to the memory cache")
		}
		if !fileExistsForURL(t, cm, url) {
			t.Error("response should still have been written to disk")
		}
	})

	t.Run("fits memory, too big for disk", func(t *testing.T) {
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = t.TempDir()
		cfg.MemoryCacheMaxEntrySize = 1024 * 1024
		cfg.MaxDiskCacheEntrySize = 10
		cm := NewCacheManager(cfg)

		url := "http://example.com/toobig-for-disk"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if err := storeNow(cm, req, mkResp(body, "Cache-Control", "max-age=3600")); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if !cm.memCache.Contains(url) {
			t.Error("response within memoryCacheMaxEntrySize was not admitted to the memory cache")
		}
		if fileExistsForURL(t, cm, url) {
			t.Error("response over maxDiskCacheEntrySize should not have been written to disk")
		}
	})
}

// TestMemoryCacheEntriesEviction checks that the memory cache evicts the
// least recently used entry once MemoryCacheEntries is exceeded.
func TestMemoryCacheEntriesEviction(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.MemoryCacheEntries = 2
	cm := NewCacheManager(cfg)

	store := func(path string) {
		url := "http://example.com/" + path
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if err := storeNow(cm, req, mkResp("body", "Cache-Control", "max-age=3600")); err != nil {
			t.Fatalf("Store(%s): %v", path, err)
		}
	}

	store("a")
	store("b")
	// Touch "a" so "b" becomes the least recently used.
	cm.Lookup(httptest.NewRequest(http.MethodGet, "http://example.com/a", nil))
	store("c")

	if cm.memCache.Contains("http://example.com/b") {
		t.Error("least recently used entry (b) should have been evicted")
	}
	if !cm.memCache.Contains("http://example.com/a") {
		t.Error("recently used entry (a) should still be cached")
	}
	if !cm.memCache.Contains("http://example.com/c") {
		t.Error("newly stored entry (c) should be cached")
	}
	if cm.memCache.Len() != 2 {
		t.Errorf("memCache.Len() = %d, want 2", cm.memCache.Len())
	}
}

// TestMemoryCacheRespectsFreshnessAndVary checks that a memory-cache hit goes
// through the same isExpired/responseVaries logic as a disk hit, rather than
// a separate copy of the rules that could drift from it.
// TestStoreCachesNoCacheButAlwaysStale: a response Cache-Control: no-cache may
// be stored but must not be reused without revalidating with the origin (RFC
// 9111 section 5.2.2.4; Polipo's objectMustRevalidate), however fresh its
// max-age says it is - in both the disk and the memory cache.
func TestStoreCachesNoCacheButAlwaysStale(t *testing.T) {
	for _, tc := range []struct {
		name string
		cc   []string
	}{
		{"bare", []string{"no-cache"}},
		{"with max-age, mixed case", []string{"max-age=3600, No-Cache"}},
		{"qualified form treated as unqualified", []string{`max-age=3600, no-cache="Set-Cookie"`}},
		{"second header line", []string{"max-age=3600", "no-cache"}},
	} {
		for _, memEntries := range []int{0, 1024} {
			cfg := NewDefaultConfig()
			cfg.DiskCacheRoot = t.TempDir()
			cfg.MemoryCacheEntries = memEntries
			cm := NewCacheManager(cfg)

			url := "http://example.com/nocache"
			req := httptest.NewRequest(http.MethodGet, url, nil)
			resp := mkResp("body", "ETag", `"v1"`, "Date", time.Now().UTC().Format(http.TimeFormat))
			for _, v := range tc.cc {
				resp.Header.Add("Cache-Control", v)
			}
			if err := storeNow(cm, req, resp); err != nil {
				t.Fatalf("%s (memoryCacheEntries=%d): Store: %v", tc.name, memEntries, err)
			}
			if !fileExistsForURL(t, cm, url) {
				t.Errorf("%s: a no-cache response should still be cached", tc.name)
			}

			if got := cm.Lookup(req); got != nil {
				got.Body.Close()
				t.Errorf("%s (memoryCacheEntries=%d): Lookup must not report a no-cache entry fresh", tc.name, memEntries)
			}
			stale, isStale := cm.LookupStale(req)
			if stale == nil || !isStale {
				t.Errorf("%s (memoryCacheEntries=%d): LookupStale should return the entry with stale=true", tc.name, memEntries)
			}
			if stale != nil {
				stale.Body.Close()
			}
		}
	}
}

func TestMemoryCacheRespectsFreshnessAndVary(t *testing.T) {
	t.Run("stale entry is a Lookup miss but a LookupStale hit", func(t *testing.T) {
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = t.TempDir()
		cm := NewCacheManager(cfg)

		url := "http://example.com/stale"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		resp := mkResp("body", "Cache-Control", "max-age=0", "Date", time.Now().UTC().Format(http.TimeFormat))
		if err := storeNow(cm, req, resp); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if !cm.memCache.Contains(url) {
			t.Fatal("entry was not admitted to the memory cache")
		}
		if got := cm.Lookup(req); got != nil {
			got.Body.Close()
			t.Error("Lookup should report a stale memory entry as a miss")
		}
		stale, isStale := cm.LookupStale(req)
		if stale == nil || !isStale {
			t.Error("LookupStale should return the stale memory entry with stale=true")
		}
		if stale != nil {
			stale.Body.Close()
		}
	})

	t.Run("Vary entry is never served fresh from memory", func(t *testing.T) {
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = t.TempDir()
		cm := NewCacheManager(cfg)

		url := "http://example.com/vary"
		req := httptest.NewRequest(http.MethodGet, url, nil)
		resp := mkResp("body", "Cache-Control", "max-age=3600", "Vary", "Accept-Encoding")
		if err := storeNow(cm, req, resp); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if got := cm.Lookup(req); got != nil {
			got.Body.Close()
			t.Error("a Vary'd response must never be served as a fresh hit, even from memory")
		}
	})
}

// TestDiscardMemoryKeepsDiskCache checks that DiscardMemory (SIGUSR2) empties
// the memory cache while the disk copy keeps serving the same entry.
func TestDiscardMemoryKeepsDiskCache(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)

	url := "http://example.com/discard"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if err := storeNow(cm, req, mkResp("body", "Cache-Control", "max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !cm.memCache.Contains(url) {
		t.Fatal("entry was not admitted to the memory cache")
	}

	cm.DiscardMemory()
	if cm.memCache.Len() != 0 {
		t.Errorf("memory cache has %d entries after DiscardMemory, want 0", cm.memCache.Len())
	}
	got := cm.Lookup(req)
	if got == nil {
		t.Fatal("the disk cache should still serve the entry")
	}
	defer got.Body.Close()
	if body, _ := io.ReadAll(got.Body); string(body) != "body" {
		t.Errorf("body = %q, want %q", body, "body")
	}

	// With the memory cache disabled there is nothing to discard.
	cfg2 := NewDefaultConfig()
	cfg2.MemoryCacheEntries = 0
	NewCacheManager(cfg2).DiscardMemory()
}

// TestDiskLookupClosesCacheFile checks that closing the body of a disk cache
// hit also closes the cache file, rather than leaving one descriptor open per
// hit until the garbage collector finalizes it. Garbage collection is turned
// off for the test so a finalizer can't hide the leak.
func TestDiskLookupClosesCacheFile(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("needs /proc/self/fd to count open files")
	}
	openFiles := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}

	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.MemoryCacheEntries = 0 // every hit comes from disk
	cm := NewCacheManager(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/fd", nil)
	if err := storeNow(cm, req, mkResp("body", "Cache-Control", "max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	before := openFiles()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for i := 0; i < 20; i++ {
			resp := cm.Lookup(httptest.NewRequest(method, "http://example.com/fd", nil))
			if resp == nil {
				t.Fatalf("%s lookup %d: miss", method, i)
			}
			resp.Body.Close()
		}
	}
	if after := openFiles(); after > before {
		t.Errorf("%d files left open after 40 closed disk hits", after-before)
	}
}

// TestDiskHitPromotedToMemory checks that a GET served from the disk cache is
// copied into the memory cache - as after a restart, when only the disk holds
// anything - and that HEAD hits and bodies over memoryCacheMaxEntrySize are
// not, while still reading back complete.
func TestDiskHitPromotedToMemory(t *testing.T) {
	root := t.TempDir()
	// Seed the disk only, through a manager with no memory cache.
	seedCfg := NewDefaultConfig()
	seedCfg.DiskCacheRoot = root
	seedCfg.MemoryCacheEntries = 0
	seed := NewCacheManager(seedCfg)
	store := func(url, body string, knownLength bool) {
		t.Helper()
		resp := mkResp(body, "Cache-Control", "max-age=3600")
		if knownLength {
			resp.ContentLength = int64(len(body))
		}
		if err := storeNow(seed, httptest.NewRequest(http.MethodGet, url, nil), resp); err != nil {
			t.Fatalf("Store(%s): %v", url, err)
		}
	}
	store("http://example.com/small", "small body", false)
	store("http://example.com/big-known", "a body over the limit", true)
	store("http://example.com/big-unknown", "a body over the limit", false)

	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = root
	cfg.MemoryCacheMaxEntrySize = 10
	cm := NewCacheManager(cfg)
	lookup := func(method, url string) string {
		t.Helper()
		resp := cm.Lookup(httptest.NewRequest(method, url, nil))
		if resp == nil {
			t.Fatalf("%s %s: miss", method, url)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("%s %s: reading body: %v", method, url, err)
		}
		return string(body)
	}

	lookup(http.MethodHead, "http://example.com/small")
	if cm.memCache.Contains("http://example.com/small") {
		t.Error("a HEAD hit must not be promoted (it has no body)")
	}
	if got := lookup(http.MethodGet, "http://example.com/small"); got != "small body" {
		t.Errorf("small: body = %q", got)
	}
	if !cm.memCache.Contains("http://example.com/small") {
		t.Fatal("a GET disk hit should be promoted to the memory cache")
	}
	// The promoted copy now answers on its own.
	path, _ := cm.CachePath("http://example.com/small")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := lookup(http.MethodGet, "http://example.com/small"); got != "small body" {
		t.Errorf("small from memory: body = %q", got)
	}

	for _, url := range []string{"http://example.com/big-known", "http://example.com/big-unknown"} {
		if got := lookup(http.MethodGet, url); got != "a body over the limit" {
			t.Errorf("%s: body = %q, want it complete", url, got)
		}
		if cm.memCache.Contains(url) {
			t.Errorf("%s: a body over memoryCacheMaxEntrySize must not be promoted", url)
		}
	}
}

// TestMemoryCacheDisabled checks that MemoryCacheEntries=0 turns the memory
// cache off entirely without affecting the disk cache.
func TestMemoryCacheDisabled(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.MemoryCacheEntries = 0
	cm := NewCacheManager(cfg)

	if cm.memCache != nil {
		t.Fatal("memCache should be nil when MemoryCacheEntries is 0")
	}

	url := "http://example.com/diskonly"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if err := storeNow(cm, req, mkResp("body", "Cache-Control", "max-age=3600")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !fileExistsForURL(t, cm, url) {
		t.Error("disk cache should still work with the memory cache disabled")
	}
	got := cm.Lookup(req)
	if got == nil {
		t.Fatal("Lookup should still hit the disk cache")
	}
	got.Body.Close()
}

// tempFiles lists the cache's temporary (still-being-written) files.
func tempFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), cacheTempPrefix) {
			found = append(found, path)
		}
		return nil
	})
	return found
}

// failingReader returns its data, then err instead of EOF.
type failingReader struct {
	data []byte
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// TestStoreDiscardsIncompleteBody: an entry is committed only once its body
// has been read to EOF. A body closed early (the client went away) or cut
// off by a read error (a broken origin connection) leaves nothing in either
// cache, and no temporary file behind.
func TestStoreDiscardsIncompleteBody(t *testing.T) {
	cases := map[string]func(cm *CacheManager, req *http.Request){
		"closed early": func(cm *CacheManager, req *http.Request) {
			resp := mkResp("a body that is not read to the end", "Cache-Control", "max-age=3600")
			cm.Store(req, resp)
			buf := make([]byte, 5)
			io.ReadFull(resp.Body, buf)
			resp.Body.Close()
		},
		"read error": func(cm *CacheManager, req *http.Request) {
			resp := mkResp("", "Cache-Control", "max-age=3600")
			resp.Body = io.NopCloser(&failingReader{data: []byte("partial"), err: io.ErrUnexpectedEOF})
			cm.Store(req, resp)
			if _, err := io.ReadAll(resp.Body); err != io.ErrUnexpectedEOF {
				t.Errorf("read error: got %v, want the origin's error passed through", err)
			}
			resp.Body.Close()
		},
	}
	for name, run := range cases {
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = t.TempDir()
		cm := NewCacheManager(cfg)
		url := "http://example.com/incomplete"
		req := httptest.NewRequest(http.MethodGet, url, nil)

		run(cm, req)

		if fileExistsForURL(t, cm, url) {
			t.Errorf("%s: an incomplete body must not be written to the disk cache", name)
		}
		if cm.memCache.Contains(url) {
			t.Errorf("%s: an incomplete body must not be added to the memory cache", name)
		}
		if left := tempFiles(t, cfg.DiskCacheRoot); len(left) > 0 {
			t.Errorf("%s: temporary files left behind: %v", name, left)
		}
	}
}

// TestStoreDropsBodyThatGrowsPastLimit: a response without a Content-Length
// is admitted, then dropped from whichever cache it outgrows while it
// streams. The client still gets the whole body.
func TestStoreDropsBodyThatGrowsPastLimit(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.MaxDiskCacheEntrySize = 10
	cfg.MemoryCacheMaxEntrySize = 20
	cm := NewCacheManager(cfg)

	const body = "fifteen bytes.."
	url := "http://example.com/grows"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp := mkResp(body, "Cache-Control", "max-age=3600")
	resp.ContentLength = -1
	if err := storeNow(cm, req, resp); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got, _ := io.ReadAll(resp.Body); string(got) != body {
		t.Errorf("client body = %q, want %q", got, body)
	}
	if fileExistsForURL(t, cm, url) {
		t.Error("a body over maxDiskCacheEntrySize must not reach the disk cache")
	}
	if left := tempFiles(t, cfg.DiskCacheRoot); len(left) > 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
	if !cm.memCache.Contains(url) {
		t.Error("the body still fits memoryCacheMaxEntrySize and should be in the memory cache")
	}
}

// TestExpireTemporaryFiles: Expire leaves a temporary file that is still
// being written alone, and removes one a crash left behind, even when its
// content would otherwise look like a valid entry.
func TestExpireTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = root
	cm := NewCacheManager(cfg)

	dir := filepath.Join(root, "example.com")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	valid := "HTTP/1.1 200 OK\r\nCache-Control: max-age=999999\r\nDate: " + timeHeader(0) + "\r\nContent-Length: 2\r\n\r\nok"
	fresh := filepath.Join(dir, cacheTempPrefix+"fresh")
	old := filepath.Join(dir, cacheTempPrefix+"old")
	for _, p := range []string{fresh, old} {
		if err := os.WriteFile(p, []byte(valid), 0600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * cacheTempMaxAge)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	stats, err := cm.Expire()
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a temporary file still being written must be left alone: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("an abandoned temporary file should be removed (stat err = %v)", err)
	}
	if stats.Scanned != 1 || stats.Removed != 1 {
		t.Errorf("stats = %+v, want Scanned 1 (the abandoned file) and Removed 1", stats)
	}
}

// TestPragmaNoCache: Pragma: no-cache counts as Cache-Control: no-cache when
// there is no Cache-Control header at all. For a request RFC 7234 section
// 5.4 required it and RFC 9111 section 5.4, which deprecates Pragma, no
// longer does; go-polipo keeps it, as Polipo did (the lookup is skipped).
// For a response neither RFC specified a meaning, and go-polipo follows
// Polipo (http_parse.c), which treated it as no-cache too (the entry is
// stored but always stale). A Cache-Control
// header, whatever it says, overrides Pragma in both directions.
func TestPragmaNoCache(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		cfg := NewDefaultConfig()
		cfg.DiskCacheRoot = t.TempDir()
		cm := NewCacheManager(cfg)
		url := "http://example.com/pragma-req"
		if err := storeNow(cm, httptest.NewRequest(http.MethodGet, url, nil), mkResp("body", "Cache-Control", "max-age=3600")); err != nil {
			t.Fatalf("Store: %v", err)
		}
		for _, tc := range []struct {
			name    string
			hdr     []string
			wantHit bool
		}{
			{"Pragma: no-cache alone", []string{"Pragma", "no-cache"}, false},
			{"mixed case", []string{"Pragma", "No-Cache"}, false},
			{"overridden by Cache-Control", []string{"Pragma", "no-cache", "Cache-Control", "max-stale=60"}, true},
			{"other Pragma", []string{"Pragma", "x-other"}, true},
		} {
			req := httptest.NewRequest(http.MethodGet, url, nil)
			for i := 0; i+1 < len(tc.hdr); i += 2 {
				req.Header.Add(tc.hdr[i], tc.hdr[i+1])
			}
			got, _ := cm.LookupStale(req)
			if got != nil {
				got.Body.Close()
			}
			if (got != nil) != tc.wantHit {
				t.Errorf("%s: hit = %v, want %v", tc.name, got != nil, tc.wantHit)
			}
		}
	})

	t.Run("response", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			hdr       []string
			wantStale bool
		}{
			{"Pragma: no-cache alone", []string{"Pragma", "no-cache", "Expires", timeHeader(time.Hour)}, true},
			{"overridden by Cache-Control", []string{"Pragma", "no-cache", "Cache-Control", "max-age=3600"}, false},
		} {
			cfg := NewDefaultConfig()
			cfg.DiskCacheRoot = t.TempDir()
			cm := NewCacheManager(cfg)
			url := "http://example.com/pragma-resp"
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if err := storeNow(cm, req, mkResp("body", append(tc.hdr, "Date", timeHeader(0))...)); err != nil {
				t.Fatalf("%s: Store: %v", tc.name, err)
			}
			got, stale := cm.LookupStale(req)
			if got == nil {
				t.Fatalf("%s: the response should be stored", tc.name)
			}
			got.Body.Close()
			if stale != tc.wantStale {
				t.Errorf("%s: stale = %v, want %v", tc.name, stale, tc.wantStale)
			}
		}
	})
}

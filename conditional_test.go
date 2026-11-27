package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestETagListMatches(t *testing.T) {
	for _, tc := range []struct {
		list, etag string
		want       bool
	}{
		{`"v1"`, `"v1"`, true},
		{`"v1"`, `"v2"`, false},
		{`W/"v1"`, `"v1"`, true},   // weak comparison: W/ ignored on either side
		{`"v1"`, `W/"v1"`, true},   //
		{`W/"v1"`, `W/"v1"`, true}, //
		{`"a", "v1"`, `"v1"`, true},
		{`"a",W/"b" , "v1"`, `"v1"`, true},
		{`"a", "b"`, `"v1"`, false},
		{`*`, `"v1"`, true},
		{`*`, ``, true},          // any stored response matches "*"
		{`"a,b"`, `"a,b"`, true}, // a comma inside an entity-tag
		{`"a,b"`, `"a"`, false},
		{`"v1"`, ``, false},              // no stored ETag
		{`v1`, `"v1"`, false},            // unquoted: malformed
		{`"a", v1, "v1"`, `"v1"`, false}, // stops at the malformed member
		{``, `"v1"`, false},
		{`"v1`, `"v1"`, false}, // unterminated
	} {
		if got := etagListMatches(tc.list, tc.etag); got != tc.want {
			t.Errorf("etagListMatches(%q, %q) = %v, want %v", tc.list, tc.etag, got, tc.want)
		}
	}
}

func TestClientValidatorsNotModified(t *testing.T) {
	lm := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	date := lm.Add(24 * time.Hour)
	at := func(t time.Time) string { return t.Format(http.TimeFormat) }
	stored := http.Header{
		"Etag":          {`"v1"`},
		"Last-Modified": {at(lm)},
		"Date":          {at(date)},
	}
	noLM := http.Header{"Etag": {`"v1"`}, "Date": {at(date)}}

	for _, tc := range []struct {
		name   string
		method string
		hdr    http.Header
		stored http.Header
		want   bool
	}{
		{"If-None-Match matches", "GET", http.Header{"If-None-Match": {`"v1"`}}, stored, true},
		{"If-None-Match differs", "GET", http.Header{"If-None-Match": {`"v0"`}}, stored, false},
		{"HEAD", "HEAD", http.Header{"If-None-Match": {`"v1"`}}, stored, true},
		{"POST", "POST", http.Header{"If-None-Match": {`"v1"`}}, stored, false},
		{"several If-None-Match lines", "GET", http.Header{"If-None-Match": {`"a"`, `"v1"`}}, stored, true},
		{
			"If-None-Match decides alone", "GET",
			http.Header{"If-None-Match": {`"v0"`}, "If-Modified-Since": {at(date)}}, stored, false,
		},
		{"If-Modified-Since after Last-Modified", "GET", http.Header{"If-Modified-Since": {at(lm.Add(time.Hour))}}, stored, true},
		{"If-Modified-Since equal to Last-Modified", "GET", http.Header{"If-Modified-Since": {at(lm)}}, stored, true},
		{"If-Modified-Since before Last-Modified", "GET", http.Header{"If-Modified-Since": {at(lm.Add(-time.Hour))}}, stored, false},
		{"no Last-Modified: Date is used", "GET", http.Header{"If-Modified-Since": {at(date)}}, noLM, true},
		{"no Last-Modified, before Date", "GET", http.Header{"If-Modified-Since": {at(lm)}}, noLM, false},
		{"invalid If-Modified-Since", "GET", http.Header{"If-Modified-Since": {"yesterday"}}, stored, false},
		{"no validators", "GET", http.Header{}, stored, false},
		{
			// Not applicable to a cache (RFC 9111 section 4.3.2).
			"If-Match and If-Unmodified-Since ignored", "GET",
			http.Header{"If-Match": {`"v0"`}, "If-Unmodified-Since": {at(lm.Add(-time.Hour))}}, stored, false,
		},
	} {
		if got := validatorsOf(tc.hdr).notModified(tc.method, tc.stored); got != tc.want {
			t.Errorf("%s: notModified = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// newConditionalFixture returns a handler whose cache holds a fresh entry
// for target, with an ETag and a Last-Modified, and counts the requests
// that reach the origin.
func newConditionalFixture(t *testing.T) (ph *ProxyHandler, target string, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("from origin"))
	}))
	t.Cleanup(backend.Close)

	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	ph = NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())
	target = backend.URL + "/page"
	resp := mkResp("0123456789",
		"Cache-Control", "max-age=600", "Date", timeHeader(-time.Minute),
		"ETag", `"v1"`, "Last-Modified", timeHeader(-time.Hour), "Content-Type", "text/plain")
	if err := storeNow(cm, httptest.NewRequest(http.MethodGet, target, nil), resp); err != nil {
		t.Fatal(err)
	}
	return ph, target, hits
}

func TestConditionalRequestAnsweredFromCache(t *testing.T) {
	ph, target, hits := newConditionalFixture(t)
	buf := withAccessLog(ph)
	seen := 0

	for _, tc := range []struct {
		name     string
		method   string
		hdr      [][2]string
		wantCode int
		wantBody string
	}{
		{"If-None-Match matches", "GET", [][2]string{{"If-None-Match", `"v1"`}}, 304, ""},
		{"weak If-None-Match matches", "GET", [][2]string{{"If-None-Match", `W/"v1"`}}, 304, ""},
		{"If-None-Match differs", "GET", [][2]string{{"If-None-Match", `"v0"`}}, 200, "0123456789"},
		{"If-Modified-Since", "GET", [][2]string{{"If-Modified-Since", timeHeader(0)}}, 304, ""},
		{"HEAD", "HEAD", [][2]string{{"If-None-Match", `"v1"`}}, 304, ""},
		// RFC 9110 section 13.2.2: If-None-Match is decided before Range.
		{"If-None-Match matches, with Range", "GET", [][2]string{{"If-None-Match", `"v1"`}, {"Range", "bytes=0-3"}}, 304, ""},
		{"If-None-Match differs, with Range", "GET", [][2]string{{"If-None-Match", `"v0"`}, {"Range", "bytes=0-3"}}, 206, "0123"},
	} {
		req := httptest.NewRequest(tc.method, target, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		for _, h := range tc.hdr {
			req.Header.Set(h[0], h[1])
		}
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, req)

		if rr.Code != tc.wantCode {
			t.Errorf("%s: status = %d, want %d", tc.name, rr.Code, tc.wantCode)
		}
		if tc.method == "GET" && rr.Body.String() != tc.wantBody {
			t.Errorf("%s: body = %q, want %q", tc.name, rr.Body.String(), tc.wantBody)
		}
		line := lastAccessLine(t, buf, &seen)
		if line["cache"] != "hit" || line["status"] != float64(tc.wantCode) {
			t.Errorf("%s: access log cache=%v status=%v, want hit and %d", tc.name, line["cache"], line["status"], tc.wantCode)
		}
		if tc.wantCode != 304 {
			continue
		}
		// RFC 9110 section 15.4.5: the fields a 200 would carry, and no
		// representation metadata.
		h := rr.Header()
		if h.Get("ETag") != `"v1"` || h.Get("Cache-Control") != "max-age=600" || h.Get("Date") == "" || h.Get("Age") == "" {
			t.Errorf("%s: 304 lacks ETag, Cache-Control, Date or Age: %v", tc.name, h)
		}
		for _, name := range []string{"Content-Type", "Content-Length", "Last-Modified", "Content-Range"} {
			if h.Get(name) != "" {
				t.Errorf("%s: 304 carries %s: %q", tc.name, name, h.Get(name))
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("origin requests = %d, want 0: a fresh entry answers every one", n)
	}
	if got := ph.stats.cacheHits.Load(); got != 7 {
		t.Errorf("cacheHits = %d, want 7", got)
	}
}

// TestConditionalStaleFallback: when the origin cannot be reached and a
// stale entry is served instead (relaxTransparency = maybe), the client's
// own validators are what decide a 304 - not the ones go-polipo added to
// the request it tried to send.
func TestConditionalStaleFallback(t *testing.T) {
	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.RelaxTransparency = TristateMaybe
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := "http://" + deadLoopback + ":9999/stale"
	resp := mkResp("stale body", "Cache-Control", "max-age=1", "Date", timeHeader(-2*time.Hour), "ETag", `"v1"`)
	if err := storeNow(cm, httptest.NewRequest(http.MethodGet, target, nil), resp); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		inm      string
		wantCode int
		wantBody string
	}{
		{"client's validator matches", `"v1"`, 304, ""},
		{"client's validator differs", `"v0"`, 200, "stale body"},
		// go-polipo sent If-None-Match: "v1" upstream itself.
		{"client sent no validator", "", 200, "stale body"},
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		if tc.inm != "" {
			req.Header.Set("If-None-Match", tc.inm)
		}
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, req)
		if rr.Code != tc.wantCode || rr.Body.String() != tc.wantBody {
			t.Errorf("%s: %d %q, want %d %q", tc.name, rr.Code, rr.Body.String(), tc.wantCode, tc.wantBody)
		}
		if !strings.HasPrefix(rr.Header().Get("Warning"), "111 ") {
			t.Errorf("%s: Warning = %q, want 111", tc.name, rr.Header().Get("Warning"))
		}
	}
}

// TestConditionalOffline: proxyOffline serves a stale entry through
// serveCached, so a matching validator gets a 304 there too.
func TestConditionalOffline(t *testing.T) {
	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.ProxyOffline = true
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())
	target := "http://example.com/offline"
	resp := mkResp("old", "Cache-Control", "max-age=1", "Date", timeHeader(-2*time.Hour), "ETag", `"v1"`)
	if err := storeNow(cm, httptest.NewRequest(http.MethodGet, target, nil), resp); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("If-None-Match", `"v1"`)
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rr.Code)
	}
	if !strings.HasPrefix(rr.Header().Get("Warning"), "112 ") {
		t.Errorf("Warning = %q, want 112 (disconnected operation)", rr.Header().Get("Warning"))
	}
}

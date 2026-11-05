package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveOnce runs one request through ph from an allowed client.
func serveOnce(ph *ProxyHandler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	return rr
}

func TestStatsCountHitMissAndRevalidation(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte("fresh from origin"))
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	seedCacheEntry(t, cm, backend.URL+"/hit", "cached body", true)
	seedStaleEntryWithValidators(t, cm, backend.URL+"/reval", "old body", `"v1"`, "")

	serveOnce(ph, http.MethodGet, backend.URL+"/hit")   // fresh hit
	serveOnce(ph, http.MethodGet, backend.URL+"/reval") // stale, origin answers 304
	serveOnce(ph, http.MethodGet, backend.URL+"/miss")  // nothing cached

	s := ph.stats
	if got := s.requests.Load(); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
	if got := s.cacheHits.Load(); got != 1 {
		t.Errorf("cacheHits = %d, want 1", got)
	}
	if got := s.cacheHitBytes.Load(); got != uint64(len("cached body")) {
		t.Errorf("cacheHitBytes = %d, want %d", got, len("cached body"))
	}
	if got := s.revalidated.Load(); got != 1 {
		t.Errorf("revalidated = %d, want 1", got)
	}
	if got := s.upstream.Load(); got != 2 {
		t.Errorf("upstream = %d, want 2 (the revalidation and the miss)", got)
	}
	if got := s.upstreamErrors.Load(); got != 0 {
		t.Errorf("upstreamErrors = %d, want 0", got)
	}
}

func TestStatsCountDeniedAndOriginErrors(t *testing.T) {
	ph := newTestHandler(NewDefaultConfig())

	serveOnce(ph, http.MethodGet, "http://example.com:25/")         // port not in allowedPorts
	serveOnce(ph, http.MethodGet, "http://"+deadLoopback+":9999/x") // origin unreachable

	if got := ph.stats.denied.Load(); got != 1 {
		t.Errorf("denied = %d, want 1", got)
	}
	if got := ph.stats.upstreamErrors.Load(); got != 1 {
		t.Errorf("upstreamErrors = %d, want 1", got)
	}
	if got := ph.stats.requests.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (a denied request never reaches the cache)", got)
	}
}

func TestStatusPage(t *testing.T) {
	ph := newTestHandler(NewDefaultConfig())
	ph.stats.requests.Add(4)
	ph.stats.cacheHits.Add(1)

	rr := serveOnce(ph, http.MethodGet, "/polipo/status")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	body := rr.Body.String()
	for _, want := range []string{"Requests (HTTP)</td><td>4<", "Cache hits</td><td>1 (25.0%)<", "Mode</td><td>on line<"} {
		if !strings.Contains(body, want) {
			t.Errorf("status page lacks %q", want)
		}
	}

	menu := serveOnce(ph, http.MethodGet, "/polipo/").Body.String()
	if !strings.Contains(menu, `href="/polipo/status"`) {
		t.Error("local menu does not link to /polipo/status")
	}
}

func TestPercent(t *testing.T) {
	if got := percent(1, 0); got != "-" {
		t.Errorf("percent(1, 0) = %q, want -", got)
	}
	if got := percent(1, 3); got != "33.3%" {
		t.Errorf("percent(1, 3) = %q, want 33.3%%", got)
	}
}

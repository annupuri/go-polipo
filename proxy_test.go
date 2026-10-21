package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestHandler(cfg *Config) *ProxyHandler {
	return NewProxyHandler(cfg, NewCacheManager(cfg), NewForbiddenList(), NewForbiddenList(), NewForbiddenList())
}

func TestProxyAllowedClients(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.AllowedClients = []string{"192.168.1.1"}
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable, NewForbiddenList())

	// Case 1: IP not allowed
	req1 := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req1.RemoteAddr = "192.168.1.2:12345"
	rr1 := httptest.NewRecorder()

	ph.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr1.Code)
	}

	// Case 2: IP allowed

	// Direct call to ServeHTTP will attempt standard RoundTrip which might fail if internet is down,
	// so for this test we only check the Status Code prior to forwarding,
	// or we intercept and verify the client IP validation.
	// Since clientIP check is the first step, let's verify if RemoteAddr 192.168.1.2 triggers 403.
	allowed1 := ph.isClientAllowed("192.168.1.1:55555")
	if !allowed1 {
		t.Error("expected 192.168.1.1 to be allowed")
	}

	allowed2 := ph.isClientAllowed("10.0.0.1:55555")
	if allowed2 {
		t.Error("expected 10.0.0.1 to be disallowed")
	}
}

func TestProxyLocalInterface(t *testing.T) {
	cfg := NewDefaultConfig()
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable, NewForbiddenList())

	// Access /polipo/
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8123/polipo/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()

	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "go-polipo Local Interface") {
		t.Errorf("expected local interface title in body, got: %s", string(body))
	}

	// Access /polipo/config
	reqConfig := httptest.NewRequest(http.MethodGet, "http://localhost:8123/polipo/config", nil)
	reqConfig.RemoteAddr = "127.0.0.1:12345"
	rrConfig := httptest.NewRecorder()

	ph.ServeHTTP(rrConfig, reqConfig)
	if rrConfig.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rrConfig.Code)
	}

	bodyBytes, _ := io.ReadAll(rrConfig.Body)
	bodyConfig := string(bodyBytes)
	if !strings.Contains(bodyConfig, "go-polipo Configuration") {
		t.Errorf("expected configuration title in body, got: %s", bodyConfig)
	}
	// The table is rendered from Config.Entries; spot-check a couple of rows.
	for _, want := range []string{
		"<tr><td>proxyPort</td><td>8123</td></tr>",
		"<tr><td>diskCacheUnlinkTime</td><td>768h0m0s</td></tr>",
	} {
		if !strings.Contains(bodyConfig, want) {
			t.Errorf("config page missing row %q\nbody:\n%s", want, bodyConfig)
		}
	}
}

func TestProxyForbiddenFiltering(t *testing.T) {
	cfg := NewDefaultConfig()
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	forbidden.Domains = []string{"badsite.com"}
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable, NewForbiddenList())

	req := httptest.NewRequest(http.MethodGet, "http://badsite.com/index.html", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()

	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// TestProxyLogLevelGatesDenialLogging confirms config.LogLevel actually
// reaches ServeHTTP's "Denied" log line (LogForbidden), not just the logf
// helper in isolation - this is the wiring most likely to regress if a call
// site is ever moved to a place without ph in scope.
func TestProxyLogLevelGatesDenialLogging(t *testing.T) {
	newHandler := func(logLevel int) *ProxyHandler {
		cfg := NewDefaultConfig()
		cfg.LogLevel = logLevel
		forbidden := NewForbiddenList()
		forbidden.Domains = []string{"badsite.com"}
		return NewProxyHandler(cfg, NewCacheManager(cfg), forbidden, NewForbiddenList(), NewForbiddenList())
	}
	deny := func(ph *ProxyHandler) {
		req := httptest.NewRequest(http.MethodGet, "http://badsite.com/", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		ph.ServeHTTP(httptest.NewRecorder(), req)
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard) // TestMain's resting state; see main_test.go.

	deny(newHandler(0x07)) // Polipo's default: LogForbidden's bit (0x8) unset.
	if strings.Contains(buf.String(), "Denied") {
		t.Errorf("expected no denial log at the default logLevel, got %q", buf.String())
	}

	buf.Reset()
	deny(newHandler(0x0f)) // 0x07 | LogForbidden.
	if !strings.Contains(buf.String(), "Denied: http://badsite.com/ is blacklisted") {
		t.Errorf("expected a denial log once LogForbidden is enabled, got %q", buf.String())
	}
}

func TestProxyReload(t *testing.T) {
	dir := t.TempDir()
	forbiddenPath := dir + "/forbidden"
	uncachablePath := dir + "/uncachable"
	forbiddenTunnelsPath := dir + "/forbiddenTunnels"

	cfg := NewDefaultConfig()
	cfg.ForbiddenFile = forbiddenPath
	cfg.UncachableFile = uncachablePath
	cfg.ForbiddenTunnelsFile = forbiddenTunnelsPath
	ph := newTestHandler(cfg)

	url, host := "http://badsite.com/", "badsite.com"
	hostPort := "badsite.com:443"

	// Neither file exists yet, so nothing is blocked or marked uncachable.
	// Checked directly (not via ServeHTTP) so a false negative here doesn't
	// send a real request to badsite.com over the network.
	if ph.forbidden.Load().Match(url, host) {
		t.Fatalf("badsite.com forbidden before forbidden file existed")
	}
	if ph.uncachable.Load().Match(url, host) {
		t.Fatalf("badsite.com marked uncachable before uncachable file existed")
	}
	if ph.forbiddenTunnels.Load().MatchTunnel(hostPort, host) {
		t.Fatalf("badsite.com forbidden tunnel before forbiddenTunnels file existed")
	}

	if err := os.WriteFile(forbiddenPath, []byte("badsite.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uncachablePath, []byte("badsite.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(forbiddenTunnelsPath, []byte("badsite.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The old (empty) lists are still in effect until Reload runs.
	if ph.forbidden.Load().Match(url, host) {
		t.Fatalf("badsite.com forbidden before Reload was called")
	}

	if err := ph.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 after Reload picked up forbidden file, got %d", rr.Code)
	}
	if !ph.uncachable.Load().Match(url, host) {
		t.Errorf("expected uncachable file to take effect after Reload")
	}
	if !ph.forbiddenTunnels.Load().MatchTunnel(hostPort, host) {
		t.Errorf("expected forbiddenTunnels file to take effect after Reload")
	}
}

func TestProxyReloadEndpoint(t *testing.T) {
	dir := t.TempDir()
	forbiddenPath := dir + "/forbidden"

	cfg := NewDefaultConfig()
	cfg.ForbiddenFile = forbiddenPath
	ph := newTestHandler(cfg)

	// GET must not trigger a reload (avoids prefetch/crawler side effects).
	getReq := httptest.NewRequest(http.MethodGet, "http://localhost:8123/polipo/reload", nil)
	getReq.RemoteAddr = "127.0.0.1:12345"
	getRR := httptest.NewRecorder()
	ph.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /polipo/reload: expected 405, got %d", getRR.Code)
	}

	if err := os.WriteFile(forbiddenPath, []byte("badsite.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	postReq := httptest.NewRequest(http.MethodPost, "http://localhost:8123/polipo/reload", nil)
	postReq.RemoteAddr = "127.0.0.1:12345"
	postRR := httptest.NewRecorder()
	ph.ServeHTTP(postRR, postReq)
	if postRR.Code != http.StatusSeeOther {
		t.Errorf("POST /polipo/reload: expected 303, got %d", postRR.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "http://badsite.com/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 after POST /polipo/reload, got %d", rr.Code)
	}
}

func TestProxyFlushEndpoint(t *testing.T) {
	dir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = dir
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	// GET must not trigger a flush (avoids prefetch/crawler side effects).
	getReq := httptest.NewRequest(http.MethodGet, "http://localhost:8123/polipo/flush", nil)
	getReq.RemoteAddr = "127.0.0.1:12345"
	getRR := httptest.NewRecorder()
	ph.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /polipo/flush: expected 405, got %d", getRR.Code)
	}

	// A corrupt entry so the flush has something concrete to remove and
	// report on.
	corruptDir := filepath.Join(dir, "corrupt.example.com")
	if err := os.MkdirAll(corruptDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "garbage"), []byte("not an HTTP response"), 0600); err != nil {
		t.Fatal(err)
	}

	postReq := httptest.NewRequest(http.MethodPost, "http://localhost:8123/polipo/flush", nil)
	postReq.RemoteAddr = "127.0.0.1:12345"
	postRR := httptest.NewRecorder()
	ph.ServeHTTP(postRR, postReq)
	if postRR.Code != http.StatusOK {
		t.Fatalf("POST /polipo/flush: expected 200, got %d", postRR.Code)
	}
	body, _ := io.ReadAll(postRR.Body)
	if !strings.Contains(string(body), "Scanned: 1") || !strings.Contains(string(body), "Removed: 1") {
		t.Errorf("flush result page missing expected stats, got: %s", body)
	}

	// disableLocalInterface must still gate /polipo/flush like every other
	// local page: the request falls through to ordinary forwarding instead
	// of being handled locally. Forward to a real backend (rather than
	// asserting "not 200" against an arbitrary host) so the test isn't
	// fooled by something else happening to listen on that host/port.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer backend.Close()

	cfg2 := NewDefaultConfig()
	cfg2.DiskCacheRoot = t.TempDir()
	cfg2.DisableLocalInterface = true
	ph2 := newTestHandler(cfg2)
	disabledReq := httptest.NewRequest(http.MethodPost, backend.URL+"/polipo/flush", nil)
	disabledReq.RemoteAddr = "127.0.0.1:12345"
	disabledRR := httptest.NewRecorder()
	ph2.ServeHTTP(disabledRR, disabledReq)
	if disabledRR.Code != http.StatusTeapot {
		t.Errorf("expected /polipo/flush to be forwarded to the backend, got %d", disabledRR.Code)
	}
}

func TestProxyFlushEndpointErrorWhenNoDiskCache(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = "" // disk cache disabled -> Expire() errors
	ph := newTestHandler(cfg)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8123/polipo/flush", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when diskCacheRoot is unset, got %d", rr.Code)
	}
}

func TestProxyHandleForward(t *testing.T) {
	// 1. Setup mock backend server
	backendMsg := "Target Server Response"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "max-age=100")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(backendMsg))
	}))
	defer backend.Close()

	// 2. Setup ProxyHandler
	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable, NewForbiddenList())

	// 3. Make request to mock backend through ProxyHandler
	req := httptest.NewRequest(http.MethodGet, backend.URL, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()

	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	body, _ := io.ReadAll(rr.Body)
	if string(body) != backendMsg {
		t.Errorf("expected %q, got %q", backendMsg, string(body))
	}

	// Verify it was cached
	cachedResp := cm.Lookup(req)
	if cachedResp == nil {
		t.Error("expected response to be cached but got nil on lookup")
	} else {
		cachedResp.Body.Close()
	}
}

func TestProxyHandleConnectInvalidPort(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.TunnelAllowedPorts = PortSet{{443, 443}} // only allow 443
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable, NewForbiddenList())

	// Connect to non-allowed port (e.g., 80)
	req := httptest.NewRequest(http.MethodConnect, "example.com:80", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.RequestURI = "example.com:80"
	rr := httptest.NewRecorder()

	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", rr.Code)
	}
}

func TestProxyAllowedClientsCIDR(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.AllowedClients = []string{"192.168.2.0/24", "10.1.0.0/16"}
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable, NewForbiddenList())

	// In CIDR range 1
	if !ph.isClientAllowed("192.168.2.50:55555") {
		t.Error("expected 192.168.2.50 to be allowed")
	}

	// In CIDR range 2
	if !ph.isClientAllowed("10.1.99.99:55555") {
		t.Error("expected 10.1.99.99 to be allowed")
	}

	// Outside range
	if ph.isClientAllowed("192.168.3.1:55555") {
		t.Error("expected 192.168.3.1 to be disallowed")
	}

	// Invalid remote IP format (which net.SplitHostPort fails on)
	if ph.isClientAllowed("invalid_remote_addr") {
		// Should return false
		t.Error("expected invalid remote addr format to be disallowed")
	}
}

func TestProxyConnectFailureCases(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.TunnelAllowedPorts = PortSet{{443, 443}}
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable, NewForbiddenList())

	// 1. Host with no port
	reqNoPort := httptest.NewRequest(http.MethodConnect, "http://example.com:443", nil)
	reqNoPort.RemoteAddr = "127.0.0.1:12345"
	reqNoPort.RequestURI = "example.com"
	rr1 := httptest.NewRecorder()
	ph.ServeHTTP(rr1, reqNoPort)
	if rr1.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rr1.Code)
	}

	// 2. Port that is not an integer
	reqBadPort := httptest.NewRequest(http.MethodConnect, "http://example.com:443", nil)
	reqBadPort.RemoteAddr = "127.0.0.1:12345"
	reqBadPort.RequestURI = "example.com:abc"
	rr2 := httptest.NewRecorder()
	ph.ServeHTTP(rr2, reqBadPort)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rr2.Code)
	}

	// 3. Hijacker not supported (typical for ResponseRecorder)
	reqAllowed := httptest.NewRequest(http.MethodConnect, "example.com:443", nil)
	reqAllowed.RemoteAddr = "127.0.0.1:12345"
	reqAllowed.RequestURI = "example.com:443"
	rr3 := httptest.NewRecorder()
	ph.ServeHTTP(rr3, reqAllowed)
	if rr3.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 Internal Server Error, got %d", rr3.Code)
	}
	if !strings.Contains(rr3.Body.String(), "Hijacking not supported") {
		t.Errorf("expected Hijacking not supported, got %q", rr3.Body.String())
	}
}

func TestProxyForbiddenConnect(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.TunnelAllowedPorts = PortSet{{443, 443}}
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	forbidden.Domains = []string{"badsite.com"}
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable, NewForbiddenList())

	req := httptest.NewRequest(http.MethodConnect, "badsite.com:443", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.RequestURI = "badsite.com:443"
	rr := httptest.NewRecorder()

	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", rr.Code)
	}
}

func TestProxyForbiddenTunnelsFile(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.TunnelAllowedPorts = PortSet{{443, 443}}
	cm := NewCacheManager(cfg)
	forbiddenTunnels := NewForbiddenList()
	forbiddenTunnels.Domains = []string{"tunnelsite.com"}
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), forbiddenTunnels)

	// Blocked by forbiddenTunnelsFile itself.
	req := httptest.NewRequest(http.MethodConnect, "tunnelsite.com:443", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.RequestURI = "tunnelsite.com:443"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for tunnelsite.com:443, got %d", rr.Code)
	}

	// forbiddenTunnelsFile requires an exact host match (Polipo's
	// tunnelIsMatched, unlike the suffix matching the general forbidden list
	// uses), so a subdomain is not blocked by the "tunnelsite.com" entry.
	req2 := httptest.NewRequest(http.MethodConnect, "sub.tunnelsite.com:443", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	req2.RequestURI = "sub.tunnelsite.com:443"
	rr2 := httptest.NewRecorder()
	ph.ServeHTTP(rr2, req2)
	if rr2.Code == http.StatusForbidden {
		t.Errorf("expected sub.tunnelsite.com:443 not to be blocked by an exact-match entry, got %d", rr2.Code)
	}
}

func TestPortSetContains(t *testing.T) {
	set := PortSet{{80, 100}, {8080, 8080}}
	for _, p := range []int{80, 90, 100, 8080} {
		if !set.Contains(p) {
			t.Errorf("Contains(%d) = false, want true", p)
		}
	}
	for _, p := range []int{0, 22, 79, 101, 8443} {
		if set.Contains(p) {
			t.Errorf("Contains(%d) = true, want false", p)
		}
	}
}

func TestTargetURL(t *testing.T) {
	connect := httptest.NewRequest(http.MethodConnect, "example.com:443", nil)
	connect.RequestURI = "example.com:443"
	if got := targetURL(connect); got != "https://example.com:443" {
		t.Errorf("CONNECT targetURL = %q", got)
	}

	abs := httptest.NewRequest(http.MethodGet, "http://example.com/a?b=1", nil)
	if got := targetURL(abs); got != "http://example.com/a?b=1" {
		t.Errorf("absolute targetURL = %q", got)
	}

	origin := httptest.NewRequest(http.MethodGet, "/path", nil)
	origin.Host = "example.org"
	origin.URL.Scheme = ""
	origin.URL.Host = ""
	if got := targetURL(origin); got != "http://example.org/path" {
		t.Errorf("origin-form targetURL = %q", got)
	}
}

func TestCopyHeader(t *testing.T) {
	src := http.Header{}
	src.Add("X-A", "1")
	src.Add("X-A", "2")
	src.Set("X-B", "3")

	dst := http.Header{}
	copyHeader(dst, src)

	if got := dst.Values("X-A"); len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Errorf("X-A not copied with all values: %v", got)
	}
	if dst.Get("X-B") != "3" {
		t.Errorf("X-B not copied: %q", dst.Get("X-B"))
	}
}

func TestNormalizeProxyURL(t *testing.T) {
	u, err := normalizeProxyURL("127.0.0.1:3128")
	if err != nil {
		t.Fatalf("normalizeProxyURL error: %v", err)
	}
	if u.Scheme != "http" || u.Host != "127.0.0.1:3128" {
		t.Errorf("normalized = %+v, want http scheme and host 127.0.0.1:3128", u)
	}

	u, err = normalizeProxyURL("https://secure:8443")
	if err != nil || u.Scheme != "https" {
		t.Errorf("https passthrough failed: %+v err=%v", u, err)
	}

	if _, err := normalizeProxyURL("http://%zz"); err == nil {
		t.Error("expected error for malformed proxy URL")
	}
}

func TestCreateTransportParentProxy(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.ParentProxy = "127.0.0.1:3128"
	ph := newTestHandler(cfg)

	if ph.transport.Proxy == nil {
		t.Fatal("expected transport.Proxy to be set")
	}
	u, err := ph.transport.Proxy(httptest.NewRequest(http.MethodGet, "http://x/", nil))
	if err != nil || u == nil || u.Host != "127.0.0.1:3128" {
		t.Errorf("proxy func returned %+v, err %v", u, err)
	}

	// A malformed parent proxy leaves Proxy unset rather than crashing.
	cfg2 := NewDefaultConfig()
	cfg2.ParentProxy = "http://%zz"
	if newTestHandler(cfg2).transport.Proxy != nil {
		t.Error("expected transport.Proxy to stay nil for malformed parentProxy")
	}
}

func TestParentProxyAuthCredentials(t *testing.T) {
	var gotAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Proxy-Authorization")
		w.Write([]byte("via parent"))
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.ParentProxy = strings.TrimPrefix(backend.URL, "http://")
	cfg.ParentAuthCredentials = "alice:secret"
	ph := newTestHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "via parent" {
		t.Errorf("body = %q", got)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	if gotAuth != want {
		t.Errorf("Proxy-Authorization = %q, want %q", gotAuth, want)
	}
}

func TestParentProxyNoAuthHeaderWithoutCredentials(t *testing.T) {
	var sawAuth bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Proxy-Authorization") != ""
		w.Write([]byte("via parent"))
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.ParentProxy = strings.TrimPrefix(backend.URL, "http://")
	// ParentAuthCredentials left empty.
	ph := newTestHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if sawAuth {
		t.Error("no Proxy-Authorization header should be sent without parentAuthCredentials")
	}
}

func TestDialContextDirect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()

	ph := newTestHandler(NewDefaultConfig())
	conn, err := ph.dialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialContext direct failed: %v", err)
	}
	conn.Close()
}

// deadLoopback is a loopback host for tests that need a port nothing listens
// on to fail fast. Not 127.0.0.1: under WSL's mirrored networking a connect
// to a closed 127.0.0.1 port is silently dropped instead of refused, so those
// tests would hang until the go test timeout. ::1 is refused at once (or, with
// IPv6 disabled, fails at once as unassignable).
const deadLoopback = "[::1]"

func TestDialContextSocks(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.SocksParentProxy = deadLoopback + ":1" // nothing listening
	ph := newTestHandler(cfg)
	if ph.socksDialer == nil {
		t.Fatal("expected socksDialer to be configured")
	}
	if _, err := ph.dialContext(context.Background(), "tcp", "example.com:443"); err == nil {
		t.Error("expected dial through dead SOCKS proxy to fail")
	}
}

// TestSocksAuthCredentials runs a minimal SOCKS5 server that only speaks the
// RFC 1929 username/password subnegotiation, to verify socksAuthCredentials
// is split and sent correctly. It stops right after accepting the
// credentials, before the CONNECT step, since only the auth exchange matters
// here.
func TestSocksAuthCredentials(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type authResult struct {
		user, pass string
		err        error
	}
	done := make(chan authResult, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- authResult{err: err}
			return
		}
		defer conn.Close()

		hdr := make([]byte, 2)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			done <- authResult{err: err}
			return
		}
		methods := make([]byte, hdr[1])
		if _, err := io.ReadFull(conn, methods); err != nil {
			done <- authResult{err: err}
			return
		}
		conn.Write([]byte{0x05, 0x02}) // select username/password auth

		if _, err := io.ReadFull(conn, hdr); err != nil { // ver, ulen
			done <- authResult{err: err}
			return
		}
		userBuf := make([]byte, hdr[1])
		if _, err := io.ReadFull(conn, userBuf); err != nil {
			done <- authResult{err: err}
			return
		}
		plen := make([]byte, 1)
		if _, err := io.ReadFull(conn, plen); err != nil {
			done <- authResult{err: err}
			return
		}
		passBuf := make([]byte, plen[0])
		if _, err := io.ReadFull(conn, passBuf); err != nil {
			done <- authResult{err: err}
			return
		}
		conn.Write([]byte{0x01, 0x00}) // auth success
		done <- authResult{user: string(userBuf), pass: string(passBuf)}
	}()

	cfg := NewDefaultConfig()
	cfg.SocksParentProxy = ln.Addr().String()
	cfg.SocksAuthCredentials = "alice:s3cret"
	ph := newTestHandler(cfg)

	// The fake server closes right after authenticating, without completing
	// the CONNECT step, so this dial is expected to fail; only the
	// credentials exchanged during auth are under test.
	ph.dialContext(context.Background(), "tcp", "example.com:443")

	res := <-done
	if res.err != nil {
		t.Fatalf("fake SOCKS server error: %v", res.err)
	}
	if res.user != "alice" || res.pass != "s3cret" {
		t.Errorf("SOCKS auth = %q/%q, want %q/%q", res.user, res.pass, "alice", "s3cret")
	}
}

func TestProxyAllowAll(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.AllowedClients = []string{"all"}
	ph := newTestHandler(cfg)
	if !ph.isClientAllowed("203.0.113.9:40000") {
		t.Error(`"all" should allow any client`)
	}
}

func TestProxyServedFromCache(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := "http://cached.example.com/resource"
	seedReq := httptest.NewRequest(http.MethodGet, target, nil)
	body := "from disk cache"
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	resp.Header.Set("Cache-Control", "max-age=3600")
	resp.Header.Set("Date", timeHeader(0))
	if err := cm.Store(seedReq, resp); err != nil {
		t.Fatalf("seed Store failed: %v", err)
	}

	// No backend is running for cached.example.com; a hit must be served
	// entirely from disk.
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestCensoredHeadersStrippedFromRequest(t *testing.T) {
	var gotSecret, gotOther string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecret = r.Header.Get("X-Secret")
		gotOther = r.Header.Get("X-Kept")
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.CensoredHeaders = []string{"X-Secret"}
	ph := newTestHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, backend.URL, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Secret", "s3cret")
	req.Header.Set("X-Kept", "still-here")
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotSecret != "" {
		t.Errorf("X-Secret reached the origin: %q", gotSecret)
	}
	if gotOther != "still-here" {
		t.Errorf("X-Kept = %q, want it forwarded untouched", gotOther)
	}
}

func TestCensoredHeadersStrippedFromResponseAndCache(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=100")
		w.Header().Set("X-Secret", "s3cret")
		w.Header().Set("X-Kept", "still-here")
		w.Write([]byte("body"))
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cfg.CensoredHeaders = []string{"X-Secret"}
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	req := httptest.NewRequest(http.MethodGet, backend.URL, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("X-Secret"); got != "" {
		t.Errorf("X-Secret reached the client: %q", got)
	}
	if got := rr.Header().Get("X-Kept"); got != "still-here" {
		t.Errorf("X-Kept = %q, want it forwarded untouched", got)
	}

	cached := cm.Lookup(req)
	if cached == nil {
		t.Fatal("expected response to be cached")
	}
	defer cached.Body.Close()
	if got := cached.Header.Get("X-Secret"); got != "" {
		t.Errorf("X-Secret was written to the disk cache: %q", got)
	}
}

func TestCensorRefererFalseForwardsUnchanged(t *testing.T) {
	var got string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Referer")
	}))
	defer backend.Close()

	ph := newTestHandler(NewDefaultConfig()) // CensorReferer left at its default (0/"false")

	req := httptest.NewRequest(http.MethodGet, backend.URL, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Referer", "http://third-party.example/page")
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if got != "http://third-party.example/page" {
		t.Errorf("Referer = %q, want it forwarded unchanged", got)
	}
}

func TestCensorRefererTrueDropsAlways(t *testing.T) {
	var got string
	var sawHeader bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, sawHeader = r.Header.Get("Referer"), r.Header.Get("Referer") != ""
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.CensorReferer = 2 // true
	ph := newTestHandler(cfg)

	// Even a same-host Referer is dropped at "true".
	req := httptest.NewRequest(http.MethodGet, backend.URL+"/page2", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Referer", backend.URL+"/page1")
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if sawHeader {
		t.Errorf("Referer = %q, want it dropped unconditionally", got)
	}
}

func TestCensorRefererMaybeDropsOnlyCrossOrigin(t *testing.T) {
	var gotCrossOrigin, gotSameOrigin string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cross" {
			gotCrossOrigin = r.Header.Get("Referer")
		} else {
			gotSameOrigin = r.Header.Get("Referer")
		}
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.CensorReferer = 1 // maybe
	ph := newTestHandler(cfg)

	// Cross-origin Referer: dropped.
	req := httptest.NewRequest(http.MethodGet, backend.URL+"/cross", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Referer", "http://third-party.example/page")
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if gotCrossOrigin != "" {
		t.Errorf("cross-origin Referer = %q, want it dropped", gotCrossOrigin)
	}

	// Same-origin Referer: kept.
	req2 := httptest.NewRequest(http.MethodGet, backend.URL+"/same", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	req2.Header.Set("Referer", backend.URL+"/other-page")
	rr2 := httptest.NewRecorder()
	ph.ServeHTTP(rr2, req2)
	if gotSameOrigin != backend.URL+"/other-page" {
		t.Errorf("same-origin Referer = %q, want it kept", gotSameOrigin)
	}
}

func TestProxyForwardUncachable(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=100")
		w.Write([]byte("live response"))
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)
	uncachable := NewForbiddenList()
	uncachable.Domains = []string{"127.0.0.1"} // backend host
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), uncachable, NewForbiddenList())

	req := httptest.NewRequest(http.MethodGet, backend.URL, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "live response" {
		t.Errorf("body = %q", got)
	}
	if cached := cm.Lookup(req); cached != nil {
		cached.Body.Close()
		t.Error("uncachable response must not be written to the cache")
	}
}

func TestProxyForwardRoundTripError(t *testing.T) {
	ph := newTestHandler(NewDefaultConfig())
	// Port 9999 is in the default allowedPorts range but nothing listens there.
	req := httptest.NewRequest(http.MethodGet, "http://"+deadLoopback+":9999/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

func TestProxyForwardPortNotAllowed(t *testing.T) {
	ph := newTestHandler(NewDefaultConfig())
	// Port 25 (SMTP) is outside the default allowedPorts: the proxy must not
	// open the connection, so this never reaches a backend.
	req := httptest.NewRequest(http.MethodGet, "http://example.com:25/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// seedCacheEntry writes a GET response for target straight into cm, either
// fresh (max-age=3600, dated now) or already stale (max-age=1, dated two
// hours ago), for tests that exercise proxyOffline / relaxTransparency.
func seedCacheEntry(t *testing.T, cm *CacheManager, target, body string, fresh bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	if fresh {
		resp.Header.Set("Cache-Control", "max-age=3600")
		resp.Header.Set("Date", timeHeader(0))
	} else {
		resp.Header.Set("Cache-Control", "max-age=1")
		resp.Header.Set("Date", timeHeader(-2*time.Hour))
	}
	if err := cm.Store(req, resp); err != nil {
		t.Fatalf("seed Store failed: %v", err)
	}
}

func TestProxyOfflineServesStaleFromCache(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cfg.ProxyOffline = true
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := "http://cached.example.com/resource"
	seedCacheEntry(t, cm, target, "stale body", false)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "stale body" {
		t.Errorf("body = %q, want %q", got, "stale body")
	}
	if got := rr.Header().Get("Warning"); got != `112 go-polipo "Disconnected operation"` {
		t.Errorf("Warning = %q, want the disconnected-operation warning", got)
	}
}

func TestProxyOfflineServesFreshWithoutWarning(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cfg.ProxyOffline = true
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := "http://cached.example.com/fresh"
	seedCacheEntry(t, cm, target, "fresh body", true)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "fresh body" {
		t.Errorf("body = %q, want %q", got, "fresh body")
	}
	if got := rr.Header().Get("Warning"); got != "" {
		t.Errorf("Warning = %q, want none for a fresh entry", got)
	}
}

func TestProxyOfflineMissNeverDialsOrigin(t *testing.T) {
	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("should never be seen"))
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cfg.ProxyOffline = true
	ph := newTestHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, backend.URL+"/not-cached", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); !strings.Contains(string(got), "Disconnected operation and object not in cache") {
		t.Errorf("body = %q, want the disconnected-operation message", got)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Error("proxyOffline must never dial the origin")
	}
}

func TestRelaxTransparencyMaybeServesStaleOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cfg.RelaxTransparency = 1 // maybe
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	// Port 9999 is in the default allowedPorts range but nothing listens there,
	// so the round trip fails the way an unreachable origin would.
	target := "http://" + deadLoopback + ":9999/resource"
	seedCacheEntry(t, cm, target, "stale body", false)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (served from stale cache)", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "stale body" {
		t.Errorf("body = %q, want %q", got, "stale body")
	}
	if got := rr.Header().Get("Warning"); got != `111 go-polipo "Revalidation failed"` {
		t.Errorf("Warning = %q, want the revalidation-failed warning", got)
	}
}

func TestRelaxTransparencyFalseStillErrorsOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	// RelaxTransparency left at its default (0/"false").
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := "http://" + deadLoopback + ":9999/resource"
	seedCacheEntry(t, cm, target, "stale body", false)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (relaxTransparency is off)", rr.Code)
	}
}

func TestRelaxTransparencyTrueServesStaleWithoutContactingOrigin(t *testing.T) {
	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("live body"))
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cfg.RelaxTransparency = 2 // true
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedCacheEntry(t, cm, target, "stale body", false)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (served from stale cache)", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "stale body" {
		t.Errorf("body = %q, want %q", got, "stale body")
	}
	if got := rr.Header().Get("Warning"); got != `110 go-polipo "Object is stale"` {
		t.Errorf("Warning = %q, want the object-is-stale warning", got)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Error(`relaxTransparency "true" must not contact the origin when a stale entry exists`)
	}
}

func TestRelaxTransparencyMaybeStillContactsOriginWhenStale(t *testing.T) {
	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("live body"))
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cfg.RelaxTransparency = 1 // maybe: only failures fall back to stale
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedCacheEntry(t, cm, target, "stale body", false)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "live body" {
		t.Errorf(`body = %q, want "live body" (relaxTransparency "maybe" should still try the network)`, got)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
}

// seedStaleEntryWithValidators seeds an already-stale (max-age=1, dated two
// hours ago) GET cache entry for target, carrying whichever of etag /
// lastModified is non-empty, for conditional-revalidation tests.
func seedStaleEntryWithValidators(t *testing.T, cm *CacheManager, target, body, etag, lastModified string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	resp.Header.Set("Cache-Control", "max-age=1")
	resp.Header.Set("Date", timeHeader(-2*time.Hour))
	if etag != "" {
		resp.Header.Set("ETag", etag)
	}
	if lastModified != "" {
		resp.Header.Set("Last-Modified", lastModified)
	}
	if err := cm.Store(req, resp); err != nil {
		t.Fatalf("seed Store failed: %v", err)
	}
}

// seedStaleVaryEntry writes a stale, Vary-bearing cache entry directly to
// disk, bypassing Store's usual staleness computation - Store already always
// writes Vary'd responses, but this pins the Date/max-age so the entry is
// unambiguously stale regardless (LookupStale would already report it
// stale purely for carrying Vary; the low max-age just keeps the test
// independent of that specific mechanism).
func seedStaleVaryEntry(t *testing.T, cm *CacheManager, target, body, vary, etag, lastModified string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	resp.Header.Set("Cache-Control", "max-age=1")
	resp.Header.Set("Date", timeHeader(-2*time.Hour))
	resp.Header.Set("Vary", vary)
	if etag != "" {
		resp.Header.Set("ETag", etag)
	}
	if lastModified != "" {
		resp.Header.Set("Last-Modified", lastModified)
	}
	if err := cm.Store(req, resp); err != nil {
		t.Fatalf("seed Store failed: %v", err)
	}
}

func TestAddConditionalHeadersVaryTrustsOnlyETag(t *testing.T) {
	var gotINM, gotIMS string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		gotIMS = r.Header.Get("If-Modified-Since")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Vary", "Accept-Encoding")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer backend.Close()

	cfg := NewDefaultConfig() // DontTrustVaryETag defaults to 1 ("maybe")
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedStaleVaryEntry(t, cm, target, "cached body", "Accept-Encoding", `"v1"`, timeHeader(-48*time.Hour))

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotINM != `"v1"` {
		t.Errorf("If-None-Match sent = %q, want %q (ETag should be trusted at the default dontTrustVaryETag)", gotINM, `"v1"`)
	}
	if gotIMS != "" {
		t.Errorf("If-Modified-Since sent = %q, want empty (Last-Modified alone is never trusted for a Vary'd entry)", gotIMS)
	}
}

func TestAddConditionalHeadersVaryNoETagMeansFullRefetch(t *testing.T) {
	var sawINM, sawIMS bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawINM = r.Header.Get("If-None-Match") != ""
		sawIMS = r.Header.Get("If-Modified-Since") != ""
		w.Write([]byte("fresh body"))
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	// Only Last-Modified, no ETag: per client.c, a Vary'd entry with no ETag
	// is never revalidated - only a full, unconditional fetch is attempted.
	seedStaleVaryEntry(t, cm, target, "old body", "Accept-Language", "", timeHeader(-48*time.Hour))

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if sawINM || sawIMS {
		t.Error("a Vary'd entry with no ETag should never trigger a conditional request")
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "fresh body" {
		t.Errorf("body = %q, want the full fresh fetch", got)
	}
}

func TestAddConditionalHeadersDontTrustVaryETagTrue(t *testing.T) {
	var sawINM bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawINM = r.Header.Get("If-None-Match") != ""
		w.Write([]byte("fresh body"))
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.DontTrustVaryETag = 2 // "true": never trust an ETag on a Vary'd entry
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedStaleVaryEntry(t, cm, target, "old body", "Accept-Encoding", `"v1"`, "")

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if sawINM {
		t.Error("dontTrustVaryETag=true should suppress If-None-Match even with a stored ETag")
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "fresh body" {
		t.Errorf("body = %q, want the full fresh fetch", got)
	}
}

func TestVaryEntryNeverServedWithoutRevalidation(t *testing.T) {
	hits := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Vary", "Accept-Encoding")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer backend.Close()

	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	// A high max-age would make an ordinary entry fresh; Vary must override
	// that and force a round-trip regardless.
	req := httptest.NewRequest(http.MethodGet, target, nil)
	resp := &http.Response{
		StatusCode: http.StatusOK, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader("cached body")),
	}
	resp.Header.Set("Cache-Control", "max-age=999999")
	resp.Header.Set("Date", timeHeader(0))
	resp.Header.Set("ETag", `"v1"`)
	resp.Header.Set("Vary", "Accept-Encoding")
	if err := cm.Store(req, resp); err != nil {
		t.Fatalf("seed Store failed: %v", err)
	}

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, r)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rr.Code)
		}
	}
	if hits != 2 {
		t.Errorf("origin hits = %d, want 2 (every request must revalidate a Vary'd entry)", hits)
	}
}

func TestConditionalRevalidation304ServesCachedBodyAndRefreshes(t *testing.T) {
	var gotINM string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("Date", timeHeader(0))
		w.WriteHeader(http.StatusNotModified)
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedStaleEntryWithValidators(t, cm, target, "cached body", `"v1"`, "")

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "cached body" {
		t.Errorf("body = %q, want the cached body (a 304 must not replace it)", got)
	}
	if gotINM != `"v1"` {
		t.Errorf("If-None-Match sent = %q, want %q", gotINM, `"v1"`)
	}

	// The 304 carried Cache-Control: max-age=3600 and a fresh Date, so the
	// re-stored entry should now be fresh - a second request must not even
	// need to consider the network.
	if cached := cm.Lookup(httptest.NewRequest(http.MethodGet, target, nil)); cached == nil {
		t.Error("expected the revalidated entry to be fresh")
	} else {
		cached.Body.Close()
	}
}

func TestConditionalRevalidationSendsIfModifiedSince(t *testing.T) {
	lastModified := timeHeader(-48 * time.Hour)
	var gotIMS string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIMS = r.Header.Get("If-Modified-Since")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedStaleEntryWithValidators(t, cm, target, "cached body", "", lastModified)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotIMS != lastModified {
		t.Errorf("If-Modified-Since sent = %q, want %q", gotIMS, lastModified)
	}
}

func TestConditionalRevalidation200ReplacesCache(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A real origin would only do this when the validator no longer
		// matches; the fake backend always answers with new content so the
		// test can check go-polipo replaces the stale entry outright.
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Write([]byte("new body"))
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedStaleEntryWithValidators(t, cm, target, "old body", `"v1"`, "")

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, _ := io.ReadAll(rr.Body); string(got) != "new body" {
		t.Errorf("body = %q, want %q", got, "new body")
	}

	cached := cm.Lookup(httptest.NewRequest(http.MethodGet, target, nil))
	if cached == nil {
		t.Fatal("expected the new response to be cached")
	}
	defer cached.Body.Close()
	if got, _ := io.ReadAll(cached.Body); string(got) != "new body" {
		t.Errorf("cached body = %q, want %q", got, "new body")
	}
}

func TestNoConditionalHeadersWithoutValidators(t *testing.T) {
	var sawINM, sawIMS bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawINM = r.Header.Get("If-None-Match") != ""
		sawIMS = r.Header.Get("If-Modified-Since") != ""
		w.Write([]byte("full body"))
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedStaleEntryWithValidators(t, cm, target, "old body", "", "") // no validators

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if sawINM || sawIMS {
		t.Error("no conditional header should be sent without a stored validator")
	}
}

func TestClientConditionalHeaderNotOverridden(t *testing.T) {
	var gotINM string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := backend.URL + "/resource"
	seedStaleEntryWithValidators(t, cm, target, "cached body", `"cached-etag"`, "")

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("If-None-Match", `"client-etag"`)
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if gotINM != `"client-etag"` {
		t.Errorf("If-None-Match sent = %q, want the client's own %q", gotINM, `"client-etag"`)
	}
	// The 304 vouches for the client's "client-etag" copy, not the cached
	// "cached-etag" one, so it must reach the client rather than be revived.
	if rr.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304 passed through to the client", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("body = %q, want empty (cached body must not be served)", rr.Body.String())
	}
}

func TestClient304PassesThroughWithoutCachedEntry(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	ph := newTestHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, backend.URL+"/never-cached", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("If-Modified-Since", timeHeader(-time.Hour))
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304 passed through (the client's own conditional request)", rr.Code)
	}
}

func TestRevalidation304WithoutCachedEntryIsAnError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer backend.Close()

	tmpDir := t.TempDir()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = tmpDir
	ph := newTestHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, backend.URL+"/never-cached", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (no cached entry to revalidate against)", rr.Code)
	}
}

func TestProxyConnectTunnel(t *testing.T) {
	// Echo backend the tunnel will connect to.
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		c, err := backend.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c)
	}()

	_, portStr, _ := net.SplitHostPort(backend.Addr().String())
	port, _ := strconv.Atoi(portStr)

	cfg := NewDefaultConfig()
	cfg.TunnelAllowedPorts = PortSet{{port, port}}
	proxySrv := httptest.NewServer(newTestHandler(cfg))
	defer proxySrv.Close()

	pc, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	target := backend.Addr().String()
	fmt.Fprintf(pc, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)

	br := bufio.NewReader(pc)
	statusLine, err := br.ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "200") {
		t.Fatalf("CONNECT status line = %q, err = %v", statusLine, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading CONNECT headers: %v", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	if _, err := io.WriteString(pc, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("reading tunnelled echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("tunnel echo = %q, want %q", buf, "ping")
	}
}

func TestProxyAuthCredentialsUnsetByDefault(t *testing.T) {
	ph := newTestHandler(NewDefaultConfig())
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if !ph.isAuthorized(req) {
		t.Error("no authCredentials configured: every request should be authorized")
	}
}

func TestProxyAuthCredentials(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.AuthCredentials = "alice:secret"
	cfg.AuthRealm = "go-polipo"
	ph := newTestHandler(cfg)

	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}

	// Proxied (absolute-form) request with no credentials: 407, challenged via
	// Proxy-Authenticate.
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusProxyAuthRequired {
		t.Errorf("no credentials: got %d, want 407", rr.Code)
	}
	if got := rr.Header().Get("Proxy-Authenticate"); got != `Basic realm="go-polipo"` {
		t.Errorf("Proxy-Authenticate = %q", got)
	}

	// Wrong password: still 407.
	reqWrong := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	reqWrong.RemoteAddr = "127.0.0.1:12345"
	reqWrong.Header.Set("Proxy-Authorization", basic("alice", "wrong"))
	rrWrong := httptest.NewRecorder()
	ph.ServeHTTP(rrWrong, reqWrong)
	if rrWrong.Code != http.StatusProxyAuthRequired {
		t.Errorf("wrong credentials: got %d, want 407", rrWrong.Code)
	}

	// Correct credentials pass the auth gate and reach the local interface
	// (chosen because it needs no outbound network access).
	reqOK := httptest.NewRequest(http.MethodGet, "http://example.com/polipo/", nil)
	reqOK.RemoteAddr = "127.0.0.1:12345"
	reqOK.Header.Set("Proxy-Authorization", basic("alice", "secret"))
	rrOK := httptest.NewRecorder()
	ph.ServeHTTP(rrOK, reqOK)
	if rrOK.Code != http.StatusOK {
		t.Errorf("correct credentials: got %d, want 200", rrOK.Code)
	}

	// Local-form request (a browser pointed straight at go-polipo rather than
	// configured to use it as a proxy): challenged with 401/WWW-Authenticate,
	// against Authorization rather than Proxy-Authorization, matching
	// Polipo's urlIsLocal split.
	reqLocal := httptest.NewRequest(http.MethodGet, "/polipo/", nil)
	reqLocal.RemoteAddr = "127.0.0.1:12345"
	rrLocal := httptest.NewRecorder()
	ph.ServeHTTP(rrLocal, reqLocal)
	if rrLocal.Code != http.StatusUnauthorized {
		t.Errorf("local-form, no credentials: got %d, want 401", rrLocal.Code)
	}
	if got := rrLocal.Header().Get("WWW-Authenticate"); got != `Basic realm="go-polipo"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}

	reqLocalOK := httptest.NewRequest(http.MethodGet, "/polipo/", nil)
	reqLocalOK.RemoteAddr = "127.0.0.1:12345"
	reqLocalOK.Header.Set("Authorization", basic("alice", "secret"))
	rrLocalOK := httptest.NewRecorder()
	ph.ServeHTTP(rrLocalOK, reqLocalOK)
	if rrLocalOK.Code != http.StatusOK {
		t.Errorf("local-form, correct Authorization: got %d, want 200", rrLocalOK.Code)
	}

	// CONNECT without credentials: 407, same as any other proxied request.
	reqConnect := httptest.NewRequest(http.MethodConnect, "example.com:443", nil)
	reqConnect.RemoteAddr = "127.0.0.1:12345"
	reqConnect.RequestURI = "example.com:443"
	rrConnect := httptest.NewRecorder()
	ph.ServeHTTP(rrConnect, reqConnect)
	if rrConnect.Code != http.StatusProxyAuthRequired {
		t.Errorf("CONNECT, no credentials: got %d, want 407", rrConnect.Code)
	}
}

func TestProxyConnectDialFailure(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.TunnelAllowedPorts = PortSet{{1, 1}}
	proxySrv := httptest.NewServer(newTestHandler(cfg))
	defer proxySrv.Close()

	pc, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	// Port 1 is allowed but nothing listens there: expect 502 from the tunnel.
	fmt.Fprintf(pc, "CONNECT %[1]s:1 HTTP/1.1\r\nHost: %[1]s:1\r\n\r\n", deadLoopback)
	statusLine, err := bufio.NewReader(pc).ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "502") {
		t.Fatalf("status line = %q, err = %v; want 502", statusLine, err)
	}
}

func TestParseByteRange(t *testing.T) {
	tests := []struct {
		header     string
		size       int64
		result     rangeResult
		start, end int64
	}{
		{"bytes=0-4", 10, rangeSatisfiable, 0, 4},
		{"bytes=5-", 10, rangeSatisfiable, 5, 9},
		{"bytes=-3", 10, rangeSatisfiable, 7, 9},
		{"bytes=-100", 10, rangeSatisfiable, 0, 9},  // suffix longer than resource: all of it
		{"bytes=0-100", 10, rangeSatisfiable, 0, 9}, // end clamped to the last byte
		{"bytes=0-0", 10, rangeSatisfiable, 0, 0},
		{"bytes=9-9", 10, rangeSatisfiable, 9, 9},
		{"bytes=10-20", 10, rangeUnsatisfiable, 0, 0}, // first >= size
		{"bytes=-0", 10, rangeUnsatisfiable, 0, 0},    // zero-length suffix
		{"bytes=0-", 0, rangeUnsatisfiable, 0, 0},     // empty resource
		{"bytes=-5", 0, rangeUnsatisfiable, 0, 0},
		{"bytes=5-2", 10, rangeNotApplicable, 0, 0}, // last < first
		{"bytes=abc-5", 10, rangeNotApplicable, 0, 0},
		{"bytes=0-abc", 10, rangeNotApplicable, 0, 0},
		{"bytes=-abc", 10, rangeNotApplicable, 0, 0},
		{"items=0-5", 10, rangeNotApplicable, 0, 0},     // wrong unit
		{"bytes=0-1,3-4", 10, rangeNotApplicable, 0, 0}, // multi-range: unsupported
		{"bytes=", 10, rangeNotApplicable, 0, 0},
		{"garbage", 10, rangeNotApplicable, 0, 0},
	}
	for _, tt := range tests {
		start, end, result := parseByteRange(tt.header, tt.size)
		if result != tt.result {
			t.Errorf("parseByteRange(%q, %d) result = %v, want %v", tt.header, tt.size, result, tt.result)
			continue
		}
		if result == rangeSatisfiable && (start != tt.start || end != tt.end) {
			t.Errorf("parseByteRange(%q, %d) = (%d, %d), want (%d, %d)", tt.header, tt.size, start, end, tt.start, tt.end)
		}
	}
}

func TestIfRangeMatches(t *testing.T) {
	const date = "Wed, 21 Oct 2026 07:28:00 GMT"
	const otherDate = "Wed, 21 Oct 2026 08:00:00 GMT"

	tests := []struct {
		name    string
		ifRange string
		header  http.Header
		want    bool
	}{
		{"matching strong etag", `"abc"`, http.Header{"Etag": {`"abc"`}}, true},
		{"mismatched etag", `"abc"`, http.Header{"Etag": {`"def"`}}, false},
		{"weak validator in If-Range never matches", `W/"abc"`, http.Header{"Etag": {`"abc"`}}, false},
		{"weak validator on the entity never matches", `"abc"`, http.Header{"Etag": {`W/"abc"`}}, false},
		{"no etag on entity", `"abc"`, http.Header{}, false},
		{"matching date", date, http.Header{"Last-Modified": {date}}, true},
		{"mismatched date", date, http.Header{"Last-Modified": {otherDate}}, false},
		{"date with no Last-Modified on entity", date, http.Header{}, false},
		{"unparsable value", "not-a-date-or-etag", http.Header{"Last-Modified": {date}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ifRangeMatches(tt.ifRange, tt.header); got != tt.want {
				t.Errorf("ifRangeMatches(%q, %v) = %v, want %v", tt.ifRange, tt.header, got, tt.want)
			}
		})
	}
}

// seedRangeTestCache stores a cacheable response with body, an ETag and a
// Last-Modified date, returning a ProxyHandler ready to serve it from cache
// (no backend is reachable for the target host, so any non-cache response
// would fail the test).
func seedRangeTestCache(t *testing.T, body string) (ph *ProxyHandler, target string) {
	t.Helper()
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	ph = NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target = "http://cached.example.com/range-resource"
	seedReq := httptest.NewRequest(http.MethodGet, target, nil)
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	resp.Header.Set("Cache-Control", "max-age=3600")
	resp.Header.Set("Date", timeHeader(0))
	resp.Header.Set("ETag", `"range-etag"`)
	resp.Header.Set("Last-Modified", timeHeader(-time.Hour))
	if err := cm.Store(seedReq, resp); err != nil {
		t.Fatalf("seed Store failed: %v", err)
	}
	return ph, target
}

func rangeRequest(target, rangeHeader, ifRangeHeader string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	if ifRangeHeader != "" {
		req.Header.Set("If-Range", ifRangeHeader)
	}
	return req
}

func TestRangeServedFromCache(t *testing.T) {
	const body = "0123456789"
	ph, target := seedRangeTestCache(t, body)

	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, rangeRequest(target, "bytes=2-4", ""))

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusPartialContent)
	}
	if got, want := rr.Body.String(), "234"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := rr.Header().Get("Content-Range"), "bytes 2-4/10"; got != want {
		t.Errorf("Content-Range = %q, want %q", got, want)
	}
	if got, want := rr.Header().Get("Content-Length"), "3"; got != want {
		t.Errorf("Content-Length = %q, want %q", got, want)
	}
	if got, want := rr.Header().Get("Accept-Ranges"), "bytes"; got != want {
		t.Errorf("Accept-Ranges = %q, want %q", got, want)
	}
}

func TestRangeUnsatisfiableFromCache(t *testing.T) {
	const body = "0123456789"
	ph, target := seedRangeTestCache(t, body)

	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, rangeRequest(target, "bytes=100-200", ""))

	if rr.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusRequestedRangeNotSatisfiable)
	}
	if got, want := rr.Header().Get("Content-Range"), "bytes */10"; got != want {
		t.Errorf("Content-Range = %q, want %q", got, want)
	}
}

func TestRangeIgnoredWhenNotApplicable(t *testing.T) {
	const body = "0123456789"

	for _, tc := range []struct {
		name   string
		range_ string
	}{
		{"multi-range", "bytes=0-1,3-4"},
		{"wrong unit", "items=0-5"},
		{"unparsable", "garbage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ph, target := seedRangeTestCache(t, body)
			rr := httptest.NewRecorder()
			ph.ServeHTTP(rr, rangeRequest(target, tc.range_, ""))

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (whole entity)", rr.Code)
			}
			if got := rr.Body.String(); got != body {
				t.Errorf("body = %q, want the whole entity %q", got, body)
			}
		})
	}
}

func TestRangeIfRangeMismatchServesWhole(t *testing.T) {
	const body = "0123456789"
	ph, target := seedRangeTestCache(t, body)

	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, rangeRequest(target, "bytes=2-4", `"stale-etag"`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (If-Range mismatch ignores Range)", rr.Code)
	}
	if got := rr.Body.String(); got != body {
		t.Errorf("body = %q, want the whole entity %q", got, body)
	}
}

func TestRangeIfRangeMatchServesPartial(t *testing.T) {
	const body = "0123456789"
	ph, target := seedRangeTestCache(t, body)

	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, rangeRequest(target, "bytes=2-4", `"range-etag"`))

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d (If-Range matches)", rr.Code, http.StatusPartialContent)
	}
	if got, want := rr.Body.String(), "234"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestRangeIgnoredForHead(t *testing.T) {
	const body = "0123456789"
	ph, target := seedRangeTestCache(t, body)

	req := httptest.NewRequest(http.MethodHead, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Range", "bytes=2-4")
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Range is not honored for HEAD)", rr.Code)
	}
}

func TestRangeServedViaProxyOffline(t *testing.T) {
	const body = "0123456789"
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.ProxyOffline = true
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())

	target := "http://cached.example.com/range-resource"
	seedReq := httptest.NewRequest(http.MethodGet, target, nil)
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	resp.Header.Set("Cache-Control", "max-age=3600")
	resp.Header.Set("Date", timeHeader(0))
	if err := cm.Store(seedReq, resp); err != nil {
		t.Fatalf("seed Store failed: %v", err)
	}

	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, rangeRequest(target, "bytes=2-4", ""))

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusPartialContent)
	}
	if got, want := rr.Body.String(), "234"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func newTestHandler(cfg *Config) *ProxyHandler {
	return NewProxyHandler(cfg, NewCacheManager(cfg), NewForbiddenList(), NewForbiddenList())
}

func TestProxyAllowedClients(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.AllowedClients = []string{"192.168.1.1"}
	cm := NewCacheManager(cfg)
	forbidden := NewForbiddenList()
	uncachable := NewForbiddenList()
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable)

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
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable)

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
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable)

	req := httptest.NewRequest(http.MethodGet, "http://badsite.com/index.html", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()

	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
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
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable)

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
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable)

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
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable)

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
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable)

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
	ph := NewProxyHandler(cfg, cm, forbidden, uncachable)

	req := httptest.NewRequest(http.MethodConnect, "badsite.com:443", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.RequestURI = "badsite.com:443"
	rr := httptest.NewRecorder()

	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", rr.Code)
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

func TestDialContextSocks(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.SocksParentProxy = "127.0.0.1:1" // nothing listening
	ph := newTestHandler(cfg)
	if ph.socksDialer == nil {
		t.Fatal("expected socksDialer to be configured")
	}
	if _, err := ph.dialContext(context.Background(), "tcp", "example.com:443"); err == nil {
		t.Error("expected dial through dead SOCKS proxy to fail")
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
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList())

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
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), uncachable)

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
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9999/", nil)
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
	fmt.Fprintf(pc, "CONNECT 127.0.0.1:1 HTTP/1.1\r\nHost: 127.0.0.1:1\r\n\r\n")
	statusLine, err := bufio.NewReader(pc).ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "502") {
		t.Fatalf("status line = %q, err = %v; want 502", statusLine, err)
	}
}

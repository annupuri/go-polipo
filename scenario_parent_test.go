package main

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Scenarios with a parent proxy between go-polipo and the origins. The
// parents here relay for real, so a scenario sees the whole path: client,
// go-polipo, parent, origin.

// relay copies a and b into each other until both directions end.
func relay(a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		io.Copy(a, b)
		a.Close()
		close(done)
	}()
	io.Copy(b, a)
	b.Close()
	<-done
}

// scenarioHTTPParent is an HTTP parent proxy that requires Basic
// credentials, forwards requests in absolute form, and opens CONNECT
// tunnels. It records each request it receives as "METHOD target". With
// useTLS it is an https:// parent, with httptest's certificate.
type scenarioHTTPParent struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newScenarioHTTPParent(t *testing.T, wantAuth string, useTLS bool) *scenarioHTTPParent {
	t.Helper()
	p := &scenarioHTTPParent{}
	transport := &http.Transport{} // straight to the origin, whatever the environment says
	t.Cleanup(transport.CloseIdleConnections)
	p.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.String()
		if r.Method == http.MethodConnect {
			target = r.Host
		}
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+target)
		p.mu.Unlock()
		if r.Header.Get("Proxy-Authorization") != wantAuth {
			w.Header().Set("Proxy-Authenticate", `Basic realm="parent"`)
			http.Error(w, "parent wants credentials", http.StatusProxyAuthRequired)
			return
		}
		if r.Method == http.MethodConnect {
			dst, err := net.Dial("tcp", r.Host)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			conn, brw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				dst.Close()
				return
			}
			io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
			if n := brw.Reader.Buffered(); n > 0 { // bytes the client sent early
				b, _ := brw.Reader.Peek(n)
				dst.Write(b)
			}
			relay(conn, dst)
			return
		}
		out, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out.Header = r.Header.Clone()
		out.Header.Del("Proxy-Authorization")
		resp, err := transport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		copyHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	if useTLS {
		p.StartTLS()
	} else {
		p.Start()
	}
	t.Cleanup(p.Close)
	return p
}

// requests returns what the parent has received so far.
func (p *scenarioHTTPParent) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// scenarioSocksParent is a SOCKS5 server (RFC 1928) that requires a
// username and password (RFC 1929) and relays CONNECT requests. It records
// the destination of each CONNECT it accepts.
type scenarioSocksParent struct {
	ln   net.Listener
	mu   sync.Mutex
	seen []string
}

func newScenarioSocksParent(t *testing.T, user, password string) *scenarioSocksParent {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &scenarioSocksParent{ln: ln}
	// go-polipo keeps idle connections for reuse, so close what is still
	// open rather than wait for it.
	var (
		connsMu sync.Mutex
		open    = map[net.Conn]bool{}
		wg      sync.WaitGroup
	)
	t.Cleanup(func() {
		ln.Close()
		connsMu.Lock()
		for c := range open {
			c.Close()
		}
		connsMu.Unlock()
		wg.Wait()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			connsMu.Lock()
			open[c] = true
			connsMu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serve(c, user, password)
				connsMu.Lock()
				delete(open, c)
				connsMu.Unlock()
			}()
		}
	}()
	return s
}

func (s *scenarioSocksParent) serve(c net.Conn, user, password string) {
	defer c.Close()
	br := bufio.NewReader(c)
	read := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			return nil
		}
		return b
	}
	// Greeting: version, then the methods the client offers.
	hdr := read(2)
	if hdr == nil || hdr[0] != 5 || read(int(hdr[1])) == nil {
		return
	}
	c.Write([]byte{5, 2}) // username/password
	// RFC 1929: version 1, then length-prefixed user and password.
	ver := read(2)
	if ver == nil {
		return
	}
	gotUser := read(int(ver[1]))
	plen := read(1)
	if gotUser == nil || plen == nil {
		return
	}
	gotPassword := read(int(plen[0]))
	if string(gotUser) != user || string(gotPassword) != password {
		c.Write([]byte{1, 1}) // failure
		return
	}
	c.Write([]byte{1, 0})
	// Request: version, command, reserved, address type, address, port.
	req := read(4)
	if req == nil || req[1] != 1 { // CONNECT only
		return
	}
	var host string
	switch req[3] {
	case 1:
		host = net.IP(read(4)).String()
	case 3:
		n := read(1)
		if n == nil {
			return
		}
		host = string(read(int(n[0])))
	case 4:
		host = net.IP(read(16)).String()
	default:
		return
	}
	port := read(2)
	if port == nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port))))
	s.mu.Lock()
	s.seen = append(s.seen, target)
	s.mu.Unlock()
	dst, err := net.Dial("tcp", target)
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0}) // connection refused
		return
	}
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	relay(&bufferedConn{Conn: c, r: br}, dst)
}

// bufferedConn reads through r, which may hold bytes already read from Conn.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// destinations returns the CONNECT destinations accepted so far.
func (s *scenarioSocksParent) destinations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// wantSeen checks that got lists exactly want, in order.
func wantSeen(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// TestScenarioHTTPParentProxy: with parentProxy and parentAuthCredentials,
// plain HTTP goes to the parent in absolute form and HTTPS as CONNECT, both
// with the credentials; the cache still answers repeat visits itself; and a
// parent that refuses the credentials is a 502, not a 407 the client cannot
// answer.
func TestScenarioHTTPParentProxy(t *testing.T) {
	origin := newScenarioOrigin(t, false, nil)
	secure := newScenarioOrigin(t, true, nil)
	parent := newScenarioHTTPParent(t, "Basic cGFyZW50OnB3", false) // parent:pw
	parentAddr := parent.Listener.Addr().String()
	p := startScenarioProxy(t, secure.Server,
		"parentProxy = "+parentAddr, "parentAuthCredentials = parent:pw", "tunnelAllowedPorts = 1-65535")
	page := origin.URL + "/page"
	scope := t

	runScenario(t, []scenarioStep{
		{"a page is fetched through the parent", func(t *testing.T) {
			resp, body := p.get(t, page)
			wantResponse(t, resp, body, http.StatusOK, "/page v1")
			wantHits(t, origin, "/page", 1)
			wantSeen(t, "requests at the parent", parent.requests(), []string{"GET " + page})
		}},
		{"a second visit is served from go-polipo's cache", func(t *testing.T) {
			resp, body := p.get(t, page)
			wantResponse(t, resp, body, http.StatusOK, "/page v1")
			wantSeen(t, "requests at the parent", parent.requests(), []string{"GET " + page})
		}},
		{"an HTTPS page goes through the parent as CONNECT", func(t *testing.T) {
			resp, body := p.get(t, secure.URL+"/secure")
			wantResponse(t, resp, body, http.StatusOK, "/secure v1")
			wantHits(t, secure, "/secure", 1)
			wantSeen(t, "requests at the parent", parent.requests(),
				[]string{"GET " + page, "CONNECT " + secure.Listener.Addr().String()})
		}},
		{"with a wrong password, the client gets a 502", func(t *testing.T) {
			wrong := startScenarioProxy(scope, nil, "parentProxy = "+parentAddr, "parentAuthCredentials = parent:wrong")
			resp, body := wrong.get(t, origin.URL+"/other")
			wantResponse(t, resp, body, http.StatusBadGateway, "")
			wantHits(t, origin, "/other", 0)
		}},
	})
}

// TestScenarioSocksParentProxy: with socksParentProxy and
// socksAuthCredentials, go-polipo reaches every origin, HTTP and HTTPS,
// through the SOCKS server; and a SOCKS server that refuses the credentials
// is a 502.
func TestScenarioSocksParentProxy(t *testing.T) {
	origin := newScenarioOrigin(t, false, nil)
	secure := newScenarioOrigin(t, true, nil)
	socks := newScenarioSocksParent(t, "socks", "pw")
	socksAddr := socks.ln.Addr().String()
	p := startScenarioProxy(t, secure.Server,
		"socksParentProxy = "+socksAddr, "socksAuthCredentials = socks:pw", "tunnelAllowedPorts = 1-65535")
	originAddr := origin.Listener.Addr().String()
	scope := t

	runScenario(t, []scenarioStep{
		{"a page is fetched through the SOCKS server", func(t *testing.T) {
			resp, body := p.get(t, origin.URL+"/page")
			wantResponse(t, resp, body, http.StatusOK, "/page v1")
			wantHits(t, origin, "/page", 1)
			wantSeen(t, "SOCKS destinations", socks.destinations(), []string{originAddr})
		}},
		{"a second visit is served from go-polipo's cache", func(t *testing.T) {
			resp, body := p.get(t, origin.URL+"/page")
			wantResponse(t, resp, body, http.StatusOK, "/page v1")
			wantHits(t, origin, "/page", 1)
		}},
		{"an HTTPS tunnel goes through the SOCKS server too", func(t *testing.T) {
			resp, body := p.get(t, secure.URL+"/secure")
			wantResponse(t, resp, body, http.StatusOK, "/secure v1")
			wantHits(t, secure, "/secure", 1)
			wantSeen(t, "SOCKS destinations", socks.destinations(),
				[]string{originAddr, secure.Listener.Addr().String()})
		}},
		{"with a wrong password, the client gets a 502", func(t *testing.T) {
			wrong := startScenarioProxy(scope, nil, "socksParentProxy = "+socksAddr, "socksAuthCredentials = socks:wrong")
			resp, body := wrong.get(t, origin.URL+"/other")
			wantResponse(t, resp, body, http.StatusBadGateway, "")
			wantHits(t, origin, "/other", 0)
		}},
	})
}

// TestScenarioHTTPSParentProxy: with an https:// parentProxy, go-polipo
// speaks TLS to the parent for both forwarded requests and CONNECT, and a
// parent whose certificate it does not trust is a 502, never a fallback to
// plain HTTP.
func TestScenarioHTTPSParentProxy(t *testing.T) {
	origin := newScenarioOrigin(t, false, nil)
	secure := newScenarioOrigin(t, true, nil)
	parent := newScenarioHTTPParent(t, "Basic cGFyZW50OnB3", true) // parent:pw
	parentCAs := parent.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	// The scenario proxy is built from a config file and cannot be given
	// the test parent's certificate, so this one is built from a Config.
	start := func(t *testing.T, trustParent bool) *http.Client {
		t.Helper()
		cfg := newTestConfig()
		cfg.ParentProxy = parent.URL
		cfg.ParentAuthCredentials = "parent:pw"
		cfg.TunnelAllowedPorts = PortSet{{1, 65535}}
		ph := newTestHandler(cfg)
		if trustParent {
			ph.transport.TLSClientConfig = &tls.Config{RootCAs: parentCAs}
		}
		proxy := httptest.NewServer(ph)
		t.Cleanup(proxy.Close)
		transport := &http.Transport{
			Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.Listener.Addr().String()}),
			TLSClientConfig: &tls.Config{RootCAs: secure.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs},
		}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 10 * time.Second}
	}
	get := func(t *testing.T, c *http.Client, target string) (int, string) {
		t.Helper()
		resp, err := c.Get(target)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	scope := t
	var client *http.Client

	runScenario(t, []scenarioStep{
		{"go-polipo starts with an https:// parent it trusts", func(t *testing.T) {
			client = start(scope, true)
		}},
		{"a page is fetched through the parent over TLS", func(t *testing.T) {
			if code, body := get(t, client, origin.URL+"/page"); code != http.StatusOK || body != "/page v1" {
				t.Fatalf("got %d %q, want 200 %q", code, body, "/page v1")
			}
			wantSeen(t, "requests at the parent", parent.requests(), []string{"GET " + origin.URL + "/page"})
		}},
		{"an HTTPS page goes through the parent as CONNECT over TLS", func(t *testing.T) {
			if code, body := get(t, client, secure.URL+"/secure"); code != http.StatusOK || body != "/secure v1" {
				t.Fatalf("got %d %q, want 200 %q", code, body, "/secure v1")
			}
			wantSeen(t, "requests at the parent", parent.requests(),
				[]string{"GET " + origin.URL + "/page", "CONNECT " + secure.Listener.Addr().String()})
		}},
		{"a parent go-polipo does not trust is a 502", func(t *testing.T) {
			untrusting := start(scope, false)
			if code, _ := get(t, untrusting, origin.URL+"/other"); code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502", code)
			}
			if _, err := untrusting.Get(secure.URL + "/other"); err == nil {
				t.Fatal("an HTTPS page went through a parent go-polipo does not trust")
			}
			wantHits(t, origin, "/other", 0)
			wantHits(t, secure, "/other", 0)
		}},
	})
}

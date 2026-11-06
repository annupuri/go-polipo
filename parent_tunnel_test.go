package main

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeParent is an HTTP parent proxy that accepts one CONNECT, records it,
// answers with reply and, on a 2xx, echoes the tunnel.
type fakeParent struct {
	ln  net.Listener
	got chan *http.Request
}

func newFakeParent(t *testing.T, reply string) *fakeParent {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	p := &fakeParent{ln: ln, got: make(chan *http.Request, 1)}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReader(c)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		p.got <- req
		io.WriteString(c, reply)
		if strings.HasPrefix(reply, "HTTP/1.1 2") {
			io.Copy(c, br) // echo, starting with anything already buffered
		}
	}()
	return p
}

// connectVia sends one write - CONNECT target plus extra - through a
// go-polipo built from cfg and returns the client end and its reader.
func connectVia(t *testing.T, cfg *Config, target, extra string) (*ProxyHandler, net.Conn, *bufio.Reader) {
	t.Helper()
	ph := newTestHandler(cfg)
	proxySrv := httptest.NewServer(ph)
	t.Cleanup(proxySrv.Close)
	pc, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	pc.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(pc, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"+extra)
	return ph, pc, bufio.NewReader(pc)
}

func readStatus(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the status line: %v", err)
	}
	for {
		h, err := br.ReadString('\n')
		if err != nil || h == "\r\n" {
			break
		}
	}
	return line
}

// CONNECT goes through the HTTP parent proxy, with parentAuthCredentials, as
// in Polipo. The target name is resolved by the parent, not here, and
// forbidLocalDestinations does not stop a parent on localhost.
func TestConnectThroughParentProxy(t *testing.T) {
	parent := newFakeParent(t, "HTTP/1.1 200 Connection established\r\n\r\nhi-from-target")
	cfg := NewDefaultConfig() // forbidLocalDestinations on
	cfg.NoDiskCache = true
	cfg.ParentProxy = parent.ln.Addr().String()
	cfg.ParentAuthCredentials = "bob:hunter2"

	ph, pc, br := connectVia(t, cfg, "origin.example:443", "hello")
	if line := readStatus(t, br); !strings.Contains(line, "200") {
		t.Fatalf("status line = %q, want 200", line)
	}
	req := <-parent.got
	if req.Method != http.MethodConnect || req.RequestURI != "origin.example:443" || req.Host != "origin.example:443" {
		t.Errorf("parent got %s %s (Host %s), want CONNECT origin.example:443", req.Method, req.RequestURI, req.Host)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("bob:hunter2"))
	if got := req.Header.Get("Proxy-Authorization"); got != want {
		t.Errorf("Proxy-Authorization = %q, want %q", got, want)
	}

	// What the parent sent past its reply reaches the client, and the
	// client's early "hello" reached the parent, which echoes it.
	buf := make([]byte, len("hi-from-targethello"))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "hi-from-targethello" {
		t.Fatalf("through the tunnel: %q, %v", buf, err)
	}
	io.WriteString(pc, "ping")
	buf = make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q, %v", buf, err)
	}
	if n := ph.stats.tunnels.Load(); n != 1 {
		t.Errorf("tunnels = %d, want 1", n)
	}
}

// Credentials written into parentProxy are used when parentAuthCredentials
// is unset, as the Transport does for forwarded requests.
func TestConnectThroughParentProxyURLCredentials(t *testing.T) {
	parent := newFakeParent(t, "HTTP/1.1 200 OK\r\n\r\n")
	cfg := newTestConfig()
	cfg.ParentProxy = "http://carol:s3cret@" + parent.ln.Addr().String()

	_, _, br := connectVia(t, cfg, "origin.example:443", "")
	if line := readStatus(t, br); !strings.Contains(line, "200") {
		t.Fatalf("status line = %q, want 200", line)
	}
	req := <-parent.got
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("carol:s3cret"))
	if got := req.Header.Get("Proxy-Authorization"); got != want {
		t.Errorf("Proxy-Authorization = %q, want %q", got, want)
	}
}

// A parent that refuses gives the client a 502, not the parent's own reply:
// a 407 from the parent would ask the client for credentials it has no way
// to pass on.
func TestConnectThroughParentProxyRefused(t *testing.T) {
	for _, reply := range []string{
		"HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"parent\"\r\nContent-Length: 0\r\n\r\n",
		"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n",
	} {
		parent := newFakeParent(t, reply)
		cfg := newTestConfig()
		cfg.ParentProxy = parent.ln.Addr().String()

		ph, _, br := connectVia(t, cfg, "origin.example:443", "")
		if line := readStatus(t, br); !strings.Contains(line, "502") {
			t.Errorf("parent %q: status line = %q, want 502", strings.SplitN(reply, "\r\n", 2)[0], line)
		}
		if n := ph.stats.tunnelFailures.Load(); n != 1 {
			t.Errorf("tunnelFailures = %d, want 1", n)
		}
	}
}

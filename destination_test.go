package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAddressClasses(t *testing.T) {
	tests := []struct {
		addr           string
		local, private bool
	}{
		{"127.0.0.1", true, false},
		{"127.1.2.3", true, false},
		{"::1", true, false},
		{"::ffff:127.0.0.1", true, false}, // IPv4-mapped
		{"0.0.0.0", true, false},
		{"0.1.2.3", true, false},
		{"::", true, false},
		{"169.254.169.254", true, false}, // cloud metadata
		{"fe80::1", true, false},
		{"fe80::1%eth0", true, false},
		{"224.0.0.1", true, false},
		{"ff02::1", true, false},
		{"10.0.0.1", false, true},
		{"172.16.0.1", false, true},
		{"192.168.1.1", false, true},
		{"::ffff:192.168.1.1", false, true},
		{"fd00::1", false, true},
		{"100.64.0.1", false, true},
		{"93.184.215.14", false, false},
		{"2606:4700::1111", false, false},
		{"172.32.0.1", false, false},
	}
	for _, tt := range tests {
		a := netip.MustParseAddr(tt.addr)
		if got := isLocalAddress(a); got != tt.local {
			t.Errorf("isLocalAddress(%s) = %v, want %v", tt.addr, got, tt.local)
		}
		if got := isPrivateAddress(a); got != tt.private {
			t.Errorf("isPrivateAddress(%s) = %v, want %v", tt.addr, got, tt.private)
		}
	}
}

func TestCheckDestination(t *testing.T) {
	tests := []struct {
		local, private bool
		addr           string
		refused        bool
	}{
		{true, false, "127.0.0.1:80", true},
		{true, false, "[::1]:443", true},
		{true, false, "192.168.1.1:80", false},
		{true, false, "93.184.215.14:80", false},
		{true, true, "192.168.1.1:80", true},
		{false, true, "127.0.0.1:80", false},
		{false, true, "10.0.0.1:443", true},
		{true, false, "not-an-address", true}, // fail closed
	}
	for _, tt := range tests {
		cfg := NewDefaultConfig()
		cfg.ForbidLocalDestinations, cfg.ForbidPrivateDestinations = tt.local, tt.private
		ph := newTestHandler(cfg)
		err := ph.checkDestination("tcp", tt.addr, nil)
		if refused := errors.Is(err, errForbiddenDestination); refused != tt.refused {
			t.Errorf("local=%v private=%v %s: err = %v, want refused=%v", tt.local, tt.private, tt.addr, err, tt.refused)
		}
	}
}

// loopbackOrigin is an origin on 127.0.0.1 that counts the requests it gets.
func loopbackOrigin(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "local service")
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestForbidLocalDestinationsForward(t *testing.T) {
	origin, hits := loopbackOrigin(t)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))

	cfg := NewDefaultConfig()
	cfg.NoDiskCache = true
	ph := newTestHandler(cfg)

	// A URL naming 127.0.0.1, and a hostname that resolves to it (what DNS
	// rebinding does with a name of the attacker's own).
	for _, target := range []string{origin.URL + "/", "http://localhost:" + port + "/"} {
		rr := serveOnce(ph, http.MethodGet, target)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", target, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "127.0.0.1") {
			t.Errorf("%s: body reveals the resolved address: %q", target, rr.Body.String())
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("origin got %d requests, want 0", n)
	}
	if n := ph.stats.denied.Load(); n != 2 {
		t.Errorf("denied = %d, want 2", n)
	}
	if n := ph.stats.upstreamErrors.Load(); n != 0 {
		t.Errorf("upstreamErrors = %d, want 0", n)
	}

	// Turned off, the same request goes through.
	cfg.ForbidLocalDestinations = false
	ph = newTestHandler(cfg)
	if rr := serveOnce(ph, http.MethodGet, origin.URL+"/"); rr.Code != http.StatusOK {
		t.Errorf("forbidLocalDestinations=false: status = %d, want 200", rr.Code)
	}
}

func TestForbidLocalDestinationsConnect(t *testing.T) {
	origin, hits := loopbackOrigin(t)
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
	port, _ := strconv.Atoi(portStr)

	cfg := NewDefaultConfig()
	cfg.NoDiskCache = true
	cfg.TunnelAllowedPorts = PortSet{{port, port}}
	ph := newTestHandler(cfg)
	proxySrv := httptest.NewServer(ph)
	defer proxySrv.Close()

	pc, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	fmt.Fprintf(pc, "CONNECT %[1]s HTTP/1.1\r\nHost: %[1]s\r\n\r\n", net.JoinHostPort(host, portStr))
	statusLine, err := bufio.NewReader(pc).ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "403") {
		t.Fatalf("status line = %q, err = %v; want 403", statusLine, err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("origin got %d requests, want 0", n)
	}
	if n := ph.stats.denied.Load(); n != 1 {
		t.Errorf("denied = %d, want 1", n)
	}
	if n := ph.stats.tunnelFailures.Load(); n != 0 {
		t.Errorf("tunnelFailures = %d, want 0", n)
	}
}

// A parent proxy on this machine is a common setup; the check applies to the
// origin, which the parent reaches, not to the parent itself.
func TestForbidLocalDestinationsAllowsLocalParentProxy(t *testing.T) {
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "from parent for "+r.URL.String())
	}))
	defer parent.Close()

	cfg := NewDefaultConfig()
	cfg.NoDiskCache = true
	cfg.ParentProxy = strings.TrimPrefix(parent.URL, "http://")
	ph := newTestHandler(cfg)

	rr := serveOnce(ph, http.MethodGet, "http://example.com/")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "from parent") {
		t.Errorf("status = %d, body = %q; want 200 from the parent", rr.Code, rr.Body.String())
	}
}

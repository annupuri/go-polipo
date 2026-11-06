package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

// The body of a 502/504 is read by the page that made the request as its
// own origin, so it must not carry what the error knows about the network.
func TestUpstreamErrorBodyRevealsNothing(t *testing.T) {
	ph := newTestHandler(newTestConfig())
	rr := serveOnce(ph, http.MethodGet, "http://"+deadLoopback+":9999/x")
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	body := rr.Body.String()
	for _, leak := range []string{"::1", "9999", "dial", "refused", "connect"} {
		if strings.Contains(body, leak) {
			t.Errorf("body %q contains %q", body, leak)
		}
	}
}

func TestUpstreamErrorReply(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp",
		Addr: &net.TCPAddr{IP: net.ParseIP("192.168.1.5"), Port: 8080},
		Err:  syscall.ECONNREFUSED}
	notFound := &net.OpError{Op: "dial", Net: "tcp",
		Err: &net.DNSError{Err: "no such host", Name: "typo.example", Server: "10.255.255.254:53", IsNotFound: true}}
	dnsTimeout := &net.OpError{Op: "dial", Net: "tcp",
		Err: &net.DNSError{Err: "i/o timeout", Name: "slow.example", Server: "10.255.255.254:53", IsTimeout: true}}

	tests := []struct {
		name     string
		err      error
		code     int
		contains string
	}{
		{"refused", refused, http.StatusBadGateway, "could not get a response"},
		{"wrapped refused", fmt.Errorf("round trip: %w", refused), http.StatusBadGateway, "could not get a response"},
		{"host not found", notFound, http.StatusBadGateway, "host name was not found"},
		{"DNS timeout", dnsTimeout, http.StatusGatewayTimeout, "did not respond in time"},
		{"serverTimeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "did not respond in time"},
		{"other", errors.New("unexpected EOF"), http.StatusBadGateway, "could not get a response"},
	}
	for _, tt := range tests {
		code, msg := upstreamErrorReply(tt.err)
		if code != tt.code || !strings.Contains(msg, tt.contains) {
			t.Errorf("%s: got %d %q, want %d containing %q", tt.name, code, msg, tt.code, tt.contains)
		}
		for _, leak := range []string{"192.168", "8080", "10.255", ":53", "typo.example", "slow.example"} {
			if strings.Contains(msg, leak) {
				t.Errorf("%s: body %q contains %q", tt.name, msg, leak)
			}
		}
	}
}

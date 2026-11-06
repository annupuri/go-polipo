package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// serveLocal sends a request addressed to go-polipo itself with the given
// Host and extra headers.
func serveLocal(ph *ProxyHandler, method, path, host string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	req.RemoteAddr = "127.0.0.1:12345"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	return rr
}

// A web page that points a hostname of its own at this machine (DNS
// rebinding) must not be able to read the local interface as same-origin.
func TestLocalInterfaceRefusesForeignHost(t *testing.T) {
	cfg := newTestConfig()
	cfg.ProxyAddress = "proxy.lan"
	ph := newTestHandler(cfg)

	hostname, _ := os.Hostname()
	tests := []struct {
		host string
		want int
	}{
		{"attacker.example:8123", http.StatusForbidden},
		{"attacker.example", http.StatusForbidden},
		{"localhost.attacker.example:8123", http.StatusForbidden},
		{"127.0.0.1:8123", http.StatusOK},
		{"192.168.1.10:8123", http.StatusOK},
		{"[::1]:8123", http.StatusOK},
		{"localhost:8123", http.StatusOK},
		{"LOCALHOST", http.StatusOK},
		{"localhost.:8123", http.StatusOK},
		{"proxy.lan:8123", http.StatusOK}, // proxyAddress
		{"", http.StatusOK},               // HTTP/1.0 without Host
	}
	if hostname != "" {
		tests = append(tests, struct {
			host string
			want int
		}{strings.ToUpper(hostname) + ":8123", http.StatusOK})
	}
	denied := uint64(0)
	for _, tt := range tests {
		for _, path := range []string{"/polipo/config", "/polipo/status"} {
			rr := serveLocal(ph, http.MethodGet, path, tt.host, nil)
			if rr.Code != tt.want {
				t.Errorf("GET %s with Host %q: status = %d, want %d", path, tt.host, rr.Code, tt.want)
			}
			if tt.want == http.StatusForbidden {
				denied++
				if strings.Contains(rr.Body.String(), "<table") {
					t.Errorf("GET %s with Host %q: body has the page", path, tt.host)
				}
			}
		}
	}
	if got := ph.stats.denied.Load(); got != denied {
		t.Errorf("denied = %d, want %d", got, denied)
	}

	// Outside /polipo/ too: a foreign Host is refused before the 404.
	if rr := serveLocal(ph, http.MethodGet, "/favicon.ico", "attacker.example", nil); rr.Code != http.StatusForbidden {
		t.Errorf("/favicon.ico with a foreign Host: status = %d, want 403", rr.Code)
	}
}

// A page on another site must not be able to have the browser POST to the
// local interface (a plain form submission needs no script and no CORS).
func TestLocalInterfaceRefusesCrossSitePost(t *testing.T) {
	ph := newTestHandler(newTestConfig())
	const host = "127.0.0.1:8123"
	tests := []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"cross-site form", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site page on another port", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"older browser, foreign Origin", http.MethodPost, map[string]string{"Origin": "http://attacker.example"}, http.StatusForbidden},
		{"the menu's own button", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusSeeOther},
		{"older browser, own Origin", http.MethodPost, map[string]string{"Origin": "http://" + host}, http.StatusSeeOther},
		{"curl", http.MethodPost, nil, http.StatusSeeOther},
		{"cross-site GET is only a read", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		rr := serveLocal(ph, tt.method, "/polipo/reload", host, tt.headers)
		if rr.Code != tt.want {
			t.Errorf("%s: status = %d, want %d", tt.name, rr.Code, tt.want)
		}
	}
	if got := ph.stats.denied.Load(); got != 3 {
		t.Errorf("denied = %d, want 3", got)
	}
}

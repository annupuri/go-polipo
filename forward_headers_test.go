package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestForwardedRequestHeaders: what reaches the origin of a forwarded
// request. Headers go-polipo adds itself survive a client's Connection
// header, which only names hop-by-hop headers of the message it arrived in
// (RFC 9110 section 7.6.1); the client's own hop-by-hop headers and its
// forwarding headers are dropped; and the query is passed on exactly as
// received, even the parts Go cannot parse.
func TestForwardedRequestHeaders(t *testing.T) {
	var got http.Header
	var gotQuery string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotQuery = r.URL.RawQuery
		io.WriteString(w, "body")
	}))
	defer backend.Close()

	ph := newTestHandler(newTestConfig())
	const query = "a=1;b=2&c=%zz"
	req := httptest.NewRequest(http.MethodGet, backend.URL+"/resource?"+query, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Connection", "Via, X-Client-Hop")
	req.Header.Set("X-Client-Hop", "1")
	req.Header.Set("X-Forwarded-For", "192.0.2.1")
	req.Header.Set("X-Forwarded-Host", "example.org")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "for=192.0.2.1")
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	if want := "1.1 " + ph.viaName; got.Get("Via") != want {
		t.Errorf("Via at the origin = %q, want %q (a client's Connection must not remove it)", got.Get("Via"), want)
	}
	for _, h := range []string{"X-Client-Hop", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
		if v := got.Values(h); len(v) > 0 {
			t.Errorf("%s reached the origin: %q", h, v)
		}
	}
	if gotQuery != query {
		t.Errorf("query at the origin = %q, want %q", gotQuery, query)
	}
}

// TestConditionalHeadersSurviveConnection: the validator go-polipo adds to
// revalidate a stale entry reaches the origin even when the client's
// Connection names that header, so the origin can still answer 304.
func TestConditionalHeadersSurviveConnection(t *testing.T) {
	var gotINM []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = append(gotINM, r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "max-age=0")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		io.WriteString(w, "body")
	}))
	defer backend.Close()

	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	ph := newTestHandler(cfg)
	for i := 0; i < 2; i++ { // a miss, then a revalidation
		req := httptest.NewRequest(http.MethodGet, backend.URL+"/resource", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Connection", "If-None-Match")
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != "body" {
			t.Fatalf("request %d: got %d %q, want 200 \"body\"", i, rr.Code, rr.Body.String())
		}
	}
	if len(gotINM) != 2 || gotINM[1] != `"v1"` {
		t.Errorf("If-None-Match at the origin = %q, want the stored ETag on the second request", gotINM)
	}
}

// TestOptionsWithEmptyPath: an OPTIONS for an absolute URL with no path and
// no query asks about the origin server as a whole, so it reaches the
// origin as "OPTIONS *" (RFC 9112 section 3.2.4). A path, even "/", or a
// query keeps the request-target as it is.
func TestOptionsWithEmptyPath(t *testing.T) {
	var got []string
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.RequestURI)
	}))
	// Let "OPTIONS *" reach the handler; net/http answers it itself by default.
	origin.Config.DisableGeneralOptionsHandler = true
	origin.Start()
	defer origin.Close()
	ph := newTestHandler(newTestConfig())

	for _, tc := range []struct{ target, want string }{
		{origin.URL, "*"},
		{origin.URL + "/", "/"},
		{origin.URL + "?q", "/?q"},
	} {
		got = nil
		req := httptest.NewRequest(http.MethodOptions, tc.target, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("OPTIONS %s: status %d, want 200", tc.target, rr.Code)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("OPTIONS %s: request-target at the origin = %q, want %q", tc.target, got, tc.want)
		}
	}
}

// TestOptionsWithEmptyPathThroughParent: through an HTTP parent, the parent
// is the last proxy and must turn the request into "OPTIONS *" itself.
// go-polipo cannot send it a URL without a path (Go's Transport always adds
// "/"), and "*" would ask about the parent, so it sends "http://host/".
func TestOptionsWithEmptyPathThroughParent(t *testing.T) {
	var got string
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.RequestURI
	}))
	defer parent.Close()
	cfg := newTestConfig()
	cfg.ParentProxy = strings.TrimPrefix(parent.URL, "http://")
	ph := newTestHandler(cfg)

	req := httptest.NewRequest(http.MethodOptions, "http://example.com", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	ph.ServeHTTP(httptest.NewRecorder(), req)
	if got != "http://example.com/" {
		t.Errorf("request-target at the parent = %q, want %q", got, "http://example.com/")
	}
}

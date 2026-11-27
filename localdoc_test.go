package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPAC = `function FindProxyForURL(url, host) { return "PROXY 127.0.0.1:8123; DIRECT"; }`

// newDocRoot fills a localDocumentRoot for the tests, and a file outside it.
func newDocRoot(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "www")
	outside = filepath.Join(base, "secret.txt")
	files := map[string]string{
		"proxy.pac":            testPAC,
		"index.html":           "<p>top</p>",
		"doc/index.html":       "<p>doc</p>",
		"empty/.keep":          "",
		"script.js":            "var x;",
		"data.bin":             "\x00\x01",
		"polipo/config":        "not the local interface",
		"Upper.PAC":            testPAC,
		"dir with space/a.txt": "a",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, outside
}

func TestLocalDocumentRootSetting(t *testing.T) {
	c := NewDefaultConfig()
	if c.LocalDocumentRoot != "" {
		t.Errorf("default = %q, want empty (nothing served)", c.LocalDocumentRoot)
	}
	if err := c.Set(keyLocalDocumentRoot, "/srv/polipo/www"); err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Entries() {
		if e.Name == keyLocalDocumentRoot && e.Value != "/srv/polipo/www" {
			t.Errorf("-v shows %q", e.Value)
		}
	}
}

func TestLocalDocumentServed(t *testing.T) {
	root, _ := newDocRoot(t)
	cfg := newTestConfig()
	cfg.LocalDocumentRoot = root
	ph := newTestHandler(cfg)

	for _, tc := range []struct {
		method, path string
		wantCode     int
		wantType     string
		wantBody     string
	}{
		{http.MethodGet, "/proxy.pac", 200, "application/x-ns-proxy-autoconfig", testPAC},
		{http.MethodHead, "/proxy.pac", 200, "application/x-ns-proxy-autoconfig", ""},
		{http.MethodGet, "/Upper.PAC", 200, "application/x-ns-proxy-autoconfig", testPAC},
		{http.MethodGet, "/", 200, "text/html; charset=utf-8", "<p>top</p>"},
		{http.MethodGet, "/doc/", 200, "text/html; charset=utf-8", "<p>doc</p>"},
		{http.MethodGet, "/doc/../proxy.pac", 200, "application/x-ns-proxy-autoconfig", testPAC},
		{http.MethodGet, "/script.js", 200, "text/javascript; charset=utf-8", "var x;"},
		{http.MethodGet, "/data.bin", 200, "application/octet-stream", "\x00\x01"},
		{http.MethodGet, "/dir%20with%20space/a.txt", 200, "text/plain; charset=utf-8", "a"},
		{http.MethodGet, "/missing.pac", 404, "", ""},
		{http.MethodGet, "/empty/", 404, "", ""},                 // no index.html, and no listing
		{http.MethodGet, "/polipo/config", 200, "", "go-polipo"}, // the local interface, not the file
		{http.MethodPost, "/proxy.pac", 405, "", ""},
		{http.MethodPut, "/index.html", 405, "", ""},
	} {
		rr := serveOnce(ph, tc.method, tc.path)
		if rr.Code != tc.wantCode {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, rr.Code, tc.wantCode)
			continue
		}
		if tc.wantType != "" && rr.Header().Get("Content-Type") != tc.wantType {
			t.Errorf("%s %s: Content-Type %q, want %q", tc.method, tc.path, rr.Header().Get("Content-Type"), tc.wantType)
		}
		if tc.wantBody != "" && !strings.Contains(rr.Body.String(), tc.wantBody) {
			t.Errorf("%s %s: body %q, want %q", tc.method, tc.path, rr.Body.String(), tc.wantBody)
		}
		if tc.wantCode == 405 && rr.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s %s: Allow %q", tc.method, tc.path, rr.Header().Get("Allow"))
		}
	}

	// A directory named without its slash is redirected to it, so that
	// relative links in its index.html resolve inside it.
	for path, want := range map[string]string{"/doc": "/doc/", "//doc": "/doc/", "/dir%20with%20space": "/dir%20with%20space/"} {
		rr := serveOnce(ph, http.MethodGet, path)
		if rr.Code != http.StatusMovedPermanently || rr.Header().Get("Location") != want {
			t.Errorf("GET %s: %d, Location %q; want 301 to %s", path, rr.Code, rr.Header().Get("Location"), want)
		}
	}
}

// TestLocalDocumentStaysInRoot: neither ".." nor a symbolic link leads
// outside localDocumentRoot.
func TestLocalDocumentStaysInRoot(t *testing.T) {
	root, outside := newDocRoot(t)
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "up")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("proxy.pac", filepath.Join(root, "alias.pac")); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig()
	cfg.LocalDocumentRoot = root
	ph := newTestHandler(cfg)

	for _, p := range []string{"/escape.txt", "/up/secret.txt", "/../secret.txt", "/%2e%2e/secret.txt"} {
		req := newLocalRequest(http.MethodGet, "/")
		req.URL.Path = strings.ReplaceAll(p, "%2e", ".")
		req.RemoteAddr = "127.0.0.1:12345"
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "secret") {
			t.Errorf("GET %s: %d %q; want it kept outside", p, rr.Code, rr.Body.String())
		}
	}
	if rr := serveOnce(ph, http.MethodGet, "/alias.pac"); rr.Code != http.StatusOK || rr.Body.String() != testPAC {
		t.Errorf("a symbolic link inside the root: %d %q, want the file", rr.Code, rr.Body.String())
	}
}

// TestLocalDocumentAccess: the Host check and allowedClients apply to every
// file; authCredentials to every file but a .pac file's GET and HEAD.
// disableLocalInterface turns off /polipo/ only.
func TestLocalDocumentAccess(t *testing.T) {
	root, _ := newDocRoot(t)
	cfg := newTestConfig()
	cfg.LocalDocumentRoot = root
	cfg.AuthCredentials = "alice:secret"
	ph := newTestHandler(cfg)

	for _, tc := range []struct {
		method, path string
		auth         bool
		want         int
	}{
		{http.MethodGet, "/proxy.pac", false, 200},
		{http.MethodHead, "/proxy.pac", false, 200},
		{http.MethodGet, "/Upper.PAC", false, 200},
		{http.MethodPost, "/proxy.pac", false, 401},
		{http.MethodGet, "/index.html", false, 401},
		{http.MethodGet, "/index.html", true, 200},
		{http.MethodGet, "/missing.pac", false, 404},
		{http.MethodGet, "/polipo/x.pac", false, 401},
	} {
		req := newLocalRequest(tc.method, tc.path)
		req.RemoteAddr = "127.0.0.1:12345"
		if tc.auth {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("alice:secret")))
		}
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, req)
		if rr.Code != tc.want {
			t.Errorf("%s %s (auth %v): %d, want %d", tc.method, tc.path, tc.auth, rr.Code, tc.want)
		}
	}

	// A Host that does not name this machine, and a client not allowed.
	req := newLocalRequest(http.MethodGet, "/proxy.pac")
	req.Host = "rebind.example.com"
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("foreign Host: %d, want 403", rr.Code)
	}
	cfgNotAllowed := newTestConfig()
	cfgNotAllowed.LocalDocumentRoot = root
	cfgNotAllowed.AllowedClients = []string{"10.0.0.1"}
	if rr := serveOnce(newTestHandler(cfgNotAllowed), http.MethodGet, "/proxy.pac"); rr.Code != http.StatusForbidden {
		t.Errorf("client not allowed: %d, want 403", rr.Code)
	}

	// disableLocalInterface: files are still served; /polipo/ is not, even
	// when the root has a file there.
	cfg2 := newTestConfig()
	cfg2.LocalDocumentRoot = root
	cfg2.DisableLocalInterface = true
	ph2 := newTestHandler(cfg2)
	if rr := serveOnce(ph2, http.MethodGet, "/proxy.pac"); rr.Code != http.StatusOK {
		t.Errorf("disableLocalInterface: /proxy.pac %d, want 200", rr.Code)
	}
	if rr := serveOnce(ph2, http.MethodGet, "/polipo/config"); rr.Code != http.StatusNotFound {
		t.Errorf("disableLocalInterface: /polipo/config %d, want 404", rr.Code)
	}

	// Without localDocumentRoot, as before: 404.
	if rr := serveOnce(newTestHandler(newTestConfig()), http.MethodGet, "/proxy.pac"); rr.Code != http.StatusNotFound {
		t.Errorf("no localDocumentRoot: %d, want 404", rr.Code)
	}
}

// TestLocalDocumentConditional: a file is served with Last-Modified, and
// answers If-Modified-Since and Range.
func TestLocalDocumentConditional(t *testing.T) {
	root, _ := newDocRoot(t)
	cfg := newTestConfig()
	cfg.LocalDocumentRoot = root
	ph := newTestHandler(cfg)

	rr := serveOnce(ph, http.MethodGet, "/proxy.pac")
	lm := rr.Header().Get("Last-Modified")
	if lm == "" {
		t.Fatal("no Last-Modified")
	}
	req := newLocalRequest(http.MethodGet, "/proxy.pac")
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	rr = httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified {
		t.Errorf("If-Modified-Since: %d, want 304", rr.Code)
	}
	req = newLocalRequest(http.MethodGet, "/proxy.pac")
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Range", "bytes=0-7")
	rr = httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "function" {
		t.Errorf("Range: %d %q, want 206 \"function\"", rr.Code, rr.Body.String())
	}
}

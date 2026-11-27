package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestForbiddenRedirectSettings(t *testing.T) {
	c := NewDefaultConfig()
	if c.ForbiddenURL != "" || c.ForbiddenRedirectCode != http.StatusFound {
		t.Errorf("defaults = %q, %d; want \"\" and 302, as in Polipo", c.ForbiddenURL, c.ForbiddenRedirectCode)
	}

	for _, val := range []string{"http://example.com/blocked.html", "https://example.com:8443/", ""} {
		if err := c.Set(keyForbiddenURL, val); err != nil {
			t.Errorf("Set(forbiddenUrl, %q): %v", val, err)
		}
	}
	for _, val := range []string{"example.com/blocked.html", "/blocked.html", "ftp://example.com/", "http://", "http://exa mple.com/"} {
		if err := c.Set(keyForbiddenURL, val); err == nil {
			t.Errorf("Set(forbiddenUrl, %q) accepted", val)
		}
	}

	for _, val := range []string{"301", "302", "303", "307", " 308 "} {
		if err := c.Set(keyForbiddenRedirectCode, val); err != nil {
			t.Errorf("Set(forbiddenRedirectCode, %q): %v", val, err)
		}
	}
	if c.ForbiddenRedirectCode != http.StatusPermanentRedirect {
		t.Errorf("ForbiddenRedirectCode = %d, want 308", c.ForbiddenRedirectCode)
	}
	for _, val := range []string{"200", "304", "403", "404", "300", "", "302x"} {
		if err := c.Set(keyForbiddenRedirectCode, val); err == nil {
			t.Errorf("Set(forbiddenRedirectCode, %q) accepted", val)
		}
	}

	c.ForbiddenURL = "http://example.com/blocked.html"
	shown := map[configKey]string{}
	for _, e := range c.Entries() {
		shown[e.Name] = e.Value
	}
	if shown[keyForbiddenURL] != c.ForbiddenURL || shown[keyForbiddenRedirectCode] != "308" {
		t.Errorf("-v shows forbiddenUrl %q, forbiddenRedirectCode %q", shown[keyForbiddenURL], shown[keyForbiddenRedirectCode])
	}
}

// TestForbiddenRedirect: a request blocked by forbiddenFile is redirected
// to forbiddenUrl, with forbiddenRedirectCode; without forbiddenUrl, for a
// CONNECT, and when forbiddenUrl is itself blocked, it gets a 403.
func TestForbiddenRedirect(t *testing.T) {
	const sorry = "http://sorry.example.org/blocked.html"
	for _, tc := range []struct {
		name         string
		forbiddenURL string
		code         int
		domains      []string
		regexps      []string
		method       string
		target       string
		wantCode     int
	}{
		{"GET redirected", sorry, 302, []string{"blocked.example.com"}, nil, http.MethodGet, "http://blocked.example.com/page", 302},
		{"subdomain redirected", sorry, 302, []string{"blocked.example.com"}, nil, http.MethodGet, "http://www.blocked.example.com/", 302},
		{"regexp match redirected", sorry, 302, nil, []string{`/ads/`}, http.MethodGet, "http://example.com/ads/x.gif", 302},
		{"HEAD redirected", sorry, 302, []string{"blocked.example.com"}, nil, http.MethodHead, "http://blocked.example.com/", 302},
		{"POST redirected", sorry, 302, []string{"blocked.example.com"}, nil, http.MethodPost, "http://blocked.example.com/form", 302},
		{"code 301", sorry, 301, []string{"blocked.example.com"}, nil, http.MethodGet, "http://blocked.example.com/", 301},
		{"code 307", sorry, 307, []string{"blocked.example.com"}, nil, http.MethodGet, "http://blocked.example.com/", 307},
		{"no forbiddenUrl", "", 302, []string{"blocked.example.com"}, nil, http.MethodGet, "http://blocked.example.com/", 403},
		{"CONNECT stays 403", sorry, 302, []string{"blocked.example.com"}, nil, http.MethodConnect, "blocked.example.com:443", 403},
		{"forbiddenUrl on the list by domain", "http://SORRY.example.org/blocked.html", 302, []string{"blocked.example.com", "sorry.example.org"}, nil, http.MethodGet, "http://blocked.example.com/", 403},
		{"forbiddenUrl on the list by regexp", sorry, 302, nil, []string{`example\.(com|org)`}, http.MethodGet, "http://blocked.example.com/", 403},
		{"forbiddenUrl requested", sorry, 302, []string{"sorry.example.org"}, nil, http.MethodGet, sorry, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newTestConfig()
			cfg.ForbiddenURL, cfg.ForbiddenRedirectCode = tc.forbiddenURL, tc.code
			forbidden := NewForbiddenList()
			forbidden.Domains = tc.domains
			for _, re := range tc.regexps {
				forbidden.Regexps = append(forbidden.Regexps, regexp.MustCompile(re))
			}
			ph := NewProxyHandler(cfg, NewCacheManager(cfg), forbidden, NewForbiddenList(), NewForbiddenList())

			rr := serveOnce(ph, tc.method, tc.target)
			if rr.Code != tc.wantCode {
				t.Fatalf("status %d, want %d", rr.Code, tc.wantCode)
			}
			if got := ph.stats.denied.Load(); got != 1 {
				t.Errorf("denied count %d, want 1", got)
			}
			loc := rr.Header().Get("Location")
			if tc.wantCode == http.StatusForbidden {
				if loc != "" {
					t.Errorf("403 with Location %q", loc)
				}
				return
			}
			if loc != tc.forbiddenURL {
				t.Errorf("Location %q, want %q", loc, tc.forbiddenURL)
			}
			if tc.method == http.MethodGet && !strings.Contains(rr.Body.String(), `href="`+tc.forbiddenURL+`"`) {
				t.Errorf("body lacks a link to forbiddenUrl: %q", rr.Body.String())
			}
		})
	}
}

// TestForbiddenURLBlockedWarning: loading a forbiddenFile that blocks
// forbiddenUrl itself logs a warning, at startup and on a reload.
func TestForbiddenURLBlockedWarning(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "forbidden")
	if err := os.WriteFile(list, []byte("blocked.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig()
	cfg.NoDiskCache = true // no disk accounting walk outliving the test
	cfg.ForbiddenFile = list
	cfg.ForbiddenURL = "http://sorry.example.org/blocked.html"

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard) // TestMain's resting state; see main_test.go.
	const warning = "forbiddenUrl http://sorry.example.org/blocked.html is itself on forbiddenFile"

	ph, _ := newProxy(cfg)
	if strings.Contains(buf.String(), warning) {
		t.Errorf("warned while forbiddenUrl is not on the list: %q", buf.String())
	}

	if err := os.WriteFile(list, []byte("blocked.example.com\nexample.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ph.Reload(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), warning) {
		t.Errorf("no warning on reload: %q", buf.String())
	}

	buf.Reset()
	newProxy(cfg)
	if !strings.Contains(buf.String(), warning) {
		t.Errorf("no warning at startup: %q", buf.String())
	}
}

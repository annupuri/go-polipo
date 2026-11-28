package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func postConfig(ph *ProxyHandler, form url.Values, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/polipo/config", strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:8123" // see newLocalRequest
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	ph.ServeHTTP(rr, req)
	return rr
}

func newEditableHandler() *ProxyHandler {
	cfg := newTestConfig()
	cfg.DisableConfiguration = false
	return newTestHandler(cfg)
}

func TestDisableConfigurationSetting(t *testing.T) {
	c := NewDefaultConfig()
	if !c.DisableConfiguration {
		t.Error("default = false, want true (editing off; Polipo's default was on)")
	}
	if err := c.Set(keyDisableConfiguration, "false"); err != nil || c.DisableConfiguration {
		t.Errorf("Set(false): %v, DisableConfiguration = %v", err, c.DisableConfiguration)
	}
	for _, k := range runtimeSettableKeys {
		found := false
		for _, e := range c.Entries() {
			found = found || e.Name == k
		}
		if !found {
			t.Errorf("runtimeSettableKeys has %q, which is not a variable", k)
		}
	}
}

func TestConfigPageForm(t *testing.T) {
	ph := newTestHandler(newTestConfig())
	body := serveOnce(ph, http.MethodGet, "/polipo/config").Body.String()
	if strings.Contains(body, "<form") || strings.Contains(body, "<input") || !strings.Contains(body, "<td>proxyOffline</td><td>false</td>") {
		t.Errorf("read-only page has a form, or lacks a value:\n%s", body)
	}

	body = serveOnce(newEditableHandler(), http.MethodGet, "/polipo/config").Body.String()
	for _, want := range []string{
		`<form method="post" action="/polipo/config">`,
		`<select name="proxyOffline"><option selected>false</option><option>true</option></select>`,
		`<select name="relaxTransparency"><option selected>false</option><option>maybe</option><option>true</option></select>`,
		`<input name="logLevel" value="0x7" size="40">`,
		`<input name="forbiddenRedirectCode" value="302" size="40">`,
		"<td>allowedClients</td><td>127.0.0.1, ::1</td>", // shown, not editable
		"not written to the configuration file",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("editable page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="allowedClients"`) || strings.Contains(body, `name="parentProxy"`) || strings.Contains(body, `name="forbiddenFile"`) {
		t.Errorf("a variable outside runtimeSettableKeys is editable:\n%s", body)
	}
}

func TestConfigChange(t *testing.T) {
	ph := newEditableHandler()
	old := ph.config()
	rr := postConfig(ph, url.Values{
		"proxyOffline":          {"true"},
		"relaxTransparency":     {"maybe"},
		"logLevel":              {"0xf"},
		"forbiddenUrl":          {"http://example.com/blocked.html"},
		"forbiddenRedirectCode": {"307"},
		"maxDiskCacheEntrySize": {"1000"},
	})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/polipo/config" {
		t.Fatalf("POST: %d %q, want 303 to /polipo/config", rr.Code, rr.Body.String())
	}
	c := ph.config()
	if !c.ProxyOffline || c.RelaxTransparency != TristateMaybe || c.LogLevel != 0xf ||
		c.ForbiddenURL != "http://example.com/blocked.html" || c.ForbiddenRedirectCode != 307 || c.MaxDiskCacheEntrySize != 1000 {
		t.Errorf("after POST: %+v", c)
	}
	if ph.cacheManager.config() != c {
		t.Error("the cache manager does not see the new configuration")
	}
	if old.ProxyOffline || old.LogLevel != 0x7 {
		t.Error("the old Config was changed in place; a copy should replace it")
	}
	if body := serveOnce(ph, http.MethodGet, "/polipo/config").Body.String(); !strings.Contains(body, `<option selected>true</option>`) {
		t.Errorf("page does not show the new value:\n%s", body)
	}
}

// TestConfigChangeRefused: a variable outside runtimeSettableKeys or an
// invalid value changes nothing, nor does a POST with editing off or from
// another site.
func TestConfigChangeRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		form url.Values
	}{
		{"not settable", url.Values{"proxyOffline": {"true"}, "allowedClients": {"all"}}},
		{"file path", url.Values{"forbiddenFile": {"/etc/passwd"}}},
		{"unknown", url.Values{"noSuchVariable": {"1"}}},
		{"bad code", url.Values{"proxyOffline": {"true"}, "forbiddenRedirectCode": {"200"}}},
		{"bad boolean", url.Values{"proxyOffline": {"yes"}}},
		{"bad tristate", url.Values{"relaxTransparency": {"sometimes"}}},
		{"bad URL", url.Values{"forbiddenUrl": {"example.com/x"}}},
		{"bad level", url.Values{"logLevel": {"loud"}}},
	} {
		ph := newEditableHandler()
		old := ph.config()
		if rr := postConfig(ph, tc.form); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", tc.name, rr.Code)
		}
		if ph.config() != old {
			t.Errorf("%s: the configuration changed", tc.name)
		}
	}

	ph := newTestHandler(newTestConfig()) // disableConfiguration = true
	old := ph.config()
	if rr := postConfig(ph, url.Values{"proxyOffline": {"true"}}); rr.Code != http.StatusForbidden || ph.config() != old {
		t.Errorf("editing off: %d, changed %v; want 403 and no change", rr.Code, ph.config() != old)
	}

	ph = newEditableHandler()
	old = ph.config()
	if rr := postConfig(ph, url.Values{"proxyOffline": {"true"}}, "Sec-Fetch-Site", "cross-site"); rr.Code != http.StatusForbidden || ph.config() != old {
		t.Errorf("cross-site POST: %d, changed %v; want 403 and no change", rr.Code, ph.config() != old)
	}
}

// TestConfigChangeTakesEffect: proxyOffline set from the page applies to
// the next request at once.
func TestConfigChangeTakesEffect(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()
	ph := newEditableHandler()

	if rr := serveOnce(ph, http.MethodGet, origin.URL+"/a"); rr.Code != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("online: %d, %d hits", rr.Code, hits.Load())
	}
	postConfig(ph, url.Values{"proxyOffline": {"true"}})
	if rr := serveOnce(ph, http.MethodGet, origin.URL+"/b"); rr.Code == http.StatusOK || hits.Load() != 1 {
		t.Errorf("offline: %d, %d hits; want no request to the origin", rr.Code, hits.Load())
	}
}

// TestConfigChangeConcurrent: changes made while requests are served are
// free of data races (run with -race).
func TestConfigChangeConcurrent(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()
	cfg := newTestConfig()
	cfg.DisableConfiguration = false
	cfg.DiskCacheRoot = t.TempDir()
	ph := newTestHandler(cfg)

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 20 {
				serveOnce(ph, http.MethodGet, origin.URL+"/"+string(rune('a'+i))+string(rune('a'+j)))
			}
		}()
		go func() {
			defer wg.Done()
			for j := range 20 {
				postConfig(ph, url.Values{"dontCacheCookies": {[]string{"true", "false"}[j%2]}, "logLevel": {"0x7"}})
			}
		}()
	}
	wg.Wait()
}

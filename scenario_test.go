package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Scenario tests drive go-polipo the way a user does: write a config file,
// start the proxy on a real port, and send a series of requests through it
// with an ordinary HTTP client, checking after each step what the client got
// and what reached the origin. Each step is a subtest named after what it
// shows; the steps of a scenario depend on each other, so the first one that
// fails stops the rest.

// scenarioStep is one step of a scenario.
type scenarioStep struct {
	name string
	run  func(t *testing.T)
}

// runScenario runs steps in order and stops at the first failure.
func runScenario(t *testing.T, steps []scenarioStep) {
	t.Helper()
	for i, s := range steps {
		if !t.Run(s.name, s.run) {
			if rest := len(steps) - i - 1; rest > 0 {
				t.Logf("%d later step(s) skipped", rest)
			}
			return
		}
	}
}

// scenarioProxy is a running go-polipo built from a config file, as serve
// builds it.
type scenarioProxy struct {
	addr       string // host:port it listens on
	configPath string // the config file it was started from
	cacheDir   string // its diskCacheRoot
	client     *http.Client
}

// startScenarioProxy writes a config file and starts go-polipo from it on a
// free loopback port; it is closed when the test ends. The file sets
// diskCacheRoot to a fresh directory and allows loopback destinations,
// because the test origins listen on loopback, which go-polipo refuses by
// default. lines follow and may override either. The proxy's client sends
// every request through the proxy and trusts tlsOrigin's certificate, if
// one is given.
func startScenarioProxy(t *testing.T, tlsOrigin *httptest.Server, lines ...string) *scenarioProxy {
	t.Helper()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	all := append([]string{
		"proxyAddress = 127.0.0.1",
		"diskCacheRoot = " + cacheDir,
		"forbidLocalDestinations = false",
	}, lines...)
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(strings.Join(all, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := NewDefaultConfig()
	if err := config.LoadConfig(configPath); err != nil {
		t.Fatalf("config file: %v", err)
	}
	handler, _ := newProxy(config)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newServer(config, ln.Addr().String(), handler)
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })

	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: ln.Addr().String()})}
	if tlsOrigin != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: tlsOrigin.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &scenarioProxy{
		addr:       ln.Addr().String(),
		configPath: configPath,
		cacheDir:   config.DiskCacheRoot,
		client:     &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}
}

// get sends a GET for target through the proxy with the given header lines
// ("Name: value") and returns the response with its body read.
func (p *scenarioProxy) get(t *testing.T, target string, header ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range header {
		name, value, _ := strings.Cut(h, ":")
		req.Header.Add(name, strings.TrimSpace(value))
	}
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: reading the body: %v", target, err)
	}
	return resp, string(body)
}

// connect sends a CONNECT for hostPort by hand, with the given header lines
// ("Name: value"), and returns the proxy's status code: the HTTP client
// would only report a refusal as an error.
func (p *scenarioProxy) connect(t *testing.T, hostPort string, header ...string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", p.addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", hostPort, hostPort)
	for _, h := range header {
		fmt.Fprintf(conn, "%s\r\n", h)
	}
	io.WriteString(conn, "\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT %s: %v", hostPort, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// local sends a request to go-polipo's own pages at path, as a browser that
// opened http://<proxy>/polipo/ does: directly, not through the proxy.
func (p *scenarioProxy) local(t *testing.T, method, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+p.addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// cacheFiles counts the entries in the proxy's disk cache.
func (p *scenarioProxy) cacheFiles(t *testing.T) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(p.cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// scenarioOrigin is an origin server that counts the requests reaching it.
// Every page has the body "<path> v1" and the ETag "v1", and answers a
// matching If-None-Match with 304. cacheControl maps a path to its
// Cache-Control (default max-age=3600).
type scenarioOrigin struct {
	*httptest.Server
	mu           sync.Mutex
	hits         map[string]int
	lastINM      map[string]string
	cacheControl map[string]string
}

func newScenarioOrigin(t *testing.T, tls bool, cacheControl map[string]string) *scenarioOrigin {
	t.Helper()
	o := &scenarioOrigin{hits: map[string]int{}, lastINM: map[string]string{}, cacheControl: cacheControl}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.hits[r.URL.Path]++
		o.lastINM[r.URL.Path] = r.Header.Get("If-None-Match")
		cc, ok := o.cacheControl[r.URL.Path]
		o.mu.Unlock()
		if !ok {
			cc = "max-age=3600"
		}
		w.Header().Set("Cache-Control", cc)
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		io.WriteString(w, r.URL.Path+" v1")
	})
	if tls {
		o.Server = httptest.NewTLSServer(h)
	} else {
		o.Server = httptest.NewServer(h)
	}
	t.Cleanup(o.Close)
	return o
}

// hitsOf reports how many requests for path reached the origin, and the
// If-None-Match of the last one.
func (o *scenarioOrigin) hitsOf(path string) (int, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hits[path], o.lastINM[path]
}

// wantResponse checks a response's status and body.
func wantResponse(t *testing.T, resp *http.Response, body string, wantCode int, wantBody string) {
	t.Helper()
	if resp.StatusCode != wantCode || (wantBody != "" && body != wantBody) {
		t.Fatalf("got %d %q, want %d %q", resp.StatusCode, body, wantCode, wantBody)
	}
}

// wantHits checks how many requests for path reached the origin.
func wantHits(t *testing.T, o *scenarioOrigin, path string, want int) {
	t.Helper()
	if got, _ := o.hitsOf(path); got != want {
		t.Fatalf("origin received %d request(s) for %s, want %d", got, path, want)
	}
}

// TestScenarioCacheLifecycle follows one page through the cache: fetched,
// served from the cache, revalidated on a reload, served stale with a
// warning when the origin goes away, and kept by -x and /polipo/flush while
// it is fresh.
func TestScenarioCacheLifecycle(t *testing.T) {
	origin := newScenarioOrigin(t, false, nil)
	p := startScenarioProxy(t, nil, "relaxTransparency = maybe")
	page := origin.URL + "/page"

	runScenario(t, []scenarioStep{
		{"a first visit is fetched from the origin", func(t *testing.T) {
			resp, body := p.get(t, page)
			wantResponse(t, resp, body, http.StatusOK, "/page v1")
			wantHits(t, origin, "/page", 1)
		}},
		{"the entry is written to the disk cache", func(t *testing.T) {
			if n := p.cacheFiles(t); n != 1 {
				t.Fatalf("%d file(s) in the disk cache, want 1", n)
			}
		}},
		{"a second visit is served from the cache", func(t *testing.T) {
			resp, body := p.get(t, page)
			wantResponse(t, resp, body, http.StatusOK, "/page v1")
			wantHits(t, origin, "/page", 1)
		}},
		{"a reload is revalidated with the stored ETag", func(t *testing.T) {
			resp, body := p.get(t, page, "Cache-Control: max-age=0")
			wantResponse(t, resp, body, http.StatusOK, "/page v1")
			wantHits(t, origin, "/page", 2)
			if _, inm := origin.hitsOf("/page"); inm != `"v1"` {
				t.Fatalf("If-None-Match at the origin = %q, want %q", inm, `"v1"`)
			}
		}},
		{"with the origin gone, a reload gets the cached copy with Warning 111", func(t *testing.T) {
			origin.Close()
			resp, body := p.get(t, page, "Cache-Control: max-age=0")
			wantResponse(t, resp, body, http.StatusOK, "/page v1")
			if w := resp.Header.Get("Warning"); !strings.HasPrefix(w, "111 ") {
				t.Fatalf("Warning = %q, want 111", w)
			}
		}},
		{"-x keeps the fresh entry", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-x", "-c", p.configPath}, &stdout, &stderr); code != 0 {
				t.Fatalf("polipo -x exited with %d: %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "scanned 1, removed 0") {
				t.Fatalf("polipo -x said %q, want scanned 1, removed 0", stdout.String())
			}
			if n := p.cacheFiles(t); n != 1 {
				t.Fatalf("%d file(s) in the disk cache, want 1", n)
			}
		}},
		{"/polipo/flush keeps the fresh entry too", func(t *testing.T) {
			code, body := p.local(t, http.MethodPost, "/polipo/flush")
			if code != http.StatusOK || !strings.Contains(body, "<li>Removed: 0") {
				t.Fatalf("got %d %q, want 200 and nothing removed", code, body)
			}
			if n := p.cacheFiles(t); n != 1 {
				t.Fatalf("%d file(s) in the disk cache, want 1", n)
			}
		}},
	})
}

// TestScenarioOffline: pages fetched while online are still served after
// go-polipo is restarted with proxyOffline on the same disk cache and the
// origin has gone away.
func TestScenarioOffline(t *testing.T) {
	origin := newScenarioOrigin(t, false, map[string]string{"/stale": "max-age=0"})
	online := startScenarioProxy(t, nil)
	var offline *scenarioProxy
	parent := t // the restarted proxy must outlive the step that starts it

	runScenario(t, []scenarioStep{
		{"pages are fetched while online", func(t *testing.T) {
			for _, path := range []string{"/fresh", "/stale"} {
				resp, body := online.get(t, origin.URL+path)
				wantResponse(t, resp, body, http.StatusOK, path+" v1")
			}
		}},
		{"go-polipo restarts offline on the same cache", func(t *testing.T) {
			origin.Close()
			offline = startScenarioProxy(parent, nil, "proxyOffline = true", "diskCacheRoot = "+online.cacheDir)
		}},
		{"a fresh page is served as it is", func(t *testing.T) {
			resp, body := offline.get(t, origin.URL+"/fresh")
			wantResponse(t, resp, body, http.StatusOK, "/fresh v1")
			if w := resp.Header.Get("Warning"); w != "" {
				t.Fatalf("Warning = %q, want none", w)
			}
		}},
		{"a stale page is served with Warning 112", func(t *testing.T) {
			resp, body := offline.get(t, origin.URL+"/stale")
			wantResponse(t, resp, body, http.StatusOK, "/stale v1")
			if w := resp.Header.Get("Warning"); !strings.HasPrefix(w, "112 ") {
				t.Fatalf("Warning = %q, want 112", w)
			}
		}},
		{"a page never fetched is a 502", func(t *testing.T) {
			resp, body := offline.get(t, origin.URL+"/never")
			wantResponse(t, resp, body, http.StatusBadGateway, "")
		}},
	})
}

// TestScenarioHTTPS: an HTTPS page goes through a CONNECT tunnel and is never
// cached.
func TestScenarioHTTPS(t *testing.T) {
	origin := newScenarioOrigin(t, true, nil)
	// The test origin's port is not one of Polipo's default tunnel ports.
	p := startScenarioProxy(t, origin.Server, "tunnelAllowedPorts = 1-65535")
	page := origin.URL + "/secure"

	runScenario(t, []scenarioStep{
		{"an HTTPS page is fetched through a tunnel", func(t *testing.T) {
			resp, body := p.get(t, page)
			wantResponse(t, resp, body, http.StatusOK, "/secure v1")
			wantHits(t, origin, "/secure", 1)
		}},
		{"a second visit reaches the origin again", func(t *testing.T) {
			resp, body := p.get(t, page)
			wantResponse(t, resp, body, http.StatusOK, "/secure v1")
			wantHits(t, origin, "/secure", 2)
		}},
		{"nothing is written to the disk cache", func(t *testing.T) {
			if n := p.cacheFiles(t); n != 0 {
				t.Fatalf("%d file(s) in the disk cache, want 0", n)
			}
		}},
	})
}

// TestScenarioRestrictedProxy: a proxy with a password, a forbidden file
// and the default tunnel ports refuses what it should and nothing more.
func TestScenarioRestrictedProxy(t *testing.T) {
	origin := newScenarioOrigin(t, false, nil)
	forbiddenPath := filepath.Join(t.TempDir(), "forbidden")
	if err := os.WriteFile(forbiddenPath, []byte("# block one directory\n/blocked/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := startScenarioProxy(t, nil, "authCredentials = user:secret", "forbiddenFile = "+forbiddenPath)
	const auth = "Proxy-Authorization: Basic dXNlcjpzZWNyZXQ=" // user:secret

	runScenario(t, []scenarioStep{
		{"without the password, the proxy asks for it", func(t *testing.T) {
			resp, body := p.get(t, origin.URL+"/open")
			wantResponse(t, resp, body, http.StatusProxyAuthRequired, "")
			if resp.Header.Get("Proxy-Authenticate") == "" {
				t.Fatal("no Proxy-Authenticate in the 407")
			}
			wantHits(t, origin, "/open", 0)
		}},
		{"with the password, a page is fetched", func(t *testing.T) {
			resp, body := p.get(t, origin.URL+"/open", auth)
			wantResponse(t, resp, body, http.StatusOK, "/open v1")
			wantHits(t, origin, "/open", 1)
		}},
		{"a URL in the forbidden file is refused", func(t *testing.T) {
			resp, body := p.get(t, origin.URL+"/blocked/page", auth)
			wantResponse(t, resp, body, http.StatusForbidden, "")
			wantHits(t, origin, "/blocked/page", 0)
		}},
		{"a tunnel to a port outside tunnelAllowedPorts is refused", func(t *testing.T) {
			if code := p.connect(t, origin.Listener.Addr().String()); code != http.StatusProxyAuthRequired {
				t.Fatalf("CONNECT without the password: %d, want 407", code)
			}
			if code := p.connect(t, origin.Listener.Addr().String(), auth); code != http.StatusForbidden {
				t.Fatalf("CONNECT: %d, want 403", code)
			}
		}},
	})
}

// TestScenarioLocalPages: go-polipo's own pages answer a browser that opens
// them directly, alongside ordinary proxying.
func TestScenarioLocalPages(t *testing.T) {
	origin := newScenarioOrigin(t, false, nil)
	p := startScenarioProxy(t, nil, "relaxTransparency = maybe")

	runScenario(t, []scenarioStep{
		{"the top page is served", func(t *testing.T) {
			if code, body := p.local(t, http.MethodGet, "/polipo/"); code != http.StatusOK || !strings.Contains(body, "go-polipo") {
				t.Fatalf("got %d %q, want 200 and a page about go-polipo", code, body)
			}
		}},
		{"the config page shows the config file's settings", func(t *testing.T) {
			code, body := p.local(t, http.MethodGet, "/polipo/config")
			if code != http.StatusOK || !strings.Contains(body, "relaxTransparency") || !strings.Contains(body, "maybe") {
				t.Fatalf("got %d %q, want 200 and relaxTransparency = maybe", code, body)
			}
		}},
		{"a page fetched twice through the proxy", func(t *testing.T) {
			for range 2 {
				resp, body := p.get(t, origin.URL+"/page")
				wantResponse(t, resp, body, http.StatusOK, "/page v1")
			}
			wantHits(t, origin, "/page", 1)
		}},
		{"flush answers only POST", func(t *testing.T) {
			if code, _ := p.local(t, http.MethodGet, "/polipo/flush"); code != http.StatusMethodNotAllowed {
				t.Fatalf("GET /polipo/flush: %d, want 405", code)
			}
		}},
	})
}

// TestOptionsAsteriskToProxy: "OPTIONS * HTTP/1.1" sent to go-polipo itself
// asks about the server in general, like a ping (RFC 9110 section 9.3.7).
// net/http answers it with an empty 200 before go-polipo's handler runs, so
// it needs no credentials and is not checked against allowedClients. That
// is kept on purpose: the answer tells nothing beyond "an HTTP server is
// listening here", which connecting already tells.
func TestOptionsAsteriskToProxy(t *testing.T) {
	p := startScenarioProxy(t, nil, "authCredentials = user:secret")
	conn, err := net.DialTimeout("tcp", p.addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "OPTIONS * HTTP/1.1\r\nHost: %s\r\n\r\n", p.addr)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("got %d %q, want an empty 200", resp.StatusCode, body)
	}
	if v := resp.Header.Get("Proxy-Authenticate"); v != "" {
		t.Errorf("Proxy-Authenticate = %q, want none", v)
	}
}

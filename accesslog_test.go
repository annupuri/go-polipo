package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

// syncBuffer is a bytes.Buffer safe to write from the proxy's goroutines
// while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// withAccessLog turns on ph's access log, writing to the returned buffer.
func withAccessLog(ph *ProxyHandler) *syncBuffer {
	buf := &syncBuffer{}
	ph.accessLog = newAccessLogger(buf, ph.config().LogLevel)
	return buf
}

// accessLines parses every line in buf as a JSON object.
func accessLines(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("access log line %q is not JSON: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}

// lastAccessLine is the one line buf holds since the last call, failing the
// test if there is not exactly one.
func lastAccessLine(t *testing.T, buf *syncBuffer, seen *int) map[string]any {
	t.Helper()
	lines := accessLines(t, buf)
	if len(lines) != *seen+1 {
		t.Fatalf("access log has %d new lines, want 1:\n%s", len(lines)-*seen, buf.String())
	}
	*seen++
	return lines[len(lines)-1]
}

func TestAccessLogCacheActions(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Cache-Control", "max-age=600")
		w.Write([]byte("from origin"))
	}))
	defer backend.Close()

	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	ph := NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList())
	buf := withAccessLog(ph)

	dead := "http://" + deadLoopback + ":9999"
	seedCacheEntry(t, cm, backend.URL+"/fresh", "cached", true)
	seedCacheEntry(t, cm, backend.URL+"/stale", "old", false)
	seedStaleEntryWithValidators(t, cm, backend.URL+"/reval", "old", `"v1"`, "")
	seedCacheEntry(t, cm, dead+"/stale", "old", false)

	seen := 0
	for _, tc := range []struct {
		name      string
		method    string
		target    string
		header    [2]string
		setup     func()
		wantCode  int
		wantCache string // "" means the field must be absent
	}{
		{name: "fresh hit", target: backend.URL + "/fresh", wantCode: 200, wantCache: "hit"},
		{name: "origin confirms a stale entry", target: backend.URL + "/reval", wantCode: 200, wantCache: "revalidated"},
		{name: "not cached", target: backend.URL + "/new", wantCode: 200, wantCache: "miss"},
		{
			// The origin's 304 to the client's validators is passed on.
			name: "client's own conditional request", target: backend.URL + "/cond",
			header: [2]string{"If-None-Match", `"v1"`}, wantCode: 304, wantCache: "miss",
		},
		{name: "POST", method: http.MethodPost, target: backend.URL + "/form", wantCode: 200, wantCache: "miss"},
		{
			name: "only-if-cached, not cached", target: backend.URL + "/absent",
			header: [2]string{"Cache-Control", "only-if-cached"}, wantCode: 504, wantCache: "miss",
		},
		{name: "unreachable origin, nothing cached", target: dead + "/none", wantCode: 502, wantCache: "miss"},
		{
			name: "relaxTransparency=maybe, unreachable origin", target: dead + "/stale",
			setup: func() { cfg.RelaxTransparency = TristateMaybe }, wantCode: 200, wantCache: "stale",
		},
		{
			name: "relaxTransparency=true", target: backend.URL + "/stale",
			setup: func() { cfg.RelaxTransparency = TristateTrue }, wantCode: 200, wantCache: "stale",
		},
		{
			name: "proxyOffline, stale entry", target: backend.URL + "/stale",
			setup: func() { cfg.RelaxTransparency = TristateFalse; cfg.ProxyOffline = true }, wantCode: 200, wantCache: "stale",
		},
		{name: "proxyOffline, fresh entry", target: backend.URL + "/fresh", wantCode: 200, wantCache: "hit"},
		{name: "proxyOffline, not cached", target: backend.URL + "/absent", wantCode: 502, wantCache: "miss"},
		{
			name: "refused at the dial (forbidLocalDestinations)", target: "http://127.0.0.2:8080/local",
			setup: func() { cfg.ProxyOffline = false; cfg.ForbidLocalDestinations = true }, wantCode: 403,
		},
		{name: "refused before the cache (allowedPorts)", target: "http://example.com:25/", wantCode: 403},
		{name: "local page", target: "/polipo/status", wantCode: 200},
	} {
		if tc.setup != nil {
			tc.setup()
		}
		method := tc.method
		if method == "" {
			method = http.MethodGet
		}
		req := httptest.NewRequest(method, tc.target, nil)
		if strings.HasPrefix(tc.target, "/") {
			req = newLocalRequest(method, tc.target)
		}
		req.RemoteAddr = "127.0.0.1:12345"
		if tc.header[0] != "" {
			req.Header.Set(tc.header[0], tc.header[1])
		}
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, req)
		if rr.Code != tc.wantCode {
			t.Errorf("%s: status = %d, want %d", tc.name, rr.Code, tc.wantCode)
		}

		line := lastAccessLine(t, buf, &seen)
		if got := line["status"]; got != float64(tc.wantCode) {
			t.Errorf("%s: logged status = %v, want %d", tc.name, got, tc.wantCode)
		}
		got, ok := line["cache"]
		switch {
		case tc.wantCache == "" && ok:
			t.Errorf("%s: cache = %v, want no cache field", tc.name, got)
		case tc.wantCache != "" && got != tc.wantCache:
			t.Errorf("%s: cache = %v, want %q", tc.name, got, tc.wantCache)
		}
	}
}

func TestAccessLogLineFields(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte("0123456789"))
	}))
	defer backend.Close()

	ph := newTestHandler(newTestConfig())
	buf := withAccessLog(ph)

	before := time.Now().Truncate(time.Millisecond)
	// The query must come through as sent, & and all, and a password in
	// the URL must not.
	target := strings.Replace(backend.URL, "http://", "http://user:secret@", 1) + "/p?a=1&b=<2>"
	rr := serveOnce(ph, http.MethodGet, target)
	after := time.Now()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	raw := buf.String()
	if strings.Contains(raw, "secret") {
		t.Errorf("access log shows the URL's password: %s", raw)
	}
	if !strings.Contains(raw, `a=1&b=<2>`) {
		t.Errorf("access log escaped the query: %s", raw)
	}
	seen := 0
	line := lastAccessLine(t, buf, &seen)

	start, err := time.Parse(accessTimeFormat, line["time"].(string))
	if err != nil {
		t.Fatalf("time %q: %v", line["time"], err)
	}
	if start.Before(before) || start.After(after) {
		t.Errorf("time = %v, want the request's start, between %v and %v", start, before, after)
	}
	if d := line["duration_ms"].(float64); d < 20 || time.Duration(d)*time.Millisecond > after.Sub(before) {
		t.Errorf("duration_ms = %v, want at least the origin's 20ms delay", d)
	}
	want := map[string]any{
		"client": "127.0.0.1:12345",
		"method": "GET",
		"url":    strings.Replace(target, "secret", "xxxxx", 1),
		"status": float64(200),
		"bytes":  float64(10),
		"cache":  "miss",
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s = %v, want %v", k, line[k], v)
		}
	}
	for _, k := range []string{"host", "bytes_sent", "bytes_received"} {
		if _, ok := line[k]; ok {
			t.Errorf("a forwarded request's line has %s", k)
		}
	}
}

// TestAccessLogOriginFormURL: a request addressed to go-polipo itself is
// logged with the Host it came to.
func TestAccessLogOriginFormURL(t *testing.T) {
	ph := newTestHandler(newTestConfig())
	buf := withAccessLog(ph)
	serveOnce(ph, http.MethodGet, "/polipo/status?x=1")
	seen := 0
	if got := lastAccessLine(t, buf, &seen)["url"]; got != "http://127.0.0.1:8123/polipo/status?x=1" {
		t.Errorf("url = %v", got)
	}
}

// TestAccessLogBodyCutShort: ReverseProxy ends a response whose body fails
// midway by panicking with http.ErrAbortHandler; the request is still
// logged, with what was sent.
func TestAccessLogBodyCutShort(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("0123456789"))
		w.(http.Flusher).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer backend.Close()

	ph := newTestHandler(newTestConfig())
	buf := withAccessLog(ph)
	proxySrv := httptest.NewServer(ph)
	defer proxySrv.Close()
	proxySrv.Config.ErrorLog = log.New(io.Discard, "", 0) // the abort is logged by net/http

	resp, err := proxiedClient(proxySrv.URL).Get(backend.URL + "/cut")
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	line := waitAccessLine(t, buf)
	if line["status"] != float64(200) || line["bytes"] != float64(10) {
		t.Errorf("line = %v, want status 200 and the 10 bytes sent", line)
	}
}

// TestAccessLogKeepsFlushing: with the access log on, a response is still
// passed on as it arrives (ReverseProxy flushes through the wrapper).
func TestAccessLogKeepsFlushing(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("first\n"))
		w.(http.Flusher).Flush()
		<-release
		w.Write([]byte("second\n"))
	}))
	defer backend.Close()
	defer close(release)

	ph := newTestHandler(newTestConfig())
	withAccessLog(ph)
	proxySrv := httptest.NewServer(ph)
	defer proxySrv.Close()

	resp, err := proxiedClient(proxySrv.URL).Get(backend.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != "first\n" {
			t.Errorf("first line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first part of the body was held back")
	}
}

// proxiedClient is a client that sends its requests through the proxy at
// proxyURL.
func proxiedClient(proxyURL string) *http.Client {
	u, _ := url.Parse(proxyURL)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

// waitAccessLine waits for the first line in buf: a request served by a
// real server is logged after its response has reached the client.
func waitAccessLine(t *testing.T, buf *syncBuffer) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for buf.String() == "" {
		if time.Now().After(deadline) {
			t.Fatal("no access log line")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return accessLines(t, buf)[0]
}

func TestAccessLogConnect(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		c, err := backend.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// Read "hello" (sent with the CONNECT) and "ping", answer with
		// 3 bytes, and close.
		buf := make([]byte, 9)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		c.Write([]byte("pong"[:3]))
	}()
	_, portStr, _ := net.SplitHostPort(backend.Addr().String())
	port, _ := strconv.Atoi(portStr)

	cfg := newTestConfig()
	cfg.TunnelAllowedPorts = PortSet{{port, port}}
	ph := newTestHandler(cfg)
	buf := withAccessLog(ph)
	proxySrv := httptest.NewServer(ph)
	defer proxySrv.Close()

	pc, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	target := backend.Addr().String()
	// "hello" goes right behind the headers, before the 200, as a TLS
	// ClientHello may.
	fmt.Fprintf(pc, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nhello", target, target)
	br := bufio.NewReader(pc)
	if status, _ := br.ReadString('\n'); !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status line = %q", status)
	}
	br.ReadString('\n') // the blank line ending the headers
	pc.Write([]byte("ping"))
	if reply, _ := io.ReadAll(br); string(reply) != "pon" {
		t.Errorf("tunnel reply = %q", reply)
	}
	pc.Close()

	line := waitAccessLine(t, buf)
	want := map[string]any{
		"method":         "CONNECT",
		"host":           target,
		"status":         float64(200),
		"bytes_sent":     float64(9),
		"bytes_received": float64(3),
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s = %v, want %v", k, line[k], v)
		}
	}
	for _, k := range []string{"url", "bytes", "cache"} {
		if _, ok := line[k]; ok {
			t.Errorf("a CONNECT line has %s", k)
		}
	}
}

func TestAccessLogConnectRefusedOrFailed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		setup  func(*Config)
		status int
	}{
		// Refused before the hijack, through the ResponseWriter.
		{name: "port not allowed", target: "example.com:25", status: 403},
		// Answered on the hijacked connection.
		{
			name: "forbidden destination", target: "127.0.0.1:443",
			setup: func(c *Config) { c.ForbidLocalDestinations = true }, status: 403,
		},
		{
			name: "unreachable", target: deadLoopback + ":443", status: 502,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newTestConfig()
			if tc.setup != nil {
				tc.setup(cfg)
			}
			ph := newTestHandler(cfg)
			buf := withAccessLog(ph)
			proxySrv := httptest.NewServer(ph)
			defer proxySrv.Close()

			pc, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			fmt.Fprintf(pc, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", tc.target, tc.target)
			status, _ := bufio.NewReader(pc).ReadString('\n')
			if !strings.Contains(status, strconv.Itoa(tc.status)) {
				t.Fatalf("status line = %q, want %d", status, tc.status)
			}

			line := waitAccessLine(t, buf)
			if line["status"] != float64(tc.status) || line["bytes_sent"] != float64(0) || line["bytes_received"] != float64(0) {
				t.Errorf("line = %v, want status %d and no bytes either way", line, tc.status)
			}
		})
	}
}

func TestAccessLogOffByDefault(t *testing.T) {
	ph := newTestHandler(newTestConfig())
	if ph.accessLog != nil {
		t.Fatal("a new handler has an access log")
	}
	// Without one, the request carries no record to fill in.
	if rec := accessRecordFrom(httptest.NewRequest(http.MethodGet, "http://example.com/", nil).Context()); rec != nil {
		t.Error("a request carries an access record")
	}
	setCacheAction(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), cacheHit) // must not panic
}

type failingWriter struct{ fail bool }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.fail {
		return 0, errors.New("disk full")
	}
	return len(p), nil
}

// TestAccessLogWriteFailureLoggedOnce: a log that cannot be written (a full
// disk) gives one error in the main log, not one per request, and a line
// saying so when writing works again.
func TestAccessLogWriteFailureLoggedOnce(t *testing.T) {
	var mainLog bytes.Buffer
	log.SetOutput(&mainLog)
	defer log.SetOutput(io.Discard) // TestMain's resting state; see main_test.go.

	w := &failingWriter{fail: true}
	l := newAccessLogger(w, LogError|LogInfo)
	e := &accessLogEntry{Method: "GET"}
	l.write(e)
	l.write(e)
	if n := strings.Count(mainLog.String(), "Couldn't write to the access log"); n != 1 {
		t.Errorf("logged the failure %d times, want 1:\n%s", n, mainLog.String())
	}
	w.fail = false
	l.write(e)
	l.write(e)
	if n := strings.Count(mainLog.String(), "Writing to the access log again"); n != 1 {
		t.Errorf("logged the recovery %d times, want 1:\n%s", n, mainLog.String())
	}
}

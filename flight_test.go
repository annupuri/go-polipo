package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanCollapse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		hdr    [2]string
		want   bool
	}{
		{name: "plain GET", method: "GET", want: true},
		{name: "conditional GET", method: "GET", hdr: [2]string{"If-None-Match", `"v1"`}, want: true},
		{name: "HEAD", method: "HEAD"},
		{name: "POST", method: "POST"},
		{name: "Authorization", method: "GET", hdr: [2]string{"Authorization", "Basic eDp5"}},
		{name: "Range", method: "GET", hdr: [2]string{"Range", "bytes=0-9"}},
		{name: "no-cache", method: "GET", hdr: [2]string{"Cache-Control", "no-cache"}},
		{name: "no-store", method: "GET", hdr: [2]string{"Cache-Control", "no-store"}},
		{name: "Pragma: no-cache", method: "GET", hdr: [2]string{"Pragma", "no-cache"}},
		{name: "max-age=0 still uses the cache", method: "GET", hdr: [2]string{"Cache-Control", "max-age=0"}, want: true},
	} {
		req := httptest.NewRequest(tc.method, "http://example.com/a", nil)
		if tc.hdr[0] != "" {
			req.Header.Set(tc.hdr[0], tc.hdr[1])
		}
		if got := canCollapse(req); got != tc.want {
			t.Errorf("%s: canCollapse = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// gatedOrigin is an origin whose first request stops - after sending its
// headers, before the body, when headersFirst is set, and before anything
// otherwise - until release is closed. Later requests are answered at
// once.
type gatedOrigin struct {
	srv     *httptest.Server
	hits    atomic.Int32
	arrived chan struct{} // closed when the first request is held
	release chan struct{}
}

func newGatedOrigin(t *testing.T, headersFirst bool, reply func(w http.ResponseWriter, r *http.Request)) *gatedOrigin {
	t.Helper()
	o := &gatedOrigin{arrived: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	hold := func() {
		once.Do(func() { close(o.arrived) })
		<-o.release
	}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first := o.hits.Add(1) == 1
		switch {
		case first && headersFirst:
			// Held at the first body write, after the headers went out, so
			// that the response is not complete yet.
			reply(&holdingWriter{ResponseWriter: w, hold: hold}, r)
			once.Do(func() { close(o.arrived) }) // a reply without a body
		case first:
			hold()
			reply(w, r)
		default:
			reply(w, r)
		}
	}))
	t.Cleanup(func() {
		select {
		case <-o.release:
		default:
			close(o.release)
		}
		o.srv.Close()
	})
	return o
}

// holdingWriter calls hold before the first body write.
type holdingWriter struct {
	http.ResponseWriter
	hold func()
	held bool
}

func (w *holdingWriter) Write(p []byte) (int, error) {
	if !w.held {
		w.held = true
		w.ResponseWriter.(http.Flusher).Flush()
		w.hold()
	}
	return w.ResponseWriter.Write(p)
}

func (w *holdingWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

// fixedReply answers with body and the given header fields, and a
// Content-Length unless it is told not to.
func fixedReply(body string, hdr ...string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		if w.Header().Get("Content-Length") == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		} else if w.Header().Get("Content-Length") == "none" {
			w.Header().Del("Content-Length")
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		io.WriteString(w, body)
	}
}

type result struct {
	code int
	body string
}

// get runs a GET for target through ph in the background.
func get(ph *ProxyHandler, ctx context.Context, target string, hdr ...string) <-chan result {
	ch := make(chan result, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
		req.RemoteAddr = "127.0.0.1:12345"
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rr := httptest.NewRecorder()
		ph.ServeHTTP(rr, req)
		ch <- result{rr.Code, rr.Body.String()}
	}()
	return ch
}

func newFlightHandler(t *testing.T) (*ProxyHandler, *CacheManager) {
	t.Helper()
	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cm := NewCacheManager(cfg)
	return NewProxyHandler(cfg, cm, NewForbiddenList(), NewForbiddenList(), NewForbiddenList()), cm
}

// followersJoin is how long the tests give followers to reach the flight
// before the leader is let go.
const followersJoin = 200 * time.Millisecond

func waitResult(t *testing.T, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
		return result{}
	}
}

// TestCollapsedForwardingSmallResponse: requests that arrive while the
// first is still being fetched wait for it and are answered from the
// cache; the origin sees one request.
func TestCollapsedForwardingSmallResponse(t *testing.T) {
	origin := newGatedOrigin(t, false, fixedReply("shared", "Cache-Control", "max-age=600"))
	ph, _ := newFlightHandler(t)
	buf := withAccessLog(ph)
	target := origin.srv.URL + "/a"

	leader := get(ph, context.Background(), target)
	<-origin.arrived
	var followers []<-chan result
	for range 3 {
		followers = append(followers, get(ph, context.Background(), target))
	}
	// A conditional follower is answered from the cache by its validator.
	conditional := get(ph, context.Background(), target, "If-None-Match", `"x"`)
	time.Sleep(followersJoin)
	close(origin.release)

	for _, ch := range append(followers, leader, conditional) {
		if r := waitResult(t, ch); r.code != 200 || r.body != "shared" {
			t.Errorf("got %d %q, want 200 %q", r.code, r.body, "shared")
		}
	}
	if n := origin.hits.Load(); n != 1 {
		t.Errorf("origin requests = %d, want 1", n)
	}
	hits := 0
	for _, line := range accessLines(t, buf) {
		if line["cache"] == "hit" {
			hits++
		}
	}
	if hits != 4 {
		t.Errorf("access log has %d hits, want 4 (the followers)", hits)
	}
}

// TestCollapsedForwardingReleasedAtHeaders: followers do not wait for a
// response that will not answer them from the cache. They are let go as
// soon as the leader's headers arrive, while its body is still on the
// way, and send their own requests.
func TestCollapsedForwardingReleasedAtHeaders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply func(w http.ResponseWriter, r *http.Request)
		setup func(*Config)
	}{
		{name: "private", reply: fixedReply("mine", "Cache-Control", "private, max-age=600")},
		{name: "no-store", reply: fixedReply("mine", "Cache-Control", "no-store")},
		{name: "Set-Cookie", reply: fixedReply("mine", "Cache-Control", "max-age=600", "Set-Cookie", "a=b")},
		{name: "Vary", reply: fixedReply("mine", "Cache-Control", "max-age=600", "Vary", "Accept-Language")},
		{name: "no-cache", reply: fixedReply("mine", "Cache-Control", "no-cache")},
		{name: "no lifetime", reply: fixedReply("mine")},
		{name: "unknown length", reply: fixedReply("mine", "Cache-Control", "max-age=600", "Content-Length", "none")},
		{
			name: "larger than memoryCacheMaxEntrySize", reply: fixedReply("mine", "Cache-Control", "max-age=600"),
			setup: func(c *Config) { c.MemoryCacheMaxEntrySize = 2 },
		},
		{name: "not 200", reply: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "max-age=600")
			w.WriteHeader(http.StatusNotFound)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := newGatedOrigin(t, true, tc.reply)
			ph, _ := newFlightHandler(t)
			if tc.setup != nil {
				tc.setup(ph.config())
			}
			target := origin.srv.URL + "/a"

			leader := get(ph, context.Background(), target)
			<-origin.arrived // the leader's headers are out; its body is held
			follower := get(ph, context.Background(), target)
			r := waitResult(t, follower)
			if r.code != 200 && tc.name != "not 200" {
				t.Errorf("follower got %d", r.code)
			}
			if n := origin.hits.Load(); n != 2 {
				t.Errorf("origin requests = %d, want 2 (the follower sent its own)", n)
			}
			close(origin.release)
			waitResult(t, leader)
		})
	}
}

// TestCollapsedForwardingFollowerLeaves: a follower whose client goes away
// stops waiting at once, and the leader's fetch carries on and is stored.
func TestCollapsedForwardingFollowerLeaves(t *testing.T) {
	origin := newGatedOrigin(t, false, fixedReply("shared", "Cache-Control", "max-age=600"))
	ph, cm := newFlightHandler(t)
	target := origin.srv.URL + "/a"

	leader := get(ph, context.Background(), target)
	<-origin.arrived
	ctx, cancel := context.WithCancel(context.Background())
	follower := get(ph, ctx, target)
	time.Sleep(followersJoin)
	cancel()
	waitResult(t, follower)

	close(origin.release)
	if r := waitResult(t, leader); r.code != 200 || r.body != "shared" {
		t.Errorf("leader got %d %q", r.code, r.body)
	}
	if cm.Lookup(httptest.NewRequest(http.MethodGet, target, nil)) == nil {
		t.Error("the leader's response was not stored")
	}
	if n := origin.hits.Load(); n != 1 {
		t.Errorf("origin requests = %d, want 1", n)
	}
}

// TestCollapsedForwardingLeaderFails: when the leader's fetch fails, the
// followers send their own requests rather than share the failure.
func TestCollapsedForwardingLeaderFails(t *testing.T) {
	var hits atomic.Int32
	arrived, release := make(chan struct{}), make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(arrived)
			<-release
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				conn.Close() // no response at all
			}
			return
		}
		fixedReply("second try", "Cache-Control", "max-age=600")(w, r)
	}))
	defer origin.Close()
	ph, _ := newFlightHandler(t)
	target := origin.URL + "/a"

	leader := get(ph, context.Background(), target)
	<-arrived
	follower := get(ph, context.Background(), target)
	time.Sleep(followersJoin)
	close(release)

	if r := waitResult(t, leader); r.code != http.StatusBadGateway {
		t.Errorf("leader got %d, want 502", r.code)
	}
	if r := waitResult(t, follower); r.code != 200 || r.body != "second try" {
		t.Errorf("follower got %d %q, want its own 200", r.code, r.body)
	}
}

// TestCollapsedForwardingRevalidation: requests for a stale entry that
// arrive while it is being revalidated wait for the 304 and are answered
// from the refreshed entry.
func TestCollapsedForwardingRevalidation(t *testing.T) {
	origin := newGatedOrigin(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		io.WriteString(w, "unexpected full fetch")
	})
	ph, cm := newFlightHandler(t)
	target := origin.srv.URL + "/a"
	seedStaleEntryWithValidators(t, cm, target, "cached body", `"v1"`, "")

	leader := get(ph, context.Background(), target)
	<-origin.arrived
	follower := get(ph, context.Background(), target)
	time.Sleep(followersJoin)
	close(origin.release)

	for _, ch := range []<-chan result{leader, follower} {
		if r := waitResult(t, ch); r.code != 200 || r.body != "cached body" {
			t.Errorf("got %d %q, want the revalidated body", r.code, r.body)
		}
	}
	if n := origin.hits.Load(); n != 1 {
		t.Errorf("origin requests = %d, want 1", n)
	}
}

// TestCollapsedForwardingNotForOthers: requests that cannot share a stored
// response - here with Range - do not wait on a flight.
func TestCollapsedForwardingNotForOthers(t *testing.T) {
	origin := newGatedOrigin(t, false, fixedReply("shared", "Cache-Control", "max-age=600"))
	ph, _ := newFlightHandler(t)
	target := origin.srv.URL + "/a"

	leader := get(ph, context.Background(), target)
	<-origin.arrived
	ranged := get(ph, context.Background(), target, "Range", "bytes=0-1")
	r := waitResult(t, ranged)
	if n := origin.hits.Load(); n != 2 || r.code == 0 {
		t.Errorf("origin requests = %d (status %d), want 2: a Range request goes on its own", n, r.code)
	}
	close(origin.release)
	waitResult(t, leader)
}

// TestCollapsedForwardingLeaderLeaves: when the leader's client goes away
// mid-body, its fetch is cancelled and nothing is stored; a follower that
// was waiting then sends its own request.
func TestCollapsedForwardingLeaderLeaves(t *testing.T) {
	origin := newGatedOrigin(t, true, fixedReply("shared", "Cache-Control", "max-age=600"))
	ph, _ := newFlightHandler(t)
	target := origin.srv.URL + "/a"

	ctx, cancel := context.WithCancel(context.Background())
	leader := get(ph, ctx, target)
	<-origin.arrived // headers out, so the follower will wait for the body
	follower := get(ph, context.Background(), target)
	time.Sleep(followersJoin)
	cancel()
	waitResult(t, leader)

	if r := waitResult(t, follower); r.code != 200 || r.body != "shared" {
		t.Errorf("follower got %d %q, want its own 200", r.code, r.body)
	}
	if n := origin.hits.Load(); n != 2 {
		t.Errorf("origin requests = %d, want 2", n)
	}
}

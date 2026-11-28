package main

import (
	"net/http"
	"sync"
	"time"
)

// A flight is one request to the origin for a URL that later requests for
// the same URL may wait on instead of sending their own: collapsed
// forwarding. Polipo got the same effect from its object model - requests
// for a URL share one in-memory object, and a request that finds it being
// fetched waits on it (client.c, OBJECT_INPROGRESS).
//
// go-polipo's followers do not share the leader's response. They wait for
// the leader to finish and then look the URL up in the cache again, so
// each is answered by the usual rules for its own request (freshness, its
// validators, Range). They wait at all only when the leader's response
// will become a small, fresh cache entry: the verdict, known once its
// headers have arrived. Otherwise - not stored, Vary, no-cache, no
// lifetime, a body larger than memoryCacheMaxEntrySize or of unknown
// length - they are let go at once and send their own request, as before.
// Polipo streamed the shared body to every waiter as it arrived, large
// ones included; doing that here would mean sharing a growing body between
// goroutines and holding it in memory for as long as anyone reads it.
type flight struct {
	verdict     chan struct{} // closed once wait is decided
	wait        bool          // followers should wait for done; set before verdict closes
	verdictOnce sync.Once
	done        chan struct{} // closed when the leader's request has finished
}

func (f *flight) decide(wait bool) {
	f.verdictOnce.Do(func() {
		f.wait = wait
		close(f.verdict)
	})
}

// flights maps a URL to the flight under way for it.
type flights struct {
	mu sync.Mutex
	m  map[string]*flight
}

// join returns the flight under way for key and false, or starts one with
// the caller as its leader and returns it and true. A leader must call
// finish when its request is done.
func (fs *flights) join(key string) (*flight, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if f, ok := fs.m[key]; ok {
		return f, false
	}
	if fs.m == nil {
		fs.m = make(map[string]*flight)
	}
	f := &flight{verdict: make(chan struct{}), done: make(chan struct{})}
	fs.m[key] = f
	return f, true
}

// finish ends the leader's flight: requests arriving from now on start a
// new one, and the followers stop waiting. A leader that never reached a
// verdict (an error, a response that was not even looked at) lets them go.
func (fs *flights) finish(key string, f *flight) {
	fs.mu.Lock()
	if fs.m[key] == f {
		delete(fs.m, key)
	}
	fs.mu.Unlock()
	f.decide(false)
	close(f.done)
}

// flightKey holds, on the leader's request context, its flight.
type flightKey struct{}

// canCollapse reports whether req may lead or join a flight: a GET that a
// stored response could answer for anyone - no Authorization (whose
// response the cache may not share), no Range (a partial response is not
// stored), and no Cache-Control that keeps the cache out of it.
func canCollapse(req *http.Request) bool {
	if req.Method != http.MethodGet || isRequestAuthenticated(req) || req.Header.Get(headerRange) != "" {
		return false
	}
	cc := parseCacheControl(req.Header)
	return !cc.has("no-cache") && !cc.has("no-store") && !hasPragmaNoCache(req.Header)
}

// awaitFlight waits, for a follower, for f's verdict and, if followers are
// to wait, for f to finish. It reports whether the follower should look in
// the cache again rather than send its own request. A follower whose
// client goes away stops waiting; the leader is not affected.
func awaitFlight(req *http.Request, f *flight) bool {
	select {
	case <-f.verdict:
	case <-req.Context().Done():
		return false
	}
	if !f.wait {
		return false
	}
	select {
	case <-f.done:
		return true
	case <-req.Context().Done():
		return false
	}
}

// decideFlight gives the verdict for the flight resp's request leads, if
// any, once cacheResponse has passed resp to Store: followers wait only
// for a response that is being stored, will be fresh for a request
// without Cache-Control of its own, and is at most memoryCacheMaxEntrySize
// long (by Content-Length), so that the wait is short.
func (ph *ProxyHandler) decideFlight(resp *http.Response) {
	f, _ := resp.Request.Context().Value(flightKey{}).(*flight)
	if f == nil {
		return
	}
	f.decide(ph.followersMayWait(resp))
}

func (ph *ProxyHandler) followersMayWait(resp *http.Response) bool {
	if _, stored := resp.Body.(*cacheBody); !stored {
		return false
	}
	if resp.StatusCode != http.StatusOK || responseVaries(resp) || responseHasNoCache(resp) {
		return false
	}
	limit := ph.config().MemoryCacheMaxEntrySize
	if resp.ContentLength < 0 || limit >= 0 && resp.ContentLength > limit {
		return false
	}
	requestTime, ok := resp.Request.Context().Value(requestTimeKey{}).(time.Time)
	if !ok {
		requestTime = time.Now()
	}
	birth := responseBirth(resp.Header, requestTime)
	return time.Since(birth) < freshnessLifetime(resp, birth)
}

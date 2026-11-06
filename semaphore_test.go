package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A request waiting for a maxConcurrentRequests slot whose client hangs up
// must give up at once. Before, it held its connection and goroutine until a
// slot freed, then went on to the Transport, which failed on the cancelled
// context and logged it as an origin error.
func TestSemaphoreWaitEndsWhenClientLeaves(t *testing.T) {
	var hits atomic.Int32
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/slow" {
			arrived <- struct{}{}
			<-release
		}
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	cfg := newTestConfig()
	cfg.MaxConcurrentRequests = 1
	cfg.NoDiskCache = true
	ph := newTestHandler(cfg)
	proxySrv := httptest.NewServer(ph)
	defer proxySrv.Close()
	proxyAddr := strings.TrimPrefix(proxySrv.URL, "http://")

	// A takes the only slot and stays at the origin until released.
	a, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	fmt.Fprintf(a, "GET %s/slow HTTP/1.1\r\nHost: %s\r\n\r\n", origin.URL, strings.TrimPrefix(origin.URL, "http://"))
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("request A never reached the origin")
	}

	// B waits for the slot, then its client hangs up.
	b, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(b, "GET %s/gone HTTP/1.1\r\nHost: %s\r\n\r\n", origin.URL, strings.TrimPrefix(origin.URL, "http://"))
	time.Sleep(200 * time.Millisecond) // B's handler is now waiting
	b.Close()
	time.Sleep(200 * time.Millisecond) // the server notices the hang-up

	// A finishes; B must not go on to the origin.
	close(release)
	a.SetDeadline(time.Now().Add(5 * time.Second))
	if line, err := bufio.NewReader(a).ReadString('\n'); err != nil || !strings.Contains(line, "200") {
		t.Fatalf("request A: status line = %q, err = %v", line, err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Errorf("origin got %d requests, want 1 (B's client had already gone)", n)
	}
	if n := ph.stats.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (B must stop before handleForward)", n)
	}
	if n := ph.stats.upstreamErrors.Load(); n != 0 {
		t.Errorf("upstreamErrors = %d, want 0 (a client that left is not an origin error)", n)
	}
}

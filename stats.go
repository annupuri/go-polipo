package main

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// proxyStats counts what the proxy has done since it started, for the
// /polipo/status page. Counters live in memory only and start again from
// zero at every restart. Polipo's status page reported its in-memory object
// and chunk usage; go-polipo has no chunk arenas, so it reports traffic
// counts instead, which are what a user of a personal cache wants to know:
// how much the cache is saving.
type proxyStats struct {
	started time.Time

	requests       atomic.Uint64 // forwarded (non-CONNECT) requests that reached the cache decision
	cacheHits      atomic.Uint64 // answered from the cache without contacting the origin
	cacheHitBytes  atomic.Uint64 // body bytes written for those answers
	revalidated    atomic.Uint64 // stale entries the origin confirmed with a 304
	upstream       atomic.Uint64 // requests sent to the origin (includes the 304s above)
	upstreamErrors atomic.Uint64 // of those, failures with no stale entry to fall back on
	tunnels        atomic.Uint64 // CONNECT tunnels opened
	tunnelFailures atomic.Uint64 // CONNECT targets that could not be reached
	denied         atomic.Uint64 // requests refused by policy (clients, auth, lists, ports)
}

func newProxyStats() *proxyStats {
	return &proxyStats{started: time.Now()}
}

// countingWriter counts the body bytes written through it.
type countingWriter struct {
	http.ResponseWriter
	n uint64
}

var _ http.ResponseWriter = (*countingWriter)(nil)

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n += uint64(n)
	return n, err
}

// percent formats part/whole as a percentage, or "-" when whole is zero.
func percent(part, whole uint64) string {
	if whole == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", float64(part)*100/float64(whole))
}

// writeStatus renders the /polipo/status page.
func (ph *ProxyHandler) writeStatus(w io.Writer) {
	s := ph.stats
	requests := s.requests.Load()
	hits := s.cacheHits.Load()
	revalidated := s.revalidated.Load()

	mode := "on line"
	switch {
	case ph.config.ProxyOffline:
		mode = "off line"
	case ph.config.RelaxTransparency > 0:
		mode = "on line (transparency relaxed)"
	}

	rows := []struct{ name, value string }{
		{"Version", Version},
		{"Mode", mode},
		{"Running since", s.started.Format("2006-01-02 15:04:05 MST")},
		{"Uptime", time.Since(s.started).Truncate(time.Second).String()},
		{"Requests (HTTP)", fmt.Sprint(requests)},
		{"Cache hits", fmt.Sprintf("%d (%s)", hits, percent(hits, requests))},
		{"Revalidated (304)", fmt.Sprintf("%d (%s)", revalidated, percent(revalidated, requests))},
		{"Served from cache, total", percent(hits+revalidated, requests)},
		{"Bytes served from cache", fmt.Sprint(s.cacheHitBytes.Load())},
		{"Sent to origin", fmt.Sprint(s.upstream.Load())},
		{"Origin errors", fmt.Sprint(s.upstreamErrors.Load())},
		{"CONNECT tunnels", fmt.Sprint(s.tunnels.Load())},
		{"CONNECT failures", fmt.Sprint(s.tunnelFailures.Load())},
		{"Denied by policy", fmt.Sprint(s.denied.Load())},
		{"In-memory cache entries", fmt.Sprint(ph.cacheManager.MemoryEntries())},
	}

	fmt.Fprint(w, `<html><head><title>go-polipo Status</title></head>
<body>
<h1>go-polipo Status</h1>
<p>Counts since the last start; they are not saved across restarts.</p>
<table border="1" cellpadding="5" cellspacing="0">
`)
	for _, r := range rows {
		fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td></tr>\n",
			html.EscapeString(r.name), html.EscapeString(r.value))
	}
	fmt.Fprint(w, `</table>
<p><a href="/polipo/">Back</a></p>
</body></html>`)
}

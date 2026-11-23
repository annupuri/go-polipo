package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// cacheAction is what the cache did for one request, as the access log's
// "cache" field records it. It is one word, not the entry's details: the
// status and the method already tell most of the rest.
type cacheAction string

const (
	// cacheHit: answered from a fresh entry without contacting the origin.
	cacheHit cacheAction = "hit"
	// cacheStale: answered from an entry past its freshness lifetime, as
	// relaxTransparency or proxyOffline allow (the response carries a
	// Warning saying which).
	cacheStale cacheAction = "stale"
	// cacheRevalidated: the origin confirmed a stored entry with a 304 to a
	// conditional request go-polipo made, and the entry was served.
	cacheRevalidated cacheAction = "revalidated"
	// cacheMiss: the cache could not answer. Usually the origin's response
	// was passed on (stored or not); with no origin to ask (proxyOffline,
	// only-if-cached, an unreachable origin and nothing to fall back on)
	// the status is an error.
	cacheMiss cacheAction = "miss"
)

// accessTimeFormat is RFC 3339 with milliseconds, in local time with its
// offset, so that a line reads as the clock on the wall did.
const accessTimeFormat = "2006-01-02T15:04:05.000Z07:00"

// accessLogEntry is one line of the access log, written as a JSON object
// when the request is done. Time is when the request came in; add
// duration_ms for when it ended. A forwarded or local request has url and
// bytes (body bytes sent to the client), and cache once it reached the
// cache decision. A CONNECT has host and the bytes each way through the
// tunnel instead, and never cache: what goes through a tunnel is not seen.
type accessLogEntry struct {
	Time          string      `json:"time"`
	Client        string      `json:"client"`
	Method        string      `json:"method"`
	URL           string      `json:"url,omitempty"`
	Host          string      `json:"host,omitempty"`
	Status        int         `json:"status"`
	Bytes         *int64      `json:"bytes,omitempty"`
	BytesSent     *int64      `json:"bytes_sent,omitempty"`
	BytesReceived *int64      `json:"bytes_received,omitempty"`
	DurationMS    int64       `json:"duration_ms"`
	Cache         cacheAction `json:"cache,omitempty"`
}

// accessLogger writes access log entries to w, one JSON object per line.
// Each line goes out in a single Write, so lines from concurrent requests
// do not interleave when w (a logFileWriter) serialises its writes.
type accessLogger struct {
	w        io.Writer
	logLevel int
	// failing is set while writes fail, so that a full disk logs one error
	// rather than one per request; the next successful write clears it.
	failing atomic.Bool
}

func newAccessLogger(w io.Writer, logLevel int) *accessLogger {
	return &accessLogger{w: w, logLevel: logLevel}
}

func (l *accessLogger) write(e *accessLogEntry) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// URLs keep their & < > as they are; JSON does not need them escaped.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil { // Encode ends the line with '\n'
		logf(l.logLevel, LogError, "Couldn't encode an access log entry: %v", err)
		return
	}
	if _, err := l.w.Write(buf.Bytes()); err != nil {
		if !l.failing.Swap(true) {
			logf(l.logLevel, LogError, "Couldn't write to the access log, entries are lost until it works again: %v", err)
		}
		return
	}
	if l.failing.Swap(false) {
		logf(l.logLevel, LogInfo, "Writing to the access log again")
	}
}

// accessRecord collects, while a request is handled, what only the code
// handling it knows: the cache action, and for a CONNECT the status sent on
// the hijacked connection and the bytes through the tunnel. It travels in
// the request's context (and so reaches the ReverseProxy hooks), and is
// only touched by the goroutine serving the request. Its methods do
// nothing on a nil record, which is what a request carries when the access
// log is off.
type accessRecord struct {
	cache         cacheAction
	status        int // a status written outside the ResponseWriter (CONNECT)
	bytesSent     int64
	bytesReceived int64
}

type accessRecordKey struct{}

func accessRecordFrom(ctx context.Context) *accessRecord {
	rec, _ := ctx.Value(accessRecordKey{}).(*accessRecord)
	return rec
}

func (r *accessRecord) setCache(a cacheAction) {
	if r != nil {
		r.cache = a
	}
}

// setCacheAction records a on the access record req carries, if any.
func setCacheAction(req *http.Request, a cacheAction) {
	accessRecordFrom(req.Context()).setCache(a)
}

// servedFromCache is the cache action for serving an entry that is stale
// or not.
func servedFromCache(stale bool) cacheAction {
	if stale {
		return cacheStale
	}
	return cacheHit
}

func (r *accessRecord) setStatus(code int) {
	if r != nil {
		r.status = code
	}
}

func (r *accessRecord) setTunnelBytes(sent, received int64) {
	if r != nil {
		r.bytesSent, r.bytesReceived = sent, received
	}
}

// accessLogWriter wraps the client's ResponseWriter to see the status and
// count the body bytes. Unwrap lets http.ResponseController reach the
// underlying writer for Flush and Hijack, which is how ReverseProxy and
// handleConnect use them.
type accessLogWriter struct {
	http.ResponseWriter
	status int
	n      int64
}

var _ http.ResponseWriter = (*accessLogWriter)(nil)

func (w *accessLogWriter) WriteHeader(code int) {
	// A 1xx other than 101 is informational; the final status follows.
	if w.status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *accessLogWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *accessLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// serveLogged handles req as ServeHTTP does, then writes its access log
// line. The line is written from a deferred call, so that a request that
// ends in a panic - ReverseProxy aborts a response whose body fails midway
// with http.ErrAbortHandler - is logged too.
func (ph *ProxyHandler) serveLogged(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	rec := &accessRecord{}
	lw := &accessLogWriter{ResponseWriter: w}
	req = req.WithContext(context.WithValue(req.Context(), accessRecordKey{}, rec))
	defer func() {
		e := &accessLogEntry{
			Time:       start.Format(accessTimeFormat),
			Client:     req.RemoteAddr,
			Method:     req.Method,
			Status:     lw.status,
			DurationMS: time.Since(start).Milliseconds(),
		}
		if req.Method == http.MethodConnect {
			e.Host = req.RequestURI
			if rec.status != 0 {
				e.Status = rec.status
			}
			e.BytesSent, e.BytesReceived = &rec.bytesSent, &rec.bytesReceived
		} else {
			e.URL = accessLogURL(req)
			e.Bytes = &lw.n
			e.Cache = rec.cache
			// A handler that returns without writing gets an empty 200
			// from net/http, unless the client has gone away, which is
			// when go-polipo does that; then nothing was sent and the
			// status stays 0.
			if e.Status == 0 && req.Context().Err() == nil {
				e.Status = http.StatusOK
			}
		}
		ph.accessLog.write(e)
	}()
	ph.serveHTTP(lw, req)
}

// accessLogURL is the request's target as an absolute URL, with any
// password in it masked. A forwarded request already has one; a request
// addressed to go-polipo itself (origin-form) is given its Host.
func accessLogURL(req *http.Request) string {
	u := *req.URL
	if u.Host == "" {
		u.Scheme, u.Host = "http", req.Host
	}
	return u.Redacted()
}

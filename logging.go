package main

import "log"

// Log categories, matching Polipo's logLevel bitmask (log.h). Each bit gates
// an independent class of message rather than an ordered severity
// threshold: unlike "WARN implies ERROR is also wanted" schemes, enabling
// LogWarn says nothing about LogError, and any combination can be set at
// once (Polipo's own default, 0x07, is LogError|LogWarn|LogInfo).
const (
	LogError       = 0x1  // internal/unexpected failures
	LogWarn        = 0x2  // recoverable problems (a skipped config/cache entry, ...)
	LogInfo        = 0x4  // lifecycle and status messages (startup, reload, ...)
	LogForbidden   = 0x8  // a request denied by policy (forbidden list, auth, ports)
	LogUncacheable = 0x10 // present for parity with Polipo's bit; no call site uses it
	LogSuperseded  = 0x20 // present for parity with Polipo's bit; no call site uses it
	LogVary        = 0x40 // present for parity with Polipo's bit; no call site uses it
	LogTunnel      = 0x80 // a CONNECT tunnel accepted or rejected
)

// logf writes format/args through the standard log package iff category's
// bit is set in logLevel - matching Polipo's do_log (log.c): "if (type &
// LOGGING_MAX & logLevel) ...". Every call site below picks the one
// category its message belongs to, the same way Polipo's own do_log call
// sites do.
func logf(logLevel, category int, format string, args ...any) {
	if logLevel&category == 0 {
		return
	}
	log.Printf(format, args...)
}

package main

import "log"

// Log categories, matching Polipo's logLevel bitmask (log.h). Each bit gates
// an independent class of message rather than an ordered severity
// threshold: unlike "WARN implies ERROR is also wanted" schemes, enabling
// LogWarn says nothing about LogError, and any combination can be set at
// once (Polipo's own default, 0x07, is LogError|LogWarn|LogInfo).
//
// Only the categories go-polipo actually logs are defined. Polipo's other
// bits (0x10 uncacheable, 0x20 superseded, 0x40 Vary, 0x80 tunnel) have no
// go-polipo messages; a logLevel that sets them is accepted, so an existing
// Polipo config still loads, and those bits are simply ignored. A rejected
// CONNECT is logged as LogForbidden, like every other policy denial.
const (
	LogError     = 0x1 // internal/unexpected failures
	LogWarn      = 0x2 // recoverable problems (a skipped config/cache entry, ...)
	LogInfo      = 0x4 // lifecycle and status messages (startup, reload, ...)
	LogForbidden = 0x8 // a request denied by policy (forbidden list, auth, ports)
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

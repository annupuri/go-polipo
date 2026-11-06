package main

import (
	"log"
	"os"
	"sync"
)

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

// logFileWriter is the log package's output when logFile is set. Reopen
// swaps in a freshly opened file at the same path, so that after a log
// rotation tool renames the file, logging continues in a new one - Polipo's
// reopenLog (log.c), run on SIGUSR1 and SIGUSR2.
type logFileWriter struct {
	mu   sync.Mutex
	path string
	perm os.FileMode
	f    *os.File
}

func openLogFileWriter(path string, perm os.FileMode) (*logFileWriter, error) {
	f, err := openLogFile(path, perm)
	if err != nil {
		return nil, err
	}
	return &logFileWriter{path: path, perm: perm, f: f}, nil
}

// openLogFile opens path for appending. perm (logFilePermissions) applies
// only when the file is created, less the umask, as with Polipo's open(2);
// an existing file keeps its mode.
func openLogFile(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perm)
}

func (w *logFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Write(p)
}

// Reopen opens the log file's path again and switches to it. If that fails,
// logging carries on in the current file and the error is returned - unlike
// Polipo, which exited, so a failed log rotation does not take the proxy down.
func (w *logFileWriter) Reopen() error {
	f, err := openLogFile(w.path, w.perm)
	if err != nil {
		return err
	}
	w.mu.Lock()
	old := w.f
	w.f = f
	w.mu.Unlock()
	return old.Close()
}

func (w *logFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

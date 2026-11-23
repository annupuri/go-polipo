//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// setupReloadSignals registers SIGUSR1 and SIGUSR2, matching Polipo's handling
// of them (event.c; polipo.texi, "Stopping"): both reopen the log files
// (reopenLog, nil when there is neither logFile nor accessLogFile) so that
// log rotation works, then reload the forbidden lists (reload). SIGUSR2
// also calls discardMemory in
// between, matching Polipo's discardObjects, which drops the in-memory cache.
// Polipo's SIGUSR1 writeoutObjects has no equivalent here: Store writes to
// disk at once. A failed reopen is logged and logging carries on in the old
// file, where Polipo exited. Windows has no SIGUSR1/SIGUSR2 (see
// signal_windows.go) - POST /polipo/reload covers the reload there. logLevel
// gates the outcome messages (see logf); pass the owning Config's LogLevel.
func setupReloadSignals(logLevel int, reopenLog func() error, reload func() error, discardMemory func()) {
	// Buffered for 2: os/signal sends non-blocking, so SIGUSR1 and SIGUSR2
	// arriving before the goroutine below drains the channel would otherwise
	// silently drop one of them.
	c := make(chan os.Signal, 2)
	signal.Notify(c, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range c {
			if reopenLog != nil {
				if err := reopenLog(); err != nil {
					logf(logLevel, LogError, "Couldn't reopen a log file (%s), still logging to the old one: %v", sig, err)
				} else {
					logf(logLevel, LogInfo, "Reopened the log files (%s)", sig)
				}
			}
			if sig == syscall.SIGUSR2 {
				discardMemory()
				logf(logLevel, LogInfo, "Discarded the memory cache (%s)", sig)
			}
			if err := reload(); err != nil {
				logf(logLevel, LogError, "Reload (%s) failed: %v", sig, err)
				continue
			}
			logf(logLevel, LogInfo, "Reloaded forbidden/uncachable lists (%s)", sig)
		}
	}()
}

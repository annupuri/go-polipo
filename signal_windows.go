//go:build windows

package main

// setupReloadSignals is a no-op on Windows: syscall.SIGUSR1/SIGUSR2 do not
// exist there, so POST /polipo/reload is the only runtime-reload path on
// this platform, and the log file cannot be reopened while running (see
// signal_unix.go for the Unix equivalent). The arguments are accepted only
// to keep the signature identical across platforms.
func setupReloadSignals(logLevel int, reopenLog func() error, reload func() error, discardMemory func()) {
}

package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLogfGatesByCategory(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard) // TestMain's resting state; see main_test.go.

	logf(LogWarn, LogError, "should not appear")
	if buf.Len() != 0 {
		t.Errorf("expected no output when category's bit is unset in logLevel, got %q", buf.String())
	}

	logf(LogWarn|LogError, LogError, "should appear: %d", 42)
	if !strings.Contains(buf.String(), "should appear: 42") {
		t.Errorf("expected the message when category's bit is set, got %q", buf.String())
	}
}

// TestLogDefaultLevelMatchesPolipo pins go-polipo's default logLevel (0x07,
// asserted in config_test.go's TestNewDefaultConfig) to Polipo's own
// LOGGING_DEFAULT (log.h: L_ERROR|L_WARN|L_INFO) - the three categories that
// were already unconditionally logged before this file existed stay visible
// out of the box, and denials are opt-in, same as real Polipo.
func TestLogDefaultLevelMatchesPolipo(t *testing.T) {
	const defaultLevel = 0x07
	for _, category := range []int{LogError, LogWarn, LogInfo} {
		if defaultLevel&category == 0 {
			t.Errorf("expected category 0x%x enabled at the default logLevel 0x%x", category, defaultLevel)
		}
	}
	for _, category := range []int{LogForbidden} {
		if defaultLevel&category != 0 {
			t.Errorf("expected category 0x%x disabled at the default logLevel 0x%x", category, defaultLevel)
		}
	}
}

// skipReopenOnWindows skips a test that renames an open log file or its
// directory, which Windows does not allow. Reopening is for log rotation and
// is triggered by SIGUSR1/SIGUSR2, which Windows does not have either.
func skipReopenOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot rename an open file; log reopening is Unix-only")
	}
}

// TestLogFileWriterReopen: after a log rotation tool renames the log file,
// Reopen makes later messages go to a new file at the original path, while
// the renamed file keeps what was written before.
func TestLogFileWriterReopen(t *testing.T) {
	skipReopenOnWindows(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "polipo.log")
	w, err := openLogFileWriter(path, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	w.Write([]byte("before\n"))
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("still old\n"))
	if err := w.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	w.Write([]byte("after\n"))

	if got, _ := os.ReadFile(rotated); string(got) != "before\nstill old\n" {
		t.Errorf("rotated file = %q", got)
	}
	if got, _ := os.ReadFile(path); string(got) != "after\n" {
		t.Errorf("new log file = %q, want only what was written after Reopen", got)
	}
}

// TestLogFileWriterReopenFailureKeepsLogging: when the path cannot be opened
// again, Reopen reports the error and logging carries on in the old file -
// Polipo exited instead.
func TestLogFileWriterReopenFailureKeepsLogging(t *testing.T) {
	skipReopenOnWindows(t)
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "polipo.log")
	w, err := openLogFileWriter(path, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Move the whole directory away, so the path has no parent to create
	// the file in.
	moved := dir + ".old"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := w.Reopen(); err == nil {
		t.Fatal("Reopen should fail when the log file's directory is gone")
	}
	if _, err := w.Write([]byte("kept\n")); err != nil {
		t.Fatalf("Write after a failed Reopen: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(moved, "polipo.log")); string(got) != "kept\n" {
		t.Errorf("old file = %q, want logging to continue there", got)
	}
}

// TestLogFilePermissions: a new log file gets logFilePermissions (Polipo's
// 0640 by default), not 0644; an existing file keeps its own mode.
func TestLogFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	if got := NewDefaultConfig().LogFilePermissions; got != 0o640 {
		t.Errorf("default logFilePermissions = %#o, want 0640", got)
	}
	cfg := NewDefaultConfig()
	if err := cfg.Set("logFilePermissions", "600"); err != nil || cfg.LogFilePermissions != 0o600 {
		t.Errorf("Set 600: mode = %#o, err = %v", cfg.LogFilePermissions, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "polipo.log")
	w, err := openLogFileWriter(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("new log file mode = %v, err = %v; want 0600", fi.Mode().Perm(), err)
	}

	existing := filepath.Join(dir, "existing.log")
	if err := os.WriteFile(existing, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(existing, 0o644)
	w, err = openLogFileWriter(existing, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o644 {
		t.Errorf("existing log file mode = %v, want it left at 0644", fi.Mode().Perm())
	}
}

package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
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

// TestLogFileWriterReopen: after a log rotation tool renames the log file,
// Reopen makes later messages go to a new file at the original path, while
// the renamed file keeps what was written before.
func TestLogFileWriterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polipo.log")
	w, err := openLogFileWriter(path)
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
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "polipo.log")
	w, err := openLogFileWriter(path)
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

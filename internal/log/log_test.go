package log

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

func TestLoggerInfoIncludesTimestamp(t *testing.T) {
	logger := New()
	logger.SetVerbosity(1)
	logger.now = func() time.Time {
		return time.Date(2026, 8, 30, 12, 34, 56, 789, time.FixedZone("EET", 2*60*60))
	}

	var gotPrefix, gotContent string
	logger.SetSender(func(prefix, content string) {
		gotPrefix = prefix
		gotContent = content
	})

	logger.Info("hello %s", "world")

	if gotPrefix != "[INFO]" {
		t.Fatalf("prefix = %q, want [INFO]", gotPrefix)
	}
	want := "2026-08-30T10:34:56.000000789Z hello world"
	if gotContent != want {
		t.Fatalf("content = %q, want %q", gotContent, want)
	}
}

func TestLoggerDebugIncludesTimestampOnStderr(t *testing.T) {
	logger := New()
	logger.SetVerbosity(2)
	logger.now = func() time.Time {
		return time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	}

	got := captureStderr(t, func() {
		logger.Debug("details=%d", 42)
	})

	want := "[DEBUG] 2026-08-30T01:02:03Z details=42\n"
	if got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestLoggerWarnIsAlwaysVisible(t *testing.T) {
	logger := New()
	logger.now = func() time.Time {
		return time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	}

	got := captureStderr(t, func() {
		logger.Warn("reduced isolation")
	})

	want := "[WARN] 2026-08-30T01:02:03Z reduced isolation\n"
	if got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = original
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	return buf.String()
}

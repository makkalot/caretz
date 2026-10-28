package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunUnsandboxedUsesCwdAndEnv(t *testing.T) {
	dir := t.TempDir()
	stdout, _, err := RunUnsandboxed(context.Background(), dir, `printf '%s|%s' "$PWD" "$CARETZ_TEST_VALUE"`, RunOptions{Env: map[string]string{"CARETZ_TEST_VALUE": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stdout, "|ok") || !strings.Contains(stdout, dir[strings.LastIndex(dir, "/"):]) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestRunApprovedOmitsCaretzCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("CARETZ_TEST_KEEP", "kept")
	stdout, _, err := RunApproved(context.Background(), t.TempDir(), `printf '%s|%s' "${OPENAI_API_KEY-unset}" "$CARETZ_TEST_KEEP"`, RunOptions{})
	if err != nil || stdout != "unset|kept" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
	if stdout, _, _ := RunUnsandboxed(context.Background(), t.TempDir(), `printf '%s' "$OPENAI_API_KEY"`, RunOptions{}); stdout != "secret" {
		t.Fatalf("disabled sandbox lost the caller's environment: %q", stdout)
	}
}

func TestRunUnsandboxedTimesOut(t *testing.T) {
	_, _, err := RunUnsandboxed(context.Background(), t.TempDir(), "sleep 5", RunOptions{TimeoutSeconds: 1})
	if err == nil || !strings.Contains(err.Error(), "timed out after 1 seconds") {
		t.Fatalf("error = %v", err)
	}
}

// A background job that keeps the output open, as in
// "cd x && setsid build > log &", is no failure of the command itself.
func TestRunApprovedBackgroundJobHoldingOutputSucceeds(t *testing.T) {
	saved := hostWaitDelay
	hostWaitDelay = 100 * time.Millisecond
	t.Cleanup(func() { hostWaitDelay = saved })
	stdout, _, err := RunApproved(context.Background(), t.TempDir(), "true && sleep 3 > /dev/null & echo started", RunOptions{})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !strings.HasPrefix(stdout, "started\n") || !strings.Contains(stdout, outputLeftOpenNote) {
		t.Fatalf("stdout = %q", stdout)
	}
}

// Run manually under a terminal. The input must arrive after the test starts
// reading. Opt in explicitly so ordinary test runs never wait for user input:
// CARETZ_RUN_TERMINAL_INTEGRATION=1 go test ./internal/sandbox -run '^TestRunApprovedForegroundUsesTerminal$' -count=1 -v
func TestRunApprovedForegroundUsesTerminal(t *testing.T) {
	if os.Getenv("CARETZ_RUN_TERMINAL_INTEGRATION") != "1" {
		t.Skip("manual terminal test; set CARETZ_RUN_TERMINAL_INTEGRATION=1 and type hello when prompted")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skip("no controlling terminal")
	}
	defer func() { _ = tty.Close() }()
	t.Log("Type hello and press Enter within 10 seconds")
	var display strings.Builder
	stdout, _, err := RunApprovedForeground(context.Background(), t.TempDir(), `read -r line; echo "got=$line"`, RunOptions{TimeoutSeconds: 10}, tty, &display)
	if err != nil || strings.TrimSpace(stdout) != "got=hello" || !strings.Contains(display.String(), "got=hello") {
		t.Fatalf("stdout=%q display=%q err=%v", stdout, display.String(), err)
	}
	foreground, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		t.Fatalf("terminal foreground group = %d, want caretz's %d (err=%v)", foreground, unix.Getpgrp(), err)
	}
}

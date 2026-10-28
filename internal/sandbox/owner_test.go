package sandbox

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSandboxOwnerIsReserved(t *testing.T) {
	if err := validateRequestEnvironment(map[string]string{sandboxOwnerEnv: "spoofed"}); err == nil {
		t.Fatal("request environment may set the sandbox owner marker")
	}
}

// Approved commands left the sandbox on purpose, so they may outlive Caretz.
func TestTerminateSandboxedProcessesSparesApprovedCommands(t *testing.T) {
	stdout, _, err := RunApproved(context.Background(), t.TempDir(), `nohup sleep 30 >/dev/null 2>&1 & echo $!`, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil {
		t.Fatalf("stdout = %q", stdout)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// Scan as if a sandboxed command had run too, so the approved one is
	// spared by the scan rather than by skipping it.
	oneShotLaunched.Store(true)
	TerminateSandboxedProcesses()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("approved background process %d was stopped: %v", pid, err)
	}
}

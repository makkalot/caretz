//go:build darwin

package sandbox

import (
	"os/exec"
	"testing"
)

// nativeSandboxBehavior records where the Seatbelt backend intentionally
// differs from Linux. Keep it in sync with conformance_linux_test.go.
var nativeSandboxBehavior = sandboxBehavior{
	privateNetworkNamespace: false,
	stopsNewSessionJobs:     false,
}

func requireNativeSandbox(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Fatal("sandbox-exec is required for macOS sandbox integration tests")
	}
}

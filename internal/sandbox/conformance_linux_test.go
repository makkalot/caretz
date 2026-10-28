//go:build linux

package sandbox

import "testing"

// nativeSandboxBehavior records where the bubblewrap backend intentionally
// differs from macOS. Keep it in sync with conformance_darwin_test.go.
var nativeSandboxBehavior = sandboxBehavior{
	privateNetworkNamespace: true,
	stopsNewSessionJobs:     true,
}

func requireNativeSandbox(t *testing.T) {
	t.Helper()
	requireBwrap(t)
}

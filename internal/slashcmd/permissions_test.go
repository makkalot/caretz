package slashcmd

import (
	"context"
	"strings"
	"testing"
)

func TestAddPathCommandGrantsReadOnlyPathImmediatelyAndPersists(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	t.Cleanup(func() { _ = session.Close() })

	result, err := (AddPathCommand{}).Run(context.Background(), session, "project/data")
	if err != nil {
		t.Fatalf("/addpath returned error: %v", err)
	}
	if !strings.Contains(strings.ToLower(result.Message), "read") || !strings.Contains(result.Message, "project/data") {
		t.Fatalf("/addpath message = %q, want read-only path confirmation", result.Message)
	}
	if !hasString(session.ToolRegistry().ReadableRoots, "project/data") {
		t.Fatalf("read-only path was not added to the active tool registry: %#v", session.ToolRegistry().ReadableRoots)
	}
	if hasString(session.ToolRegistry().WritableRoots, "project/data") {
		t.Fatalf("read-only path was added as writable: %#v", session.ToolRegistry().WritableRoots)
	}

	if _, err := (AddPathCommand{}).Run(context.Background(), session, "--rw project/output"); err != nil {
		t.Fatalf("second /addpath returned error: %v", err)
	}
	if !hasString(session.ToolRegistry().WritableRoots, "project/output") {
		t.Fatalf("read-write path was not retained for the rest of the session: %#v", session.ToolRegistry().WritableRoots)
	}
}

func TestAddNetCommandAllowsHostForRestOfSession(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	t.Cleanup(func() { _ = session.Close() })

	result, err := (AddNetCommand{}).Run(context.Background(), session, "github.com:443")
	if err != nil {
		t.Fatalf("/addnet returned error: %v", err)
	}
	if !strings.Contains(result.Message, "github.com:443") {
		t.Fatalf("/addnet message = %q, want host confirmation", result.Message)
	}
	// A second command execution represents a later turn in the same session;
	// the implementation must retain the grant rather than only approving one call.
	later, err := (AddNetCommand{}).Run(context.Background(), session, "github.com:443")
	if err != nil {
		t.Fatalf("network grant was not retained for the rest of the session: %v", err)
	}
	if !strings.Contains(later.Message, "already") && !strings.Contains(later.Message, "github.com:443") {
		t.Fatalf("later /addnet message = %q, want retained-host confirmation", later.Message)
	}
}

func TestAddNetCommandAllowsSOCKSTCPForRestOfSession(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	t.Cleanup(func() { _ = session.Close() })

	result, err := (AddNetCommand{}).Run(context.Background(), session, "tcp://GitHub.COM.:22")
	if err != nil {
		t.Fatalf("/addnet returned error: %v", err)
	}
	if !strings.Contains(result.Message, "tcp://GitHub.COM.:22") {
		t.Fatalf("/addnet message = %q, want TCP target confirmation", result.Message)
	}
	if !hasString(session.ToolRegistry().NetworkAllowlist, "tcp://github.com:22") {
		t.Fatalf("TCP grant was not added to the active tool registry: %#v", session.ToolRegistry().NetworkAllowlist)
	}
}

func TestPermissionCommandsRejectMissingAndInvalidArguments(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	t.Cleanup(func() { _ = session.Close() })

	tests := []struct {
		name string
		cmd  Command
		args string
	}{
		{"addpath missing", AddPathCommand{}, ""},
		{"addpath too many", AddPathCommand{}, "one two"},
		{"addpath unknown option", AddPathCommand{}, "--nope path"},
		{"addpath missing rw value", AddPathCommand{}, "--rw"},
		{"addnet missing", AddNetCommand{}, ""},
		{"addnet too many", AddNetCommand{}, "one two"},
		{"addnet invalid host", AddNetCommand{}, "not a host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.cmd.Run(context.Background(), session, tt.args); err == nil {
				t.Fatalf("%s accepted invalid arguments %q", tt.name, tt.args)
			}
		})
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

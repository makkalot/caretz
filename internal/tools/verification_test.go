package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/sandbox"
)

func TestRunVerificationCommandPreservesFailureStatus(t *testing.T) {
	registry := &Registry{WorkspaceDir: t.TempDir(), RunSandboxedOptions: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		if command != "test -f report.csv" {
			t.Fatalf("command = %q", command)
		}
		return "partial output", "missing file", fmt.Errorf("command exited with status 7")
	}}
	result, err := registry.RunVerificationCommand(context.Background(), "test -f report.csv")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || !strings.Contains(result.Stderr, "missing file") {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunVerificationCommandDetectsFailurePrintedWithZeroExit(t *testing.T) {
	registry := &Registry{WorkspaceDir: t.TempDir(), RunSandboxedOptions: func(_ context.Context, _, _ string, _ sandbox.RunOptions) (string, string, error) {
		return "PASS january\nFAIL february\n", "", nil
	}}
	result, err := registry.RunVerificationCommand(context.Background(), "check statements")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !result.OutputFailure {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunVerificationCommandReportsTimeoutAsResult(t *testing.T) {
	var timeout int
	registry := &Registry{WorkspaceDir: t.TempDir(), RunSandboxedOptions: func(_ context.Context, _, _ string, opts sandbox.RunOptions) (string, string, error) {
		timeout = opts.TimeoutSeconds
		return "partial", "", fmt.Errorf("command timed out after 300 seconds")
	}}
	result, err := registry.RunVerificationCommandWithTimeout(context.Background(), "go test ./...", 300)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.ExitCode != -1 || timeout != 300 {
		t.Fatalf("result = %+v, timeout = %d", result, timeout)
	}
}

func TestInteractiveCommandSharesPersistentBashSession(t *testing.T) {
	workspace := t.TempDir()
	runner := sandbox.NewRunner(sandbox.Config{})
	base := &Registry{
		WorkspaceDir:      workspace,
		SessionExec:       true,
		SandboxDisabled:   true,
		NewSandboxSession: func() *sandbox.Session { return runner.NewSession(true) },
	}
	registry, owned := base.BindSessionExecution()
	if !owned {
		t.Fatal("session execution was not bound")
	}
	t.Cleanup(func() { _ = registry.CloseSessionExecution() })
	if _, err := registry.HandleToolCall(makeToolCall("Bash", "seed", map[string]any{
		"command": `printf shared > "$TMPDIR/value"`,
	})); err != nil {
		t.Fatal(err)
	}
	result, err := registry.RunInteractiveCommand(context.Background(), `cat "$TMPDIR/value"`)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "shared" {
		t.Fatalf("interactive result = %+v", result)
	}
}

func TestPersistentPathGrantRecreatesInteractiveSandbox(t *testing.T) {
	workspace := t.TempDir()
	runner := sandbox.NewRunner(sandbox.Config{})
	base := &Registry{
		WorkspaceDir:      workspace,
		SessionExec:       true,
		SandboxDisabled:   true,
		NewSandboxSession: func() *sandbox.Session { return runner.NewSession(true) },
	}
	registry, _ := base.BindSessionExecution()
	t.Cleanup(func() { _ = registry.CloseSessionExecution() })
	if _, err := registry.RunInteractiveCommand(context.Background(), `printf old > "$TMPDIR/value"`); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddPermissionGrant(PermissionPathRead, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	result, err := registry.RunInteractiveCommand(context.Background(), `test ! -e "$TMPDIR/value" && printf refreshed`)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "Session execution supervisor was recreated") || !strings.HasSuffix(result.Stdout, "refreshed") {
		t.Fatalf("interactive result = %+v", result)
	}
}

func TestRunVerificationCommandReportsSandboxDenials(t *testing.T) {
	workspace := t.TempDir()
	run := func(stderr string, err error) VerificationResult {
		t.Helper()
		registry := &Registry{WorkspaceDir: workspace, RunSandboxedOptions: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
			return "", stderr, err
		}}
		result, runErr := registry.RunVerificationCommand(context.Background(), "go test ./...")
		if runErr != nil {
			t.Fatal(runErr)
		}
		return result
	}
	network := run("dial tcp: connection refused", &sandbox.NetworkBlockedError{Target: "proxy.golang.org:443"})
	if network.ExitCode == 0 || len(network.Denials) != 1 || network.Denials[0] != (sandbox.Denial{Kind: sandbox.DenialNetwork, Target: "proxy.golang.org:443"}) {
		t.Fatalf("network denial = %+v", network)
	}
	outside := run("mkdir /root/.cache/go-build: operation not permitted", fmt.Errorf("exit status 1"))
	if len(outside.Denials) != 1 || outside.Denials[0].Kind != sandbox.DenialFilesystem || outside.Denials[0].Target != "/root/.cache/go-build" {
		t.Fatalf("filesystem denial = %+v", outside)
	}
	// A refusal inside the writable workspace is the code's problem, not the sandbox's.
	inside := run("chmod "+workspace+"/out: operation not permitted", fmt.Errorf("exit status 1"))
	if len(inside.Denials) != 0 {
		t.Fatalf("workspace permission error reported as a denial: %+v", inside.Denials)
	}
	disabled := &Registry{WorkspaceDir: workspace, SandboxDisabled: true, RunSandboxedOptions: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
		return "", "listen tcp: bind: operation not permitted", fmt.Errorf("exit status 1")
	}}
	if result, _ := disabled.RunVerificationCommand(context.Background(), "go test"); len(result.Denials) != 0 {
		t.Fatalf("unsandboxed run reported denials: %+v", result.Denials)
	}
}

func TestWithProtectedPathsMakesFilesReadOnly(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := (&Registry{WorkspaceDir: workspace}).WithSandboxConfig(sandbox.Config{})
	protected := base.WithProtectedPaths("evidence/verify.sh", "")
	path := filepath.Join(workspace, "evidence", "verify.sh")
	if ok, _ := protected.isPathWritable(path, workspace); ok {
		t.Fatal("protected path is writable")
	}
	if ok, _ := base.isPathWritable(path, workspace); !ok {
		t.Fatal("protection leaked into the parent registry")
	}
	if !sandbox.PathContains(protected.sandboxConfig.ReadOnlyPaths[len(protected.sandboxConfig.ReadOnlyPaths)-1], path) {
		t.Fatalf("sandbox read-only paths = %v", protected.sandboxConfig.ReadOnlyPaths)
	}
	if protected.executionState != nil {
		t.Fatal("protected registry reuses the parent's session shell")
	}
}

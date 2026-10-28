package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

func TestLiveChildPermissionsFollowParentGrants(t *testing.T) {
	parent := &Registry{WorkspaceDir: t.TempDir()}
	child := parent.WithLivePermissionsFrom(parent).WithoutSessionTools().ForNewTopLevelSession()
	sibling := parent.ForNewTopLevelSession()
	root := canonicalApprovalPath(t.TempDir())
	if err := parent.AddPermissionGrant(PermissionPathWrite, root, true); err != nil {
		t.Fatal(err)
	}
	if err := parent.AddPermissionGrant(PermissionNetwork, "example.com:443", false); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{PermissionPathRead, PermissionPathWrite} {
		if !child.permissionGranted(capability, root) || sibling.permissionGranted(capability, root) {
			t.Fatalf("incorrect %s inheritance", capability)
		}
	}
	if !child.permissionGranted(PermissionNetwork, "example.com:443") || sibling.permissionGranted(PermissionNetwork, "example.com:443") {
		t.Fatal("incorrect network grant inheritance")
	}
	child.RunSandboxedOptions = func(_ context.Context, _, _ string, opts sandbox.RunOptions) (string, string, error) {
		if !containsPath(opts.ReadableRoots, root) || !containsPath(opts.WritableRoots, root) || len(opts.NetworkAllowlist) != 1 || opts.NetworkAllowlist[0] != "example.com:443" {
			t.Errorf("command missed parent grants: %+v", opts)
		}
		return "ok", "", nil
	}
	if _, err := child.HandleToolCallContext(t.Context(), llm.ToolCall{ID: "child", Name: "Bash", Arguments: `{"command":"true"}`}); err != nil {
		t.Fatal(err)
	}
	childRoot := canonicalApprovalPath(t.TempDir())
	if err := child.AddPermissionGrant(PermissionPathRead, childRoot, true); err != nil {
		t.Fatal(err)
	}
	if parent.permissionGranted(PermissionPathRead, childRoot) {
		t.Fatal("child grant leaked into parent")
	}
}

func TestNetworkAuthorizerCancellationAndValues(t *testing.T) {
	for _, source := range []string{"tool", "request", "deadline"} {
		t.Run(source, func(t *testing.T) {
			toolCtx, cancelTool := context.WithCancel(t.Context())
			defer cancelTool()
			requestCtx, cancelRequest := context.WithCancel(t.Context())
			defer cancelRequest()
			if source == "deadline" {
				var cancel context.CancelFunc
				requestCtx, cancel = context.WithTimeout(requestCtx, 50*time.Millisecond)
				defer cancel()
			}
			started := make(chan struct{})
			toolCtx = WithPermissionRequester(toolCtx, func(ctx context.Context, _ llm.ToolCall, _ *PermissionError) (bool, error) {
				close(started)
				<-ctx.Done()
				return false, ctx.Err()
			})
			done := make(chan error, 1)
			go func() {
				_, err := networkAuthorizer(toolCtx, llm.ToolCall{}, "test")(requestCtx, "localhost:42")
				done <- err
			}()
			<-started
			if source == "tool" {
				cancelTool()
			}
			if source == "request" {
				cancelRequest()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("approval did not cancel")
			}
		})
	}
}

func TestNetworkPermissionPreservesTCPTarget(t *testing.T) {
	ctx := WithPermissionGrant(context.Background(), PermissionNetwork, "tcp://Example.COM.:22", false)
	if !permissionGranted(ctx, PermissionNetwork, "tcp://example.com:22") {
		t.Fatal("canonical TCP permission was not granted")
	}
	if permissionGranted(ctx, PermissionNetwork, "example.com:22") {
		t.Fatal("TCP permission also granted HTTP target")
	}
	if got := permissionTargets(ctx, PermissionNetwork); len(got) != 1 || got[0] != "tcp://example.com:22" {
		t.Fatalf("network permission targets = %#v", got)
	}
}

func TestOutsidePathPermissionGrantIsOneContextAndExact(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	path := filepath.Join(outside, "allowed.txt")
	if err := os.WriteFile(path, []byte("allowed"), 0600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(outside, "other.txt")
	if err := os.WriteFile(other, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := &Registry{WorkspaceDir: workspace}
	call := llm.ToolCall{ID: "read-1", Name: "Read", Arguments: `{"file_path":"` + path + `"}`}
	_, err := registry.HandleToolCallContext(context.Background(), call)
	denial, ok := AsPermissionError(err)
	if !ok || denial.Target != canonicalApprovalPath(path) || denial.Capability != PermissionPathRead {
		t.Fatalf("denial = %#v, %v", denial, err)
	}
	ctx := WithPermissionGrant(context.Background(), denial.Capability, denial.Target, denial.Recursive)
	if _, err := registry.HandleToolCallContext(ctx, call); err != nil {
		t.Fatalf("approved read failed: %v", err)
	}
	otherCall := llm.ToolCall{ID: "read-2", Name: "Read", Arguments: `{"file_path":"` + other + `"}`}
	if _, err := registry.HandleToolCallContext(ctx, otherCall); err == nil {
		t.Fatal("exact-file grant allowed a sibling")
	}
	if _, err := registry.HandleToolCallContext(context.Background(), call); err == nil {
		t.Fatal("grant escaped its context")
	}
}

func TestOutsidePathGrantDoesNotOverrideSymlinkOrHiddenPath(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	realPath := filepath.Join(outside, "real.txt")
	if err := os.WriteFile(realPath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(outside, "link.txt")
	if err := os.Symlink(realPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	registry := &Registry{WorkspaceDir: workspace}
	call := llm.ToolCall{ID: "read-link", Name: "Read", Arguments: `{"file_path":"` + symlinkPath + `"}`}
	_, err := registry.HandleToolCallContext(context.Background(), call)
	denial, ok := AsPermissionError(err)
	if !ok {
		t.Fatalf("initial denial = %v", err)
	}
	ctx := WithPermissionGrant(context.Background(), denial.Capability, denial.Target, denial.Recursive)
	if _, err := registry.HandleToolCallContext(ctx, call); err == nil || !containsString(err.Error(), "symlink") {
		t.Fatalf("approved symlink error = %v", err)
	}

	hidden := filepath.Join(workspace, ".caretz")
	if err := os.MkdirAll(hidden, 0700); err != nil {
		t.Fatal(err)
	}
	hiddenFile := filepath.Join(hidden, "config.json")
	if err := os.WriteFile(hiddenFile, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	registry.HiddenPaths = []string{hidden}
	hiddenCall := llm.ToolCall{ID: "read-hidden", Name: "Read", Arguments: `{"file_path":"` + hiddenFile + `"}`}
	if _, err := registry.HandleToolCallContext(context.Background(), hiddenCall); err == nil {
		t.Fatal("hidden path was readable")
	} else if _, approvable := AsPermissionError(err); approvable {
		t.Fatalf("hidden path was approvable: %v", err)
	}
}

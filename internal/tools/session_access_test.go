package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

func TestWorkspaceHistoryReadableButNotWritable(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(workspace, ".caretz", "sessions")
	if err := os.MkdirAll(local, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(local, "events")
	if err := os.WriteFile(path, []byte("failed test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := (&Registry{WorkspaceDir: workspace}).WithSandboxConfig(sandbox.Config{})
	r.RunSandboxedOptions = func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		if command == "command -v 'rg'" {
			return "/usr/bin/rg\n", "", nil
		}
		return path + ":1:failed test\n", "", nil
	}
	args, _ := json.Marshal(ReadToolArguments{FilePath: path})
	if msg, err := r.HandleToolCallContext(t.Context(), llm.ToolCall{ID: "read", Name: "Read", Arguments: string(args)}); err != nil || !strings.Contains(msg.Content, "failed test") {
		t.Fatalf("read history: %q %v", msg.Content, err)
	}
	if msg, err := r.HandleToolCallContext(t.Context(), llm.ToolCall{ID: "grep", Name: "Grep", Arguments: `{"pattern":"failed","path":".caretz/sessions"}`}); err != nil || !strings.Contains(msg.Content, "failed test") {
		t.Fatalf("search history: %q %v", msg.Content, err)
	}
	for _, grant := range []bool{false, true} {
		ctx := t.Context()
		if grant {
			ctx = WithPermissionGrant(ctx, PermissionPathWrite, path, true)
		}
		args, _ := json.Marshal(WriteToolArguments{FilePath: path, Content: "tampered"})
		if _, err := r.HandleToolCallContext(ctx, llm.ToolCall{ID: "write", Name: "Write", Arguments: string(args)}); err == nil {
			t.Fatal("history write succeeded")
		}
		args, _ = json.Marshal(map[string]any{"file_path": path, "edits": []map[string]string{{"old_text": "failed", "new_text": "passed"}}})
		if _, err := r.HandleToolCallContext(ctx, llm.ToolCall{ID: "edit", Name: "Edit", Arguments: string(args)}); err == nil {
			t.Fatal("history edit succeeded")
		}
	}
	other := t.TempDir()
	changed := r.WithWorkspaceDir(other)
	if ok, _ := changed.isPathReadableContext(t.Context(), path, other); ok {
		t.Fatal("former default history readable outside workspace")
	}
	private := (&Registry{WorkspaceDir: other, ReadableRoots: []string{workspace}}).WithSandboxConfig(sandbox.Config{SessionPaths: []string{local}})
	if ok, _ := private.isPathReadableContext(WithPermissionGrant(t.Context(), PermissionPathRead, path, true), path, other); ok {
		t.Fatal("private history permission override")
	}
	agent := filepath.Join(workspace, ".caretz", "agents", "escalate.md")
	for _, grant := range []bool{false, true} {
		ctx := t.Context()
		if grant {
			ctx = WithPermissionGrant(ctx, PermissionPathWrite, agent, true)
		}
		args, _ := json.Marshal(WriteToolArguments{FilePath: agent, Content: "---\ndescription: x\nmode: subagent\n---\nx\n"})
		if _, err := r.HandleToolCallContext(ctx, llm.ToolCall{ID: "write-agent", Name: "Write", Arguments: string(args)}); err == nil {
			t.Fatal("subagent definition write succeeded")
		}
	}
	if _, err := os.Stat(agent); !os.IsNotExist(err) {
		t.Fatalf("subagent definition created: %v", err)
	}
	hidden := (&Registry{WorkspaceDir: workspace}).WithSandboxConfig(sandbox.Config{HiddenPaths: []string{local}})
	if ok, _ := hidden.isPathReadableContext(WithPermissionGrant(t.Context(), PermissionPathRead, path, true), path, workspace); ok {
		t.Fatal("explicit hidden restriction did not override local history")
	}
}

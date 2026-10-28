package caretz

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/makkalot/caretz/lib/llm"
)

// An adapter session with its own WorkspaceDir must not inherit the client
// workspace as a root: that exposes the client workspace and every sibling
// session's workspace below it.
func TestSessionWorkspaceConfinesFileTools(t *testing.T) {
	runtime, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chatA := filepath.Join(runtime, "workspaces", "telegram", "a")
	chatB := filepath.Join(runtime, "workspaces", "telegram", "b")
	shared, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{chatA, chatB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(runtime, ".env"):     "TOKEN=secret",
		filepath.Join(chatA, "notes.txt"):  "chat a",
		filepath.Join(chatB, "notes.txt"):  "chat b",
		filepath.Join(shared, "notes.txt"): "shared",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	call := func(registryOwner *Session, name string, args map[string]any) error {
		t.Helper()
		data, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		_, err = registryOwner.inner.ToolRegistry().HandleToolCall(llm.ToolCall{ID: "call", Name: name, Arguments: string(data)})
		return err
	}

	client, err := NewClient(t.Context(), ClientOptions{WorkspaceDir: runtime})
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession("a", SessionOptions{Adapter: "telegram", WorkspaceDir: chatA})
	if err != nil {
		t.Fatal(err)
	}
	if err := call(session, "Read", map[string]any{"file_path": filepath.Join(chatA, "notes.txt")}); err != nil {
		t.Fatalf("own workspace read: %v", err)
	}
	if err := call(session, "Write", map[string]any{"file_path": filepath.Join(chatA, "out.txt"), "content": "x"}); err != nil {
		t.Fatalf("own workspace write: %v", err)
	}
	if err := call(session, "Read", map[string]any{"file_path": filepath.Join(runtime, ".env")}); err == nil {
		t.Fatal("session read the client workspace root")
	}
	if err := call(session, "Read", map[string]any{"file_path": filepath.Join(chatB, "notes.txt")}); err == nil {
		t.Fatal("session read a sibling session's workspace")
	}
	if err := call(session, "Write", map[string]any{"file_path": filepath.Join(chatB, "marker"), "content": "x"}); err == nil {
		t.Fatal("session wrote a sibling session's workspace")
	}
	registry := session.inner.ToolRegistry()
	for _, root := range append(append([]string(nil), registry.ReadableRoots...), registry.WritableRoots...) {
		if root == runtime {
			t.Fatalf("session roots include the client workspace: readable=%v writable=%v", registry.ReadableRoots, registry.WritableRoots)
		}
	}

	// A session in the client workspace keeps full access to it.
	local, err := client.NewSession("local", SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := call(local, "Read", map[string]any{"file_path": filepath.Join(runtime, ".env")}); err != nil {
		t.Fatalf("client workspace session read: %v", err)
	}

	// Explicitly configured roots still apply to every session.
	client, err = NewClient(t.Context(), ClientOptions{
		WorkspaceDir: runtime,
		Sandbox:      SandboxOptions{ReadableRoots: []string{shared}},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err = client.NewSession("a", SessionOptions{Adapter: "telegram", WorkspaceDir: chatA})
	if err != nil {
		t.Fatal(err)
	}
	if err := call(session, "Read", map[string]any{"file_path": filepath.Join(shared, "notes.txt")}); err != nil {
		t.Fatalf("explicit readable root: %v", err)
	}
	if err := call(session, "Read", map[string]any{"file_path": filepath.Join(runtime, ".env")}); err == nil {
		t.Fatal("explicit roots re-exposed the client workspace")
	}
}

func TestEmbeddingSessionAccess(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, local := range []bool{true, false} {
		state := filepath.Join(workspace, "history")
		if !local {
			state, err = filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
		}
		client, err := NewClient(t.Context(), ClientOptions{
			WorkspaceDir: workspace,
			State:        StateOptions{Dir: state},
			Sandbox:      SandboxOptions{ReadOnlyPaths: []string{filepath.Join(workspace, "reference")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		history := filepath.Join(state, "sessions")
		if slices.Contains(client.toolRegistry.ReadOnlyPaths, history) != local || slices.Contains(client.toolRegistry.HiddenPaths, history) == local {
			t.Fatalf("embedding policy: hidden=%v readonly=%v", client.toolRegistry.HiddenPaths, client.toolRegistry.ReadOnlyPaths)
		}
		if !slices.Contains(client.toolRegistry.ReadOnlyPaths, filepath.Join(workspace, "reference")) {
			t.Fatal("explicit embedding read-only restriction lost")
		}
		if _, err := client.NewSession("history-policy", SessionOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(history, "lib", "history-policy")); err != nil {
			t.Fatalf("host persistence: %v", err)
		}
		changed := client.toolRegistry.WithWorkspaceDir(state)
		if !slices.Contains(changed.ReadOnlyPaths, history) || slices.Contains(changed.HiddenPaths, history) {
			t.Fatal("workspace switch did not reclassify embedding storage")
		}
	}
}

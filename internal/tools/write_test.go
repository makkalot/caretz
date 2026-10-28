package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/llm"
)

func makeToolCall(name, id string, args map[string]any) llm.ToolCall {
	argsJSON, _ := json.Marshal(args)
	return llm.ToolCall{
		ID:        id,
		Name:      name,
		Arguments: string(argsJSON),
	}
}

func extractToolMessageText(msg llm.Message) string {
	if msg.Role != llm.RoleTool {
		return ""
	}
	return msg.Content
}

func newTestRegistry() *Registry {
	return &Registry{
		RunSandboxed: func(cwd, command string) (string, string, error) {
			return "", "", fmt.Errorf("RunSandboxed not implemented in test")
		},
	}
}

func TestRegistryWithWorkspaceDirClonesConfiguration(t *testing.T) {
	registry := &Registry{
		ReadableRoots:     []string{"/read"},
		WritableRoots:     []string{"/write"},
		DisabledTools:     map[string]bool{"Edit": true},
		WorkspaceDir:      "/old",
		TavilyAPIKey:      "tavily",
		BraveSearchAPIKey: "brave",
	}

	clone := registry.WithWorkspaceDir("/new")
	if clone == registry {
		t.Fatal("expected clone to be a different registry")
	}
	if clone.WorkspaceDir != "/new" {
		t.Fatalf("WorkspaceDir = %q, want /new", clone.WorkspaceDir)
	}
	clone.ReadableRoots[0] = "/changed"
	clone.WritableRoots[0] = "/changed"
	clone.DisabledTools["Edit"] = false
	if registry.ReadableRoots[0] != "/read" || registry.WritableRoots[0] != "/write" || !registry.DisabledTools["Edit"] {
		t.Fatalf("clone mutated original registry")
	}
}

func TestRegistryCloneHelpersPreserveWorkspaceAndRoots(t *testing.T) {
	registry := (&Registry{
		WorkspaceDir:  "/workspace",
		ReadableRoots: []string{"/read"},
	}).WithReadableRoots("/uploads").WithDisabledTools(map[string]bool{"Write": true})

	if registry.WorkspaceDir != "/workspace" {
		t.Fatalf("WorkspaceDir = %q, want /workspace", registry.WorkspaceDir)
	}
	if fmt.Sprint(registry.ReadableRoots) != fmt.Sprint([]string{"/read", "/uploads"}) {
		t.Fatalf("ReadableRoots = %#v, want /read and /uploads", registry.ReadableRoots)
	}
	if !registry.DisabledTools["Write"] {
		t.Fatalf("expected Write to be disabled")
	}
}

func TestHandleWriteToolCallUsesWorkspaceDirForRelativePath(t *testing.T) {
	workspaceDir := t.TempDir()
	workspaceDir, err := filepath.EvalSymlinks(workspaceDir)
	if err != nil {
		t.Fatalf("resolving workspace dir: %v", err)
	}
	registry := newTestRegistry().WithWorkspaceDir(workspaceDir)

	toolCall := makeToolCall("Write", "w-workspace", map[string]any{
		"file_path": "nested/file.txt",
		"content":   "workspace content",
	})

	_, err = registry.HandleToolCall(toolCall)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(workspaceDir, "nested", "file.txt"))
	if err != nil {
		t.Fatalf("reading workspace file: %v", err)
	}
	if string(content) != "workspace content" {
		t.Fatalf("content = %q, want workspace content", string(content))
	}
}

func TestHandleWriteToolCall(t *testing.T) {
	origCwd, _ := os.Getwd()
	registry := newTestRegistry()

	t.Run("create new file", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolvedTmpDir, _ := filepath.EvalSymlinks(tmpDir)
		origCwd, _ := os.Getwd()
		if err := os.Chdir(resolvedTmpDir); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		defer func() { _ = os.Chdir(origCwd) }()

		filePath := filepath.Join(resolvedTmpDir, "new.txt")
		toolCall := makeToolCall("Write", "w-1", map[string]any{
			"file_path": filePath,
			"content":   "hello world",
		})

		msg, err := registry.HandleToolCall(toolCall)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		content, _ := os.ReadFile(filePath)
		if string(content) != "hello world" {
			t.Errorf("file content = %q, want %q", string(content), "hello world")
		}

		result := extractToolMessageText(msg)
		if !strings.Contains(result, "bytes written") {
			t.Errorf("result should confirm bytes written, got: %s", result)
		}
	})

	t.Run("overwrite existing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolvedTmpDir, _ := filepath.EvalSymlinks(tmpDir)
		origCwd, _ := os.Getwd()
		if err := os.Chdir(resolvedTmpDir); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		defer func() { _ = os.Chdir(origCwd) }()

		filePath := filepath.Join(resolvedTmpDir, "existing.txt")
		if err := os.WriteFile(filePath, []byte("old content"), 0644); err != nil {
			t.Fatal(err)
		}

		toolCall := makeToolCall("Write", "w-2", map[string]any{
			"file_path": filePath,
			"content":   "new content",
		})

		_, err := registry.HandleToolCall(toolCall)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		content, _ := os.ReadFile(filePath)
		if string(content) != "new content" {
			t.Errorf("file content = %q, want %q", string(content), "new content")
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolvedTmpDir, _ := filepath.EvalSymlinks(tmpDir)
		origCwd, _ := os.Getwd()
		if err := os.Chdir(resolvedTmpDir); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		defer func() { _ = os.Chdir(origCwd) }()

		filePath := filepath.Join(resolvedTmpDir, "sub", "dir", "file.txt")
		toolCall := makeToolCall("Write", "w-3", map[string]any{
			"file_path": filePath,
			"content":   "nested",
		})

		_, err := registry.HandleToolCall(toolCall)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		content, _ := os.ReadFile(filePath)
		if string(content) != "nested" {
			t.Errorf("file content = %q, want %q", string(content), "nested")
		}
	})

	t.Run("path safety rejection", func(t *testing.T) {
		toolCall := makeToolCall("Write", "w-4", map[string]any{
			"file_path": "/etc/hacked",
			"content":   "malicious",
		})

		_, err := registry.HandleToolCall(toolCall)
		if err == nil {
			t.Fatal("expected error for unsafe path")
		}
		if !strings.Contains(err.Error(), "access denied") {
			t.Errorf("error should mention access denied, got: %v", err)
		}
	})

	t.Run("empty content writes empty file", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolvedTmpDir, _ := filepath.EvalSymlinks(tmpDir)
		origCwd, _ := os.Getwd()
		if err := os.Chdir(resolvedTmpDir); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		defer func() { _ = os.Chdir(origCwd) }()

		filePath := filepath.Join(resolvedTmpDir, "empty.txt")
		toolCall := makeToolCall("Write", "w-5", map[string]any{
			"file_path": filePath,
			"content":   "",
		})

		_, err := registry.HandleToolCall(toolCall)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		content, _ := os.ReadFile(filePath)
		if len(content) != 0 {
			t.Errorf("expected empty file, got %d bytes", len(content))
		}
	})

	_ = origCwd
}

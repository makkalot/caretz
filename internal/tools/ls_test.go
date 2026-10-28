package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chdirTempRoot(t *testing.T, tmpDir string) string {
	t.Helper()
	resolvedTmpDir, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(resolvedTmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	return resolvedTmpDir
}

func TestHandleLsToolCall(t *testing.T) {
	registry := newTestRegistry()

	t.Run("list directory with files and subdirs", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolvedTmpDir := chdirTempRoot(t, tmpDir)

		if err := os.WriteFile(filepath.Join(resolvedTmpDir, "afile.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(resolvedTmpDir, "bfile.go"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(resolvedTmpDir, "cdir"), 0755); err != nil {
			t.Fatal(err)
		}

		toolCall := makeToolCall("Ls", "ls-1", map[string]any{
			"path": resolvedTmpDir,
		})

		msg, err := registry.HandleToolCall(toolCall)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result := extractToolMessageText(msg)
		if !strings.Contains(result, "afile.txt") {
			t.Error("missing afile.txt")
		}
		if !strings.Contains(result, "bfile.go") {
			t.Error("missing bfile.go")
		}
		if !strings.Contains(result, "cdir/") {
			t.Error("directory should have / suffix")
		}
		if strings.Contains(result, "afile.txt/") {
			t.Error("file should not have / suffix")
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolvedTmpDir := chdirTempRoot(t, tmpDir)

		emptyDir := filepath.Join(resolvedTmpDir, "empty")
		if err := os.MkdirAll(emptyDir, 0755); err != nil {
			t.Fatal(err)
		}

		toolCall := makeToolCall("Ls", "ls-2", map[string]any{
			"path": emptyDir,
		})

		msg, err := registry.HandleToolCall(toolCall)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result := extractToolMessageText(msg)
		if !strings.Contains(result, "(empty directory)") {
			t.Errorf("expected empty directory message, got: %s", result)
		}
	})

	t.Run("limit enforcement", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolvedTmpDir := chdirTempRoot(t, tmpDir)

		limitDir := filepath.Join(resolvedTmpDir, "many")
		if err := os.MkdirAll(limitDir, 0755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			if err := os.WriteFile(filepath.Join(limitDir, fmt.Sprintf("file%02d.txt", i)), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}

		toolCall := makeToolCall("Ls", "ls-3", map[string]any{
			"path":  limitDir,
			"limit": 5,
		})

		msg, err := registry.HandleToolCall(toolCall)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result := extractToolMessageText(msg)
		if !strings.Contains(result, "limit reached") {
			t.Errorf("expected limit reached notice, got: %s", result)
		}
		if !strings.Contains(result, "20 total") {
			t.Errorf("notice should mention total count, got: %s", result)
		}
	})

	t.Run("path safety rejection", func(t *testing.T) {
		toolCall := makeToolCall("Ls", "ls-4", map[string]any{
			"path": "/etc",
		})

		_, err := registry.HandleToolCall(toolCall)
		if err == nil {
			t.Fatal("expected error for unsafe path")
		}
		if !strings.Contains(err.Error(), "access denied") {
			t.Errorf("error should mention access denied, got: %v", err)
		}
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		resolvedTmpDir := chdirTempRoot(t, tmpDir)

		toolCall := makeToolCall("Ls", "ls-5", map[string]any{
			"path": filepath.Join(resolvedTmpDir, "does_not_exist"),
		})

		_, err := registry.HandleToolCall(toolCall)
		if err == nil {
			t.Fatal("expected error for nonexistent directory")
		}
		if !strings.Contains(err.Error(), "listing directory") {
			t.Errorf("error should mention listing directory, got: %v", err)
		}
	})
}

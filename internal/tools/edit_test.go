package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleEditToolCall(t *testing.T) {
	registry := newTestRegistry()

	t.Run("successful single edit", func(t *testing.T) {
		tmpDir := t.TempDir()
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
		defer func() { _ = os.Chdir(origCwd) }()

		filePath := filepath.Join(resolvedTmpDir, "test.txt")
		if err := os.WriteFile(filePath, []byte("hello world\nfoo bar\nbaz qux"), 0644); err != nil {
			t.Fatal(err)
		}

		toolCall := makeToolCall("Edit", "edit-1", map[string]any{
			"file_path": filePath,
			"edits": []map[string]any{
				{"old_text": "foo bar", "new_text": "FOO BAR"},
			},
		})

		msg, err := registry.HandleToolCall(toolCall)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		content, _ := os.ReadFile(filePath)
		if !strings.Contains(string(content), "FOO BAR") {
			t.Error("file should contain FOO BAR")
		}
		if strings.Contains(string(content), "foo bar") {
			t.Error("file should not contain original foo bar")
		}

		result := extractToolMessageText(msg)
		if !strings.Contains(result, "Successfully replaced 1 block(s)") {
			t.Errorf("result should confirm edit count, got: %s", result)
		}
		if !strings.Contains(result, "@@") {
			t.Error("result should contain diff")
		}
	})

	t.Run("file not found", func(t *testing.T) {
		tmpDir := t.TempDir()
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
		defer func() { _ = os.Chdir(origCwd) }()

		nonexistentPath := filepath.Join(resolvedTmpDir, "nonexistent.txt")
		toolCall := makeToolCall("Edit", "edit-2", map[string]any{
			"file_path": nonexistentPath,
			"edits": []map[string]any{
				{"old_text": "x", "new_text": "y"},
			},
		})

		_, err = registry.HandleToolCall(toolCall)
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error should mention not found, got: %v", err)
		}
	})

	t.Run("path safety rejection", func(t *testing.T) {
		toolCall := makeToolCall("Edit", "edit-3", map[string]any{
			"file_path": "/etc/passwd",
			"edits": []map[string]any{
				{"old_text": "x", "new_text": "y"},
			},
		})

		_, err := registry.HandleToolCall(toolCall)
		if err == nil {
			t.Fatal("expected error for unsafe path")
		}
		if !strings.Contains(err.Error(), "access denied") {
			t.Errorf("error should mention access denied, got: %v", err)
		}
	})

	t.Run("empty edits array", func(t *testing.T) {
		tmpDir := t.TempDir()
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
		defer func() { _ = os.Chdir(origCwd) }()

		filePath := filepath.Join(resolvedTmpDir, "test2.txt")
		if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}

		toolCall := makeToolCall("Edit", "edit-4", map[string]any{
			"file_path": filePath,
			"edits":     []map[string]any{},
		})

		_, err = registry.HandleToolCall(toolCall)
		if err == nil {
			t.Fatal("expected error for empty edits")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("error should mention empty, got: %v", err)
		}
	})

	t.Run("edit not found in file", func(t *testing.T) {
		tmpDir := t.TempDir()
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
		defer func() { _ = os.Chdir(origCwd) }()

		filePath := filepath.Join(resolvedTmpDir, "test3.txt")
		if err := os.WriteFile(filePath, []byte("hello world"), 0644); err != nil {
			t.Fatal(err)
		}

		toolCall := makeToolCall("Edit", "edit-5", map[string]any{
			"file_path": filePath,
			"edits": []map[string]any{
				{"old_text": "nonexistent text", "new_text": "replacement"},
			},
		})

		_, err = registry.HandleToolCall(toolCall)
		if err == nil {
			t.Fatal("expected error for old_text not found")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error should mention not found, got: %v", err)
		}
	})
}

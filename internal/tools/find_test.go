package tools

import "testing"

func TestHandleReadToolCall_PathSafety(t *testing.T) {
	registry := newTestRegistry()

	t.Run("path safety rejection", func(t *testing.T) {
		toolCall := makeToolCall("Read", "r-1", map[string]any{
			"file_path": "/etc/passwd",
		})

		_, err := registry.HandleToolCall(toolCall)
		if err == nil {
			t.Fatal("expected error for unsafe path")
		}
		if !containsString(err.Error(), "access denied") {
			t.Errorf("error should mention access denied, got: %v", err)
		}
	})
}

func TestHandleBashToolCall_EmptyCommand(t *testing.T) {
	registry := newTestRegistry()

	toolCall := makeToolCall("Bash", "b-1", map[string]any{
		"command": "",
	})

	_, err := registry.HandleToolCall(toolCall)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !containsString(err.Error(), "empty command") {
		t.Errorf("error should mention empty command, got: %v", err)
	}
}

func TestHandleGrepToolCall_NoPattern(t *testing.T) {
	registry := newTestRegistry()

	toolCall := makeToolCall("Grep", "g-1", map[string]any{
		"pattern": "",
	})

	_, err := registry.HandleToolCall(toolCall)
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
	if !containsString(err.Error(), "pattern is required") {
		t.Errorf("error should mention pattern is required, got: %v", err)
	}
}

func TestHandleFindToolCall_NoPattern(t *testing.T) {
	registry := newTestRegistry()

	toolCall := makeToolCall("Find", "f-1", map[string]any{
		"pattern": "",
	})

	_, err := registry.HandleToolCall(toolCall)
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
	if !containsString(err.Error(), "pattern is required") {
		t.Errorf("error should mention pattern is required, got: %v", err)
	}
}

func TestHandleToolCall_InvalidTool(t *testing.T) {
	registry := newTestRegistry()

	toolCall := makeToolCall("NonexistentTool", "x-1", map[string]any{})
	_, err := registry.HandleToolCall(toolCall)
	if err == nil {
		t.Fatal("expected error for invalid tool")
	}
	if !containsString(err.Error(), "invalid tool") {
		t.Errorf("error should mention invalid tool, got: %v", err)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

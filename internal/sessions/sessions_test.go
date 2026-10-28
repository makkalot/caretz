package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
)

func closeTestFile(t *testing.T, f *os.File) {
	t.Helper()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestList_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	result, err := List(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(result))
	}
}

func TestList_SingleSession(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a CLI session with a few events
	sessionDir := filepath.Join(tmpDir, "cli", "sess-abc")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	f, err := os.Create(eventsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Write two events: user message + agent response
	writeEvent(t, f, eventing.LogMessageUserCreated, map[string]string{"content": "hello world"})
	writeEvent(t, f, eventing.LogMessageAgentCreated, map[string]string{"content": "hi there"})
	closeTestFile(t, f)

	result, err := List(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 session, got %d", len(result))
	}

	s := result[0]
	if s.ID != "sess-abc" {
		t.Errorf("expected ID sess-abc, got %s", s.ID)
	}
	if s.AdapterType != "cli" {
		t.Errorf("expected adapter cli, got %s", s.AdapterType)
	}
	if s.EventCount != 2 {
		t.Errorf("expected event count 2, got %d", s.EventCount)
	}
	if s.MessageCount != 1 {
		t.Errorf("expected message count 1, got %d", s.MessageCount)
	}
	if s.FirstPrompt != "hello world" {
		t.Errorf("expected first prompt 'hello world', got %q", s.FirstPrompt)
	}
}

func TestList_TelegramSession(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "telegram", "123")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	f, err := os.Create(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	writeEvent(t, f, eventing.LogMessageUserCreated, map[string]string{"content": "hello telegram"})
	closeTestFile(t, f)

	result, err := List(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 session, got %d", len(result))
	}
	if result[0].AdapterType != "telegram" || result[0].ID != "123" {
		t.Fatalf("session = %#v, want telegram/123", result[0])
	}
}

func TestList_MultipleSessionsSorted(t *testing.T) {
	tmpDir := t.TempDir()

	// Create older session
	sessionDir1 := filepath.Join(tmpDir, "cli", "sess-old")
	if err := os.MkdirAll(sessionDir1, 0755); err != nil {
		t.Fatal(err)
	}
	eventsPath1 := filepath.Join(sessionDir1, "events.jsonpl")
	f1, err := os.Create(eventsPath1)
	if err != nil {
		t.Fatal(err)
	}
	writeEvent(t, f1, eventing.LogMessageUserCreated, map[string]string{"content": "old prompt"})
	closeTestFile(t, f1)
	// Touch file to an older time
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(eventsPath1, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Create newer session
	sessionDir2 := filepath.Join(tmpDir, "tui", "sess-new")
	if err := os.MkdirAll(sessionDir2, 0755); err != nil {
		t.Fatal(err)
	}
	eventsPath2 := filepath.Join(sessionDir2, "events.jsonpl")
	f2, err := os.Create(eventsPath2)
	if err != nil {
		t.Fatal(err)
	}
	writeEvent(t, f2, eventing.LogMessageUserCreated, map[string]string{"content": "new prompt"})
	closeTestFile(t, f2)

	result, err := List(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(result))
	}

	// Should be sorted by LastModified descending: new first, then old
	if result[0].ID != "sess-new" {
		t.Errorf("expected first session to be sess-new, got %s", result[0].ID)
	}
	if result[1].ID != "sess-old" {
		t.Errorf("expected second session to be sess-old, got %s", result[1].ID)
	}
}

func TestList_TruncatesLongPrompt(t *testing.T) {
	tmpDir := t.TempDir()

	sessionDir := filepath.Join(tmpDir, "cli", "sess-long")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	f, err := os.Create(eventsPath)
	if err != nil {
		t.Fatal(err)
	}

	longPrompt := "a very long prompt that should definitely be truncated because it exceeds sixty characters"
	writeEvent(t, f, eventing.LogMessageUserCreated, map[string]string{"content": longPrompt})
	closeTestFile(t, f)

	result, err := List(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 session, got %d", len(result))
	}

	if len(result[0].FirstPrompt) > 63 {
		t.Errorf("expected truncated prompt, got length %d", len(result[0].FirstPrompt))
	}
}

func TestList_SkipsCorruptedEventLine(t *testing.T) {
	tmpDir := t.TempDir()

	sessionDir := filepath.Join(tmpDir, "cli", "sess-corrupt")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	f, err := os.Create(eventsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Valid event
	writeEvent(t, f, eventing.LogMessageUserCreated, map[string]string{"content": "valid"})
	// Corrupted line
	if _, err := f.WriteString("this is not json\n"); err != nil {
		t.Fatal(err)
	}
	// Another valid event
	writeEvent(t, f, eventing.LogMessageAgentCreated, map[string]string{"content": "ok"})
	closeTestFile(t, f)

	result, err := List(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 session, got %d", len(result))
	}
	if result[0].EventCount != 2 {
		t.Errorf("expected 2 valid events, got %d", result[0].EventCount)
	}
}

func TestList_HidesSubagentSessionsByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	parentDir := filepath.Join(tmpDir, "tui", "parent")
	childDir := filepath.Join(tmpDir, "tui", "parent-subagent-review-123")
	for _, dir := range []string{parentDir, childDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	parentEvents, err := os.Create(filepath.Join(parentDir, "events.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	writeEvent(t, parentEvents, eventing.LogMessageUserCreated, map[string]string{"content": "review this"})
	writeSubagentEvent(t, parentEvents, eventing.SubagentInvocationStarted, eventing.SubagentInvocation{
		Name:            "review",
		ChildSessionID:  "parent-subagent-review-123",
		ParentSessionID: "parent",
	})
	closeTestFile(t, parentEvents)

	childEvents, err := os.Create(filepath.Join(childDir, "events.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	writeEvent(t, childEvents, eventing.LogMessageUserCreated, map[string]string{"content": "subagent task"})
	closeTestFile(t, childEvents)

	result, err := List(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].ID != "parent" {
		t.Fatalf("expected only parent session, got %#v", result)
	}

	result, err = ListWithOptions(tmpDir, ListOptions{IncludeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("expected parent and subagent sessions, got %d", len(result))
	}
}

func writeEvent(t *testing.T, f *os.File, eventType string, payload map[string]string) {
	t.Helper()
	payloadBytes, _ := json.Marshal(payload)
	e := storedFileEvent{
		OriginatorID:      "test-id",
		OriginatorVersion: 1,
		EventType:         eventType,
		Payload:           json.RawMessage(payloadBytes),
		CreatedAt:         time.Now().Unix(),
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(string(b) + "\n"); err != nil {
		t.Fatal(err)
	}
}

func writeSubagentEvent(t *testing.T, f *os.File, eventType string, payload eventing.SubagentInvocation) {
	t.Helper()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	e := storedFileEvent{
		OriginatorID:      "test-id",
		OriginatorVersion: 1,
		EventType:         eventType,
		Payload:           json.RawMessage(payloadBytes),
		CreatedAt:         time.Now().Unix(),
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(string(b) + "\n"); err != nil {
		t.Fatal(err)
	}
}

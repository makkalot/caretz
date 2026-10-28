package explorer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

func writeStoredEvent(t *testing.T, file *os.File, event *types.Event) {
	t.Helper()
	line, err := json.Marshal(toStoredEvent(event))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(string(line) + "\n"); err != nil {
		t.Fatal(err)
	}
}

func closeTestFile(t *testing.T, file *os.File) {
	t.Helper()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadSessionEvents(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "cli", "test123")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Unix()

	// Write events log.
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	eventsFile, err := os.Create(eventsPath)
	if err != nil {
		t.Fatal(err)
	}

	userEvent := eventing.NewLogMessage("test123", "hello world")
	userEvent.OccurredOn = time.Unix(now, 0).UTC()
	writeStoredEvent(t, eventsFile, userEvent)

	agentEvent := eventing.NewAgentLogMessage("test123", "hi there")
	agentEvent.OccurredOn = time.Unix(now+1, 0).UTC()
	writeStoredEvent(t, eventsFile, agentEvent)
	closeTestFile(t, eventsFile)

	// Write context log.
	contextPath := filepath.Join(sessionDir, "context.jsonpl")
	contextFile, err := os.Create(contextPath)
	if err != nil {
		t.Fatal(err)
	}

	userMsg := eventing.NewLLMUserMessage("test123", "events-id", 1, llm.Message{Role: llm.RoleUser, Content: "hello world"})
	userMsg.OccurredOn = time.Unix(now, 0).UTC()
	writeStoredEvent(t, contextFile, userMsg)

	agentMsg := eventing.NewLLMAgentMessage("test123", "events-id", 1, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   "thinking...",
		ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Bash", Arguments: `{"command": "ls"}`}},
	}, llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, 15)
	agentMsg.OccurredOn = time.Unix(now+2, 0).UTC()
	writeStoredEvent(t, contextFile, agentMsg)

	toolResult := eventing.NewLLMToolCallResult("test123", "events-id", 1, llm.ToolCall{ID: "tc1", Name: "Bash", Arguments: `{"command": "ls"}`}, "", "", llm.Message{Role: llm.RoleTool, Content: "file1\nfile2", ToolCallID: "tc1"})
	toolResult.OccurredOn = time.Unix(now+3, 0).UTC()
	writeStoredEvent(t, contextFile, toolResult)

	agentResponse := eventing.NewLLMAgentResponse("test123", "events-id", 1, llm.Message{Role: llm.RoleAssistant, Content: "hi there"}, llm.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}, 45)
	agentResponse.OccurredOn = time.Unix(now+4, 0).UTC()
	writeStoredEvent(t, contextFile, agentResponse)
	closeTestFile(t, contextFile)

	// Read and verify.
	evts, err := ReadSessionEvents(sessionDir)
	if err != nil {
		t.Fatalf("ReadSessionEvents failed: %v", err)
	}

	if len(evts) != 6 {
		t.Fatalf("expected 6 events, got %d", len(evts))
	}

	// Check ordering.
	for i := 1; i < len(evts); i++ {
		if evts[i].CreatedAt.Before(evts[i-1].CreatedAt) {
			t.Fatalf("events not sorted: %v before %v", evts[i-1].CreatedAt, evts[i].CreatedAt)
		}
	}

	// Check depths.
	if evts[2].Type == eventing.LLMUserMessageCreated && evts[2].Depth != 0 {
		t.Fatalf("expected user msg depth 0, got %d", evts[2].Depth)
	}
	if evts[3].Type == eventing.LLMAgentMessageCreated && evts[3].Depth != 1 {
		t.Fatalf("expected agent msg depth 1, got %d", evts[3].Depth)
	}
	if evts[4].Type == eventing.LLMToolCallResultCreated && evts[4].Depth != 2 {
		t.Fatalf("expected tool result depth 2, got %d", evts[4].Depth)
	}
	if evts[5].Type == eventing.LLMAgentResponseCreated && evts[5].Depth != 1 {
		t.Fatalf("expected agent response depth 1, got %d", evts[5].Depth)
	}

	// Check previews.
	if evts[0].Preview != "hello world" {
		t.Fatalf("unexpected preview: %q", evts[0].Preview)
	}
	if evts[3].Preview == "" {
		t.Fatalf("expected agent message preview, got empty")
	}
}

// TestReadSessionEvents_IdenticalTimestamps is a regression test for the
// explorer tool ordering bug: when LLMAgentMessageCreated and
// LLMToolCallResultCreated share the same CreatedAt, the stable sort must
// preserve the file-append order so the agent message precedes the tool result.
func TestReadSessionEvents_IdenticalTimestamps(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "cli", "test123")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	sameTime := time.Now().UTC().Unix()

	// Write context log with agent message and tool result at the same timestamp.
	contextPath := filepath.Join(sessionDir, "context.jsonpl")
	contextFile, err := os.Create(contextPath)
	if err != nil {
		t.Fatal(err)
	}

	userMsg := eventing.NewLLMUserMessage("test123", "events-id", 1, llm.Message{Role: llm.RoleUser, Content: "hello world"})
	userMsg.OccurredOn = time.Unix(sameTime, 0).UTC()
	writeStoredEvent(t, contextFile, userMsg)

	// Write agent message FIRST in the file (it happens first in the real loop).
	agentMsg := eventing.NewLLMAgentMessage("test123", "events-id", 1, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   "thinking...",
		ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Bash", Arguments: `{"command": "ls"}`}},
	}, llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, 15)
	agentMsg.OccurredOn = time.Unix(sameTime, 0).UTC()
	writeStoredEvent(t, contextFile, agentMsg)

	// Write tool result SECOND in the file (it happens after the agent message).
	toolResult := eventing.NewLLMToolCallResult("test123", "events-id", 1, llm.ToolCall{ID: "tc1", Name: "Bash", Arguments: `{"command": "ls"}`}, "", "", llm.Message{Role: llm.RoleTool, Content: "file1\nfile2", ToolCallID: "tc1"})
	toolResult.OccurredOn = time.Unix(sameTime, 0).UTC()
	writeStoredEvent(t, contextFile, toolResult)

	closeTestFile(t, contextFile)

	// Read and verify ordering.
	evts, err := ReadSessionEvents(sessionDir)
	if err != nil {
		t.Fatalf("ReadSessionEvents failed: %v", err)
	}
	if len(evts) != 3 {
		t.Fatalf("expected 3 events, got %d", len(evts))
	}

	// With stable sort, the agent message must appear before the tool result
	// even though they share the same timestamp.
	var agentIdx, toolIdx int
	for i, ev := range evts {
		switch ev.Type {
		case eventing.LLMAgentMessageCreated:
			agentIdx = i
		case eventing.LLMToolCallResultCreated:
			toolIdx = i
		}
	}
	if agentIdx >= toolIdx {
		t.Fatalf("agent message (idx %d) should appear before tool result (idx %d) with identical timestamps", agentIdx, toolIdx)
	}
}

// TestReadSessionEvents_LongLine is a regression test for the
// "bufio.Scanner: token too long" bug: context.jsonpl lines can exceed
// the default 64KB scanner limit.
func TestReadSessionEvents_LongLine(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "cli", "test123")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Unix()

	// Write events log.
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	if err := os.WriteFile(eventsPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Write context log with a very long payload line (>64KB).
	contextPath := filepath.Join(sessionDir, "context.jsonpl")
	contextFile, err := os.Create(contextPath)
	if err != nil {
		t.Fatal(err)
	}

	longContent := strings.Repeat("A", 100_000)
	userMsg := eventing.NewLLMUserMessage("test123", "events-id", 1, llm.Message{Role: llm.RoleUser, Content: longContent})
	userMsg.OccurredOn = time.Unix(now, 0).UTC()
	writeStoredEvent(t, contextFile, userMsg)
	closeTestFile(t, contextFile)

	// Read and verify — should not error with "token too long".
	evts, err := ReadSessionEvents(sessionDir)
	if err != nil {
		t.Fatalf("ReadSessionEvents failed on long line: %v", err)
	}
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	if evts[0].Preview == "" {
		t.Fatal("expected non-empty preview for long content")
	}
}

func TestFilterEvents(t *testing.T) {
	evts := []ExplorerEvent{
		{Source: "events", Type: eventing.LogMessageUserCreated, Preview: "user"},
		{Source: "events", Type: eventing.LogMessageAgentCreated, Preview: "agent"},
		{Source: "context", Type: eventing.LLMAgentMessageCreated, Preview: "agent msg"},
		{Source: "context", Type: eventing.LLMToolCallResultCreated, Preview: "tool"},
		{Source: "context", Type: eventing.SummaryPromptCreated, Preview: "compact", IsCompaction: true},
	}

	indices := FilterEvents(evts, FilterConversation)
	if len(indices) != 2 {
		t.Fatalf("expected 2 conversation events, got %d", len(indices))
	}

	indices = FilterEvents(evts, FilterToolCalls)
	if len(indices) != 2 {
		t.Fatalf("expected 2 toolcall events, got %d", len(indices))
	}

	indices = FilterEvents(evts, FilterCompaction)
	if len(indices) != 1 {
		t.Fatalf("expected 1 compaction event, got %d", len(indices))
	}
}

func TestDetailText(t *testing.T) {
	ev := ExplorerEvent{
		Type:         eventing.LogMessageUserCreated,
		Source:       "events",
		CreatedAt:    time.Now(),
		OriginatorID: "abc",
		Version:      1,
		Payload:      eventing.LogMessage{Content: "hello"},
	}
	text := DetailText(ev)
	if text == "" {
		t.Fatal("expected non-empty detail text")
	}
	if !contains(text, "hello") {
		t.Fatalf("expected detail to contain 'hello', got:\n%s", text)
	}
}

func TestReadSessionEvents_SubagentInvocation(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "tui", "parent")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Unix()
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	eventsFile, err := os.Create(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	started := eventing.NewSubagentInvocationStarted("parent", "task-validator", "child-1", "parent", "check CHANGELOG", 120000)
	started.OccurredOn = time.Unix(now, 0).UTC()
	writeStoredEvent(t, eventsFile, started)

	completed := eventing.NewSubagentInvocationCompleted("parent", "task-validator", "child-1", "parent", "PASS: complete", 3704, 120000)
	completed.OccurredOn = time.Unix(now+1, 0).UTC()
	writeStoredEvent(t, eventsFile, completed)
	closeTestFile(t, eventsFile)

	evts, err := ReadSessionEvents(sessionDir)
	if err != nil {
		t.Fatalf("ReadSessionEvents() error = %v", err)
	}
	if len(evts) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evts))
	}
	if _, ok := evts[0].Payload.(eventing.SubagentInvocation); !ok {
		t.Fatalf("expected parsed subagent payload, got %T", evts[0].Payload)
	}
	if !contains(evts[0].Preview, "task-validator started") {
		t.Fatalf("unexpected start preview: %q", evts[0].Preview)
	}
	if !contains(evts[1].Preview, "task-validator completed") {
		t.Fatalf("unexpected completion preview: %q", evts[1].Preview)
	}

	detail := DetailText(evts[1])
	for _, want := range []string{"Subagent:      task-validator", "Child session: child-1", "Result:", "PASS: complete"} {
		if !contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
}

func TestDetailText_LongContent(t *testing.T) {
	longContent := strings.Repeat("A", 100_000)
	ev := ExplorerEvent{
		Type:         eventing.LLMUserMessageCreated,
		Source:       "context",
		CreatedAt:    time.Now(),
		OriginatorID: "abc",
		Version:      1,
		Payload:      eventing.LLMUserMessage{Message: llm.Message{Role: llm.RoleUser, Content: longContent}},
	}
	text := DetailText(ev)
	if text == "" {
		t.Fatal("expected non-empty detail text")
	}
	if contains(text, longContent) {
		t.Fatal("expected detail text to be truncated")
	}
	if !contains(text, "more chars") {
		t.Fatal("expected detail text to contain truncation notice")
	}
}

func TestBadge(t *testing.T) {
	ev := ExplorerEvent{Type: eventing.LogMessageUserCreated}
	badge, color := Badge(ev)
	if badge != "user" {
		t.Fatalf("expected badge 'user', got %q", badge)
	}
	if color == "" {
		t.Fatal("expected non-empty color")
	}

	ev = ExplorerEvent{Type: eventing.SubagentInvocationCompleted}
	badge, color = Badge(ev)
	if badge != "sub-done" {
		t.Fatalf("expected badge 'sub-done', got %q", badge)
	}
	if color == "" {
		t.Fatal("expected non-empty color")
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Fatal("truncate should not shorten short strings")
	}
	if truncate("hello world", 8) != "hello..." {
		t.Fatalf("unexpected truncate result: %q", truncate("hello world", 8))
	}
}

func TestFindSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "cli", "s1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "tui", "s2"), 0755); err != nil {
		t.Fatal(err)
	}

	dir, adapter, err := FindSessionDir(tmpDir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if adapter != "cli" {
		t.Fatalf("expected adapter 'cli', got %q", adapter)
	}
	if filepath.Base(dir) != "s1" {
		t.Fatalf("unexpected dir: %s", dir)
	}

	_, _, err = FindSessionDir(tmpDir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestFindSessionDirRequiresAdapterForDuplicateID(t *testing.T) {
	root := t.TempDir()
	for _, adapter := range []string{"cli", "tui"} {
		if err := os.MkdirAll(filepath.Join(root, adapter, "duplicate"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := FindSessionDir(root, "duplicate"); err == nil || !strings.Contains(err.Error(), "multiple adapters") {
		t.Fatalf("FindSessionDir duplicate error = %v", err)
	}
	dir, adapter, err := FindSessionDirWithAdapter(root, "duplicate", "tui")
	if err != nil || adapter != "tui" || dir != filepath.Join(root, "tui", "duplicate") {
		t.Fatalf("explicit adapter: dir=%q adapter=%q err=%v", dir, adapter, err)
	}
}

// --- helpers ---

func toStoredEvent(e *types.Event) storedFileEvent {
	return storedFileEvent{
		OriginatorID:      e.Originator.ID,
		OriginatorVersion: e.Originator.Version,
		EventType:         e.EventType,
		Payload:           json.RawMessage(e.Payload),
		CreatedAt:         e.OccurredOn.Unix(),
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSub(s, substr))
}

func containsSub(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

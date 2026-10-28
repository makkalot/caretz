package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxstore "github.com/makkalot/caretz/internal/context"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/eventstore"
	"github.com/makkalot/caretz/lib/llm"
)

func TestInspectSessionRepairExplainsWhyRepairIsRequired(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	sessionID := "repair-ux"
	sessionDir := filepath.Join(sessionsDir, "tui", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	eventsLog, err := eventstore.NewFileMemoryStore(filepath.Join(sessionDir, "events.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	contextLog, err := eventstore.NewFileMemoryStore(filepath.Join(sessionDir, "context.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	eventStore, err := ctxstore.NewEventSessionStore("tui", sessionID, sessionsDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}
	contextStore, err := ctxstore.NewContextSessionStore(sessionID, eventStore, contextLog, eventing.NewSyncEventLoop(context.Background()))
	if err != nil {
		t.Fatal(err)
	}

	if err := contextStore.AppendUserMessage(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "run tool"})); err != nil {
		t.Fatal(err)
	}
	toolCall := llm.ToolCall{ID: "tc-1", Name: "Bash", Arguments: `{"command":"date"}`}
	if _, err := contextStore.AppendAgentMessage(eventing.NewLLMAgentMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	if err := contextStore.AppendUserMessage(eventing.NewLLMUserMessage(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleUser, Content: "later message"})); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := inspectSessionRepair(&out, sessionsDir, sessionID); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"Repair is required before this session can continue.",
		"did not write a final assistant response",
		"later user message exists",
		"pending tool call(s): tc-1",
		"Rewrite context.jsonpl",
		"Back up the original context.jsonpl",
		"caretz session repair repair-ux",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("inspect output missing %q:\n%s", want, got)
		}
	}
}

func TestRepairSessionRepairsMultipleInterruptedTurns(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	sessionID := "multi-repair"
	sessionDir := filepath.Join(sessionsDir, "tui", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	eventsLog, err := eventstore.NewFileMemoryStore(filepath.Join(sessionDir, "events.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	contextLog, err := eventstore.NewFileMemoryStore(filepath.Join(sessionDir, "context.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	eventStore, err := ctxstore.NewEventSessionStore("tui", sessionID, sessionsDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}
	contextStore, err := ctxstore.NewContextSessionStore(sessionID, eventStore, contextLog, eventing.NewSyncEventLoop(context.Background()))
	if err != nil {
		t.Fatal(err)
	}

	if err := contextStore.AppendUserMessage(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "first"})); err != nil {
		t.Fatal(err)
	}
	firstTool := llm.ToolCall{ID: "tc-1", Name: "Bash", Arguments: `{"command":"one"}`}
	if _, err := contextStore.AppendAgentMessage(eventing.NewLLMAgentMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{firstTool}}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := contextStore.AppendToolResult(eventing.NewLLMToolCallResult(sessionID, "parent-1", 1, firstTool, "", "", llm.Message{Role: llm.RoleTool, Content: "one", ToolCallID: "tc-1"})); err != nil {
		t.Fatal(err)
	}
	if err := contextStore.AppendUserMessage(eventing.NewLLMUserMessage(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleUser, Content: "second"})); err != nil {
		t.Fatal(err)
	}
	secondTool := llm.ToolCall{ID: "tc-2", Name: "Bash", Arguments: `{"command":"two"}`}
	if _, err := contextStore.AppendAgentMessage(eventing.NewLLMAgentMessage(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{secondTool}}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := contextStore.AppendToolResult(eventing.NewLLMToolCallResult(sessionID, "parent-2", 2, secondTool, "", "", llm.Message{Role: llm.RoleTool, Content: "two", ToolCallID: "tc-2"})); err != nil {
		t.Fatal(err)
	}
	if err := contextStore.AppendUserMessage(eventing.NewLLMUserMessage(sessionID, "parent-3", 3, llm.Message{Role: llm.RoleUser, Content: "third"})); err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(sessionDir, "context.jsonpl")
	originalContext, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := repairSession(&out, sessionsDir, sessionID); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "original context version(s): 4, 7") {
		t.Fatalf("repair output did not include both repaired versions:\n%s", got)
	}
	if !strings.Contains(got, "backup: ") {
		t.Fatalf("repair output did not include backup path:\n%s", got)
	}
	backups, err := filepath.Glob(contextPath + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1: %v", len(backups), backups)
	}
	backupBytes, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backupBytes) != string(originalContext) {
		t.Fatalf("backup does not match original context")
	}
	repairedEvents, err := readContextEventsForSession(sessionDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	gotTypes := make([]string, 0, len(repairedEvents))
	for _, e := range repairedEvents {
		gotTypes = append(gotTypes, e.EventType)
		if e.Originator.Version != uint64(len(gotTypes)) {
			t.Fatalf("event %d has version %d", len(gotTypes), e.Originator.Version)
		}
	}
	wantTypes := []string{
		eventing.LLMUserMessageCreated,
		eventing.LLMAgentMessageCreated,
		eventing.LLMToolCallResultCreated,
		eventing.LLMAgentInterruptedResponseCreated,
		eventing.LLMUserMessageCreated,
		eventing.LLMAgentMessageCreated,
		eventing.LLMToolCallResultCreated,
		eventing.LLMAgentInterruptedResponseCreated,
		eventing.LLMUserMessageCreated,
	}
	if strings.Join(gotTypes, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("repaired event types = %v, want %v", gotTypes, wantTypes)
	}
	out.Reset()
	if err := inspectSessionRepair(&out, sessionsDir, sessionID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No repair is required") {
		t.Fatalf("session should be clean after multi-repair:\n%s", out.String())
	}
}

func TestRepairSessionInjectsMissingToolResult(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	sessionID := "missing-tool-result"
	sessionDir := filepath.Join(sessionsDir, "tui", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	eventsLog, err := eventstore.NewFileMemoryStore(filepath.Join(sessionDir, "events.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	contextLog, err := eventstore.NewFileMemoryStore(filepath.Join(sessionDir, "context.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	eventStore, err := ctxstore.NewEventSessionStore("tui", sessionID, sessionsDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}
	contextStore, err := ctxstore.NewContextSessionStore(sessionID, eventStore, contextLog, eventing.NewSyncEventLoop(context.Background()))
	if err != nil {
		t.Fatal(err)
	}

	if err := contextStore.AppendUserMessage(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "run tool"})); err != nil {
		t.Fatal(err)
	}
	toolCall := llm.ToolCall{ID: "tc-missing", Name: "Bash", Arguments: `{"command":"date"}`}
	if _, err := contextStore.AppendAgentMessage(eventing.NewLLMAgentMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	if err := contextStore.AppendUserMessage(eventing.NewLLMUserMessage(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleUser, Content: "later"})); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := repairSession(&out, sessionsDir, sessionID); err != nil {
		t.Fatal(err)
	}
	repairedEvents, err := readContextEventsForSession(sessionDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	gotTypes := make([]string, 0, len(repairedEvents))
	for _, e := range repairedEvents {
		gotTypes = append(gotTypes, e.EventType)
	}
	wantTypes := []string{
		eventing.LLMUserMessageCreated,
		eventing.LLMAgentMessageCreated,
		eventing.LLMToolCallRecoveryResultCreated,
		eventing.LLMAgentInterruptedResponseCreated,
		eventing.LLMUserMessageCreated,
	}
	if strings.Join(gotTypes, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("repaired event types = %v, want %v", gotTypes, wantTypes)
	}
	recoveryPayload, err := eventing.DecodeRecoveryPayload(repairedEvents[2])
	if err != nil {
		t.Fatal(err)
	}
	if recoveryPayload.ToolCallID != "tc-missing" || recoveryPayload.ToolName != "Bash" {
		t.Fatalf("unexpected recovery payload: %#v", recoveryPayload)
	}
}

func TestSessionListDoesNotRequireModelConfiguration(t *testing.T) {
	var stdout, stderr strings.Builder
	code := Run(context.Background(), []string{"session", "list", "--config-root", t.TempDir()}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "No sessions found.") {
		t.Fatalf("session list code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestSessionParentFlagsWorkBeforeAndAfterAction(t *testing.T) {
	for _, args := range [][]string{
		{"session", "--config-root", t.TempDir(), "list"},
		{"session", "list", "--config-root", t.TempDir()},
	} {
		var stdout, stderr strings.Builder
		code := Run(context.Background(), args, &stdout, &stderr)
		if code != 0 || !strings.Contains(stdout.String(), "No sessions found.") {
			t.Fatalf("Run(%v) code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

func TestInjectContextRecoveryAddsMissingToolResultsBeforeTarget(t *testing.T) {
	sessionID := "ch-repair"
	toolCalls := []llm.ToolCall{
		{ID: "tc-1", Name: "Bash", Arguments: `{"command":"one"}`},
		{ID: "tc-2", Name: "Read", Arguments: `{"file":"two.txt"}`},
	}
	events := []*types.Event{
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "run tools"}), 1),
		numberedEvent(eventing.NewLLMAgentMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: toolCalls}, llm.Usage{}, 0), 2),
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleUser, Content: "later"}), 3),
	}
	analysis := sessionRepairAnalysis{
		Required:                true,
		Reason:                  "incomplete_turn_detected",
		OriginatorID:            sessionID,
		ParentOriginatorID:      "parent-1",
		ParentOriginatorVersion: 1,
		TargetBeforeVersion:     3,
		PendingToolCallIDs:      []string{"tc-1", "tc-2"},
		PendingToolCalls:        toolCalls,
		Message:                 eventing.InterruptedDuringToolsMessage,
	}

	repaired, inserted := injectContextRecovery(events, analysis, "recovery-1")
	if !inserted {
		t.Fatal("expected recovery insertion")
	}
	gotTypes := eventTypes(repaired)
	wantTypes := []string{
		eventing.LLMUserMessageCreated,
		eventing.LLMAgentMessageCreated,
		eventing.LLMToolCallRecoveryResultCreated,
		eventing.LLMToolCallRecoveryResultCreated,
		eventing.LLMAgentInterruptedResponseCreated,
		eventing.LLMUserMessageCreated,
	}
	if stringsJoin(gotTypes) != stringsJoin(wantTypes) {
		t.Fatalf("event types = %v, want %v", gotTypes, wantTypes)
	}

	for i, wantToolID := range []string{"tc-1", "tc-2"} {
		payload, err := eventing.DecodeRecoveryPayload(repaired[2+i])
		if err != nil {
			t.Fatal(err)
		}
		if payload.ToolCallID != wantToolID {
			t.Fatalf("recovery tool id %d = %q, want %q", i, payload.ToolCallID, wantToolID)
		}
	}
	if repaired[5].Payload != events[2].Payload {
		t.Fatalf("expected later user to remain after injected recovery")
	}
}

func TestAnalyzeSessionRepairDetectsIncompleteToolTurnBeforeLaterUser(t *testing.T) {
	sessionID := "ch-analyze"
	toolCall := llm.ToolCall{ID: "tc-1", Name: "Bash", Arguments: `{"command":"echo hi"}`}
	events := []*types.Event{
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-0", 0, llm.Message{Role: llm.RoleUser, Content: "done-user"}), 1),
		numberedEvent(eventing.NewLLMAgentResponse(sessionID, "parent-0", 0, llm.Message{Role: llm.RoleAssistant, Content: "done-asst"}, llm.Usage{}, 0), 2),
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "pending-user"}), 3),
		numberedEvent(eventing.NewLLMAgentMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0), 4),
		numberedEvent(eventing.NewLLMToolCallResult(sessionID, "parent-1", 1, toolCall, "", "", llm.Message{Role: llm.RoleTool, Content: "hi", ToolCallID: toolCall.ID}), 5),
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleUser, Content: "new user"}), 6),
	}

	analysis := analyzeSessionRepair(events, sessionID)
	if !analysis.Required {
		t.Fatal("expected repair to be required")
	}
	if analysis.Reason != "incomplete_turn_detected" {
		t.Fatalf("repair reason = %q, want incomplete_turn_detected", analysis.Reason)
	}
	if len(analysis.PendingToolCallIDs) != 0 {
		t.Fatalf("pending tool IDs = %v, want none", analysis.PendingToolCallIDs)
	}
	if analysis.Message != eventing.InterruptedAfterToolsMessage {
		t.Fatalf("message = %q, want %q", analysis.Message, eventing.InterruptedAfterToolsMessage)
	}
	if analysis.TargetBeforeVersion != 6 {
		t.Fatalf("target version = %d, want 6", analysis.TargetBeforeVersion)
	}
}

func TestAnalyzeSessionRepairIgnoresSummaryEventsAsProtocolEdges(t *testing.T) {
	sessionID := "ch-summary-repair"
	toolCall := llm.ToolCall{ID: "tc-missing", Name: "Bash", Arguments: `{"command":"date"}`}
	events := []*types.Event{
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "run tool"}), 1),
		numberedEvent(eventing.NewLLMAgentMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0), 2),
		numberedEvent(eventing.NewSummaryPromptCreatedWithMetadata(sessionID, "summary-parent", 1, "summarize", "normal", 1, 2), 3),
		numberedEvent(eventing.NewLLMAgentSummaryResponseWithMetadata(sessionID, "summary-parent", 1, llm.Message{Role: llm.RoleAssistant, Content: "summary"}, llm.Usage{}, 0, "normal", 1, 2), 4),
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleUser, Content: "later user"}), 5),
	}

	analysis := analyzeSessionRepair(events, sessionID)
	if !analysis.Required {
		t.Fatal("expected repair to be required")
	}
	if got, want := stringsJoin(analysis.PendingToolCallIDs), "tc-missing"; got != want {
		t.Fatalf("pending tool IDs = %q, want %q", got, want)
	}
	if analysis.Message != eventing.InterruptedDuringToolsMessage {
		t.Fatalf("message = %q, want %q", analysis.Message, eventing.InterruptedDuringToolsMessage)
	}
	if analysis.TargetBeforeVersion != 5 {
		t.Fatalf("target version = %d, want 5", analysis.TargetBeforeVersion)
	}
}

func TestAnalyzeSessionRepairIgnoresPreSummaryToolFragments(t *testing.T) {
	sessionID := "ch-fragment"
	toolCall := llm.ToolCall{ID: "tc-fragment", Name: "Bash", Arguments: `{"command":"echo fragment"}`}
	events := []*types.Event{
		numberedEvent(eventing.NewSummaryPromptCreatedWithMetadata(sessionID, "summary-parent", 1, "summarize", "normal", 1, 1), 1),
		numberedEvent(eventing.NewLLMAgentSummaryResponseWithMetadata(sessionID, "summary-parent", 1, llm.Message{Role: llm.RoleAssistant, Content: "previous summary"}, llm.Usage{}, 0, "normal", 1, 1), 2),
		numberedEvent(eventing.NewLLMAgentMessage(sessionID, "parent-fragment", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0), 3),
		numberedEvent(eventing.NewLLMToolCallResult(sessionID, "parent-fragment", 1, toolCall, "", "", llm.Message{Role: llm.RoleTool, Content: "fragment output", ToolCallID: toolCall.ID}), 4),
		numberedEvent(eventing.NewSummaryPromptCreatedWithMetadata(sessionID, "summary-parent", 2, "summarize", "normal", 1, 4), 5),
		numberedEvent(eventing.NewLLMAgentSummaryResponseWithMetadata(sessionID, "summary-parent", 2, llm.Message{Role: llm.RoleAssistant, Content: "latest summary"}, llm.Usage{}, 0, "normal", 1, 4), 6),
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleUser, Content: "after summary user"}), 7),
		numberedEvent(eventing.NewLLMAgentResponse(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleAssistant, Content: "after summary assistant"}, llm.Usage{}, 0), 8),
	}

	analysis := analyzeSessionRepair(events, sessionID)
	if analysis.Required {
		t.Fatalf("repair required unexpectedly: %#v", analysis)
	}
}

func TestAnalyzeSessionRepairDetectsIncompleteTailAfterToolResults(t *testing.T) {
	sessionID := "ch-tail"
	toolCall := llm.ToolCall{ID: "tc-tail", Name: "Bash", Arguments: `{"command":"echo tail"}`}
	events := []*types.Event{
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "run tool"}), 1),
		numberedEvent(eventing.NewLLMAgentMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0), 2),
		numberedEvent(eventing.NewLLMToolCallResult(sessionID, "parent-1", 1, toolCall, "", "", llm.Message{Role: llm.RoleTool, Content: "tail output", ToolCallID: toolCall.ID}), 3),
	}

	analysis := analyzeSessionRepair(events, sessionID)
	if !analysis.Required {
		t.Fatal("expected repair to be required")
	}
	if analysis.Reason != "incomplete_tail_detected" {
		t.Fatalf("repair reason = %q, want incomplete_tail_detected", analysis.Reason)
	}
	if analysis.TargetBeforeVersion != 0 {
		t.Fatalf("target version = %d, want 0 for append-at-end repair", analysis.TargetBeforeVersion)
	}
	if len(analysis.PendingToolCallIDs) != 0 {
		t.Fatalf("pending tool IDs = %v, want none", analysis.PendingToolCallIDs)
	}
	if analysis.Message != eventing.InterruptedAfterToolsMessage {
		t.Fatalf("message = %q, want %q", analysis.Message, eventing.InterruptedAfterToolsMessage)
	}
}

func TestInjectContextRecoveryAppendsIncompleteTailRepair(t *testing.T) {
	sessionID := "ch-tail-repair"
	events := []*types.Event{
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "run tool"}), 1),
	}
	analysis := sessionRepairAnalysis{
		Required:                true,
		Reason:                  "incomplete_tail_detected",
		OriginatorID:            sessionID,
		ParentOriginatorID:      "parent-1",
		ParentOriginatorVersion: 1,
		Message:                 eventing.InterruptedAfterToolsMessage,
	}

	repaired, inserted := injectContextRecovery(events, analysis, "recovery-tail")
	if !inserted {
		t.Fatal("expected tail recovery insertion")
	}
	gotTypes := eventTypes(repaired)
	wantTypes := []string{
		eventing.LLMUserMessageCreated,
		eventing.LLMAgentInterruptedResponseCreated,
	}
	if stringsJoin(gotTypes) != stringsJoin(wantTypes) {
		t.Fatalf("event types = %v, want %v", gotTypes, wantTypes)
	}
}

func TestRenumberContextEventsRemapsCompactionMetadata(t *testing.T) {
	sessionID := "ch-renumber"
	events := []*types.Event{
		numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "first"}), 3),
		numberedEvent(eventing.NewLLMAgentInterruptedResponse(sessionID, "parent-1", 1, "recovery-1", "incomplete_turn_detected", eventing.InterruptedAfterToolsMessage, nil), 0),
		numberedEvent(eventing.NewSummaryPromptCreatedWithMetadata(sessionID, "parent-2", 2, "summary prompt", "active_tool_chain", 3, 9), 9),
		numberedEvent(eventing.NewLLMAgentSummaryResponseWithMetadata(sessionID, "parent-2", 2, llm.Message{Role: llm.RoleAssistant, Content: "summary"}, llm.Usage{}, 0, "active_tool_chain", 3, 9), 10),
	}

	renumbered := renumberContextEvents(events, sessionID)
	for i, event := range renumbered {
		if event.Originator.Version != uint64(i+1) {
			t.Fatalf("event %d version = %d, want %d", i, event.Originator.Version, i+1)
		}
	}

	var prompt eventing.SummaryPrompt
	if err := json.Unmarshal([]byte(renumbered[2].Payload), &prompt); err != nil {
		t.Fatal(err)
	}
	if prompt.ActiveUserVersion != 1 || prompt.CoversThroughVersion != 3 {
		t.Fatalf("prompt metadata = active %d covers %d, want 1/3", prompt.ActiveUserVersion, prompt.CoversThroughVersion)
	}
	var summary eventing.LLMAgentResponse
	if err := json.Unmarshal([]byte(renumbered[3].Payload), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ActiveUserVersion != 1 || summary.CoversThroughVersion != 3 {
		t.Fatalf("summary metadata = active %d covers %d, want 1/3", summary.ActiveUserVersion, summary.CoversThroughVersion)
	}
}

func TestWriteContextJSONPLWritesStoredPayloadObjects(t *testing.T) {
	sessionID := "ch-write"
	event := numberedEvent(eventing.NewLLMUserMessage(sessionID, "parent-1", 1, llm.Message{Role: llm.RoleUser, Content: "hello"}), 1)
	path := filepath.Join(t.TempDir(), "context.jsonpl")

	if err := writeContextJSONPL(path, []*types.Event{event}); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatal("expected one JSONPL line")
	}
	var stored struct {
		OriginatorID      string          `json:"originator_id"`
		OriginatorVersion uint64          `json:"originator_version"`
		EventType         string          `json:"event_type"`
		Payload           json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.OriginatorID != sessionID || stored.OriginatorVersion != 1 || stored.EventType != eventing.LLMUserMessageCreated {
		t.Fatalf("unexpected stored event: %#v", stored)
	}
	if len(stored.Payload) == 0 || stored.Payload[0] != '{' {
		t.Fatalf("payload should be stored as JSON object, got %s", string(stored.Payload))
	}
	if scanner.Scan() {
		t.Fatal("expected only one JSONPL line")
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func numberedEvent(event *types.Event, version uint64) *types.Event {
	event.Originator.Version = version
	return event
}

func eventTypes(events []*types.Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.EventType)
	}
	return types
}

func stringsJoin(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ","
		}
		out += value
	}
	return out
}

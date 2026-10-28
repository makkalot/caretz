package context

import (
	stdctx "context"
	"encoding/json"
	"os"
	"path"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/eventstore"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

func setupContextSessionStore(t *testing.T) (*ContextSessionStore, string, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "context-session-store-*")
	if err != nil {
		t.Fatal(err)
	}

	eventsPath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(eventsPath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	contextPath := path.Join(tmpDir, "context.jsonpl")
	contextFile, err := eventstore.NewFileMemoryStore(contextPath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	eventStore, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	eventLoop := eventing.NewSyncEventLoop(stdctx.Background())

	ctxStore, err := NewContextSessionStore("ch-1", eventStore, contextFile, eventLoop)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}

	return ctxStore, tmpDir, cleanup
}

func TestContextSessionStore_AppendUserMessage(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	msg := llm.Message{Role: llm.RoleUser, Content: "hello"}
	event := eventing.NewLLMUserMessage("ch-1", "parent-1", 0, msg)

	if err := ctxStore.AppendUserMessage(event); err != nil {
		t.Fatalf("failed to append user message: %v", err)
	}

	latest := ctxStore.latestContext
	if latest == nil {
		t.Fatal("expected latestContext to be set")
	}
	if latest.EventType != eventing.LLMUserMessageCreated {
		t.Errorf("expected %s, got %s", eventing.LLMUserMessageCreated, latest.EventType)
	}

	// Wrong type should error
	badEvent := eventing.NewAgentLogMessage("ch-1", "bad")
	if err := ctxStore.AppendUserMessage(badEvent); err == nil {
		t.Fatal("expected error for wrong event type")
	}
}

func TestContextSessionStore_AppendAgentResponse(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	msg := llm.Message{Role: llm.RoleAssistant, Content: "done"}
	usage := llm.Usage{TotalTokens: 10}
	event := eventing.NewLLMAgentResponse("ch-1", "parent-1", 0, msg, usage, 0)

	res, err := ctxStore.AppendAgentResponse(event)
	if err != nil {
		t.Fatalf("failed to append agent response: %v", err)
	}
	if res == nil {
		t.Fatal("expected returned event, got nil")
	}

	latest := ctxStore.latestContext
	if latest == nil {
		t.Fatal("expected latestContext to be set")
	}
	if latest.EventType != eventing.LLMAgentResponseCreated {
		t.Errorf("expected %s, got %s", eventing.LLMAgentResponseCreated, latest.EventType)
	}

	// Wrong type should error
	badEvent := eventing.NewAgentLogMessage("ch-1", "bad")
	if _, err := ctxStore.AppendAgentResponse(badEvent); err == nil {
		t.Fatal("expected error for wrong event type")
	}
}

func TestContextSessionStore_AppendToolResult(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	tool := llm.ToolCall{ID: "tc-1", Name: "Read", Arguments: `{"file_path":"main.go"}`}
	msg := llm.Message{Role: llm.RoleTool, Content: "result", ToolCallID: "tc-1"}
	event := eventing.NewLLMToolCallResult("ch-1", "parent-1", 0, tool, "", "", msg)

	res, err := ctxStore.AppendToolResult(event)
	if err != nil {
		t.Fatalf("failed to append tool result: %v", err)
	}
	if res == nil {
		t.Fatal("expected returned event, got nil")
	}

	// Wrong type should error
	badEvent := eventing.NewAgentLogMessage("ch-1", "bad")
	if _, err := ctxStore.AppendToolResult(badEvent); err == nil {
		t.Fatal("expected error for wrong event type")
	}
}

func TestContextSessionStore_RestoreTaskStatePrunesExpiredDoneTasks(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	originalNow := nowUTC
	defer func() { nowUTC = originalNow }()

	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	nowUTC = func() time.Time { return now }

	state := tasks.NewState()
	for _, id := range []string{"T1", "T2", "T3"} {
		if _, err := state.Apply(tasks.Update{Op: tasks.OpAdd, ID: id, Title: "task " + id}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if _, err := state.Apply(tasks.Update{Op: tasks.OpUpdate, ID: "T2", Validation: "Check the result"}); err != nil {
		t.Fatalf("update T2 validation: %v", err)
	}
	if _, err := state.Apply(tasks.Update{Op: tasks.OpDone, ID: "T1", Outcome: "old done"}); err != nil {
		t.Fatalf("done T1: %v", err)
	}
	state.MarkCompletedAt("T1", now.Add(-25*time.Hour))
	if _, err := state.Apply(tasks.Update{Op: tasks.OpDone, ID: "T2", Outcome: "recent done"}); err != nil {
		t.Fatalf("done T2: %v", err)
	}
	state.MarkCompletedAt("T2", now.Add(-2*time.Hour))
	if _, err := state.Apply(tasks.Update{Op: tasks.OpStart, ID: "T3", NextAction: "continue active"}); err != nil {
		t.Fatalf("start T3: %v", err)
	}

	snapshot := eventing.NewTaskSnapshot("ch-1", "parent-1", 0, state.Snapshot())
	if _, err := ctxStore.AppendTaskEvent(snapshot); err != nil {
		t.Fatalf("append snapshot: %v", err)
	}

	restored, _, err := ctxStore.RestoreTaskState("ch-1")
	if err != nil {
		t.Fatalf("RestoreTaskState: %v", err)
	}
	if _, ok := restored.Tasks["T1"]; ok {
		t.Fatalf("expired done task T1 should be pruned: %#v", restored.Tasks)
	}
	if task := restored.Tasks["T2"]; task.Status != tasks.StatusDone || task.Validation != "Check the result" {
		t.Fatalf("recent done task T2 should remain: %+v", task)
	}
	if task := restored.Tasks["T3"]; task.Status != tasks.StatusInProgress {
		t.Fatalf("active task T3 should remain: %+v", task)
	}
	if got := countPruneEvents(t, ctxStore, "ch-1"); got != 1 {
		t.Fatalf("prune events after first restore = %d, want 1", got)
	}

	if _, _, err := ctxStore.RestoreTaskState("ch-1"); err != nil {
		t.Fatalf("second RestoreTaskState: %v", err)
	}
	if got := countPruneEvents(t, ctxStore, "ch-1"); got != 1 {
		t.Fatalf("prune events after second restore = %d, want 1", got)
	}
}

func countPruneEvents(t *testing.T, ctxStore *ContextSessionStore, channelID string) int {
	t.Helper()
	events, err := ctxStore.contextStore.Get(types.NewOriginator(channelID, 0), false)
	if err != nil {
		t.Fatalf("get context events: %v", err)
	}
	count := 0
	for _, e := range events {
		if e.EventType != eventing.TaskUpdatedCreated {
			continue
		}
		var payload eventing.TaskUpdated
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			t.Fatalf("unmarshal task update: %v", err)
		}
		if payload.Update.Op == tasks.OpPrune {
			count++
		}
	}
	return count
}

func TestContextSessionStore_IsEventProcessed(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	msg := llm.Message{Role: llm.RoleUser, Content: "hello"}
	userEvent := eventing.NewLLMUserMessage("ch-1", "parent-1", 0, msg)

	// Event not yet in context store -> not processed
	processed, err := ctxStore.IsEventProcessed(userEvent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed {
		t.Fatal("expected not processed")
	}

	// Append user message
	if err := ctxStore.AppendUserMessage(userEvent); err != nil {
		t.Fatal(err)
	}

	// Still not processed because no agent response yet
	processed, err = ctxStore.IsEventProcessed(userEvent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed {
		t.Fatal("expected not processed (no agent response yet)")
	}

	// Append agent response
	agentMsg := llm.Message{Role: llm.RoleAssistant, Content: "hi"}
	agentEvent := eventing.NewLLMAgentResponse("ch-1", "parent-1", 0, agentMsg, llm.Usage{}, 0)
	if _, err := ctxStore.AppendAgentResponse(agentEvent); err != nil {
		t.Fatal(err)
	}

	// Now it should be processed
	processed, err = ctxStore.IsEventProcessed(userEvent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !processed {
		t.Fatal("expected processed")
	}
}

func TestContextSessionStore_IsEventProcessed_DifferentEvent(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	msg1 := llm.Message{Role: llm.RoleUser, Content: "hello"}
	userEvent1 := eventing.NewLLMUserMessage("ch-1", "parent-1", 0, msg1)

	msg2 := llm.Message{Role: llm.RoleUser, Content: "world"}
	userEvent2 := eventing.NewLLMUserMessage("ch-1", "parent-1", 0, msg2)

	// Append first user message and agent response
	if err := ctxStore.AppendUserMessage(userEvent1); err != nil {
		t.Fatal(err)
	}
	agentMsg := llm.Message{Role: llm.RoleAssistant, Content: "hi"}
	agentEvent := eventing.NewLLMAgentResponse("ch-1", "parent-1", 0, agentMsg, llm.Usage{}, 0)
	if _, err := ctxStore.AppendAgentResponse(agentEvent); err != nil {
		t.Fatal(err)
	}

	// First event is processed
	processed, _ := ctxStore.IsEventProcessed(userEvent1)
	if !processed {
		t.Fatal("expected userEvent1 to be processed")
	}

	// Second event is not in context store at all -> not processed
	processed, _ = ctxStore.IsEventProcessed(userEvent2)
	if processed {
		t.Fatal("expected userEvent2 to not be processed")
	}
}

func TestContextSessionStore_IsEventProcessed_DuplicateContentDifferentVersion(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	msg := llm.Message{Role: llm.RoleUser, Content: "yes"}

	// First user message with parent version 1
	userEvent1 := eventing.NewLLMUserMessage("ch-1", "parent-1", 1, msg)
	if err := ctxStore.AppendUserMessage(userEvent1); err != nil {
		t.Fatal(err)
	}
	agentMsg := llm.Message{Role: llm.RoleAssistant, Content: "ok"}
	agentEvent := eventing.NewLLMAgentResponse("ch-1", "parent-1", 0, agentMsg, llm.Usage{}, 0)
	if _, err := ctxStore.AppendAgentResponse(agentEvent); err != nil {
		t.Fatal(err)
	}

	// First event is processed
	processed, _ := ctxStore.IsEventProcessed(userEvent1)
	if !processed {
		t.Fatal("expected userEvent1 to be processed")
	}

	// Second user message with same content but different parent version
	userEvent2 := eventing.NewLLMUserMessage("ch-1", "parent-1", 21, msg)
	// Not appended yet, so should not be processed
	processed, _ = ctxStore.IsEventProcessed(userEvent2)
	if processed {
		t.Fatal("expected userEvent2 (same content, different parent version) to NOT be processed")
	}

	// Append the second user message and agent response
	if err := ctxStore.AppendUserMessage(userEvent2); err != nil {
		t.Fatal(err)
	}
	if _, err := ctxStore.AppendAgentResponse(agentEvent); err != nil {
		t.Fatal(err)
	}

	// Now it should be processed
	processed, _ = ctxStore.IsEventProcessed(userEvent2)
	if !processed {
		t.Fatal("expected userEvent2 to be processed after appending agent response")
	}
}

func TestContextSessionStore_Versioning_SameOriginator(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	msg1 := llm.Message{Role: llm.RoleUser, Content: "hello"}
	e1 := eventing.NewLLMUserMessage("ch-1", "parent-1", 0, msg1)
	if err := ctxStore.AppendUserMessage(e1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if e1.Originator.Version != 1 {
		t.Errorf("expected version 1, got %d", e1.Originator.Version)
	}

	agentMsg := llm.Message{Role: llm.RoleAssistant, Content: "hi"}
	e2 := eventing.NewLLMAgentResponse("ch-1", "parent-1", 0, agentMsg, llm.Usage{}, 0)
	if _, err := ctxStore.AppendAgentResponse(e2); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if e2.Originator.Version != 2 {
		t.Errorf("expected version 2, got %d", e2.Originator.Version)
	}
}

func TestContextSessionStore_Versioning_DifferentOriginator(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	// Event for originator A
	msgA1 := llm.Message{Role: llm.RoleUser, Content: "hello A"}
	eA1 := eventing.NewLLMUserMessage("originator-a", "parent-a", 0, msgA1)
	if err := ctxStore.AppendUserMessage(eA1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if eA1.Originator.Version != 1 {
		t.Errorf("expected originator-a version 1, got %d", eA1.Originator.Version)
	}

	// Event for originator B — latestContext now points to B
	msgB1 := llm.Message{Role: llm.RoleUser, Content: "hello B"}
	eB1 := eventing.NewLLMUserMessage("originator-b", "parent-b", 0, msgB1)
	if err := ctxStore.AppendUserMessage(eB1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if eB1.Originator.Version != 1 {
		t.Errorf("expected originator-b version 1, got %d", eB1.Originator.Version)
	}

	// Another event for originator A — in-memory latest is B, so must fall back to file lookup
	msgA2 := llm.Message{Role: llm.RoleUser, Content: "hello A again"}
	eA2 := eventing.NewLLMUserMessage("originator-a", "parent-a", 0, msgA2)
	if err := ctxStore.AppendUserMessage(eA2); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if eA2.Originator.Version != 2 {
		t.Errorf("expected originator-a version 2, got %d", eA2.Originator.Version)
	}

	// Another event for originator B — in-memory latest is A, so must fall back to file lookup
	msgB2 := llm.Message{Role: llm.RoleUser, Content: "hello B again"}
	eB2 := eventing.NewLLMUserMessage("originator-b", "parent-b", 0, msgB2)
	if err := ctxStore.AppendUserMessage(eB2); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if eB2.Originator.Version != 2 {
		t.Errorf("expected originator-b version 2, got %d", eB2.Originator.Version)
	}
}

func TestContextSessionStore_WhatsPending(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	// Empty store -> nothing pending
	pending, err := ctxStore.WhatsPending()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending, got %d", len(pending))
	}

	// Append a user message to events log
	userEvent := eventing.NewLogMessage("ch-1", "hello")
	if err := ctxStore.eventsLog.Append(userEvent); err != nil {
		t.Fatal(err)
	}

	// No agent response yet -> 1 pending
	pending, err = ctxStore.WhatsPending()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].Payload != userEvent.Payload {
		t.Errorf("expected payload %s, got %s", userEvent.Payload, pending[0].Payload)
	}

	// Append agent response to context file
	agentMsg := llm.Message{Role: llm.RoleAssistant, Content: "hi"}
	agentEvent := eventing.NewLLMAgentResponse("ch-1", "parent-1", 0, agentMsg, llm.Usage{}, 0)
	if _, err := ctxStore.AppendAgentResponse(agentEvent); err != nil {
		t.Fatal(err)
	}

	// Now there should be no pending
	pending, err = ctxStore.WhatsPending()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending after response, got %d", len(pending))
	}
}

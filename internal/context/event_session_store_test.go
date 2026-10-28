package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/eventstore"
	"github.com/makkalot/caretz/lib/types"
)

func TestEventSessionStore_ConstructorEmpty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if s.adapterType != "test" {
		t.Errorf("expected adapterType test, got %s", s.adapterType)
	}
	if s.channelId != "ch-1" {
		t.Errorf("expected channelId ch-1, got %s", s.channelId)
	}
	if s.baseDir != tmpDir {
		t.Errorf("expected baseDir %s, got %s", tmpDir, s.baseDir)
	}

	latest, err := s.GetLatest()
	if err != nil {
		t.Fatal(err)
	}
	if latest != nil {
		t.Fatalf("expected nil latest for empty store, got %+v", latest)
	}
}

func TestEventSessionStore_AppendPublishesEvent(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	var seen string
	if err := s.AddEventHandler(eventing.SubagentInvocationStarted, func(e *types.Event) error {
		seen = e.EventType
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(eventing.NewSubagentInvocationStarted("ch-1", "review", "child-1", "ch-1", "task", 120000)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if seen != eventing.SubagentInvocationStarted {
		t.Fatalf("handler saw %q, want %q", seen, eventing.SubagentInvocationStarted)
	}
}

func TestEventSessionStore_HandlerError(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEventHandler(eventing.SubagentInvocationStarted, func(e *types.Event) error {
		return fmt.Errorf("handler failed")
	}); err != nil {
		t.Fatal(err)
	}
	err = s.Append(eventing.NewSubagentInvocationStarted("ch-1", "review", "child-1", "ch-1", "task", 120000))
	if err == nil || err.Error() != "handler failed" {
		t.Fatalf("Append() error = %v, want handler failed", err)
	}
}

func TestEventSessionStore_AppendAndGetLatest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	e1 := eventing.NewLogMessage("ch-1", "hello")
	if err := s.Append(e1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}

	latest, err := s.GetLatest()
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil {
		t.Fatal("expected latest event, got nil")
	}
	if latest.Payload != e1.Payload {
		t.Errorf("expected payload %s, got %s", e1.Payload, latest.Payload)
	}

	// Append a different event
	e2 := eventing.NewAgentLogMessage("ch-1", "done")
	if err := s.Append(e2); err != nil {
		t.Fatalf("failed to append agent response: %v", err)
	}

	latest, _ = s.GetLatest()
	if latest.EventType != eventing.LogMessageAgentCreated {
		t.Errorf("expected event type %s, got %s", eventing.LogMessageAgentCreated, latest.EventType)
	}
}

func TestEventSessionStore_Dedup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	e1 := eventing.NewLogMessage("ch-1", "hello")
	if err := s.Append(e1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}

	// Exact duplicate within 2 seconds should be silently skipped
	e2 := eventing.NewLogMessage("ch-1", "hello")
	if err := s.Append(e2); err != nil {
		t.Fatalf("failed to append duplicate: %v", err)
	}

	latest, _ := s.GetLatest()
	if latest.OccurredOn != e1.OccurredOn {
		t.Fatalf("expected dedup to keep first event, but latest changed")
	}

	// Different payload should succeed
	e3 := eventing.NewLogMessage("ch-1", "world")
	if err := s.Append(e3); err != nil {
		t.Fatalf("failed to append different event: %v", err)
	}

	latest, _ = s.GetLatest()
	if latest.Payload != e3.Payload {
		t.Errorf("expected payload %s, got %s", e3.Payload, latest.Payload)
	}
}

func TestEventSessionStore_DedupAfterWindow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	e1 := eventing.NewLogMessage("ch-1", "hello")
	e1.OccurredOn = time.Now().Add(-3 * time.Second)
	if err := s.Append(e1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}

	// Duplicate outside the 2-second window should be appended
	e2 := eventing.NewLogMessage("ch-1", "hello")
	if err := s.Append(e2); err != nil {
		t.Fatalf("failed to append: %v", err)
	}

	latest, _ := s.GetLatest()
	if latest.OccurredOn != e2.OccurredOn {
		t.Fatalf("expected second event to be appended after window")
	}
}

func TestEventSessionStore_GetLatestUserMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	// No user messages yet
	latest, err := s.GetLatestUserMessage()
	if err != nil {
		t.Fatal(err)
	}
	if latest != nil {
		t.Fatalf("expected nil, got %+v", latest)
	}

	// Append agent response first
	agentEvent := eventing.NewAgentLogMessage("ch-1", "hi")
	if err := s.Append(agentEvent); err != nil {
		t.Fatal(err)
	}

	// Append user message
	userEvent := eventing.NewLogMessage("ch-1", "hello")
	if err := s.Append(userEvent); err != nil {
		t.Fatal(err)
	}

	latest, err = s.GetLatestUserMessage()
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil {
		t.Fatal("expected user message, got nil")
	}
	if latest.EventType != eventing.LogMessageUserCreated {
		t.Errorf("expected %s, got %s", eventing.LogMessageUserCreated, latest.EventType)
	}
}

func TestEventSessionStore_GetLatestAgentResponse(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	// No agent responses yet
	latest, err := s.GetLatestAgentResponse()
	if err != nil {
		t.Fatal(err)
	}
	if latest != nil {
		t.Fatalf("expected nil, got %+v", latest)
	}

	// Append user message first
	userEvent := eventing.NewLogMessage("ch-1", "hello")
	if err := s.Append(userEvent); err != nil {
		t.Fatal(err)
	}

	// Append agent response
	agentEvent := eventing.NewAgentLogMessage("ch-1", "done")
	if err := s.Append(agentEvent); err != nil {
		t.Fatal(err)
	}

	latest, err = s.GetLatestAgentResponse()
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil {
		t.Fatal("expected agent response, got nil")
	}
	if latest.EventType != eventing.LogMessageAgentCreated {
		t.Errorf("expected %s, got %s", eventing.LogMessageAgentCreated, latest.EventType)
	}
}

func TestEventSessionStore_Persistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")

	// First session: append events
	eventsLog1, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Append(eventing.NewLogMessage("ch-1", "hello")); err != nil {
		t.Fatal(err)
	}

	// Second session: reload
	eventsLog2, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog2)
	if err != nil {
		t.Fatal(err)
	}

	latest, _ := s2.GetLatest()
	if latest == nil {
		t.Fatal("expected latest event after reload")
	}

	var payload eventing.LogMessage
	if err := json.Unmarshal([]byte(latest.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Content != "hello" {
		t.Errorf("expected content hello, got %s", payload.Content)
	}
}

func TestEventSessionStore_Versioning_SameOriginator(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	// Same originator — should use optimized in-memory path
	e1 := eventing.NewLogMessage("ch-1", "first")
	if err := s.Append(e1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if e1.Originator.Version != 1 {
		t.Errorf("expected version 1, got %d", e1.Originator.Version)
	}

	e2 := eventing.NewLogMessage("ch-1", "second")
	if err := s.Append(e2); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if e2.Originator.Version != 2 {
		t.Errorf("expected version 2, got %d", e2.Originator.Version)
	}

	e3 := eventing.NewAgentLogMessage("ch-1", "third")
	if err := s.Append(e3); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if e3.Originator.Version != 3 {
		t.Errorf("expected version 3, got %d", e3.Originator.Version)
	}
}

func TestEventSessionStore_Versioning_DifferentOriginator(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	// Event for originator A
	eA1 := eventing.NewLogMessage("originator-a", "hello A")
	if err := s.Append(eA1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if eA1.Originator.Version != 1 {
		t.Errorf("expected originator-a version 1, got %d", eA1.Originator.Version)
	}

	// Event for originator B — latestEvent now points to B
	eB1 := eventing.NewLogMessage("originator-b", "hello B")
	if err := s.Append(eB1); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if eB1.Originator.Version != 1 {
		t.Errorf("expected originator-b version 1, got %d", eB1.Originator.Version)
	}

	// Another event for originator A — in-memory latest is B, so must fall back to file lookup
	eA2 := eventing.NewLogMessage("originator-a", "hello A again")
	if err := s.Append(eA2); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if eA2.Originator.Version != 2 {
		t.Errorf("expected originator-a version 2, got %d", eA2.Originator.Version)
	}

	// Another event for originator B — in-memory latest is A, so must fall back to file lookup
	eB2 := eventing.NewLogMessage("originator-b", "hello B again")
	if err := s.Append(eB2); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	if eB2.Originator.Version != 2 {
		t.Errorf("expected originator-b version 2, got %d", eB2.Originator.Version)
	}
}

func TestEventSessionStore_GetLatestAgentResponseForUserMessage_Stub(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "event-session-store-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	storePath := path.Join(tmpDir, "events.jsonpl")
	eventsLog, err := eventstore.NewFileMemoryStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}

	userEvent := eventing.NewLogMessage("ch-1", "hello")
	res, err := s.GetLatestAgentResponseForUserMessage(userEvent)
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatalf("expected nil stub result, got %+v", res)
	}
}

func TestEventSessionStore_GetLatestAgentResponseForUserMessageUsesLatestMatch(t *testing.T) {
	tmpDir := t.TempDir()
	eventsLog, err := eventstore.NewFileMemoryStore(path.Join(tmpDir, "events.jsonpl"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewEventSessionStore("test", "ch-1", tmpDir, eventsLog)
	if err != nil {
		t.Fatal(err)
	}
	user := eventing.NewLogMessageWithID("ch-1", "user-1", "hello")
	if err := store.Append(user); err != nil {
		t.Fatal(err)
	}
	interrupted := eventing.NewAgentInterruptedLogMessage("ch-1", "interrupted-1", "user-1", "recovery-1", "provider_error", eventing.InterruptedAfterToolsMessage, nil)
	if err := store.Append(interrupted); err != nil {
		t.Fatal(err)
	}
	completed := eventing.NewAgentLogMessageInReplyTo("ch-1", "agent-1", "user-1", "done")
	if err := store.Append(completed); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetLatestAgentResponseForUserMessage(user)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.EventType != eventing.LogMessageAgentCreated {
		t.Fatalf("latest response = %+v, want %s", got, eventing.LogMessageAgentCreated)
	}
}

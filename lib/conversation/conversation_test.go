package conversation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type fakeSession struct {
	mu               sync.Mutex
	events           []*types.Event
	processErrs      []error
	processCalls     int
	processedMessage chan string
	eventAppended    chan *types.Event
}

func (s *fakeSession) OnContextEvent(string, eventing.HandleFunc) error { return nil }
func (s *fakeSession) OnAuditEvent(string, eventing.HandleFunc) error   { return nil }

func newFakeSession() *fakeSession {
	return &fakeSession{
		processedMessage: make(chan string, 10),
		eventAppended:    make(chan *types.Event, 10),
	}
}

func (s *fakeSession) PendingUserEvents() ([]*types.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replied := map[string]bool{}
	for _, event := range s.events {
		if event.EventType != eventing.LogMessageAgentCreated {
			continue
		}
		message, err := eventing.DecodeLogMessage(event)
		if err == nil && message.InReplyTo != "" {
			replied[message.InReplyTo] = true
		}
	}
	var pending []*types.Event
	for _, event := range s.events {
		if event.EventType != eventing.LogMessageUserCreated {
			continue
		}
		message, err := eventing.DecodeLogMessage(event)
		if err == nil && !replied[message.MessageID] {
			pending = append(pending, event)
		}
	}
	return pending, nil
}

func (s *fakeSession) ProcessUserEvent(_ context.Context, event *types.Event) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processCalls++
	if len(s.processErrs) > 0 {
		err := s.processErrs[0]
		s.processErrs = s.processErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	message, err := eventing.DecodeLogMessage(event)
	if err != nil {
		return nil, err
	}
	agent := eventing.NewAgentLogMessageInReplyTo(event.Originator.ID, "agent-"+message.MessageID, message.MessageID, "response")
	s.events = append(s.events, agent)
	s.processedMessage <- message.MessageID
	return &llm.Response{}, nil
}

func (s *fakeSession) AppendAuditEvent(event *types.Event) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	s.eventAppended <- event
	return nil
}

func (s *fakeSession) AuditEvents() ([]*types.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*types.Event(nil), s.events...), nil
}

func TestRuntimeCreatesOneWorkerPerChannelAndProcessesPending(t *testing.T) {
	session := newFakeSession()
	session.events = append(session.events, eventing.NewLogMessageWithID("ch-1", "msg-1", "hello"))
	openCalls := 0
	runtime := NewRuntime(Options{
		OpenSession: func(channelID string) (Session, error) {
			openCalls++
			if channelID != "ch-1" {
				t.Fatalf("channelID = %q, want ch-1", channelID)
			}
			return session, nil
		},
	})
	defer runtime.Stop()

	if err := runtime.Wake(context.Background(), "ch-1"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Wake(context.Background(), "ch-1"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-session.processedMessage:
	case <-time.After(time.Second):
		t.Fatal("message was not processed")
	}
	if openCalls != 1 {
		t.Fatalf("openCalls = %d, want 1", openCalls)
	}
}

func TestRuntimeRetriesAndAppendsTerminalFailure(t *testing.T) {
	session := newFakeSession()
	session.events = append(session.events, eventing.NewLogMessageWithID("ch-1", "msg-1", "hello"))
	session.processErrs = []error{errors.New("one"), errors.New("two"), errors.New("three")}
	runtime := NewRuntime(Options{
		OpenSession: func(string) (Session, error) { return session, nil },
	})
	defer runtime.Stop()

	if err := runtime.Wake(context.Background(), "ch-1"); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-session.eventAppended:
		if event.EventType != eventing.LogMessageAgentCreated {
			t.Fatalf("terminal event type = %s", event.EventType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal failure was not appended")
	}
	if session.processCalls != 3 {
		t.Fatalf("processCalls = %d, want 3", session.processCalls)
	}
}

func TestRuntimeExecuteSerializesTasks(t *testing.T) {
	session := newFakeSession()
	runtime := NewRuntime(Options{
		OpenSession: func(string) (Session, error) { return session, nil },
	})
	defer runtime.Stop()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- runtime.Execute(context.Background(), "ch-1", func(context.Context, Session) error {
			close(firstStarted)
			<-releaseFirst
			return session.AppendAuditEvent(eventing.NewAgentLogMessage("ch-1", "first"))
		})
	}()
	<-firstStarted

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- runtime.Execute(context.Background(), "ch-1", func(context.Context, Session) error {
			close(secondStarted)
			return nil
		})
	}()
	select {
	case <-secondStarted:
		t.Fatal("second task started while first task was still running")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeExecuteHonorsCancelledContext(t *testing.T) {
	runtime := NewRuntime(Options{OpenSession: func(string) (Session, error) { return newFakeSession(), nil }})
	defer runtime.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := runtime.Execute(ctx, "ch-1", func(context.Context, Session) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("Execute error/called = %v/%t, want cancelled/false", err, called)
	}
}

func TestRuntimeExecuteContainsTaskPanicAndKeepsWorkerAlive(t *testing.T) {
	runtime := NewRuntime(Options{OpenSession: func(string) (Session, error) { return newFakeSession(), nil }})
	defer runtime.Stop()
	err := runtime.Execute(context.Background(), "ch-1", func(context.Context, Session) error {
		panic("boom")
	})
	if err == nil || !strings.Contains(err.Error(), "conversation task panicked: boom") {
		t.Fatalf("panic error = %v", err)
	}
	if err := runtime.Execute(context.Background(), "ch-1", func(context.Context, Session) error { return nil }); err != nil {
		t.Fatalf("worker did not survive task panic: %v", err)
	}
}

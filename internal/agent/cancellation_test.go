package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ctxstore "github.com/makkalot/caretz/internal/context"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/turnscope"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type cancellationProvider struct {
	mockProvider
	chat func(context.Context) (llm.Response, error)
}

func (p *cancellationProvider) ChatContext(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	return p.chat(ctx)
}

func TestCancellationDiscardsLateModelResponse(t *testing.T) {
	for _, toolResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "final", true: "tools"}[toolResponse], func(t *testing.T) {
			session, _, cleanup := setupAgentSession(t, nil)
			defer cleanup()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			session.llmProvider = &cancellationProvider{chat: func(context.Context) (llm.Response, error) {
				calls++
				cancel()
				msg := llm.Message{Role: llm.RoleAssistant, Content: "discard me"}
				if toolResponse {
					msg.ToolCalls = []llm.ToolCall{{ID: "never", Name: "Bash", Arguments: `{"command":"false"}`}}
				}
				return llm.Response{Message: msg, Content: msg.Content, ToolCalls: msg.ToolCalls}, nil
			}}
			if _, err := session.Call(ctx, "wrong direction"); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			messages := session.GetContextMessages()
			if len(messages) != 2 || !strings.Contains(messages[1].Content, "stopped by the user") {
				t.Fatalf("messages = %+v", messages)
			}
			if calls != 1 {
				t.Fatalf("model calls = %d", calls)
			}
		})
	}
}

func TestCancellationAfterFinalPersistenceCompletesAudit(t *testing.T) {
	final := llm.Message{Role: llm.RoleAssistant, Content: "already complete"}
	session, _, cleanup := setupAgentSession(t, []llm.Response{{Message: final, Content: final.Content}})
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := session.OnContextEvent(eventing.LLMAgentResponseCreated, func(*types.Event) error { cancel(); return nil }); err != nil {
		t.Fatal(err)
	}
	response, err := session.Call(ctx, "finish")
	if err != nil || response == nil {
		t.Fatalf("response = %v, error = %v", response, err)
	}
	events, err := session.GetEvents()
	if err != nil {
		t.Fatal(err)
	}
	var user *types.Event
	for _, e := range events {
		if e.EventType == eventing.LogMessageUserCreated {
			user = e
		}
	}
	if user == nil {
		t.Fatal("missing user")
	}
	stored, err := session.eventSessionStore.GetLatestAgentResponseForUserMessage(user)
	if err != nil || stored == nil || stored.EventType == eventing.LogMessageAgentInterrupted {
		t.Fatalf("audit response = %v, error = %v", stored, err)
	}
	if _, err := session.ProcessUserEvent(t.Context(), user); err != nil {
		t.Fatalf("retry reran completed turn: %v", err)
	}
}

func TestCancellationAfterUserAuditPersistsInterruption(t *testing.T) {
	session, _, cleanup := setupAgentSession(t, nil)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := session.OnAuditEvent(eventing.LogMessageUserCreated, func(*types.Event) error { cancel(); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Call(ctx, "stop before preflight"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	messages := session.GetContextMessages()
	if len(messages) != 2 || messages[0].Content != "stop before preflight" || !strings.Contains(messages[1].Content, "stopped by the user") {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestCancellationPreservesPartialToolOutputAndJobNotes(t *testing.T) {
	tc := llm.ToolCall{ID: "partial", Name: "Bash", Arguments: `{"command":"build"}`}
	session, _, cleanup := setupAgentSession(t, []llm.Response{{ToolCalls: []llm.ToolCall{tc}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{tc}}}})
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session.toolRegistry.RunSandboxedOptions = func(ctx context.Context, _, _ string, _ sandbox.RunOptions) (string, string, error) {
		turnscope.From(ctx).Register(func() (string, error) { return "Stopped background job PID 123.", nil })
		cancel()
		return "compiled first file\n", "warning\n", ctx.Err()
	}
	if _, err := session.Call(ctx, "build"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	messages := session.GetContextMessages()
	if len(messages) != 4 || !strings.Contains(messages[2].Content, "compiled first file") || !strings.Contains(messages[2].Content, "warning") || !strings.Contains(messages[3].Content, "PID 123") {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestCancellationWhileWaitingPersistsNothing(t *testing.T) {
	session, _, cleanup := setupAgentSession(t, nil)
	defer cleanup()
	session.executionMu.Lock()
	defer session.executionMu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := session.Call(ctx, "waiting"); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("waiting prompt ignored cancellation")
	}
	events, err := session.GetEvents()
	if err != nil || len(events) != 0 {
		t.Fatalf("events = %v, error = %v", events, err)
	}
}

func TestIsCleanStopKeepsCleanupFailuresVisible(t *testing.T) {
	if !IsCleanStop(fmt.Errorf("turn stopped: %w", errors.Join(context.Canceled, context.Canceled))) {
		t.Fatal("clean stop was not recognized")
	}
	if IsCleanStop(errors.Join(context.Canceled, errors.New("cancel ack timed out"))) {
		t.Fatal("cleanup failure was suppressed")
	}
	if IsCleanStop(errors.Join(context.Canceled, errors.New("persist interrupted response failed"))) {
		t.Fatal("persistence failure was suppressed")
	}
}

func TestCancellationInterruptsBlockedModelPromptly(t *testing.T) {
	session, _, cleanup := setupAgentSession(t, nil)
	defer cleanup()
	var thinkingStarted, thinkingFinished bool
	if err := session.OnContextEvent(eventing.LLMReasoningDeltaCreated, func(event *types.Event) error {
		var update eventing.LLMReasoningDelta
		if err := json.Unmarshal([]byte(event.Payload), &update); err != nil {
			return err
		}
		thinkingStarted = thinkingStarted || update.Started
		thinkingFinished = thinkingFinished || update.Done
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	session.llmProvider = &cancellationProvider{chat: func(ctx context.Context) (llm.Response, error) {
		close(entered)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := session.Call(ctx, "wrong direction"); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !IsCleanStop(err) {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("model cancellation exceeded 250 ms")
	}
	if !thinkingStarted || !thinkingFinished {
		t.Fatalf("thinking lifecycle after cancellation: started=%v finished=%v", thinkingStarted, thinkingFinished)
	}
}

func TestCancellationInterruptsHTTPStreamingAndRetry(t *testing.T) {
	for _, mode := range []string{"stream", "retry"} {
		t.Run(mode, func(t *testing.T) {
			session, _, cleanup := setupAgentSession(t, nil)
			defer cleanup()
			ready := make(chan struct{})
			var once sync.Once
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if mode == "retry" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"message":"retry later"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			session.llmProvider = llm.NewOpenAIProviderWithOptions("test", server.URL+"/v1", "mock", llm.OpenAIProviderOptions{InitialBackoff: time.Minute, MaxBackoff: time.Minute})
			if mode == "stream" {
				session.SetAssistantDeltaHandler(func(delta string, _ bool) {
					if delta != "" {
						once.Do(func() { close(ready) })
					}
				})
			} else {
				if err := session.OnContextEvent(eventing.LLMRequestRetryScheduled, func(*types.Event) error { once.Do(func() { close(ready) }); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := session.Call(ctx, "stop stream or retry"); done <- err }()
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("model did not reach cancellation stage")
			}
			beforeCancel := calls.Load()
			cancel()
			select {
			case err := <-done:
				if !IsCleanStop(err) {
					t.Fatal(err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("HTTP cancellation exceeded 250 ms")
			}
			if calls.Load() != beforeCancel {
				t.Fatalf("HTTP requests increased after cancellation: before=%d after=%d", beforeCancel, calls.Load())
			}
		})
	}
}

type cancelledPreflightManager struct {
	ctxstore.Manager
	cancel context.CancelFunc
}

func (m cancelledPreflightManager) IsCompactionRequired([]llm.Message, []llm.ToolDefinition) bool {
	return true
}
func (m cancelledPreflightManager) GetSummaryPromptEventWithActiveRequest(string, string, uint64, string, string) (*types.Event, error) {
	m.cancel()
	return nil, context.Canceled
}

func TestCancellationDuringPreflightPersistsUserAndInterruption(t *testing.T) {
	session, _, cleanup := setupAgentSession(t, nil)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session.contextManager = cancelledPreflightManager{Manager: session.contextManager, cancel: cancel}
	if _, err := session.Call(ctx, "cancel during compaction"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	messages := session.GetContextMessages()
	if len(messages) != 2 || messages[0].Content != "cancel during compaction" || !strings.Contains(messages[1].Content, "stopped by the user") {
		t.Fatalf("messages = %+v", messages)
	}
	stored, err := session.contextManager.RestoreContext(session.ChannelID())
	if err != nil || len(stored) != 2 {
		t.Fatalf("restored = %+v, error = %v", stored, err)
	}
}

func TestCancellationStopsOwnedAsyncCommand(t *testing.T) {
	session, _, cleanup := setupAgentSession(t, nil)
	defer cleanup()
	defer func() { _ = session.Close() }()
	started := make(chan struct{})
	session.toolRegistry.RunSandboxedOptions = func(ctx context.Context, _, _ string, _ sandbox.RunOptions) (string, string, error) {
		close(started)
		<-ctx.Done()
		return "partial", "", ctx.Err()
	}
	completed, err := session.SubscribeAuditEvents(eventing.AsyncRunCompleted)
	if err != nil {
		t.Fatal(err)
	}
	defer completed.Close()
	scope := &turnscope.Scope{}
	request := eventing.AsyncRunRequest{RequestID: "owned-command", Mode: eventing.AsyncRunCommand, Prompt: "sleep 60"}
	if err := session.PublishAsyncEvent(turnscope.With(t.Context(), scope), eventing.NewAsyncRunRequested(session.ChannelID(), request)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("async command did not start")
	}
	if _, err := scope.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-completed.Events():
		result, err := eventing.DecodeAsyncRunResult(event)
		if err != nil || result.Status != "cancelled" {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	case <-time.After(time.Second):
		t.Fatal("async command did not finish cancellation")
	}
	if len(session.GetContextMessages()) != 0 {
		t.Fatal("cancelled async command started a report turn")
	}
}

func TestCancellationStopsOwnedAsyncSubagentWaitingForSession(t *testing.T) {
	session, _, cleanup := setupAgentSession(t, nil)
	defer cleanup()
	defer func() { _ = session.Close() }()
	session.executionMu.Lock()
	scope := &turnscope.Scope{}
	request := eventing.AsyncRunRequest{RequestID: "owned-child", Mode: eventing.AsyncRunSubagent, Prompt: "wrong direction"}
	if err := session.PublishAsyncEvent(turnscope.With(t.Context(), scope), eventing.NewAsyncRunRequested(session.ChannelID(), request)); err != nil {
		session.executionMu.Unlock()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := scope.Stop(); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		session.executionMu.Unlock()
		t.Fatal("cancelled async child waited for execution lock")
	}
	session.executionMu.Unlock()
}

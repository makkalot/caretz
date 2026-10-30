package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

// newOutsideReadSession returns a session whose first model turn reads a file
// outside the workspace, which needs permission.
func newOutsideReadSession(t *testing.T) *AgentSession {
	t.Helper()
	workspace := t.TempDir()
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]string{"file_path": path})
	call := llm.ToolCall{ID: "read-outside", Name: "Read", Arguments: string(arguments)}
	provider := &mockProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
		{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	session, err := NewAgentSession("test", "async-turn", SessionParams{
		InMemorySessionStore: true,
		WorkspaceDir:         workspace,
		Provider:             provider,
		ToolRegistry:         &tools.Registry{WorkspaceDir: workspace},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func publishForegroundAsyncRun(t *testing.T, session *AgentSession) eventing.Subscription {
	t.Helper()
	completed, err := session.SubscribeAuditEvents(eventing.AsyncRunCompleted)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(completed.Close)
	request := eventing.AsyncRunRequest{RequestID: "request-1", Source: "test", Prompt: "read it", Timeout: 5 * time.Second}
	if err := session.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunRequested(session.ChannelID(), request)); err != nil {
		t.Fatal(err)
	}
	return completed
}

func waitAsyncResult(t *testing.T, completed eventing.Subscription) eventing.AsyncRunResult {
	t.Helper()
	select {
	case event := <-completed.Events():
		result, err := eventing.DecodeAsyncRunResult(event)
		if err != nil {
			t.Fatal(err)
		}
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("async run did not complete")
		return eventing.AsyncRunResult{}
	}
}

func TestAsyncTurnRunnerAsksForPermission(t *testing.T) {
	session := newOutsideReadSession(t)
	ran := make(chan struct{}, 1)
	session.SetAsyncTurnRunner(func(ctx context.Context, turn func(context.Context, <-chan struct{}) error) error {
		ran <- struct{}{}
		return turn(context.Background(), nil)
	})
	completed := publishForegroundAsyncRun(t, session)

	request := waitForPendingPermission(t, session)
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: request.RequestID, Decision: eventing.PermissionDecisionAllow}); err != nil {
		t.Fatal(err)
	}
	if result := waitAsyncResult(t, completed); result.Status != "succeeded" || result.Response != "done" {
		t.Fatalf("result = %+v", result)
	}
	select {
	case <-ran:
	default:
		t.Fatal("runner was not used")
	}
}

func TestAsyncTurnRunnerContextCancelsTurn(t *testing.T) {
	session := newOutsideReadSession(t)
	frontendCtx, cancel := context.WithCancel(context.Background())
	session.SetAsyncTurnRunner(func(_ context.Context, turn func(context.Context, <-chan struct{}) error) error {
		return turn(frontendCtx, nil)
	})
	completed := publishForegroundAsyncRun(t, session)

	waitForPendingPermission(t, session)
	cancel() // Ctrl-C in the terminal
	if result := waitAsyncResult(t, completed); result.Status != "cancelled" {
		t.Fatalf("result = %+v, want cancelled", result)
	}
}

func TestAsyncTurnWithoutRunnerDeniesWithoutAsking(t *testing.T) {
	session := newOutsideReadSession(t)
	completed := publishForegroundAsyncRun(t, session)
	if result := waitAsyncResult(t, completed); result.Status != "succeeded" {
		t.Fatalf("result = %+v", result)
	}
	if pending := session.PendingPermissions(); len(pending) != 0 {
		t.Fatalf("headless run asked for permission: %+v", pending)
	}
}

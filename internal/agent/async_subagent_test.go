package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/turnscope"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type asyncSubagentProvider struct {
	subagentModelTestProvider
	chat func(context.Context, []llm.Message) (llm.Response, error)
}

func (p *asyncSubagentProvider) ChatContext(ctx context.Context, _ string, messages []llm.Message, _ []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	return p.chat(ctx, messages)
}

func asyncTestResponse(text string) llm.Response {
	return llm.Response{Content: text, Message: llm.Message{Role: llm.RoleAssistant, Content: text}}
}

func newAsyncSubagentTestSession(t *testing.T, child llm.Provider, registry *tools.Registry) *AgentSession {
	t.Helper()
	if registry == nil {
		registry = &tools.Registry{Logger: log.New(), WorkspaceDir: t.TempDir()}
	}
	parent := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) {
		return asyncTestResponse("parent reply"), nil
	}}
	session, err := NewAgentSession("test", "async-parent", SessionParams{
		BaseDir: filepath.Join(t.TempDir(), "sessions"), WorkspaceDir: registry.WorkspaceDir,
		Logger: registry.Logger, Provider: parent, ToolRegistry: registry, SystemPrompt: "parent prompt",
		ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	session.SetSubagentModelResolver(subagentModelTestResolver{models: map[string]SubagentModelResolution{
		"child": {Model: "child", Provider: child, ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000, SummaryTokens: 2000},
	}})
	return session
}

func requestAsyncSubagent(t *testing.T, ctx context.Context, session *AgentSession, id string) {
	t.Helper()
	request := eventing.AsyncRunRequest{RequestID: id, Mode: eventing.AsyncRunSubagent, Model: "child", Prompt: id, Timeout: 10 * time.Second}
	if err := session.PublishAsyncEvent(ctx, eventing.NewAsyncRunRequested(session.ChannelID(), request)); err != nil {
		t.Fatal(err)
	}
}

func awaitAsyncSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for background work")
	}
}

func subscribeAsyncTest(t *testing.T, session *AgentSession, eventType string) <-chan *types.Event {
	t.Helper()
	sub, err := session.SubscribeAuditEvents(eventType)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub.Events()
}

func awaitAsyncResult(t *testing.T, events <-chan *types.Event, status string) {
	t.Helper()
	select {
	case event := <-events:
		result, err := eventing.DecodeAsyncRunResult(event)
		if err != nil || result.Status != status {
			t.Fatalf("result = %+v, error = %v, want %s", result, err, status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background run did not complete")
	}
}

func TestAsyncSubagentDoesNotBlockUserTurns(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	child := &asyncSubagentProvider{chat: func(ctx context.Context, _ []llm.Message) (llm.Response, error) {
		close(started)
		select {
		case <-release:
			return asyncTestResponse("child result"), nil
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}}
	session := newAsyncSubagentTestSession(t, child, nil)
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	requestAsyncSubagent(t, t.Context(), session, "background task")
	awaitAsyncSignal(t, started)
	user := eventing.NewLogMessageWithID(session.ChannelID(), "user-1", "hello during background work")
	if err := session.AppendAuditEvent(user); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := session.ProcessUserEvent(ctx, user); err != nil {
		t.Fatalf("user turn blocked or failed: %v", err)
	}
	close(release)
	awaitAsyncResult(t, completed, "succeeded")
	messages := session.GetContextMessages()
	if len(messages) != 4 || messages[0].Content != "hello during background work" || messages[1].Content != "parent reply" || !strings.Contains(messages[2].Content, "child result") || messages[3].Content != "parent reply" {
		t.Fatalf("parent conversation = %+v", messages)
	}
}

func TestAsyncSubagentsRunConcurrentlyInOneSession(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	child := &asyncSubagentProvider{chat: func(ctx context.Context, _ []llm.Message) (llm.Response, error) {
		started <- struct{}{}
		select {
		case <-release:
			return asyncTestResponse("done"), nil
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}}
	session := newAsyncSubagentTestSession(t, child, nil)
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	requestAsyncSubagent(t, t.Context(), session, "first")
	awaitAsyncSignal(t, started)
	requestAsyncSubagent(t, t.Context(), session, "second")
	awaitAsyncSignal(t, started)
	close(release)
	awaitAsyncResult(t, completed, "succeeded")
	awaitAsyncResult(t, completed, "succeeded")
}

func TestAsyncSubagentsRaceSafelyWithRuntimeChanges(t *testing.T) {
	child := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) {
		return asyncTestResponse("done"), nil
	}}
	session := newAsyncSubagentTestSession(t, child, nil)
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	registry := session.ToolRegistry()
	stop, stopped := make(chan struct{}), make(chan struct{})
	defer func() { close(stop); <-stopped }()
	go func() {
		defer close(stopped)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := registry.AddPermissionGrant(tools.PermissionNetwork, "example.com:443", false); err != nil {
				t.Error(err)
				return
			}
			if err := session.withExecution(func() error {
				if err := session.SwitchModel(session.currentProvider(), 128000+i%2, 16384, 20000); err != nil {
					return err
				}
				return session.SwitchRuntime(registry, fmt.Sprintf("prompt %d", i))
			}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := range 8 {
		requestAsyncSubagent(t, t.Context(), session, fmt.Sprintf("run-%d", i))
	}
	for range 8 {
		awaitAsyncResult(t, completed, "succeeded")
	}
}

func TestCancellationStopsRunningOwnedAsyncSubagent(t *testing.T) {
	started := make(chan struct{})
	child := &asyncSubagentProvider{chat: func(ctx context.Context, _ []llm.Message) (llm.Response, error) {
		close(started)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}}
	session := newAsyncSubagentTestSession(t, child, nil)
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	scope := &turnscope.Scope{}
	requestAsyncSubagent(t, turnscope.With(t.Context(), scope), session, "owned-child")
	awaitAsyncSignal(t, started)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if _, err := scope.Stop(); err != nil {
			t.Error(err)
		}
	}()
	awaitAsyncSignal(t, stopped)
	awaitAsyncResult(t, completed, "cancelled")
	if len(session.GetContextMessages()) != 0 {
		t.Fatal("cancelled child started a report turn")
	}
}

func TestAsyncSubagentHasIsolatedShellAndLivePathGrants(t *testing.T) {
	ready, release := make(chan struct{}), make(chan struct{})
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: workspace, SessionExec: true, SandboxDisabled: true,
		NewSandboxSession: func() *sandbox.Session { return sandbox.NewRunner(sandbox.Config{}).NewSession(true) },
	}
	calls := 0
	child := &asyncSubagentProvider{chat: func(ctx context.Context, messages []llm.Message) (llm.Response, error) {
		calls++
		switch calls {
		case 1:
			return asyncBashResponse("seed", `printf child > "$TMPDIR/child-marker"; cd /; pwd`), nil
		case 2:
			if last := messages[len(messages)-1].Content; strings.TrimSpace(last) != "/" {
				return llm.Response{}, fmt.Errorf("child Bash failed: %s", last)
			}
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
			// A new grant recreates this child's supervisor on its next Bash.
			return asyncBashResponse("refresh", `test ! -e "$TMPDIR/child-marker" && printf refreshed`), nil
		default:
			last := messages[len(messages)-1].Content
			if !strings.Contains(last, "Session execution supervisor was recreated") || !strings.HasSuffix(last, "refreshed") {
				return llm.Response{}, fmt.Errorf("child missed live grant: %s", last)
			}
			return asyncTestResponse("isolated and refreshed"), nil
		}
	}}
	session := newAsyncSubagentTestSession(t, child, registry)
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	requestAsyncSubagent(t, t.Context(), session, "shell-test")
	awaitAsyncSignal(t, ready)
	result, err := session.ToolRegistry().HandleToolCallContext(t.Context(), llm.ToolCall{ID: "parent", Name: "Bash", Arguments: `{"command":"test ! -e \"$TMPDIR/child-marker\" && pwd"}`})
	if err != nil || strings.TrimSpace(result.Content) != workspace {
		t.Fatalf("child shell leaked into parent: result=%+v error=%v", result, err)
	}
	if err := session.ToolRegistry().AddPermissionGrant(tools.PermissionPathRead, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitAsyncResult(t, completed, "succeeded")
}

func asyncBashResponse(id, command string) llm.Response {
	call := llm.ToolCall{ID: id, Name: "Bash", Arguments: fmt.Sprintf(`{"command":%q}`, command)}
	return llm.Response{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}}
}

func TestAsyncSubagentReportWaitDoesNotBlockScopeCleanup(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var ownedDone <-chan struct{}
	child := &asyncSubagentProvider{chat: func(ctx context.Context, _ []llm.Message) (llm.Response, error) {
		ownedDone = ctx.Value(ownedAsyncKey{}).(*ownedAsyncRun).done
		close(started)
		select {
		case <-release:
			return asyncTestResponse("done"), nil
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}}
	session := newAsyncSubagentTestSession(t, child, nil)
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	scope := &turnscope.Scope{}
	requestAsyncSubagent(t, turnscope.With(t.Context(), scope), session, "owned-report")
	awaitAsyncSignal(t, started)
	session.executionMu.Lock()
	var unlock sync.Once
	defer unlock.Do(session.executionMu.Unlock)
	childCompleted := subscribeAsyncTest(t, session, eventing.SubagentInvocationCompleted)
	close(release)
	select {
	case <-childCompleted:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not finish")
	}
	awaitAsyncSignal(t, ownedDone)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if _, err := scope.Stop(); err != nil {
			t.Error(err)
		}
	}()
	awaitAsyncSignal(t, stopped)
	unlock.Do(session.executionMu.Unlock)
	awaitAsyncResult(t, completed, "succeeded")
}

// countingAsyncProvider answers every request with reply, or fails with err,
// and counts the requests it got.
func countingAsyncProvider(model string, reply string, err error) (*asyncSubagentProvider, *int) {
	var mu sync.Mutex
	calls := 0
	provider := &asyncSubagentProvider{subagentModelTestProvider: subagentModelTestProvider{model: model}, chat: func(context.Context, []llm.Message) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if err != nil {
			return llm.Response{}, err
		}
		return asyncTestResponse(reply), nil
	}}
	return provider, &calls
}

func TestAsyncSubagentStartsOverOnFallbackWhenModelUnreachable(t *testing.T) {
	local, localCalls := countingAsyncProvider("local-code", "", errUnreachable)
	cloud, cloudCalls := countingAsyncProvider("cloud-mini", "cloud result", nil)
	session := newAsyncSubagentTestSession(t, local, nil)
	session.SetSubagentModelResolver(subagentModelTestResolver{models: map[string]SubagentModelResolution{
		"local/code": {Model: "local-code", Provider: local, ContextWindow: 64000, ReservedTokens: 8000, KeepRecentTokens: 10000, SummaryTokens: 2000},
		"cloud/mini": {Model: "cloud-mini", Provider: cloud, ContextWindow: 200000, ReservedTokens: 16384, KeepRecentTokens: 20000, SummaryTokens: 2000},
	}})
	started := subscribeAsyncTest(t, session, eventing.SubagentInvocationStarted)
	statuses := subscribeAsyncTest(t, session, eventing.StatusUpdated)
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)

	request := eventing.AsyncRunRequest{
		RequestID: "scheduled", Source: "schedules", Mode: eventing.AsyncRunSubagent, Prompt: "check the build", Timeout: 10 * time.Second,
		Model: "local/code", ContextLimits: &eventing.ContextLimits{Context: 32000},
		Fallback: []eventing.AsyncRunModel{{Model: "cloud/mini"}, {}},
	}
	if err := session.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunRequested(session.ChannelID(), request)); err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "succeeded")

	if *localCalls != 1 || *cloudCalls != 1 {
		t.Fatalf("calls: local %d, cloud %d; want 1, 1", *localCalls, *cloudCalls)
	}
	var invocations []eventing.SubagentInvocation
	for len(invocations) < 2 {
		select {
		case event := <-started:
			var invocation eventing.SubagentInvocation
			if err := json.Unmarshal([]byte(event.Payload), &invocation); err != nil {
				t.Fatal(err)
			}
			invocations = append(invocations, invocation)
		case <-time.After(time.Second):
			t.Fatalf("started events = %+v, want two", invocations)
		}
	}
	// The first child ran with the schedule's limits, the fresh one with the
	// fallback model's own.
	if invocations[0].Model != "local/code" || invocations[0].ContextWindow != 32000 || invocations[1].Model != "cloud/mini" || invocations[1].ContextWindow != 200000 {
		t.Fatalf("started = %+v, want local/code at 32000 then cloud/mini at 200000", invocations)
	}
	if invocations[0].ChildSessionID == invocations[1].ChildSessionID {
		t.Fatal("fallback reused the failed child")
	}
	select {
	case event := <-statuses:
		if !strings.Contains(event.Payload, "local/code unreachable") || !strings.Contains(event.Payload, "cloud/mini") {
			t.Fatalf("status = %s, want the unreachable notice", event.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("no unreachable notice")
	}
}

func TestAsyncSubagentFallsBackToSessionModel(t *testing.T) {
	local, localCalls := countingAsyncProvider("local-code", "", errUnreachable)
	session := newAsyncSubagentTestSession(t, local, nil)
	session.SetSubagentModelResolver(subagentModelTestResolver{models: map[string]SubagentModelResolution{
		"local/code": {Model: "local-code", Provider: local, ContextWindow: 64000, ReservedTokens: 8000, KeepRecentTokens: 10000, SummaryTokens: 2000},
	}})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	request := eventing.AsyncRunRequest{RequestID: "scheduled", Source: "schedules", Mode: eventing.AsyncRunSubagent, Prompt: "check the build", Timeout: 10 * time.Second, Model: "local/code", Fallback: []eventing.AsyncRunModel{{}}}
	if err := session.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunRequested(session.ChannelID(), request)); err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "succeeded")
	if *localCalls != 1 {
		t.Fatalf("local calls = %d, want 1", *localCalls)
	}
}

func TestAsyncSubagentFailsWhenEveryModelUnreachable(t *testing.T) {
	local, localCalls := countingAsyncProvider("local-code", "", errUnreachable)
	cloud, cloudCalls := countingAsyncProvider("cloud-mini", "", errUnreachable)
	session := newAsyncSubagentTestSession(t, local, nil)
	session.SetSubagentModelResolver(subagentModelTestResolver{models: map[string]SubagentModelResolution{
		"local/code": {Model: "local-code", Provider: local, ContextWindow: 64000, ReservedTokens: 8000, KeepRecentTokens: 10000, SummaryTokens: 2000},
		"cloud/mini": {Model: "cloud-mini", Provider: cloud, ContextWindow: 64000, ReservedTokens: 8000, KeepRecentTokens: 10000, SummaryTokens: 2000},
	}})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	request := eventing.AsyncRunRequest{RequestID: "scheduled", Source: "schedules", Mode: eventing.AsyncRunSubagent, Prompt: "check the build", Timeout: 10 * time.Second, Model: "local/code", Fallback: []eventing.AsyncRunModel{{Model: "cloud/mini"}}}
	if err := session.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunRequested(session.ChannelID(), request)); err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "failed")
	if *localCalls != 1 || *cloudCalls != 1 {
		t.Fatalf("calls: local %d, cloud %d; want 1, 1", *localCalls, *cloudCalls)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

func quietParent() *asyncSubagentProvider {
	return &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
}

// blockingChild waits until its context ends, signalling when it has begun.
func blockingChild() (*backgroundChildProvider, <-chan struct{}) {
	started := make(chan struct{}, 8)
	return &backgroundChildProvider{chat: func(ctx context.Context) (llm.Response, error) {
		started <- struct{}{}
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}}, started
}

func callSessionTool(t *testing.T, session *AgentSession, name string, args map[string]any) (string, error) {
	t.Helper()
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	message, err := session.ToolRegistry().HandleToolCallContext(t.Context(), llm.ToolCall{ID: "call-1", Name: name, Arguments: string(arguments)})
	return message.Content, err
}

func TestCancelSubagentToolStopsABackgroundRun(t *testing.T) {
	child, started := blockingChild()
	session := newBackgroundSession(t, quietParent(), child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	run, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"})
	if err != nil {
		t.Fatal(err)
	}
	awaitAsyncSignal(t, started)

	output, err := callSessionTool(t, session, cancelSubagentToolName, map[string]any{"subagent": "search"})
	if err != nil || !strings.Contains(output, run.ChildSessionID) {
		t.Fatalf("output = %q, error = %v", output, err)
	}
	awaitAsyncResult(t, completed, "cancelled")
	got := onlySubagentRun(t, session)
	if got.Status != SubagentCancelled || got.StopReason != eventing.SubagentStopModelCancelled {
		t.Fatalf("run = %+v", got)
	}
}

func TestUserCancelIsRecordedAsTheUsers(t *testing.T) {
	child, started := blockingChild()
	session := newBackgroundSession(t, quietParent(), child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"}); err != nil {
		t.Fatal(err)
	}
	awaitAsyncSignal(t, started)
	if _, err := session.CancelBackgroundSubagentForUser("SEARCH"); err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "cancelled")
	if got := onlySubagentRun(t, session); got.StopReason != eventing.SubagentStopUserCancelled {
		t.Fatalf("run = %+v", got)
	}
}

func TestCancelStopsARunStillQueued(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) {
		t.Error("cancelled run reached the model")
		return asyncTestResponse("done"), nil
	}}
	session := newBackgroundSession(t, quietParent(), child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	fillAsyncCapacity(t)
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CancelBackgroundSubagentForUser("search"); err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "cancelled")
	got := onlySubagentRun(t, session)
	if got.Status != SubagentCancelled || got.StopReason != eventing.SubagentStopUserCancelled || session.childActive(got.ChildSessionID) {
		t.Fatalf("run = %+v", got)
	}
}

func TestCancelNeedsOneRunningBackgroundRun(t *testing.T) {
	child, started := blockingChild()
	session := newBackgroundSession(t, quietParent(), child, map[string]string{"search": readOnlyAgent})
	if _, err := session.CancelBackgroundSubagentForUser("search"); err == nil || !strings.Contains(err.Error(), "no subagent run matches") {
		t.Fatalf("no runs: %v", err)
	}
	first, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "two"})
	if err != nil {
		t.Fatal(err)
	}
	awaitAsyncSignal(t, started)
	awaitAsyncSignal(t, started)
	if _, err := session.CancelBackgroundSubagentForUser("search"); err == nil || !strings.Contains(err.Error(), first.ChildSessionID) || !strings.Contains(err.Error(), second.ChildSessionID) {
		t.Fatalf("two runs: %v", err)
	}
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	for _, id := range []string{first.ChildSessionID, second.ChildSessionID} {
		if _, err := session.CancelBackgroundSubagentForUser(id); err != nil {
			t.Fatal(err)
		}
	}
	awaitAsyncResult(t, completed, "cancelled")
	awaitAsyncResult(t, completed, "cancelled")
	if _, err := session.CancelBackgroundSubagentForUser(first.ChildSessionID); err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("finished run: %v", err)
	}
}

func TestCancelRefusesAForegroundRun(t *testing.T) {
	child, started := blockingChild()
	session := newBackgroundSession(t, quietParent(), child, map[string]string{"search": readOnlyAgent})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := session.RunSubagent(ctx, SubagentRunOptions{Name: "search", Task: "find callers"})
		done <- err
	}()
	awaitAsyncSignal(t, started)
	if _, err := session.CancelBackgroundSubagentForUser("search"); err == nil || !strings.Contains(err.Error(), "foreground") {
		t.Fatalf("error = %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestListSubagentsToolShowsRunsNewestFirstAndInFull(t *testing.T) {
	session := newBackgroundSession(t, quietParent(), quietParent(), map[string]string{"search": readOnlyAgent})
	long := strings.Repeat("x", subagentPreviewLen+50)
	first, second := "parent-subagent-search-1", "parent-subagent-review-2"
	if err := errors.Join(
		session.AppendAuditEvent(eventing.NewSubagentInvocationStartedWithConfig("parent", "search", first, "parent", "find callers", "local/fast", "", 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationCompleted("parent", "search", first, "parent", long, 0, 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", "review", second, "parent", "check the diff", 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationStopped("parent", "review", second, "parent", "context canceled", eventing.SubagentStopUserCancelled, 0, 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", "goal-worker", "parent-goal-worker-3", "parent", "goal step", 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", "background", "parent-background-4", "parent", "scheduled", 128000)),
	); err != nil {
		t.Fatal(err)
	}
	list, err := callSessionTool(t, session, listSubagentsToolName, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(list, "review: cancelled (user_cancelled)") > strings.Index(list, "search: completed") || !strings.Contains(list, "review: cancelled") {
		t.Fatalf("list order:\n%s", list)
	}
	if !strings.Contains(list, "model: local/fast") || strings.Contains(list, long) {
		t.Fatalf("list:\n%s", list)
	}
	if strings.Contains(list, "goal-worker") || strings.Contains(list, "scheduled") {
		t.Fatalf("list shows internal runs:\n%s", list)
	}
	detail, err := callSessionTool(t, session, listSubagentsToolName, map[string]any{"child_session_id": first})
	if err != nil || !strings.Contains(detail, long) {
		t.Fatalf("detail = %q, error = %v", detail, err)
	}
	if _, err := callSessionTool(t, session, listSubagentsToolName, map[string]any{"child_session_id": "parent-background-4"}); err == nil {
		t.Fatal("detail of an internal run should fail")
	}
}

func TestFormatSubagentRunsLimitsTheList(t *testing.T) {
	var runs []SubagentRun
	for i := range subagentListLimit + 3 {
		runs = append(runs, SubagentRun{Name: fmt.Sprintf("agent%d", i), Status: SubagentCompleted})
	}
	list := FormatSubagentRuns(runs)
	if !strings.Contains(list, fmt.Sprintf("agent%d:", subagentListLimit+2)) || strings.Contains(list, "agent2:") || !strings.Contains(list, "3 older runs not shown") {
		t.Fatalf("list:\n%s", list)
	}
	if got := FormatSubagentRuns(nil); got != "No subagent runs in this session." {
		t.Fatal(got)
	}
}

func TestSubagentControlToolsNeedAnAgentToRun(t *testing.T) {
	controls := []string{listSubagentsToolName, cancelSubagentToolName}
	session := newBackgroundSession(t, quietParent(), quietParent(), nil)
	names := toolDefinitionNames(session.ToolRegistry().ToolDefinitions())
	for _, name := range controls {
		if slices.Contains(names, name) {
			t.Fatalf("session with no agents has %s: %v", name, names)
		}
	}
	// Agents outside the workspace, such as home config, arrive after creation.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "search.md"), []byte(readOnlyAgent), 0o644); err != nil {
		t.Fatal(err)
	}
	session.SetAgentsDirs([]string{dir})
	session.SetAgentsDirs([]string{dir})
	names = toolDefinitionNames(session.ToolRegistry().ToolDefinitions())
	for _, name := range controls {
		if !slices.Contains(names, name) {
			t.Fatalf("session with an agent lacks %s: %v", name, names)
		}
	}
	// /clear keeps them.
	sibling, err := session.NewSiblingSession("parent-2")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sibling.Close() }()
	if names := toolDefinitionNames(sibling.ToolRegistry().ToolDefinitions()); !slices.Contains(names, listSubagentsToolName) {
		t.Fatalf("sibling lacks subagent tools: %v", names)
	}
}

func TestSubagentControlToolsOnlyExistOnTopLevelSessions(t *testing.T) {
	session := newBackgroundSession(t, quietParent(), quietParent(), map[string]string{"search": readOnlyAgent})
	names := toolDefinitionNames(session.ToolRegistry().ToolDefinitions())
	for _, name := range []string{listSubagentsToolName, cancelSubagentToolName} {
		if !slices.Contains(names, name) {
			t.Fatalf("parent lacks %s: %v", name, names)
		}
	}
	child, err := session.NewSubagentChildSession(t.Context(), subagents.Definition{Name: "search", SystemPrompt: "search"}, SubagentChildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Close() }()
	names = toolDefinitionNames(child.ToolRegistry().ToolDefinitions())
	for _, name := range []string{listSubagentsToolName, cancelSubagentToolName, callSubagentToolName} {
		if slices.Contains(names, name) {
			t.Fatalf("child has %s: %v", name, names)
		}
	}
}

// A failed run must reach the model, whose last word was that it had started.
func TestFailedBackgroundSubagentIsReportedToTheModel(t *testing.T) {
	var mu sync.Mutex
	var last string
	parent := &asyncSubagentProvider{chat: func(_ context.Context, messages []llm.Message) (llm.Response, error) {
		mu.Lock()
		last = messages[len(messages)-1].Content
		mu.Unlock()
		return asyncTestResponse("search failed"), nil
	}}
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) {
		return llm.Response{}, errors.New("provider exploded")
	}}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"}); err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "failed")
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(last, `Background subagent "search" ended without a result: failed`) || !strings.Contains(last, "provider exploded") {
		t.Fatalf("report = %q", last)
	}
}

func TestCancelledBackgroundSubagentStartsNoTurn(t *testing.T) {
	var calls int
	var mu sync.Mutex
	parent := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return asyncTestResponse("ok"), nil
	}}
	child, started := blockingChild()
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"}); err != nil {
		t.Fatal(err)
	}
	awaitAsyncSignal(t, started)
	if _, err := session.CancelBackgroundSubagentForUser("search"); err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "cancelled")
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("parent model calls = %d", calls)
	}
}

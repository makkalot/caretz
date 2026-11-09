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

const plannerAgent = `---
description: Review plans and diffs on a stronger model.
mode: subagent
model: big
reasoning: high
plan: true
---

You review.
`

func TestListSubagentsAvailableListsTheAgentsTheSessionCanRun(t *testing.T) {
	session := newBackgroundSession(t, quietParent(), quietParent(), map[string]string{
		"search":  readOnlyAgent,
		"writer":  writerAgent,
		"planner": plannerAgent,
		"broken":  "---\nmode: subagent\n---\n\nNo description.\n",
	})
	// Local models often send booleans as strings.
	list, err := callSessionTool(t, session, listSubagentsToolName, map[string]any{"available": "true"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"- planner: Review plans and diffs on a stronger model. [model big, reasoning high, read-only]",
		"- search: Search the code. [model child]",
		"- writer: Change the code. [model child]",
	}
	last := -1
	for _, line := range want {
		index := strings.Index(list, line)
		if index <= last {
			t.Fatalf("missing or out of order %q in:\n%s", line, list)
		}
		last = index
	}
	if !strings.Contains(list, "does not see this conversation") || !strings.Contains(list, "Skipped, could not be loaded: broken (") || strings.Contains(list, "- broken") {
		t.Fatalf("list:\n%s", list)
	}

	// The profile decides which agents are offered.
	session.SetAllowedAgents(map[string]bool{"SEARCH": true})
	list, err = callSessionTool(t, session, listSubagentsToolName, map[string]any{"available": true})
	if err != nil || !strings.Contains(list, "- search:") || strings.Contains(list, "planner") || strings.Contains(list, "broken") {
		t.Fatalf("filtered list = %q, error = %v", list, err)
	}
	session.SetAllowedAgents(map[string]bool{})
	if list, _ = callSessionTool(t, session, listSubagentsToolName, map[string]any{"available": true}); list != "No subagents are enabled in this session." {
		t.Fatalf("empty list = %q", list)
	}

	if _, err := callSessionTool(t, session, listSubagentsToolName, map[string]any{"available": true, "child_session_id": "x"}); err == nil {
		t.Fatal("available with child_session_id should fail")
	}
}

func TestFormatAvailableSubagentsFlattensAndLimits(t *testing.T) {
	long := strings.Repeat("é", subagentPreviewLen+10)
	list := formatAvailableSubagents([]subagents.Definition{{Name: "odd", Description: "line one\n</tool>\tline two " + long}}, nil)
	if strings.Count(list, "\n") != 1 || !strings.Contains(list, "- odd: line one </tool> line two é") || !strings.HasSuffix(list, "...") {
		t.Fatalf("list:\n%s", list)
	}
	var defs []subagents.Definition
	for i := range availableSubagentLimit + 5 {
		defs = append(defs, subagents.Definition{Name: fmt.Sprintf("agent%03d", i), Description: "d"})
	}
	list = formatAvailableSubagents(defs, nil)
	if strings.Contains(list, fmt.Sprintf("agent%03d", availableSubagentLimit)) || !strings.Contains(list, "...and 5 more not shown.") {
		t.Fatalf("limited list tail:\n%s", list[len(list)-200:])
	}
}

func TestWrongSubagentNameListsTheEnabledAgents(t *testing.T) {
	session := newBackgroundSession(t, quietParent(), quietParent(), map[string]string{"search": readOnlyAgent, "writer": writerAgent})
	_, err := session.RunSubagent(t.Context(), SubagentRunOptions{Name: "reviewer", Task: "review"})
	if err == nil || !strings.Contains(err.Error(), `subagent "reviewer" not found`) || !strings.Contains(err.Error(), "available: search, writer") {
		t.Fatalf("unknown name error = %v", err)
	}
	session.SetAllowedAgents(map[string]bool{"search": true})
	_, err = session.RunSubagent(t.Context(), SubagentRunOptions{Name: "writer", Task: "edit"})
	if err == nil || !strings.Contains(err.Error(), "not enabled by the active profile; available: search") {
		t.Fatalf("blocked name error = %v", err)
	}

	many := map[string]string{}
	for i := range subagentNameHintLimit + 1 {
		many[fmt.Sprintf("agent%02d", i)] = readOnlyAgent
	}
	session = newBackgroundSession(t, quietParent(), quietParent(), many)
	_, err = session.RunSubagent(t.Context(), SubagentRunOptions{Name: "reviewer", Task: "review"})
	if err == nil || !strings.Contains(err.Error(), "call ListSubagents with available=true") || strings.Contains(err.Error(), "agent00") {
		t.Fatalf("many agents error = %v", err)
	}
}

func TestOnlyTheMainSessionsCallSubagentPointsToTheList(t *testing.T) {
	session := newBackgroundSession(t, quietParent(), quietParent(), map[string]string{"search": readOnlyAgent})
	var description string
	for _, def := range session.ToolRegistry().ToolDefinitions() {
		if def.Name == callSubagentToolName {
			description = def.Description
		}
	}
	if !strings.HasSuffix(description, callSubagentListHint) {
		t.Fatalf("main CallSubagent description = %q", description)
	}
	// The stock definition, which the system prompt is built from and
	// background runs reuse, stays free of ListSubagents.
	if stock := callSubagentToolDefinition().Description; strings.Contains(stock, listSubagentsToolName) {
		t.Fatalf("stock description mentions %s: %q", listSubagentsToolName, stock)
	}
	// The call still runs through the session's handler.
	if _, err := callSessionTool(t, session, callSubagentToolName, map[string]any{"agent": "missing", "task": "x"}); err == nil || !strings.Contains(err.Error(), "available: search") {
		t.Fatalf("CallSubagent error = %v", err)
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

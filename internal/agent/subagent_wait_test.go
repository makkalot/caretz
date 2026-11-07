package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

// recordingParent replies from script, then with "ok", and records the last
// message of every request.
type recordingParent struct {
	asyncSubagentProvider
	mu     sync.Mutex
	script []llm.Response
	lasts  []string
}

func newRecordingParent(script ...llm.Response) *recordingParent {
	p := &recordingParent{script: script}
	p.chat = func(_ context.Context, messages []llm.Message) (llm.Response, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.lasts = append(p.lasts, messages[len(messages)-1].Content)
		if len(p.script) == 0 {
			return asyncTestResponse("ok"), nil
		}
		reply := p.script[0]
		p.script = p.script[1:]
		return reply, nil
	}
	return p
}

func (p *recordingParent) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.lasts)
}

func toolCalls(t *testing.T, calls ...llm.ToolCall) llm.Response {
	t.Helper()
	for i := range calls {
		calls[i].ID = fmt.Sprintf("call-%d-%s", i, calls[i].Name)
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: calls}}
}

func toolCallOf(t *testing.T, name string, args map[string]any) llm.ToolCall {
	t.Helper()
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return llm.ToolCall{Name: name, Arguments: string(arguments)}
}

func startSearch(t *testing.T, task string) llm.ToolCall {
	t.Helper()
	return toolCallOf(t, callSubagentToolName, map[string]any{"agent": "search", "task": task, "background": true})
}

func TestRunsFinishingTogetherShareOneReportTurn(t *testing.T) {
	parent := newRecordingParent()
	release := make(chan struct{})
	child := &backgroundChildProvider{chat: func(ctx context.Context) (llm.Response, error) {
		<-release
		return asyncTestResponse("child result"), nil
	}}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	for _, task := range []string{"first", "second", "third"} {
		if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: task}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	for range 3 {
		awaitAsyncResult(t, completed, "succeeded")
	}
	calls := parent.calls()
	if len(calls) != 1 {
		t.Fatalf("report turns = %d: %q", len(calls), calls)
	}
	for _, want := range []string{"first", "second", "third", "Report the results to the user concisely."} {
		if !strings.Contains(calls[0], want) {
			t.Fatalf("report lacks %q:\n%s", want, calls[0])
		}
	}
}

// Reports queue behind a user's turn rather than interrupting it.
func TestReportsWaitForTheCurrentTurn(t *testing.T) {
	entered, leave := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var lasts []string
	parent := &asyncSubagentProvider{chat: func(_ context.Context, messages []llm.Message) (llm.Response, error) {
		mu.Lock()
		first := len(lasts) == 0
		lasts = append(lasts, messages[len(messages)-1].Content)
		mu.Unlock()
		if first {
			close(entered)
			<-leave
		}
		return asyncTestResponse("ok"), nil
	}}
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("child result"), nil }}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	turn := make(chan error, 1)
	go func() {
		_, err := session.Call(t.Context(), "user question")
		turn <- err
	}()
	awaitAsyncSignal(t, entered)
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "second"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * subagentReportWindow)
	close(leave)
	if err := <-turn; err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "succeeded")
	awaitAsyncResult(t, completed, "succeeded")
	mu.Lock()
	defer mu.Unlock()
	if len(lasts) != 2 || lasts[0] != "user question" || !strings.Contains(lasts[1], "first") || !strings.Contains(lasts[1], "second") {
		t.Fatalf("turns = %q", lasts)
	}
}

func TestWaitSubagentsReturnsResultsAndStartsNoReportTurn(t *testing.T) {
	parent := newRecordingParent(
		toolCalls(t, startSearch(t, "find callers")),
		toolCalls(t, toolCallOf(t, waitSubagentsToolName, map[string]any{})),
	)
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("found 3 callers"), nil }}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)

	if _, err := session.Call(t.Context(), "look for callers"); err != nil {
		t.Fatal(err)
	}
	calls := parent.calls()
	if len(calls) != 3 || !strings.Contains(calls[2], "found 3 callers") {
		t.Fatalf("calls = %q", calls)
	}
	awaitAsyncResult(t, completed, "succeeded")
	time.Sleep(2 * subagentReportWindow)
	if calls := parent.calls(); len(calls) != 3 {
		t.Fatalf("a claimed result still started a report turn: %q", calls)
	}
}

// More runs than slots, all waited for in one turn. Finished runs must give up
// their slots while their reports wait for this turn, or the rest never start.
func TestWaitSubagentsWithMoreRunsThanSlots(t *testing.T) {
	count := cap(asyncRunCapacity) + 2
	calls := make([]llm.ToolCall, count)
	for i := range calls {
		calls[i] = startSearch(t, fmt.Sprintf("task %d", i))
	}
	parent := newRecordingParent(
		toolCalls(t, calls...),
		toolCalls(t, toolCallOf(t, waitSubagentsToolName, map[string]any{"timeout_seconds": 5})),
	)
	var mu sync.Mutex
	replies := 0
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		replies++
		return asyncTestResponse(fmt.Sprintf("result %d", replies)), nil
	}}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	if _, err := session.Call(t.Context(), "look everywhere"); err != nil {
		t.Fatal(err)
	}
	got := parent.calls()
	if len(got) != 3 {
		t.Fatalf("calls = %q", got)
	}
	if strings.Contains(got[2], "Still running") || strings.Count(got[2], "search: completed") != count {
		t.Fatalf("wait result:\n%s", got[2])
	}
}

func TestWaitSubagentsTimesOutLeavingTheRunToReportLater(t *testing.T) {
	parent := newRecordingParent()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) {
		started <- struct{}{}
		<-release
		return asyncTestResponse("late result"), nil
	}}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"}); err != nil {
		t.Fatal(err)
	}
	awaitAsyncSignal(t, started)
	output, err := callSessionTool(t, session, waitSubagentsToolName, map[string]any{"timeout_seconds": 1})
	if err != nil || !strings.Contains(output, "Still running after 1 seconds: search") {
		t.Fatalf("output = %q, error = %v", output, err)
	}
	close(release)
	awaitAsyncResult(t, completed, "succeeded")
	if calls := parent.calls(); len(calls) != 1 || !strings.Contains(calls[0], "late result") {
		t.Fatalf("calls = %q", calls)
	}
}

func TestWaitSubagentsStopsWhenTheTurnIsCancelled(t *testing.T) {
	child, started := blockingChild()
	session := newBackgroundSession(t, quietParent(), child, map[string]string{"search": readOnlyAgent})
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"}); err != nil {
		t.Fatal(err)
	}
	awaitAsyncSignal(t, started)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := session.handleWaitSubagents(ctx, llm.ToolCall{Arguments: "{}"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestWaitSubagentsArguments(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("found 3 callers"), nil }}
	session := newBackgroundSession(t, quietParent(), child, map[string]string{"search": readOnlyAgent})
	output, err := callSessionTool(t, session, waitSubagentsToolName, map[string]any{})
	if err != nil || output != "No background subagents are running." {
		t.Fatalf("no runs: %q, %v", output, err)
	}
	if _, err := callSessionTool(t, session, waitSubagentsToolName, map[string]any{"subagents": []string{"missing"}}); err == nil || !strings.Contains(err.Error(), "no subagent run matches") {
		t.Fatalf("unknown: %v", err)
	}
	run, err := session.RunSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"})
	if err != nil {
		t.Fatal(err)
	}
	output, err = callSessionTool(t, session, waitSubagentsToolName, map[string]any{"subagents": []string{run.ChildSessionID}})
	if err != nil || !strings.Contains(output, "found 3 callers") {
		t.Fatalf("finished run: %q, %v", output, err)
	}
	// By name, only the latest of several finished runs comes back.
	if _, err := session.RunSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "second task"}); err != nil {
		t.Fatal(err)
	}
	output, err = callSessionTool(t, session, waitSubagentsToolName, map[string]any{"subagents": []string{"search"}})
	if err != nil || !strings.Contains(output, "second task") || strings.Count(output, "search: completed") != 1 {
		t.Fatalf("by name:\n%s\nerror: %v", output, err)
	}
}

func TestWaitForBackgroundSubagentsCoversReportsAndRunsTheyStart(t *testing.T) {
	parent := newRecordingParent(toolCalls(t, startSearch(t, "follow-up")))
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("child result"), nil }}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	if err := session.WaitForBackgroundSubagents(t.Context()); err != nil {
		t.Fatalf("no runs: %v", err)
	}
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "first"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := session.WaitForBackgroundSubagents(ctx); err != nil {
		t.Fatal(err)
	}
	if count := session.BackgroundSubagentsRunning(); count != 0 {
		t.Fatalf("still running: %d", count)
	}
	// First report starts the follow-up, the model reacts to the start, and the
	// follow-up's own report comes last.
	calls := parent.calls()
	if len(calls) != 3 || !strings.Contains(calls[0], "first") || !strings.Contains(calls[2], "follow-up") {
		t.Fatalf("calls = %q", calls)
	}
}

func TestWaitForBackgroundSubagentsHonoursItsContext(t *testing.T) {
	child, started := blockingChild()
	session := newBackgroundSession(t, quietParent(), child, map[string]string{"search": readOnlyAgent})
	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "first"}); err != nil {
		t.Fatal(err)
	}
	awaitAsyncSignal(t, started)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := session.WaitForBackgroundSubagents(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

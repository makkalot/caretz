package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

const readOnlyAgent = `---
description: Search the code.
mode: subagent
model: child
permission:
  edit: deny
  bash: deny
---

You search code.
`

const writerAgent = `---
description: Change the code.
mode: subagent
model: child
---

You change code.
`

// backgroundChildProvider records what the child model was given.
type backgroundChildProvider struct {
	subagentModelTestProvider
	mu       sync.Mutex
	prompt   string
	messages []llm.Message
	tools    []string
	chat     func(context.Context) (llm.Response, error)
}

func (p *backgroundChildProvider) ChatContext(ctx context.Context, systemPrompt string, messages []llm.Message, defs []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	p.mu.Lock()
	p.prompt = systemPrompt
	p.messages = slices.Clone(messages)
	p.tools = nil
	for _, def := range defs {
		p.tools = append(p.tools, def.Name)
	}
	p.mu.Unlock()
	return p.chat(ctx)
}

func newBackgroundSession(t *testing.T, parent, child llm.Provider, agents map[string]string) *AgentSession {
	t.Helper()
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".caretz", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range agents {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logger := log.New()
	session, err := NewAgentSession("test", "parent", SessionParams{
		BaseDir: filepath.Join(t.TempDir(), "sessions"), WorkspaceDir: workspace, Logger: logger, Provider: parent,
		ToolRegistry: &tools.Registry{Logger: logger, WorkspaceDir: workspace}, SystemPrompt: "parent prompt",
		ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	session.SetSubagentModelResolver(subagentModelTestResolver{models: map[string]SubagentModelResolution{
		"child": {Model: "child", Provider: child, ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000, SummaryTokens: 2000},
	}})
	return session
}

func backgroundToolCall(t *testing.T, agent string) llm.ToolCall {
	t.Helper()
	arguments, err := json.Marshal(map[string]any{"agent": agent, "task": "find callers", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	return llm.ToolCall{ID: "call-1", Name: callSubagentToolName, Arguments: string(arguments)}
}

// A background subagent must start while the turn that launched it is still
// running. The parent's second model call only returns once the child has
// begun, so this deadlocks if the child waits for the turn's execution lock.
func TestBackgroundSubagentStartsDuringTheLaunchingTurn(t *testing.T) {
	childStarted, release := make(chan struct{}), make(chan struct{})
	child := &backgroundChildProvider{chat: func(ctx context.Context) (llm.Response, error) {
		close(childStarted)
		select {
		case <-release:
			return asyncTestResponse("found 3 callers"), nil
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}}
	var parentMessages [][]llm.Message
	var parentCalls int
	var mu sync.Mutex
	var session *AgentSession
	parent := &asyncSubagentProvider{chat: func(ctx context.Context, messages []llm.Message) (llm.Response, error) {
		mu.Lock()
		parentCalls++
		call := parentCalls
		parentMessages = append(parentMessages, slices.Clone(messages))
		mu.Unlock()
		switch call {
		case 1:
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{backgroundToolCall(t, "search")}}}, nil
		case 2:
			select {
			case <-childStarted:
				return asyncTestResponse("search is running"), nil
			case <-time.After(5 * time.Second):
				return llm.Response{}, errors.New("child did not start during the turn")
			}
		default:
			return asyncTestResponse("search found 3 callers"), nil
		}
	}}
	session = newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)

	response, err := session.Call(t.Context(), "look for callers")
	if err != nil {
		t.Fatal(err)
	}
	if responseText(response) != "search is running" {
		t.Fatalf("response = %+v", response)
	}
	run := onlySubagentRun(t, session)
	if run.Status != SubagentRunning || run.Name != "search" || run.Model != "child" {
		t.Fatalf("while running: %+v", run)
	}
	mu.Lock()
	toolResult := parentMessages[1][len(parentMessages[1])-1]
	mu.Unlock()
	if !strings.Contains(toolResult.Content, "started in the background") || !strings.Contains(toolResult.Content, run.ChildSessionID) {
		t.Fatalf("tool result = %q", toolResult.Content)
	}

	close(release)
	awaitAsyncResult(t, completed, "succeeded")
	run = onlySubagentRun(t, session)
	if run.Status != SubagentCompleted || run.Result != "found 3 callers" || !run.Resumable {
		t.Fatalf("after finishing: %+v", run)
	}
	mu.Lock()
	report := parentMessages[len(parentMessages)-1]
	mu.Unlock()
	last := report[len(report)-1].Content
	if !strings.Contains(last, `Background subagent "search" completed.`) || !strings.Contains(last, "found 3 callers") {
		t.Fatalf("report = %q", last)
	}
}

func TestBackgroundSubagentUsesItsAgentDefinition(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("done"), nil }}
	parent := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)

	if _, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"}); err != nil {
		t.Fatal(err)
	}
	awaitAsyncResult(t, completed, "succeeded")

	child.mu.Lock()
	defer child.mu.Unlock()
	if !strings.Contains(child.prompt, "You search code.") || strings.Contains(child.prompt, "parent prompt") {
		t.Fatalf("system prompt = %q", child.prompt)
	}
	for _, denied := range []string{"Edit", "Write", "Bash", callSubagentToolName, tools.AskUserToolName} {
		if slices.Contains(child.tools, denied) {
			t.Fatalf("child was given %s: %v", denied, child.tools)
		}
	}
	if !slices.Contains(child.tools, "Read") {
		t.Fatalf("child lost Read: %v", child.tools)
	}
}

func TestBackgroundSubagentRefusals(t *testing.T) {
	tests := []struct {
		name    string
		agent   string
		allowed map[string]bool
		wantErr string
	}{
		{name: "can change files", agent: "writer", wantErr: "cannot run in the background"},
		{name: "unknown agent", agent: "missing", wantErr: "not found"},
		{name: "not in the profile", agent: "search", allowed: map[string]bool{"writer": true}, wantErr: "not enabled by the active profile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) {
				t.Error("refused agent reached the model")
				return asyncTestResponse("done"), nil
			}}
			parent := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
			session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent, "writer": writerAgent})
			if tt.allowed != nil {
				session.SetAllowedAgents(tt.allowed)
			}
			_, err := session.handleCallSubagentToolCall(t.Context(), backgroundToolCall(t, tt.agent))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if runs, err := session.SubagentRuns(); err != nil || len(runs) != 0 {
				t.Fatalf("runs = %+v, error = %v", runs, err)
			}
		})
	}
}

// A writer agent can still run in the foreground; only background is limited.
func TestWriterAgentStillRunsInForeground(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("changed"), nil }}
	parent := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
	session := newBackgroundSession(t, parent, child, map[string]string{"writer": writerAgent})
	run, err := session.RunSubagent(t.Context(), SubagentRunOptions{Name: "writer", Task: "change it"})
	if err != nil || run.Result != "changed" {
		t.Fatalf("run = %+v, error = %v", run, err)
	}
}

// fillAsyncCapacity takes every run slot, so a new run stays queued, until the
// test ends.
func fillAsyncCapacity(t *testing.T) {
	t.Helper()
	for range cap(asyncRunCapacity) {
		asyncRunCapacity <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(asyncRunCapacity) {
			<-asyncRunCapacity
		}
	})
}

func TestQueuedBackgroundSubagentIsRecordedWhenTheSessionCloses(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) {
		t.Error("queued child reached the model")
		return asyncTestResponse("done"), nil
	}}
	parent := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	fillAsyncCapacity(t)

	start, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"})
	if err != nil {
		t.Fatal(err)
	}
	if run := onlySubagentRun(t, session); run.Status != SubagentRunning || run.ChildSessionID != start.ChildSessionID {
		t.Fatalf("queued run = %+v", run)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	run := onlySubagentRun(t, session)
	if run.Status != SubagentInterrupted || run.StopReason != eventing.SubagentStopSessionClosed || !run.Resumable {
		t.Fatalf("after close: %+v", run)
	}
	if session.childActive(run.ChildSessionID) {
		t.Fatal("abandoned child is still open")
	}
}

func TestCancellingTheLaunchingTurnStopsABackgroundSubagent(t *testing.T) {
	childStarted := make(chan struct{})
	child := &backgroundChildProvider{chat: func(ctx context.Context) (llm.Response, error) {
		close(childStarted)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls int
	var mu sync.Mutex
	parent := &asyncSubagentProvider{chat: func(chatCtx context.Context, _ []llm.Message) (llm.Response, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{backgroundToolCall(t, "search")}}}, nil
		}
		<-childStarted
		cancel()
		<-chatCtx.Done()
		return llm.Response{}, chatCtx.Err()
	}}
	session := newBackgroundSession(t, parent, child, map[string]string{"search": readOnlyAgent})
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)

	if _, err := session.Call(ctx, "look for callers"); !errors.Is(err, context.Canceled) {
		t.Fatalf("turn error = %v", err)
	}
	awaitAsyncResult(t, completed, "cancelled")
	run := onlySubagentRun(t, session)
	if run.Status != SubagentCancelled || run.StopReason != eventing.SubagentStopUserCancelled || !run.Resumable {
		t.Fatalf("run = %+v", run)
	}
}

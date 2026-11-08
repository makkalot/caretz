package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

type resumeFixture struct {
	baseDir, workspace string
	parent             llm.Provider
	child              llm.Provider
	requests           *[]SubagentModelRequest
}

func newResumeFixture(t *testing.T, child llm.Provider, agents map[string]string) *resumeFixture {
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
	return &resumeFixture{baseDir: filepath.Join(t.TempDir(), "sessions"), workspace: workspace, parent: quietParent(), child: child, requests: &[]SubagentModelRequest{}}
}

// open starts a process's view of session "parent"; calling it again after
// closing the last one is a session resume.
func (f *resumeFixture) open(t *testing.T) *AgentSession {
	t.Helper()
	logger := log.New()
	session, err := NewAgentSession("test", "parent", SessionParams{
		BaseDir: f.baseDir, WorkspaceDir: f.workspace, Logger: logger, Provider: f.parent,
		ToolRegistry: &tools.Registry{Logger: logger, WorkspaceDir: f.workspace}, SystemPrompt: "parent prompt",
		ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	resolution := SubagentModelResolution{Model: "child", Provider: f.child, ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000, SummaryTokens: 2000}
	session.SetSubagentModelResolver(subagentModelTestResolver{requests: f.requests, models: map[string]SubagentModelResolution{"child": resolution, "other": resolution}})
	return session
}

func messageRoles(messages []llm.Message) string {
	roles := make([]string, len(messages))
	for i, message := range messages {
		roles[i] = string(message.Role)
	}
	return strings.Join(roles, ",")
}

func TestResumeContinuesACancelledRunInItsConversation(t *testing.T) {
	calls := 0
	var child *backgroundChildProvider
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	child = &backgroundChildProvider{chat: func(chatCtx context.Context) (llm.Response, error) {
		calls++
		if calls == 1 {
			cancel()
			<-chatCtx.Done()
			return llm.Response{}, chatCtx.Err()
		}
		return asyncTestResponse("found 3 callers"), nil
	}}
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
	session := fixture.open(t)

	if _, err := session.RunSubagent(ctx, SubagentRunOptions{Name: "search", Task: "find callers"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error = %v", err)
	}
	first := onlySubagentRun(t, session)
	if first.Status != SubagentCancelled || !first.Resumable {
		t.Fatalf("first run = %+v", first)
	}

	run, err := session.RunSubagent(t.Context(), SubagentRunOptions{ResumeChildID: first.ChildSessionID, Task: "keep going"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ChildSessionID != first.ChildSessionID || run.Result != "found 3 callers" {
		t.Fatalf("run = %+v", run)
	}
	got := onlySubagentRun(t, session)
	if got.Status != SubagentCompleted || got.Task != "find callers" || got.StopReason != "" {
		t.Fatalf("after resume: %+v", got)
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	if !strings.Contains(child.prompt, "You search code.") {
		t.Fatalf("system prompt = %q", child.prompt)
	}
	if roles := messageRoles(child.messages); roles != "user,assistant,user" {
		t.Fatalf("roles = %s: %+v", roles, child.messages)
	}
	if child.messages[0].Content == "" || !strings.Contains(child.messages[0].Content, "find callers") {
		t.Fatalf("original task lost: %+v", child.messages[0])
	}
	last := child.messages[2].Content
	for _, want := range []string{"Your earlier run ended as: cancelled", "Original task:\nfind callers", "Now:\nkeep going", "Re-check the current state"} {
		if !strings.Contains(last, want) {
			t.Fatalf("resume prompt lacks %q:\n%s", want, last)
		}
	}
}

// writeCrashedChild leaves what a dead process leaves: a Started event with no
// ending in the parent, and a child log ending in a tool call with no result.
func writeCrashedChild(t *testing.T, fixture *resumeFixture, session *AgentSession) string {
	t.Helper()
	childID := "parent-subagent-search-1"
	logger := log.New()
	child, err := NewAgentSession("test", childID, SessionParams{
		BaseDir: fixture.baseDir, WorkspaceDir: fixture.workspace, Logger: logger, Provider: fixture.child,
		ToolRegistry: &tools.Registry{Logger: logger, WorkspaceDir: fixture.workspace}, SystemPrompt: "search",
		ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000, ChildSession: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	userEvent := eventing.NewLLMUserMessage(childID, "audit-1", 1, llm.Message{Role: llm.RoleUser, Content: "find callers"})
	if err := child.contextSessionStore.AppendUserMessage(userEvent); err != nil {
		t.Fatal(err)
	}
	call := llm.ToolCall{ID: "tool-1", Name: "Grep", Arguments: `{"pattern":"x"}`}
	agentEvent := eventing.NewLLMAgentMessage(childID, "audit-1", 1, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}, llm.Usage{}, 0)
	if _, err := child.contextSessionStore.AppendAgentMessage(agentEvent); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.AppendAuditEvent(eventing.NewSubagentInvocationStartedWithConfig("parent", "search", childID, "parent", "find callers", "child", "", 128000)); err != nil {
		t.Fatal(err)
	}
	return childID
}

// The process-kill scenario itself, with ListSubagents and a resume through
// CallSubagent, is the e2e test TestCustomizationSubagentResumesAfterCaretzIsKilled.
// This covers what it cannot reach: the reopen notice, which only the TUI
// prints, and a child that died between a tool call and its result.
func TestInterruptedNoticeAndChildRepairAfterACrash(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("found 3 callers"), nil }}
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
	first := fixture.open(t)
	childID := writeCrashedChild(t, fixture, first)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	session := fixture.open(t)
	notice, err := session.InterruptedSubagentNotice()
	if err != nil || !strings.Contains(notice, "search") {
		t.Fatalf("notice = %q, error = %v", notice, err)
	}

	output, err := callSessionTool(t, session, callSubagentToolName, map[string]any{"resume": childID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "found 3 callers") {
		t.Fatalf("output = %q", output)
	}
	child.mu.Lock()
	messages := child.messages
	child.mu.Unlock()
	// [task][assistant tool call][repair tool result][interrupted assistant response][resume message]
	if roles := messageRoles(messages); roles != "user,assistant,tool,assistant,user" {
		t.Fatalf("roles = %s: %+v", roles, messages)
	}
	if messages[2].ToolCallID != "tool-1" {
		t.Fatalf("repair result = %+v", messages[2])
	}
	if !strings.Contains(messages[4].Content, "Continue the original task.") {
		t.Fatalf("resume message = %q", messages[4].Content)
	}
	if got := onlySubagentRun(t, session); got.Status != SubagentCompleted || got.ChildSessionID != childID {
		t.Fatalf("run = %+v", got)
	}
	if notice, err := session.InterruptedSubagentNotice(); err != nil || notice != "" {
		t.Fatalf("notice after resume = %q, error = %v", notice, err)
	}
}

func TestResumeInTheBackgroundAfterACrash(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("found 3 callers"), nil }}
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
	session := fixture.open(t)
	childID := writeCrashedChild(t, fixture, session)
	completed := subscribeAsyncTest(t, session, eventing.AsyncRunCompleted)

	output, err := callSessionTool(t, session, callSubagentToolName, map[string]any{"resume": childID, "background": true, "task": "keep going"})
	if err != nil || !strings.Contains(output, "started in the background") || !strings.Contains(output, childID) {
		t.Fatalf("output = %q, error = %v", output, err)
	}
	awaitAsyncResult(t, completed, "succeeded")
	if got := onlySubagentRun(t, session); got.Status != SubagentCompleted || got.Result != "found 3 callers" {
		t.Fatalf("run = %+v", got)
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	if roles := messageRoles(child.messages); roles != "user,assistant,tool,assistant,user" {
		t.Fatalf("roles = %s", roles)
	}
}

func TestResumeKeepsTheModelUnlessOverridden(t *testing.T) {
	tests := []struct {
		name, override, want string
	}{
		{name: "keeps", want: "child"},
		{name: "overridden", override: "other", want: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("done"), nil }}
			fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
			session := fixture.open(t)
			childID := writeCrashedChild(t, fixture, session)
			*fixture.requests = nil
			if _, err := session.RunSubagent(t.Context(), SubagentRunOptions{ResumeChildID: childID, ModelRefOverride: tt.override}); err != nil {
				t.Fatal(err)
			}
			if len(*fixture.requests) != 1 || (*fixture.requests)[0].Ref != tt.want {
				t.Fatalf("requests = %+v, want %s", *fixture.requests, tt.want)
			}
			if got := onlySubagentRun(t, session); got.Model != tt.want {
				t.Fatalf("recorded model = %q, want %s", got.Model, tt.want)
			}
		})
	}
}

func TestResumeRefusals(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) {
		t.Error("refused resume reached the model")
		return asyncTestResponse("done"), nil
	}}
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent, "writer": writerAgent})
	session := fixture.open(t)
	for _, event := range []struct{ name, id string }{
		{"goal-worker", "parent-goal-worker-3"},
		{"search", "other-subagent-search-4"},
		{"writer", "parent-subagent-writer-5"},
		{"gone", "parent-subagent-gone-6"},
	} {
		if err := session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", event.name, event.id, "parent", "task", 128000)); err != nil {
			t.Fatal(err)
		}
	}
	searchID := writeCrashedChild(t, fixture, session)
	tests := []struct {
		name    string
		opts    SubagentRunOptions
		start   func(context.Context, SubagentRunOptions) (SubagentRunResult, error)
		wantErr string
	}{
		{name: "unknown ID", opts: SubagentRunOptions{ResumeChildID: "parent-subagent-search-99"}, start: session.RunSubagent, wantErr: "no subagent run of this session"},
		{name: "another session's child", opts: SubagentRunOptions{ResumeChildID: "other-subagent-search-4"}, start: session.RunSubagent, wantErr: "no subagent run of this session"},
		{name: "goal child", opts: SubagentRunOptions{ResumeChildID: "parent-goal-worker-3"}, start: session.RunSubagent, wantErr: "no subagent run of this session"},
		{name: "wrong agent", opts: SubagentRunOptions{ResumeChildID: searchID, Name: "writer"}, start: session.RunSubagent, wantErr: `belongs to subagent "search"`},
		{name: "agent file deleted", opts: SubagentRunOptions{ResumeChildID: "parent-subagent-gone-6"}, start: session.RunSubagent, wantErr: "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.start(t.Context(), tt.opts); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestProfileBlocksResumingAnAgentItDisabled(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("done"), nil }}
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
	session := fixture.open(t)
	childID := writeCrashedChild(t, fixture, session)
	session.SetAllowedAgents(map[string]bool{"other": true})
	if _, err := session.RunSubagent(t.Context(), SubagentRunOptions{ResumeChildID: childID}); err == nil || !strings.Contains(err.Error(), "not enabled by the active profile") {
		t.Fatalf("error = %v", err)
	}
}

func TestResumeRefusesARunStillGoing(t *testing.T) {
	child, started := blockingChild()
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
	session := fixture.open(t)
	run, err := session.startBackgroundSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"})
	if err != nil {
		t.Fatal(err)
	}
	awaitAsyncSignal(t, started)
	if _, err := session.RunSubagent(t.Context(), SubagentRunOptions{ResumeChildID: run.ChildSessionID}); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("error = %v", err)
	}
}

// Both resumes pass the status check before either has opened the child; the
// reservation is what stops two instances writing one log.
func TestConcurrentResumesOpenTheChildOnce(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("done"), nil }}
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
	session := fixture.open(t)
	childID := writeCrashedChild(t, fixture, session)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	children := make([]*AgentSession, len(errs))
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			children[i], errs[i] = session.NewChildSessionWithOptions(ChildSessionOptions{ResumeChannelID: childID, Prefix: "subagent-search"})
		}()
	}
	wg.Wait()
	opened := 0
	for i, err := range errs {
		switch {
		case err == nil:
			opened++
			defer func() { _ = children[i].Close() }()
		case !strings.Contains(err.Error(), "already running"):
			t.Fatalf("error = %v", err)
		}
	}
	if opened != 1 {
		t.Fatalf("opened %d instances: %v", opened, errs)
	}
}

func TestListTellsTheModelNotToResumeAUserCancelledRun(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("done"), nil }}
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
	session := fixture.open(t)
	childID := "parent-subagent-search-1"
	if err := errors.Join(
		session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", "search", childID, "parent", "task", 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationStopped("parent", "search", childID, "parent", "context canceled", eventing.SubagentStopUserCancelled, 0, 128000)),
	); err != nil {
		t.Fatal(err)
	}
	list, err := callSessionTool(t, session, listSubagentsToolName, map[string]any{})
	if err != nil || !strings.Contains(list, "only if the user asks") {
		t.Fatalf("list = %q, error = %v", list, err)
	}
	if notice, err := session.InterruptedSubagentNotice(); err != nil || notice != "" {
		t.Fatalf("notice = %q, error = %v", notice, err)
	}
}

func TestFollowUpToACompletedRun(t *testing.T) {
	child := &backgroundChildProvider{chat: func(context.Context) (llm.Response, error) { return asyncTestResponse("answer"), nil }}
	fixture := newResumeFixture(t, child, map[string]string{"search": readOnlyAgent})
	session := fixture.open(t)
	first, err := session.RunSubagent(t.Context(), SubagentRunOptions{Name: "search", Task: "find callers"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.RunSubagent(t.Context(), SubagentRunOptions{ResumeChildID: first.ChildSessionID, Task: "which file has the most?"}); err != nil {
		t.Fatal(err)
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	if roles := messageRoles(child.messages); roles != "user,assistant,user" {
		t.Fatalf("roles = %s", roles)
	}
	if !strings.Contains(child.messages[2].Content, "which file has the most?") {
		t.Fatalf("follow-up = %q", child.messages[2].Content)
	}
}

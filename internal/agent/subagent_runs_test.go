package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

// newSubagentRunsSession opens session "parent" with one "search" agent that
// runs on provider.
func newSubagentRunsSession(t *testing.T, baseDir, workspace string, provider llm.Provider) *AgentSession {
	t.Helper()
	logger := log.New()
	session, err := NewAgentSession("test", "parent", SessionParams{
		BaseDir: baseDir, WorkspaceDir: workspace, Logger: logger, Provider: provider,
		ToolRegistry: &tools.Registry{Logger: logger, WorkspaceDir: workspace}, SystemPrompt: "parent prompt",
		ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func writeSearchAgent(t *testing.T, workspace string) {
	t.Helper()
	dir := filepath.Join(workspace, ".caretz", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: Search the code.\nmode: subagent\n---\n\nSearch.\n"
	if err := os.WriteFile(filepath.Join(dir, "search.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func onlySubagentRun(t *testing.T, session *AgentSession) SubagentRun {
	t.Helper()
	runs, err := session.SubagentRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	return runs[0]
}

func TestSubagentRunsReportHowForegroundRunsEnd(t *testing.T) {
	errProvider := errors.New("provider exploded")
	tests := []struct {
		name       string
		ctx        func() (context.Context, context.CancelFunc)
		chat       func(context.Context) (llm.Response, error)
		wantStatus SubagentRunStatus
		wantReason string
	}{
		{
			name:       "completed",
			chat:       func(context.Context) (llm.Response, error) { return asyncTestResponse("found it"), nil },
			wantStatus: SubagentCompleted,
		},
		{
			name: "user cancelled",
			ctx:  func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			chat: func(ctx context.Context) (llm.Response, error) {
				return llm.Response{}, context.Canceled
			},
			wantStatus: SubagentCancelled, wantReason: eventing.SubagentStopUserCancelled,
		},
		{
			name: "timed out",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), time.Hour)
			},
			chat: func(ctx context.Context) (llm.Response, error) {
				return llm.Response{}, context.DeadlineExceeded
			},
			wantStatus: SubagentTimedOut, wantReason: eventing.SubagentStopTimeout,
		},
		{
			name:       "provider error",
			chat:       func(context.Context) (llm.Response, error) { return llm.Response{}, errProvider },
			wantStatus: SubagentFailed, wantReason: eventing.SubagentStopError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeSearchAgent(t, workspace)
			provider := &asyncSubagentProvider{chat: func(ctx context.Context, _ []llm.Message) (llm.Response, error) { return tt.chat(ctx) }}
			session := newSubagentRunsSession(t, filepath.Join(t.TempDir(), "sessions"), workspace, provider)
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if tt.ctx != nil {
				ctx, cancel = tt.ctx()
			}
			defer cancel()
			if _, err := session.RunSubagent(ctx, SubagentRunOptions{Name: "search", Task: "find callers"}); (err != nil) != (tt.wantStatus != SubagentCompleted) {
				t.Fatalf("RunSubagent error = %v", err)
			}
			run := onlySubagentRun(t, session)
			if run.Status != tt.wantStatus || run.StopReason != tt.wantReason {
				t.Fatalf("run = %+v, want status %s reason %q", run, tt.wantStatus, tt.wantReason)
			}
			if run.Name != "search" || run.Task != "find callers" || !run.Resumable {
				t.Fatalf("run = %+v", run)
			}
		})
	}
}

func TestSubagentRunIsRunningUntilItEnds(t *testing.T) {
	workspace := t.TempDir()
	writeSearchAgent(t, workspace)
	started, release := make(chan struct{}), make(chan struct{})
	provider := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) {
		close(started)
		<-release
		return asyncTestResponse("done"), nil
	}}
	session := newSubagentRunsSession(t, filepath.Join(t.TempDir(), "sessions"), workspace, provider)
	done := make(chan error, 1)
	go func() {
		_, err := session.RunSubagent(context.Background(), SubagentRunOptions{Name: "search", Task: "find callers"})
		done <- err
	}()
	awaitAsyncSignal(t, started)
	if run := onlySubagentRun(t, session); run.Status != SubagentRunning || run.Resumable {
		t.Fatalf("while running: %+v", run)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if run := onlySubagentRun(t, session); run.Status != SubagentCompleted || run.Result != "done" {
		t.Fatalf("after finishing: %+v", run)
	}
}

func TestSubagentRunWithNoFinalEventIsInterruptedAfterRestart(t *testing.T) {
	baseDir, workspace := filepath.Join(t.TempDir(), "sessions"), t.TempDir()
	provider := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
	first := newSubagentRunsSession(t, baseDir, workspace, provider)
	// The process dies between these two: only Started reaches the log.
	childID := "parent-subagent-search-1"
	if err := first.AppendAuditEvent(eventing.NewSubagentInvocationStartedWithConfig("parent", "search", childID, "parent", "find callers", "local/fast", "high", 128000)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newSubagentRunsSession(t, baseDir, workspace, provider)
	run := onlySubagentRun(t, second)
	if run.Status != SubagentInterrupted || run.StopReason != eventing.SubagentStopProcessExit || !run.Resumable {
		t.Fatalf("run = %+v", run)
	}
	if run.Model != "local/fast" || run.ReasoningEffort != "high" {
		t.Fatalf("run lost its configuration: %+v", run)
	}
}

func TestAsyncSubagentReportsClosingTheSession(t *testing.T) {
	started := make(chan struct{})
	child := &asyncSubagentProvider{chat: func(ctx context.Context, _ []llm.Message) (llm.Response, error) {
		close(started)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}}
	session := newAsyncSubagentTestSession(t, child, nil)
	requestAsyncSubagent(t, t.Context(), session, "background task")
	awaitAsyncSignal(t, started)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	run := onlySubagentRun(t, session)
	if run.Status != SubagentInterrupted || run.StopReason != eventing.SubagentStopSessionClosed {
		t.Fatalf("run = %+v", run)
	}
	if run.Model != "child" {
		t.Fatalf("model = %q", run.Model)
	}
	if run.Resumable {
		t.Fatalf("a run with no agent definition must not be resumable: %+v", run)
	}
}

func TestSubagentRunsHandleLegacyEvents(t *testing.T) {
	tests := []struct {
		name       string
		errText    string
		wantStatus SubagentRunStatus
	}{
		{"cancelled", "turn: context canceled", SubagentCancelled},
		{"timed out", "context deadline exceeded", SubagentTimedOut},
		{"failed", "provider exploded", SubagentFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
			session := newSubagentRunsSession(t, filepath.Join(t.TempDir(), "sessions"), t.TempDir(), provider)
			childID := "parent-subagent-search-1"
			for _, event := range []func() error{
				func() error {
					return session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", "search", childID, "parent", "find callers", 128000))
				},
				func() error {
					return session.AppendAuditEvent(eventing.NewSubagentInvocationFailed("parent", "search", childID, "parent", tt.errText, 0, 128000))
				},
			} {
				if err := event(); err != nil {
					t.Fatal(err)
				}
			}
			if run := onlySubagentRun(t, session); run.Status != tt.wantStatus || !run.Resumable {
				t.Fatalf("run = %+v, want %s", run, tt.wantStatus)
			}
		})
	}
}

func TestSubagentRunsExcludeOtherSessionsAndInternalChildrenFromResume(t *testing.T) {
	provider := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
	session := newSubagentRunsSession(t, filepath.Join(t.TempDir(), "sessions"), t.TempDir(), provider)
	for name, childID := range map[string]string{
		"search":      "other-subagent-search-1",
		"goal-worker": "parent-goal-worker-1",
		"background":  "parent-background-1",
	} {
		if err := session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", name, childID, "parent", "task", 128000)); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := session.SubagentRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("runs = %+v", runs)
	}
	for _, run := range runs {
		if run.Resumable {
			t.Fatalf("%s must not be resumable: %+v", run.ChildSessionID, run)
		}
	}
}

func TestSubagentRunRestartedKeepsOneEntryWithLatestState(t *testing.T) {
	provider := &asyncSubagentProvider{chat: func(context.Context, []llm.Message) (llm.Response, error) { return asyncTestResponse("ok"), nil }}
	session := newSubagentRunsSession(t, filepath.Join(t.TempDir(), "sessions"), t.TempDir(), provider)
	childID := "parent-subagent-search-1"
	appendEvents := []error{
		session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", "search", childID, "parent", "find callers", 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationStopped("parent", "search", childID, "parent", "context canceled", eventing.SubagentStopUserCancelled, 0, 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("parent", "search", childID, "parent", "continue", 128000)),
		session.AppendAuditEvent(eventing.NewSubagentInvocationCompleted("parent", "search", childID, "parent", "found it", 0, 128000)),
	}
	if err := errors.Join(appendEvents...); err != nil {
		t.Fatal(err)
	}
	run := onlySubagentRun(t, session)
	if run.Status != SubagentCompleted || run.Result != "found it" || run.StopReason != "" || run.Error != "" || run.Task != "find callers" {
		t.Fatalf("run = %+v", run)
	}
}

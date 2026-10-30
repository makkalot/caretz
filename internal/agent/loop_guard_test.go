package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/loopguard"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

// loopGuardProvider plays both roles: the agent, which runs one Bash command
// per step and then answers, and the judge, recognized by its system prompt.
type loopGuardProvider struct {
	mu         sync.Mutex
	steps      int
	agentCalls [][]llm.Message
	judgeCalls []string
	judgeReply string
	judgeErr   error
}

func (p *loopGuardProvider) Chat(systemPrompt string, messages []llm.Message, _ []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if systemPrompt == loopguard.JudgeSystemPrompt {
		p.judgeCalls = append(p.judgeCalls, messages[len(messages)-1].Content)
		if p.judgeErr != nil {
			return llm.Response{}, p.judgeErr
		}
		return llm.Response{Content: p.judgeReply, Message: llm.Message{Role: llm.RoleAssistant, Content: p.judgeReply}}, nil
	}
	p.agentCalls = append(p.agentCalls, cloneMessages(messages))
	if len(p.agentCalls) > p.steps {
		return llm.Response{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}}, nil
	}
	call := llm.ToolCall{ID: fmt.Sprintf("call-%d", len(p.agentCalls)), Name: "Bash", Arguments: `{"command":"go test ./..."}`}
	return llm.Response{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}}, nil
}

func (p *loopGuardProvider) GetModel() string   { return "loop-guard-mock" }
func (p *loopGuardProvider) GetBaseURL() string { return "http://mock" }

const stuckVerdict = `{"stuck": true, "confidence": 0.9, "reason": "Foo is never declared", "alternative": "add the missing import for Foo"}`

// newLoopGuardSession returns a session whose Bash runs fail with stderr
// from failures, one entry per call (the last repeats).
func newLoopGuardSession(t *testing.T, baseDir string, provider llm.Provider, failures []string) *AgentSession {
	t.Helper()
	logger := log.New()
	logger.SetVerbosity(0)
	calls := 0
	registry := &tools.Registry{
		Logger: logger,
		RunSandboxed: func(cwd, command string) (string, string, error) {
			stderr := failures[min(calls, len(failures)-1)]
			calls++
			return "", fmt.Sprintf("/tmp/build%d/x_test.go:%d:2: %s", calls, 10+calls, stderr), errors.New("exit status 1")
		},
		DisabledTools: map[string]bool{"WebFetch": true},
	}
	session, err := NewAgentSession("test", "loop-guard", SessionParams{
		BaseDir:      baseDir,
		WorkspaceDir: t.TempDir(),
		Logger:       logger,
		Provider:     provider,
		ToolRegistry: registry,
	})
	if err != nil {
		t.Fatalf("NewAgentSession() error = %v", err)
	}
	return session
}

func loopGuardNotes(messages []llm.Message) []string {
	var notes []string
	for _, msg := range messages {
		if msg.Role == llm.RoleTool && strings.Contains(msg.Content, "[loop guard]") {
			notes = append(notes, msg.Content)
		}
	}
	return notes
}

func TestLoopGuardRedirectsRepeatedFailureOnce(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	provider := &loopGuardProvider{steps: 5, judgeReply: stuckVerdict}
	session := newLoopGuardSession(t, baseDir, provider, []string{"undefined: Foo"})
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeActive})
	var statuses []string
	if err := session.OnAuditEvent(eventing.StatusUpdated, func(e *types.Event) error {
		statuses = append(statuses, e.Payload)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := session.Call(context.Background(), "make the tests pass"); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(provider.judgeCalls) != 1 {
		t.Fatalf("judge calls = %d, want 1 (at the third repeat)", len(provider.judgeCalls))
	}
	if len(statuses) != 1 || !strings.Contains(statuses[0], `"source":"loop-guard"`) || !strings.Contains(statuses[0], "judging: repeat: 3×") {
		t.Fatalf("status notices = %q, want one judging notice", statuses)
	}
	if digest := provider.judgeCalls[0]; !strings.Contains(digest, "make the tests pass") || !strings.Contains(digest, "3× Bash:") {
		t.Fatalf("unexpected digest:\n%s", digest)
	}
	// The fourth agent request is the first to see the third tool result.
	notes := loopGuardNotes(provider.agentCalls[3])
	if len(notes) != 1 || !strings.Contains(notes[0], "add the missing import for Foo") || !strings.Contains(notes[0], "3 times") {
		t.Fatalf("agent request notes = %q, want one redirect", notes)
	}
	if got := loopGuardNotes(provider.agentCalls[len(provider.agentCalls)-1]); len(got) != 1 {
		t.Fatalf("redirects in final request = %d, want 1 (capped)", len(got))
	}

	events, err := session.AuditEvents()
	if err != nil {
		t.Fatalf("AuditEvents() error = %v", err)
	}
	judged := 0
	for _, e := range events {
		if e.EventType == LoopGuardJudgedEvent {
			judged++
			if !strings.Contains(e.Payload, `"redirected":true`) {
				t.Fatalf("judged event = %s, want redirected", e.Payload)
			}
		}
	}
	if judged != 1 {
		t.Fatalf("LoopGuard.Judged events = %d, want 1", judged)
	}

	// The redirect lives inside a tool result, so a reloaded session
	// replays the same context.
	reloaded := newLoopGuardSession(t, baseDir, &mockProvider{}, []string{"unused"})
	if got := loopGuardNotes(reloaded.GetContextMessages()); len(got) != 1 || got[0] != notes[0] {
		t.Fatalf("reloaded notes = %q, want %q", got, notes)
	}
}

func TestLoopGuardStaysQuietOnProgress(t *testing.T) {
	provider := &loopGuardProvider{steps: 5, judgeReply: stuckVerdict}
	session := newLoopGuardSession(t, filepath.Join(t.TempDir(), "sessions"), provider,
		[]string{"undefined: A", "undefined: B", "cannot use x as int", "missing return", "undefined: C"})
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeActive})

	if _, err := session.Call(context.Background(), "fix the build"); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(provider.judgeCalls) != 0 {
		t.Fatalf("judge calls = %d, want 0 for a progressing turn", len(provider.judgeCalls))
	}
}

func TestLoopGuardFailsOpen(t *testing.T) {
	for name, provider := range map[string]*loopGuardProvider{
		"judge error":     {steps: 4, judgeErr: errors.New("judge down")},
		"malformed reply": {steps: 4, judgeReply: "I think it is stuck"},
	} {
		t.Run(name, func(t *testing.T) {
			session := newLoopGuardSession(t, filepath.Join(t.TempDir(), "sessions"), provider, []string{"undefined: Foo"})
			session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeActive})
			resp, err := session.Call(context.Background(), "make the tests pass")
			if err != nil || resp.Content != "done" {
				t.Fatalf("Call() = %v, %v; want done", resp, err)
			}
			if len(provider.judgeCalls) != 1 {
				t.Fatalf("judge calls = %d, want 1", len(provider.judgeCalls))
			}
			if notes := loopGuardNotes(session.GetContextMessages()); len(notes) != 0 {
				t.Fatalf("notes = %q, want none", notes)
			}
		})
	}
}

func TestLoopGuardShadowModeJudgesWithoutRedirect(t *testing.T) {
	provider := &loopGuardProvider{steps: 4, judgeReply: stuckVerdict}
	session := newLoopGuardSession(t, filepath.Join(t.TempDir(), "sessions"), provider, []string{"undefined: Foo"})
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeShadow})

	if _, err := session.Call(context.Background(), "make the tests pass"); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(provider.judgeCalls) != 1 {
		t.Fatalf("judge calls = %d, want 1", len(provider.judgeCalls))
	}
	if notes := loopGuardNotes(session.GetContextMessages()); len(notes) != 0 {
		t.Fatalf("shadow mode redirected: %q", notes)
	}
}

func TestValidateLoopGuardResolvesJudgeModel(t *testing.T) {
	session := newLoopGuardSession(t, filepath.Join(t.TempDir(), "sessions"), &mockProvider{}, []string{"x"})
	session.SetSubagentModelResolver(subagentModelTestResolver{})

	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeActive})
	if err := session.ValidateLoopGuard(context.Background()); err != nil {
		t.Fatalf("no judge model: error = %v, want nil (current model)", err)
	}
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeOff, JudgeModel: "missing/model"})
	if err := session.ValidateLoopGuard(context.Background()); err != nil {
		t.Fatalf("guard off: error = %v, want nil", err)
	}
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeActive, JudgeModel: "missing/model"})
	if err := session.ValidateLoopGuard(context.Background()); err == nil {
		t.Fatal("unknown judge model: error = nil")
	}
}

func TestLoopGuardJudgesOnConfiguredModel(t *testing.T) {
	agentProvider := &loopGuardProvider{steps: 3}
	judgeProvider := &loopGuardProvider{judgeReply: stuckVerdict}
	var requests []SubagentModelRequest
	session := newLoopGuardSession(t, filepath.Join(t.TempDir(), "sessions"), agentProvider, []string{"undefined: Foo"})
	session.SetSubagentModelResolver(subagentModelTestResolver{
		models:   map[string]SubagentModelResolution{"judge/model": {Provider: judgeProvider}},
		requests: &requests,
	})
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeActive, JudgeModel: "judge/model", JudgeReasoning: "high"})

	if _, err := session.Call(context.Background(), "make the tests pass"); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(judgeProvider.judgeCalls) != 1 || len(agentProvider.judgeCalls) != 0 {
		t.Fatalf("judge calls: judge model %d, agent model %d; want 1, 0", len(judgeProvider.judgeCalls), len(agentProvider.judgeCalls))
	}
	if len(requests) != 1 || requests[0].Ref != "judge/model" || requests[0].ReasoningEffort != "high" {
		t.Fatalf("resolver requests = %+v", requests)
	}
}

func TestLoopGuardInheritedByChildSessions(t *testing.T) {
	session := newLoopGuardSession(t, filepath.Join(t.TempDir(), "sessions"), &mockProvider{}, []string{"x"})
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeShadow, JudgeModel: "judge"})
	child, err := session.NewChildSession("child")
	if err != nil {
		t.Fatalf("NewChildSession() error = %v", err)
	}
	if got := child.LoopGuard(); got.Mode != loopguard.ModeShadow || got.JudgeModel != "judge" {
		t.Fatalf("child loop guard = %+v", got)
	}
}

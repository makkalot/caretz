package agent

import (
	"context"
	"encoding/json"
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
	// agentTools is how many tools each agent request carried.
	agentTools []int
	judgeCalls []string
	judgeReply string
	judgeErr   error
}

func (p *loopGuardProvider) Chat(systemPrompt string, messages []llm.Message, tools []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
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
	p.agentTools = append(p.agentTools, len(tools))
	if len(tools) == 0 {
		return llm.Response{Content: "wrapped up", Message: llm.Message{Role: llm.RoleAssistant, Content: "wrapped up"}}, nil
	}
	if len(p.agentCalls) > p.steps {
		return llm.Response{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}}, nil
	}
	call := llm.ToolCall{ID: fmt.Sprintf("call-%d", len(p.agentCalls)), Name: "Bash", Arguments: `{"command":"go test ./..."}`}
	say := fmt.Sprintf("Step %d: running the tests.", len(p.agentCalls))
	return llm.Response{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, Content: say, ToolCalls: []llm.ToolCall{call}}}, nil
}

func (p *loopGuardProvider) GetModel() string   { return "loop-guard-mock" }
func (p *loopGuardProvider) GetBaseURL() string { return "http://mock" }

const stuckVerdict = `{"stuck": true, "confidence": 0.9, "reason": "Foo is never declared", "alternative": "add the missing import for Foo"}`

// newLoopGuardSession returns a session whose Bash runs fail with stderr
// from failures, one entry per call (the last repeats); an empty entry
// succeeds.
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
			if stderr == "" {
				return "ok", "", nil
			}
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

// loopGuardNotes returns the loop-guard notes among messages, from any role.
func loopGuardNotes(messages []llm.Message) []string {
	var notes []string
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[loop guard]") {
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
	if digest := provider.judgeCalls[0]; !strings.Contains(digest, "make the tests pass") || !strings.Contains(digest, "Trigger: repeat: 3×") || !strings.Contains(digest, "3× Bash:") {
		t.Fatalf("unexpected digest:\n%s", digest)
	}
	// The fourth agent request is the first to see the third tool result,
	// and the note comes after it as a message of its own.
	if got := loopGuardNotes(provider.agentCalls[2]); len(got) != 0 {
		t.Fatalf("note before the judge ran: %q", got)
	}
	request := provider.agentCalls[3]
	last := request[len(request)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Content, "add the missing import for Foo") || !strings.Contains(last.Content, "3 times") || !strings.Contains(last.Content, `"make the tests pass"`) {
		t.Fatalf("last message of request 4 = %+v, want the redirect note", last)
	}
	if notes := loopGuardNotes(request); len(notes) != 1 {
		t.Fatalf("request 4 notes = %q, want only the separate note", notes)
	}
	// The note stays last for the rest of the turn, once.
	final := provider.agentCalls[len(provider.agentCalls)-1]
	if got := loopGuardNotes(final); len(got) != 1 || final[len(final)-1].Content != last.Content {
		t.Fatalf("final request notes = %q, want the same note last", got)
	}
	for i, n := range provider.agentTools {
		if n == 0 {
			t.Fatalf("request %d went without tools after a single redirect", i+1)
		}
	}

	events, err := session.AuditEvents()
	if err != nil {
		t.Fatalf("AuditEvents() error = %v", err)
	}
	var records []LoopGuardJudged
	for _, e := range events {
		if e.EventType == LoopGuardJudgedEvent {
			var record LoopGuardJudged
			if err := json.Unmarshal([]byte(e.Payload), &record); err != nil {
				t.Fatal(err)
			}
			records = append(records, record)
		}
	}
	if len(records) != 1 || !records[0].Redirected || records[0].Escalated || records[0].Note != last.Content {
		t.Fatalf("LoopGuard.Judged records = %+v, want one redirect carrying the note", records)
	}

	// The note is not part of the session's history, live or reloaded; the
	// audit record keeps it.
	if got := loopGuardNotes(session.GetContextMessages()); len(got) != 0 {
		t.Fatalf("context notes = %q, want none", got)
	}
	reloaded := newLoopGuardSession(t, baseDir, &mockProvider{}, []string{"unused"})
	if got := loopGuardNotes(reloaded.GetContextMessages()); len(got) != 0 {
		t.Fatalf("reloaded notes = %q, want none", got)
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

func TestLoopGuardCheckpointJudgesErrorFreeTurn(t *testing.T) {
	provider := &loopGuardProvider{steps: 9, judgeReply: `{"stuck": true, "confidence": 0.8, "reason": "the same test command is run over and over", "alternative": "read the test file instead"}`}
	session := newLoopGuardSession(t, filepath.Join(t.TempDir(), "sessions"), provider, []string{""})
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeActive, CheckEvery: 4})

	var statuses []string
	if err := session.OnAuditEvent(eventing.StatusUpdated, func(e *types.Event) error {
		statuses = append(statuses, e.Payload)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := session.Call(context.Background(), "investigate the flaky test")
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	// Checkpoints at calls 4 and 8; the second verdict wraps the turn up
	// instead of redirecting again.
	if len(provider.judgeCalls) != 2 || !strings.Contains(provider.judgeCalls[0], "checkpoint: 4 tool calls") {
		t.Fatalf("judge calls = %q, want two checkpoints", provider.judgeCalls)
	}
	if !strings.Contains(provider.judgeCalls[0], "4× Bash go test ./...") {
		t.Fatalf("digest does not show the repeated command:\n%s", provider.judgeCalls[0])
	}
	if !strings.Contains(provider.judgeCalls[0], "- Step 4: running the tests.") {
		t.Fatalf("digest does not show what the agent said:\n%s", provider.judgeCalls[0])
	}
	if !strings.Contains(provider.judgeCalls[1], "Earlier checks this turn:\n- after call 4 (checkpoint: 4 tool calls since the last check): stuck (0.80)") {
		t.Fatalf("second digest does not show the first verdict:\n%s", provider.judgeCalls[1])
	}

	// Each audit record carries exactly what the judge was shown.
	events, err := session.AuditEvents()
	if err != nil {
		t.Fatal(err)
	}
	var digests []string
	for _, e := range events {
		if e.EventType == LoopGuardJudgedEvent {
			var record LoopGuardJudged
			if err := json.Unmarshal([]byte(e.Payload), &record); err != nil {
				t.Fatal(err)
			}
			digests = append(digests, record.Digest)
		}
	}
	if len(digests) != 2 || digests[0] != provider.judgeCalls[0] || digests[1] != provider.judgeCalls[1] {
		t.Fatalf("recorded digests do not match what the judge saw")
	}

	// After the wrap-up the agent gets one request without tools, with the
	// wrap-up note replacing the redirect, and its answer ends the turn.
	if len(provider.agentCalls) != 9 || resp.Content != "wrapped up" {
		t.Fatalf("agent requests = %d, response %q; want 9 ending in the wrap-up", len(provider.agentCalls), resp.Content)
	}
	if provider.agentTools[8] != 0 || provider.agentTools[7] == 0 {
		t.Fatalf("tools per request = %v, want tools removed only for the last", provider.agentTools)
	}
	final := provider.agentCalls[8]
	if notes := loopGuardNotes(final); len(notes) != 1 || !strings.Contains(notes[0], "tools are no longer available") || final[len(final)-1].Content != notes[0] {
		t.Fatalf("final request notes = %q, want only the wrap-up, last", notes)
	}
	if notes := loopGuardNotes(provider.agentCalls[4]); len(notes) != 1 || !strings.Contains(notes[0], "read the test file instead") {
		t.Fatalf("request 5 notes = %q, want the redirect", notes)
	}
	if !strings.Contains(statuses[len(statuses)-1], "asked the agent to stop and report") {
		t.Fatalf("last status = %s, want the wrap-up notice", statuses[len(statuses)-1])
	}
}

func TestNewTurnLoopGuardAddsPreviousExchange(t *testing.T) {
	session := newLoopGuardSession(t, filepath.Join(t.TempDir(), "sessions"), &mockProvider{}, []string{"x"})
	session.SetLoopGuard(loopguard.Config{Mode: loopguard.ModeShadow})
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{}`}
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "older request"},
		{Role: llm.RoleAssistant, Content: "older answer"},
		{Role: llm.RoleUser, Content: "migrate the users table"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}},
		{Role: llm.RoleTool, ToolCallID: "1", Content: "ok"},
		{Role: llm.RoleAssistant, Content: "Migration written; I'll verify it next."},
		{Role: llm.RoleUser, Content: "go ahead"},
	}
	d := session.newTurnLoopGuard(messages, false).guard.Digest()
	if !strings.Contains(d, "Task: go ahead") || !strings.Contains(d, "previous user message: migrate the users table") || !strings.Contains(d, "previous agent answer (start): Migration written") {
		t.Fatalf("digest lacks the previous exchange:\n%s", d)
	}
	if session.newTurnLoopGuard(messages, true) != nil {
		t.Fatal("summary turns should not get a guard")
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
			for _, request := range provider.agentCalls {
				if notes := loopGuardNotes(request); len(notes) != 0 {
					t.Fatalf("notes = %q, want none", notes)
				}
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
	for _, request := range provider.agentCalls {
		if notes := loopGuardNotes(request); len(notes) != 0 {
			t.Fatalf("shadow mode redirected: %q", notes)
		}
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

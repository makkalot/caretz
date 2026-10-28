package slashcmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type agentCommandProvider struct {
	responses     []llm.Response
	systemPrompts []string
	prompts       []string
	tools         [][]llm.ToolDefinition
}

func (p *agentCommandProvider) Chat(systemPrompt string, messages []llm.Message, defs []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	p.systemPrompts = append(p.systemPrompts, systemPrompt)
	p.tools = append(p.tools, append([]llm.ToolDefinition{}, defs...))
	if len(messages) > 0 {
		p.prompts = append(p.prompts, messages[len(messages)-1].Content)
	}
	if len(p.responses) == 0 {
		t := llm.Response{Content: "ok", Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}
		return t, nil
	}
	resp := p.responses[0]
	p.responses = p.responses[1:]
	return resp, nil
}

func (p *agentCommandProvider) GetModel() string   { return "mock-model" }
func (p *agentCommandProvider) GetBaseURL() string { return "http://mock" }

type agentCommandModelResolver struct {
	provider llm.Provider
	requests []agent.SubagentModelRequest
}

func (r *agentCommandModelResolver) ResolveSubagentModel(ctx context.Context, request agent.SubagentModelRequest) (agent.SubagentModelResolution, error) {
	r.requests = append(r.requests, request)
	if request.Ref != "local/review" {
		return agent.SubagentModelResolution{}, fmt.Errorf("unknown configured subagent model %q", request.Ref)
	}
	return agent.SubagentModelResolution{
		Ref:              request.Ref,
		Model:            "review-model",
		Provider:         r.provider,
		ContextWindow:    32000,
		ReservedTokens:   4000,
		KeepRecentTokens: 8000,
		SummaryTokens:    2000,
	}, nil
}

func TestSubagentRunnerRunsChildAndKeepsLifecycleOutOfParentContext(t *testing.T) {
	cwd := t.TempDir()
	writeTestSubagent(t, cwd, "review", `---
description: Reviews code
mode: subagent
permission:
  edit: deny
  bash: deny
---
You are in code review mode.`)

	provider := &agentCommandProvider{responses: []llm.Response{
		{
			ToolCalls: []llm.ToolCall{{ID: "tc-1", Name: "Read", Arguments: `{"file_path":"CHANGELOG.md"}`}},
			Message:   llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "tc-1", Name: "Read", Arguments: `{"file_path":"CHANGELOG.md"}`}}},
		},
		{Content: "review result", Message: llm.Message{Role: llm.RoleAssistant, Content: "review result"}},
		{Content: "parent used review", Message: llm.Message{Role: llm.RoleAssistant, Content: "parent used review"}},
	}}
	logger := log.New()
	registry := &tools.Registry{
		Logger: logger,
		RunSandboxed: func(cwd, command string) (string, string, error) {
			return "ok", "", nil
		},
	}
	session, err := agent.NewAgentSession("test", "parent", agent.SessionParams{
		BaseDir:          filepath.Join(cwd, ".caretz", "sessions"),
		WorkspaceDir:     cwd,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     registry,
		SystemPrompt:     "parent prompt",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatalf("NewAgentSession() error = %v", err)
	}

	var liveProgress int
	if err := session.OnAuditEvent(eventing.SubagentInvocationProgress, func(e *types.Event) error {
		liveProgress++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	result, err := (SubagentRunner{}).Run(context.Background(), session, "review", "check the diff")
	if err != nil {
		t.Fatalf("SubagentRunner.Run() error = %v", err)
	}
	if result.Message != "" {
		t.Fatalf("result message = %q, want empty because audit event renders completion", result.Message)
	}

	events, err := session.GetEvents()
	if err != nil {
		t.Fatal(err)
	}
	var sawStarted, sawCompleted bool
	for _, e := range events {
		if e.EventType == eventing.SubagentInvocationStarted {
			sawStarted = true
		}
		if e.EventType == eventing.SubagentInvocationCompleted {
			sawCompleted = true
		}
		if e.EventType == eventing.SubagentInvocationProgress {
			t.Fatalf("subagent progress should not be persisted in parent events.jsonpl")
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("missing subagent audit events: started=%t completed=%t", sawStarted, sawCompleted)
	}
	if liveProgress == 0 {
		t.Fatal("expected live progress to be published")
	}

	ctxEvents, err := session.GetContextEvents()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ctxEvents {
		if strings.HasPrefix(e.EventType, "SubagentInvocation.") {
			t.Fatalf("subagent audit event leaked into context: %s", e.EventType)
		}
	}
	if len(provider.prompts) != 3 || !strings.Contains(provider.prompts[2], "Subagent result:\nreview result") {
		t.Fatalf("parent prompt did not receive subagent result: %#v", provider.prompts)
	}
	if len(provider.tools) < 1 {
		t.Fatal("provider did not capture child tool definitions")
	}
	for _, denied := range []string{"Edit", "Write", "Bash"} {
		if hasTool(provider.tools[0], denied) {
			t.Fatalf("child tool definitions include denied tool %s", denied)
		}
	}
}

func TestSubagentRunnerUsesConfiguredReasoning(t *testing.T) {
	cwd := t.TempDir()
	writeTestSubagent(t, cwd, "review", `---
description: Reviews code
mode: subagent
model: local/review
reasoning: xhigh
---
You are in code review mode.`)

	parentProvider := &agentCommandProvider{responses: []llm.Response{
		{Content: "parent used review", Message: llm.Message{Role: llm.RoleAssistant, Content: "parent used review"}},
	}}
	childProvider := &agentCommandProvider{responses: []llm.Response{
		{Content: "review result", Message: llm.Message{Role: llm.RoleAssistant, Content: "review result"}},
	}}
	logger := log.New()
	session, err := agent.NewAgentSession("test", "parent", agent.SessionParams{
		BaseDir:          filepath.Join(cwd, ".caretz", "sessions"),
		WorkspaceDir:     cwd,
		Logger:           logger,
		Provider:         parentProvider,
		ToolRegistry:     &tools.Registry{Logger: logger},
		SystemPrompt:     "parent prompt",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatalf("NewAgentSession() error = %v", err)
	}
	resolver := &agentCommandModelResolver{provider: childProvider}
	session.SetSubagentModelResolver(resolver)

	if _, err := (SubagentRunner{}).Run(context.Background(), session, "review", "check the diff"); err != nil {
		t.Fatalf("SubagentRunner.Run() error = %v", err)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("resolver requests = %d, want 1", len(resolver.requests))
	}
	if resolver.requests[0].ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning effort = %q, want xhigh", resolver.requests[0].ReasoningEffort)
	}
}

func TestSubagentRunnerFallbackUsesSessionWorkspaceDir(t *testing.T) {
	workspaceDir := t.TempDir()
	configRoot := t.TempDir()
	writeTestSubagent(t, workspaceDir, "review", `---
description: Reviews code
mode: subagent
---
You are in code review mode.`)

	provider := &agentCommandProvider{responses: []llm.Response{
		{Content: "review result", Message: llm.Message{Role: llm.RoleAssistant, Content: "review result"}},
		{Content: "parent used review", Message: llm.Message{Role: llm.RoleAssistant, Content: "parent used review"}},
	}}
	logger := log.New()
	registry := &tools.Registry{Logger: logger}
	session, err := agent.NewAgentSession("test", "parent", agent.SessionParams{
		BaseDir:          filepath.Join(configRoot, "sessions"),
		WorkspaceDir:     workspaceDir,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     registry,
		SystemPrompt:     "parent prompt",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatalf("NewAgentSession() error = %v", err)
	}

	if _, err := (SubagentRunner{}).Run(context.Background(), session, "review", "check the diff"); err != nil {
		t.Fatalf("SubagentRunner.Run() error = %v", err)
	}
	if len(provider.systemPrompts) == 0 || !strings.HasPrefix(provider.systemPrompts[0], "You are in code review mode.") {
		t.Fatalf("first system prompt = %q, want workspace subagent prompt", provider.systemPrompts)
	}
}

func TestParseSubagentShorthand(t *testing.T) {
	available := func(name string) bool { return name == "review" }
	name, args, ok := ParseSubagentShorthand("@review check this", available)
	if !ok || name != "review" || args != "check this" {
		t.Fatalf("ParseSubagentShorthand() = %q %q %t", name, args, ok)
	}
	if _, _, ok := ParseSubagentShorthand("@README.md", available); ok {
		t.Fatal("path-like @README.md parsed as subagent")
	}
}

func writeTestSubagent(t *testing.T, cwd, name, content string) {
	t.Helper()
	dir := filepath.Join(cwd, ".caretz", "agents")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func hasTool(defs []llm.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

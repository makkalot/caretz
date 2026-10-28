package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/llm"
)

type promptRecordingProvider struct {
	systemPrompts []string
	lastUser      []string
	tools         [][]llm.ToolDefinition
}

func (p *promptRecordingProvider) Chat(systemPrompt string, messages []llm.Message, tools []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	p.systemPrompts = append(p.systemPrompts, systemPrompt)
	p.tools = append(p.tools, tools)
	p.lastUser = append(p.lastUser, messages[len(messages)-1].Content)
	return llm.Response{Content: "ok", Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}, nil
}

func (p *promptRecordingProvider) GetModel() string   { return "mock-model" }
func (p *promptRecordingProvider) GetBaseURL() string { return "http://mock" }

func TestPlanModeToggleNotifiesModelOnNextUserMessageOnly(t *testing.T) {
	provider := &promptRecordingProvider{}
	session, err := NewAgentSession("test", "plan-toggle", SessionParams{InMemorySessionStore: true, Provider: provider, SystemPrompt: "system"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(prompt string) {
		t.Helper()
		if _, err := session.CallUntilStopWithPermissions(context.Background(), prompt, nil); err != nil {
			t.Fatal(err)
		}
	}

	call("look around")
	session.SetPlanMode(true)
	call("plan the change")
	call("refine it")
	session.SetPlanMode(false)
	call("implement it")

	want := []string{"look around", "plan the change\n\n" + planModeNotice, "refine it", "implement it\n\n" + buildModeNotice}
	for i, content := range want {
		if provider.lastUser[i] != content {
			t.Fatalf("message %d = %q, want %q", i, provider.lastUser[i], content)
		}
	}
	for i := range provider.systemPrompts {
		if provider.systemPrompts[i] != provider.systemPrompts[0] || len(provider.tools[i]) != len(provider.tools[0]) {
			t.Fatalf("turn %d changed the system prompt or tool list", i)
		}
	}
	for _, msg := range session.GetContextMessages() {
		if msg.Role == llm.RoleUser && strings.Contains(StripModeNotice(msg.Content), "caretz-mode") {
			t.Fatalf("StripModeNotice left a notice in %q", msg.Content)
		}
	}
}

func TestAnnouncedPlanModeUsesLatestNotice(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "a\n\n" + planModeNotice},
		{Role: llm.RoleAssistant, Content: "ok"},
		{Role: llm.RoleUser, Content: "b"},
	}
	if !announcedPlanMode(messages) {
		t.Fatal("plan notice should stay in effect until a build notice")
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "c\n\n" + buildModeNotice})
	if announcedPlanMode(messages) {
		t.Fatal("latest build notice should end plan mode")
	}
	if announcedPlanMode(nil) {
		t.Fatal("a conversation without notices is in build mode")
	}
}

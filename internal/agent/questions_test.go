package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

func hasTool(defs []llm.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

// AskUser is offered only to a turn someone can answer: an interactive turn,
// not prompt mode.
func TestAskUserOfferedOnlyWhenSomeoneCanAnswer(t *testing.T) {
	provider := &promptRecordingProvider{}
	session, err := NewAgentSession("test", "ask-user-offer", SessionParams{InMemorySessionStore: true, Provider: provider, ToolRegistry: &tools.Registry{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.Call(context.Background(), "prompt mode"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallUntilStopWithPermissions(context.Background(), "interactive", nil); err != nil {
		t.Fatal(err)
	}
	if hasTool(provider.tools[0], tools.AskUserToolName) {
		t.Fatal("AskUser offered to a turn nobody can answer")
	}
	if !hasTool(provider.tools[1], tools.AskUserToolName) {
		t.Fatal("AskUser not offered to an interactive turn")
	}
}

func TestSubagentRegistryDisablesAskUser(t *testing.T) {
	session, err := NewAgentSession("test", "ask-user-subagent", SessionParams{InMemorySessionStore: true, Provider: &mockProvider{}, ToolRegistry: &tools.Registry{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if SubagentToolRegistry(session, subagents.Definition{Name: "child"}).IsToolEnabled(tools.AskUserToolName) {
		t.Fatal("subagent can ask the user directly")
	}
}

// The questions travel as a permission request and the answers come back to
// the model as the tool result.
func TestAskUserAnswersReachModel(t *testing.T) {
	questions := []eventing.Question{{
		Header:   "Auth",
		Question: "Which auth flow?",
		Options:  []eventing.QuestionOption{{Label: "Device code", Recommended: true}, {Label: "Browser"}},
	}}
	arguments, _ := json.Marshal(map[string]any{"questions": questions})
	call := llm.ToolCall{ID: "ask", Name: tools.AskUserToolName, Arguments: string(arguments)}
	provider := &mockProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
		{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	session, err := NewAgentSession("test", "ask-user-answer", SessionParams{InMemorySessionStore: true, Provider: provider, ToolRegistry: &tools.Registry{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	done := make(chan error, 1)
	go func() {
		_, callErr := session.CallUntilStopWithPermissions(context.Background(), "set up auth", nil)
		done <- callErr
	}()
	request := waitForPendingPermission(t, session)
	got, err := eventing.DecodeQuestions(request)
	if err != nil || len(got) != 1 || got[0].Header != "Auth" || !request.NoSessionGrant {
		t.Fatalf("request = %+v, questions = %+v, err = %v", request, got, err)
	}
	answers := []eventing.QuestionAnswer{{Selected: []string{"Browser"}}}
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: request.RequestID, Decision: eventing.PermissionDecisionAllow, Metadata: eventing.QuestionAnswersMetadata(answers)}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var result string
	for _, message := range session.activeMessages() {
		if message.Role == llm.RoleTool && message.ToolCallID == "ask" {
			result = message.Content
		}
	}
	if !strings.Contains(result, "- Auth: Browser") {
		t.Fatalf("tool result = %q", result)
	}
}

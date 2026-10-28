package slashcmd

import (
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
)

type loopMockProvider struct{}

func (*loopMockProvider) Chat(_ string, _ []llm.Message, _ []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	return llm.Response{Content: "Context noted.", Message: llm.Message{Role: llm.RoleAssistant, Content: "Context noted."}}, nil
}

func (*loopMockProvider) GetModel() string   { return "mock-model" }
func (*loopMockProvider) GetBaseURL() string { return "http://mock" }

func newLoopTestSession(t *testing.T, provider *loopMockProvider, disableTasks bool) *agent.AgentSession {
	t.Helper()
	logger := log.New()
	registry := &tools.Registry{Logger: logger, DisableTasks: disableTasks}
	session, err := agent.NewAgentSession("test", "ch-loop", agent.SessionParams{
		BaseDir: t.TempDir(), Logger: logger, Provider: provider, ToolRegistry: registry,
		SystemPrompt: "you are a test agent", ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
		DisableTasks: disableTasks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

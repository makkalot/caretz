package caretz_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/makkalot/caretz"
	"github.com/makkalot/caretz/lib/conversation"
	"github.com/makkalot/caretz/lib/llm"
	libruntime "github.com/makkalot/caretz/lib/runtime"
	"github.com/makkalot/caretz/lib/tools"
)

var (
	_ conversation.Session = (*caretz.Session)(nil)
	_ libruntime.Session   = (*caretz.Session)(nil)
)

type fakeProvider struct {
	model        string
	resp         string
	calls        int
	lastMessages []llm.Message
	responses    []llm.Response
}

func (p *fakeProvider) Chat(systemPrompt string, messages []llm.Message, tools []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	p.calls++
	p.lastMessages = append([]llm.Message{}, messages...)
	if len(p.responses) > 0 {
		resp := p.responses[0]
		p.responses = p.responses[1:]
		return resp, nil
	}
	content := p.resp
	if content == "" {
		content = "ok"
	}
	return llm.Response{Content: content}, nil
}

func (p *fakeProvider) GetModel() string {
	if p.model == "" {
		return "fake"
	}
	return p.model
}

func (p *fakeProvider) GetBaseURL() string { return "" }

func TestClientRunUsesDirectSessionAPI(t *testing.T) {
	provider := &fakeProvider{resp: "done"}
	client, err := caretz.NewClient(context.Background(), caretz.ClientOptions{
		Provider:     provider,
		WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Run(context.Background(), "conversation-1", "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("Run() content = %q, want done", resp.Content)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

func TestClientDefaultStateIsInMemory(t *testing.T) {
	workspace := t.TempDir()
	client, err := caretz.NewClient(context.Background(), caretz.ClientOptions{
		Provider:     &fakeProvider{},
		WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.NewSession("conversation-1", caretz.SessionOptions{}); err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("default state created sessions dir: err=%v", err)
	}
}

func TestClientStateDirCreatesPersistentSession(t *testing.T) {
	stateDir := t.TempDir()
	client, err := caretz.NewClient(context.Background(), caretz.ClientOptions{
		Provider:     &fakeProvider{},
		WorkspaceDir: t.TempDir(),
		State:        caretz.StateOptions{Dir: stateDir},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.NewSession("conversation-1", caretz.SessionOptions{}); err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sessionDir := filepath.Join(stateDir, "sessions", "lib", "conversation-1")
	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("persistent session dir stat: %v", err)
	}
}

func TestClientStateDirRestoresConversationHistory(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()

	firstProvider := &fakeProvider{resp: "first response"}
	firstClient, err := caretz.NewClient(context.Background(), caretz.ClientOptions{
		Provider:     firstProvider,
		WorkspaceDir: workspace,
		State:        caretz.StateOptions{Dir: stateDir},
	})
	if err != nil {
		t.Fatalf("NewClient() first error = %v", err)
	}
	if _, err := firstClient.Run(context.Background(), "conversation-1", "first prompt"); err != nil {
		t.Fatalf("Run() first error = %v", err)
	}

	secondProvider := &fakeProvider{resp: "second response"}
	secondClient, err := caretz.NewClient(context.Background(), caretz.ClientOptions{
		Provider:     secondProvider,
		WorkspaceDir: workspace,
		State:        caretz.StateOptions{Dir: stateDir},
	})
	if err != nil {
		t.Fatalf("NewClient() second error = %v", err)
	}
	if _, err := secondClient.Run(context.Background(), "conversation-1", "second prompt"); err != nil {
		t.Fatalf("Run() second error = %v", err)
	}

	if len(secondProvider.lastMessages) != 3 {
		t.Fatalf("restored message count = %d, want 3", len(secondProvider.lastMessages))
	}
	if secondProvider.lastMessages[0].Content != "first prompt" ||
		secondProvider.lastMessages[1].Content != "first response" ||
		secondProvider.lastMessages[2].Content != "second prompt" {
		t.Fatalf("restored messages = %#v", secondProvider.lastMessages)
	}
}

func TestClientCustomToolExecutes(t *testing.T) {
	toolCalled := false
	provider := &fakeProvider{responses: []llm.Response{
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "call-1",
				Name:      "DeployPreview",
				Arguments: "{}",
			}},
		},
		{Content: "deployed"},
	}}
	client, err := caretz.NewClient(context.Background(), caretz.ClientOptions{
		Provider:     provider,
		WorkspaceDir: t.TempDir(),
		Tools: []tools.Tool{{
			Definition: llm.ToolDefinition{
				Name:        "DeployPreview",
				Description: "Deploy a preview build",
				Parameters:  map[string]any{"type": "object"},
			},
			Handler: func(ctx context.Context, call llm.ToolCall) (string, error) {
				toolCalled = true
				return "preview deployed", nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Run(context.Background(), "conversation-1", "deploy")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Content != "deployed" {
		t.Fatalf("Run() content = %q, want deployed", resp.Content)
	}
	if !toolCalled {
		t.Fatal("custom tool was not called")
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
}

func TestClientRejectsProviderAndModelOptions(t *testing.T) {
	_, err := caretz.NewClient(context.Background(), caretz.ClientOptions{
		Provider: &fakeProvider{},
		Model:    caretz.ModelOptions{Model: "gpt-5"},
	})
	if err == nil {
		t.Fatal("NewClient() error = nil, want validation error")
	}
}

func ExampleClientOptions_customTool() {
	customTool := tools.Tool{
		Definition: llm.ToolDefinition{
			Name:        "DeployPreview",
			Description: "Deploy a preview build",
			Parameters:  map[string]any{"type": "object"},
		},
		Handler: func(ctx context.Context, call llm.ToolCall) (string, error) {
			return "preview deployed", nil
		},
	}

	_ = caretz.ClientOptions{Tools: []tools.Tool{customTool}}
}

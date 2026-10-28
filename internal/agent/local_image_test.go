package agent

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
)

type localImageTestHTTP func(*http.Request) (*http.Response, error)

func (f localImageTestHTTP) Do(req *http.Request) (*http.Response, error) { return f(req) }

// LocalImage with a provider/model reference resolves it through the
// session's subagent resolver, so model rules apply to images too.
func TestLocalImageResolvesProviderModelThroughSession(t *testing.T) {
	workspaceDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(workspaceDir, "shot.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
	args := mustJSON(t, map[string]any{"file_path": imagePath})
	call := llm.ToolCall{ID: "img-1", Name: "LocalImage", Arguments: args}
	provider := &mockProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
		{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	var sentTo, auth string
	logger := log.New()
	registry := &tools.Registry{
		Logger:        logger,
		ToolConfig:    tools.Config{"localImage": {"model": "vision/qwen-vl"}},
		ReadableRoots: []string{workspaceDir},
		HTTPClient: localImageTestHTTP(func(req *http.Request) (*http.Response, error) {
			sentTo, auth = req.URL.String(), req.Header.Get("Authorization")
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"a login form"}}]}`))}, nil
		}),
	}
	session, err := NewAgentSession("test", "local-image", SessionParams{
		BaseDir:          filepath.Join(t.TempDir(), "sessions"),
		WorkspaceDir:     workspaceDir,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     registry,
		SystemPrompt:     "prompt",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests []SubagentModelRequest
	session.SetSubagentModelResolver(subagentModelTestResolver{requests: &requests, models: map[string]SubagentModelResolution{
		"vision/qwen-vl": {Model: "qwen2.5-vl-7b", BaseURL: "http://127.0.0.1:8002/v1", APIKey: "vl-key"},
	}})

	if _, err := session.Call(context.Background(), "what is in shot.png?"); err != nil {
		t.Fatal(err)
	}
	if sentTo != "http://127.0.0.1:8002/v1/chat/completions" || auth != "Bearer vl-key" {
		t.Fatalf("request to %q with %q, want the provider endpoint and key", sentTo, auth)
	}
	if len(requests) != 1 || requests[0].Name != "LocalImage" || requests[0].WorkspaceDir != workspaceDir {
		t.Fatalf("resolver requests = %+v", requests)
	}
}

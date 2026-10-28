package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
)

func TestRunCarriesConfiguredResponsesProtocolIntoSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"ok"}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"usage":{}}}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	configRoot := t.TempDir()
	configJSON := fmt.Sprintf(`{
  "activeModel": "hosted/responses-model",
  "provider": {
    "hosted": {
      "options": {"baseURL": %q},
      "models": {"responses-model": {"protocol": "responses"}}
    }
  }
}`, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := Run(context.Background(), []string{"--incognito", "--config-root", configRoot, "--no-model-discovery", "-p", "hello"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() exit code = %d; stderr = %q", code, stderr.String())
	}
	if requestPath != "/v1/responses" {
		t.Fatalf("request path = %q, want /v1/responses", requestPath)
	}
}

func (m *replayMockProvider) Chat(systemPrompt string, messages []llm.Message, tools []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	if len(tools) == 0 && len(messages) == 1 && strings.Contains(messages[0].Content, "<conversation>") {
		return m.summaryResponse, nil
	}
	return m.normalResponse, nil
}

func (m *replayMockProvider) GetModel() string { return "mock-model" }

func (m *replayMockProvider) GetBaseURL() string { return "http://mock" }

func TestSelectSessionAdapterDefaults(t *testing.T) {
	adapter, err := selectSessionAdapter("", "", false, "")
	if err != nil {
		t.Fatalf("selectSessionAdapter(interactive) error = %v", err)
	}
	if adapter != "tui" {
		t.Fatalf("adapter = %q, want tui", adapter)
	}

	adapter, err = selectSessionAdapter("hello", "", false, "")
	if err != nil {
		t.Fatalf("selectSessionAdapter(prompt) error = %v", err)
	}
	if adapter != "cli" {
		t.Fatalf("adapter = %q, want cli", adapter)
	}
}

func TestRunBangPrompt(t *testing.T) {
	var gotCommand string
	registry := &tools.Registry{
		WorkspaceDir: t.TempDir(),
		RunSandboxed: func(_ string, command string) (string, string, error) {
			gotCommand = command
			return "stdout\n", "stderr\n", nil
		},
	}
	var stdout, stderr strings.Builder
	code, handled := runBangPrompt(context.Background(), "!  printf hello  ", registry, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("runBangPrompt() = (%d, %v), want (0, true)", code, handled)
	}
	if gotCommand != "printf hello" {
		t.Fatalf("command = %q, want %q", gotCommand, "printf hello")
	}
	if stdout.String() != "stdout\n" || stderr.String() != "stderr\n" {
		t.Fatalf("output = (%q, %q)", stdout.String(), stderr.String())
	}
}

func TestRunBangPromptReturnsCommandExitCode(t *testing.T) {
	registry := &tools.Registry{
		WorkspaceDir: t.TempDir(),
		RunSandboxed: func(_ string, _ string) (string, string, error) {
			return "", "failed\n", fmt.Errorf("exit status 7")
		},
	}
	var stdout, stderr strings.Builder
	code, handled := runBangPrompt(context.Background(), "!false", registry, &stdout, &stderr)
	if !handled || code != 7 || stderr.String() != "failed\n" {
		t.Fatalf("runBangPrompt() = (%d, %v, %q), want (7, true, failed)", code, handled, stderr.String())
	}
}

func TestRunBangPromptAsksToRerunRefusedCommandOutsideSandbox(t *testing.T) {
	const refusal = "sudo: The \"no new privileges\" flag is set, which prevents sudo from running as root.\n"
	newRegistry := func(ran *[]string) *tools.Registry {
		return &tools.Registry{
			WorkspaceDir:  t.TempDir(),
			AskSandboxOff: true,
			RunSandboxed: func(_ string, command string) (string, string, error) {
				*ran = append(*ran, "sandboxed:"+command)
				return "", refusal, fmt.Errorf("exit status 1")
			},
			RunUnsandboxed: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
				*ran = append(*ran, "host:"+command)
				return "active (running)\n", "", nil
			},
		}
	}
	answer := func(allowed bool, asked *[]tools.PermissionError) tools.PermissionRequester {
		return func(_ context.Context, _ llm.ToolCall, request *tools.PermissionError) (bool, error) {
			*asked = append(*asked, *request)
			return allowed, nil
		}
	}

	t.Run("approved", func(t *testing.T) {
		var ran []string
		var asked []tools.PermissionError
		ctx := tools.WithPermissionRequester(context.Background(), answer(true, &asked))
		var stdout, stderr strings.Builder
		code, _ := runBangPrompt(ctx, "!sudo systemctl status ollama", newRegistry(&ran), &stdout, &stderr)
		if code != 0 || stdout.String() != "active (running)\n" || stderr.String() != refusal {
			t.Fatalf("runBangPrompt() = %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
		}
		if strings.Join(ran, ",") != "sandboxed:sudo systemctl status ollama,host:sudo systemctl status ollama" {
			t.Fatalf("ran = %v", ran)
		}
		if len(asked) != 1 || asked[0].Capability != tools.PermissionSandboxOff || asked[0].Command != "sudo systemctl status ollama" {
			t.Fatalf("asked = %+v", asked)
		}
	})

	t.Run("denied", func(t *testing.T) {
		var ran []string
		var asked []tools.PermissionError
		ctx := tools.WithPermissionRequester(context.Background(), answer(false, &asked))
		var stdout, stderr strings.Builder
		code, _ := runBangPrompt(ctx, "!sudo true", newRegistry(&ran), &stdout, &stderr)
		if code != 1 || len(asked) != 1 || strings.Join(ran, ",") != "sandboxed:sudo true" {
			t.Fatalf("code = %d, asked = %d, ran = %v", code, len(asked), ran)
		}
	})

	t.Run("nobody to ask", func(t *testing.T) {
		var ran []string
		var stdout, stderr strings.Builder
		code, _ := runBangPrompt(context.Background(), "!sudo true", newRegistry(&ran), &stdout, &stderr)
		if code != 1 || strings.Join(ran, ",") != "sandboxed:sudo true" || stderr.String() != refusal {
			t.Fatalf("code = %d, ran = %v, stderr = %q", code, ran, stderr.String())
		}
	})

	t.Run("on the terminal", func(t *testing.T) {
		var ran []string
		var asked []tools.PermissionError
		ctx := tools.WithPermissionRequester(context.Background(), answer(true, &asked))
		ctx = tools.WithForegroundRunner(ctx, func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
			ran = append(ran, "terminal:"+command)
			return "shown on the terminal\n", "", nil
		})
		var stdout, stderr strings.Builder
		code, _ := runBangPrompt(ctx, "!sudo true", newRegistry(&ran), &stdout, &stderr)
		if code != 0 || stdout.String() != "" || strings.Join(ran, ",") != "sandboxed:sudo true,terminal:sudo true" {
			t.Fatalf("code = %d, stdout %q, ran = %v", code, stdout.String(), ran)
		}
	})

	t.Run("ask-sandbox-off disabled", func(t *testing.T) {
		var ran []string
		var asked []tools.PermissionError
		registry := newRegistry(&ran)
		registry.AskSandboxOff = false
		ctx := tools.WithPermissionRequester(context.Background(), answer(true, &asked))
		var stdout, stderr strings.Builder
		code, _ := runBangPrompt(ctx, "!sudo true", registry, &stdout, &stderr)
		if code != 1 || len(asked) != 0 || strings.Join(ran, ",") != "sandboxed:sudo true" {
			t.Fatalf("code = %d, asked = %d, ran = %v", code, len(asked), ran)
		}
	})
}

func TestSelectSessionAdapterTelegramDebugAttach(t *testing.T) {
	adapter, err := selectSessionAdapter("", "telegram", true, "12345")
	if err != nil {
		t.Fatalf("selectSessionAdapter() error = %v", err)
	}
	if adapter != "telegram" {
		t.Fatalf("adapter = %q, want telegram", adapter)
	}
}

func TestSelectSessionAdapterValidation(t *testing.T) {
	tests := []struct {
		name      string
		prompt    string
		adapter   string
		sessionID string
		wantErr   string
	}{
		{
			name:    "invalid adapter",
			adapter: "irc",
			wantErr: "invalid adapter",
		},
		{
			name:    "non-default adapter requires explicit session",
			adapter: "telegram",
			wantErr: "-s is required",
		},
		{
			name:      "telegram not supported for prompt mode",
			prompt:    "hello",
			adapter:   "telegram",
			sessionID: "12345",
			wantErr:   "interactive TUI mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := selectSessionAdapter(tt.prompt, tt.adapter, true, tt.sessionID)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("selectSessionAdapter() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunIncognitoUsesInMemorySessionStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	configPath := filepath.Join(configRoot, "config.json")
	configJSON := `{
  "activeModel": "local/qwen",
  "provider": {
    "local": {
      "options": {"baseURL": "http://localhost:8000/v1"},
      "models": {"qwen": {"model": "qwen"}}
    }
  }
}
`
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := Run(context.Background(), []string{
		"-incognito",
		"--config-root", configRoot,
		"-p", "/help",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run(-incognito) exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Available commands:") {
		t.Fatalf("stdout = %q, want help output", stdout.String())
	}
	sessionsDir := filepath.Join(configRoot, "sessions")
	if _, err := os.Stat(sessionsDir); !os.IsNotExist(err) {
		t.Fatalf("sessions dir stat error = %v, want not exist", err)
	}
}

func TestContextCommandShowsSelectedProfileAndModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configRoot := t.TempDir()
	configJSON := `{
  "activeModel": "local/qwen",
  "provider": {
    "local": {
      "options": {"baseURL": "http://localhost:8000/v1"},
      "models": {"qwen": {"model": "qwen-code"}}
    }
  }
}`
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := Run(context.Background(), []string{"--incognito", "--config-root", configRoot, "--profile", "coding", "--session-exec=false", "--no-model-discovery", "-p", "/context"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() exit code = %d; stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"model: qwen-code", "profile: coding", "context: ", "reserved: ", "messages: "} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %q", want, stdout.String())
		}
	}
}

func TestRunPromptFileFeedsPromptMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	configPath := filepath.Join(configRoot, "config.json")
	configJSON := `{
  "activeModel": "local/qwen",
  "provider": {
    "local": {
      "options": {"baseURL": "http://localhost:8000/v1"},
      "models": {"qwen": {"model": "qwen"}}
    }
  }
}
`
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("/help"), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := Run(context.Background(), []string{
		"--config-root", configRoot,
		"-s", "prompt-file-session",
		"--prompt-file", promptPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run(--prompt-file) exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Available commands:") {
		t.Fatalf("stdout = %q, want help output", stdout.String())
	}
}

func TestTelegramDebugAttachSessionUsesConfigRootAndCwdWorkspace(t *testing.T) {
	cwd := t.TempDir()
	configRoot := t.TempDir()
	sessionsDir := filepath.Join(configRoot, "sessions")
	provider := &replayMockProvider{
		normalResponse: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}},
	}
	registry := &tools.Registry{WorkspaceDir: cwd}
	logger := log.New()

	session, err := agent.NewAgentSession("telegram", "12345", agent.SessionParams{
		BaseDir:          sessionsDir,
		WorkspaceDir:     cwd,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     registry,
		SystemPrompt:     "system",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatalf("NewAgentSession() error = %v", err)
	}

	if session.AdapterType() != "telegram" {
		t.Fatalf("AdapterType() = %q, want telegram", session.AdapterType())
	}
	if session.ChannelID() != "12345" {
		t.Fatalf("ChannelID() = %q, want 12345", session.ChannelID())
	}
	if session.WorkspaceDir() != cwd {
		t.Fatalf("WorkspaceDir() = %q, want cwd %q", session.WorkspaceDir(), cwd)
	}
	if _, err := os.Stat(filepath.Join(sessionsDir, "telegram", "12345")); err != nil {
		t.Fatalf("telegram session dir was not created under config root: %v", err)
	}
}

func TestRuntimeSettingsForModelSwitchAppliesDefaults(t *testing.T) {
	settings, err := runtimeSettingsForModelSwitch(config.Resolved{
		BaseURL:         "http://localhost:8000/v1",
		Model:           "qwen-code",
		ReasoningEffort: "xhigh",
	})
	if err != nil {
		t.Fatalf("runtimeSettingsForModelSwitch() error = %v", err)
	}
	if settings.APIKey != "noset" {
		t.Fatalf("APIKey = %q, want noset", settings.APIKey)
	}
	if settings.ContextWindow != 200000 {
		t.Fatalf("ContextWindow = %d, want 200000", settings.ContextWindow)
	}
	if settings.ReasoningEffort != "xhigh" {
		t.Fatalf("ReasoningEffort = %q, want xhigh", settings.ReasoningEffort)
	}
	if settings.ReservedTokens != 16000 {
		t.Fatalf("ReservedTokens = %d, want 16000", settings.ReservedTokens)
	}
	if settings.KeepRecentTokens != 30000 {
		t.Fatalf("KeepRecentTokens = %d, want 30000", settings.KeepRecentTokens)
	}
	if settings.SummaryTokens != 6000 {
		t.Fatalf("SummaryTokens = %d, want 6000", settings.SummaryTokens)
	}
}

func TestReplayContextIntoModel_CompactedSession(t *testing.T) {
	longContent := strings.Repeat("x", 1000)

	provider := &replayMockProvider{
		normalResponse: llm.Response{
			Content: "assistant response",
			Usage:   llm.Usage{CompletionTokens: 250, TotalTokens: 250},
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: longContent,
			},
		},
		summaryResponse: llm.Response{
			Content: "## Goal\nTest compaction.\n## Progress\n### Done\n- Task completed.",
			Usage:   llm.Usage{PromptTokens: 500, CompletionTokens: 200, TotalTokens: 700},
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "## Goal\nTest compaction.\n## Progress\n### Done\n- Task completed.",
			},
		},
	}

	tmpDir := t.TempDir()
	logger := log.New()
	logger.SetVerbosity(0)

	registry := &tools.Registry{
		Logger: logger,
		RunSandboxed: func(cwd, command string) (string, string, error) {
			return "mock-bash-output", "", nil
		},
	}

	// Create session with a small context window to force compaction.
	session, err := agent.NewAgentSession("test", "ch-replay", agent.SessionParams{
		BaseDir:          tmpDir,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     registry,
		SystemPrompt:     "you are a test agent",
		ContextWindow:    5000,
		ReservedTokens:   1000,
		KeepRecentTokens: 2000,
	})
	if err != nil {
		t.Fatalf("failed to create agent session: %v", err)
	}

	// Run enough turns to trigger compaction.
	for i := 0; i < 12; i++ {
		_, err := session.Call(context.Background(), fmt.Sprintf("turn %d", i))
		if err != nil {
			t.Fatalf("unexpected error on turn %d: %v", i, err)
		}
	}

	// Verify the session was compacted.
	messages := session.GetContextMessages()
	if len(messages) >= 20 {
		t.Fatalf("expected compacted messages to be under 20, got %d", len(messages))
	}
	hasSummary := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "<summary>") {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		t.Fatalf("expected a synthetic summary message in compacted context, none found")
	}

}

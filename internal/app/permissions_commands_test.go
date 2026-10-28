package app

import (
	"context"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
)

type permissionsCommandProvider struct{}

func (*permissionsCommandProvider) Chat(string, []llm.Message, []llm.ToolDefinition, func(string)) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}, nil
}
func (*permissionsCommandProvider) GetModel() string   { return "permissions-test" }
func (*permissionsCommandProvider) GetBaseURL() string { return "http://permissions-test" }

func TestSlashRegistryRegistersInteractivePermissionCommands(t *testing.T) {
	registry, err := newSlashCommandRegistry(t.TempDir(), nil, nil, "", slashcmd.ModelsCommand{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"addpath", "addnet"} {
		if !registry.Handles("/" + want + " example") {
			t.Fatalf("slash registry does not register /%s", want)
		}
	}
}

func containsPermissionPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func TestPermissionCommandPropagatesIntoActiveSessionRuntime(t *testing.T) {
	logger := log.New()
	session, err := agent.NewAgentSession("test", "permissions", agent.SessionParams{
		BaseDir: t.TempDir(), WorkspaceDir: t.TempDir(), Logger: logger,
		Provider: &permissionsCommandProvider{}, ToolRegistry: &tools.Registry{Logger: logger},
		SystemPrompt: "test", ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	registry, err := newSlashCommandRegistry(session.WorkspaceDir(), nil, nil, "", slashcmd.ModelsCommand{})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"/addpath readonly", "/addpath --rw output", "/addnet github.com:443", "/addnet tcp://github.com:22"} {
		if _, handled, err := (publicSlashCommandRegistry{inner: registry}).Execute(context.Background(), session, input); err != nil || !handled {
			t.Fatalf("%s failed: handled=%v err=%v", input, handled, err)
		}
	}
	if len(session.ToolRegistry().ReadableRoots) != 2 || !containsPermissionPath(session.ToolRegistry().ReadableRoots, "readonly") || !containsPermissionPath(session.ToolRegistry().ReadableRoots, "output") {
		t.Fatalf("active runtime readable roots = %#v", session.ToolRegistry().ReadableRoots)
	}
	if len(session.ToolRegistry().WritableRoots) != 1 || session.ToolRegistry().WritableRoots[0] != "output" {
		t.Fatalf("active runtime writable roots = %#v", session.ToolRegistry().WritableRoots)
	}
	if len(session.ToolRegistry().NetworkAllowlist) != 2 || session.ToolRegistry().NetworkAllowlist[1] != "tcp://github.com:22" {
		t.Fatalf("active runtime network allowlist = %#v", session.ToolRegistry().NetworkAllowlist)
	}

	other, err := agent.NewAgentSession("test", "other-permissions", agent.SessionParams{
		BaseDir: t.TempDir(), WorkspaceDir: t.TempDir(), Logger: logger,
		Provider: &permissionsCommandProvider{}, ToolRegistry: &tools.Registry{Logger: logger},
		SystemPrompt: "test", ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if len(other.ToolRegistry().ReadableRoots) != 0 || len(other.ToolRegistry().WritableRoots) != 0 {
		t.Fatalf("permission grants leaked into another session: read=%#v write=%#v", other.ToolRegistry().ReadableRoots, other.ToolRegistry().WritableRoots)
	}
}

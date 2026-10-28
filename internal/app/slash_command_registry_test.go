package app

import (
	"context"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/tools"
	libruntime "github.com/makkalot/caretz/lib/runtime"
)

func TestSlashCommandRegistryUsesLoopAndScheduleList(t *testing.T) {
	registry, err := newSlashCommandRegistry("", nil, nil, "", slashcmd.ModelsCommand{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, command := range registry.Commands() {
		names = append(names, command.Name())
	}
	joined := " " + strings.Join(names, " ") + " "
	for _, name := range []string{"loop", "schedule-list", "tasks", "goal", "goal:approve", "goal:answer", "goal:evidence", "goal:resume", "goal:retry", "goal:status", "goal:stop"} {
		if !strings.Contains(joined, " "+name+" ") {
			t.Fatalf("commands = %v, missing %s", names, name)
		}
	}
	for _, name := range []string{"solve", "solve:approve", "solve:answer", "solve:resume", "solve:retry", "solve:status", "solve:stop"} {
		if strings.Contains(joined, " "+name+" ") {
			t.Fatalf("commands still include removed %s: %v", name, names)
		}
	}
	if strings.Contains(joined, " schedule ") {
		t.Fatalf("commands still include schedule: %v", names)
	}
	for _, name := range []string{"agent", "subagent"} {
		if strings.Contains(joined, " "+name+" ") {
			t.Fatalf("commands still include %s: %v", name, names)
		}
	}
}

type echoCommand struct {
	name string
	got  *libruntime.CommandSession
}

func (c echoCommand) Name() string      { return c.name }
func (echoCommand) Description() string { return "echo the workspace" }
func (c echoCommand) Usage() string     { return "/" + c.name }
func (echoCommand) Aliases() []string   { return nil }
func (c echoCommand) Run(_ context.Context, session libruntime.CommandSession, args string) (libruntime.SlashCommandResult, error) {
	*c.got = session
	return libruntime.SlashCommandResult{Message: args + " in " + session.WorkspaceDir()}, nil
}

func TestCustomSlashCommandsRunAgainstTheAgentSession(t *testing.T) {
	logger := log.New()
	session, err := agent.NewAgentSession("test", "custom", agent.SessionParams{
		BaseDir: t.TempDir(), WorkspaceDir: t.TempDir(), Logger: logger,
		Provider: &permissionsCommandProvider{}, ToolRegistry: &tools.Registry{Logger: logger},
		SystemPrompt: "test", ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	registry, err := newSlashCommandRegistry("", nil, nil, "", slashcmd.ModelsCommand{})
	if err != nil {
		t.Fatal(err)
	}
	var got libruntime.CommandSession
	if err := registerCustomSlashCommands(registry, []libruntime.CustomSlashCommand{echoCommand{name: "echo", got: &got}}); err != nil {
		t.Fatal(err)
	}
	result, handled, err := registry.Execute(context.Background(), session, "/echo hi")
	if err != nil || !handled {
		t.Fatalf("Execute() handled = %v, err = %v", handled, err)
	}
	if want := "hi in " + session.WorkspaceDir(); result.Message != want {
		t.Fatalf("Message = %q, want %q", result.Message, want)
	}
	if got.SessionDir() != session.SessionDir() || got.ChannelID() != "custom" {
		t.Fatalf("command saw session %s/%s", got.SessionDir(), got.ChannelID())
	}

	if err := registerCustomSlashCommands(registry, []libruntime.CustomSlashCommand{echoCommand{name: "tasks", got: &got}}); err == nil {
		t.Fatal("a custom command replaced the built-in /tasks")
	}
}

func TestPublicSlashCommandResultPreservesNilNewSession(t *testing.T) {
	result := publicSlashCommandResult(slashcmd.Result{ExitCode: 4, MessagePublished: true})
	if result.NewSession != nil {
		t.Fatalf("NewSession = %#v, want nil", result.NewSession)
	}
	if result.ExitCode != 4 {
		t.Fatalf("ExitCode = %d, want 4", result.ExitCode)
	}
	if !result.MessagePublished {
		t.Fatal("published command result flag was lost")
	}
}

package slashcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
)

type testCommand struct {
	name    string
	aliases []string
}

func (c testCommand) Name() string        { return c.name }
func (c testCommand) Description() string { return "test command" }
func (c testCommand) Usage() string       { return "/" + c.name }
func (c testCommand) Aliases() []string   { return c.aliases }
func (c testCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	return Result{Message: args}, nil
}

func TestParse(t *testing.T) {
	tests := []struct {
		input string
		name  string
		args  string
		ok    bool
	}{
		{input: "hello", ok: false},
		{input: " /help", name: "help", ok: true},
		{input: "/help compact", name: "help", args: "compact", ok: true},
		{input: "/context verbose", name: "context", args: "verbose", ok: true},
		{input: "explain /context", ok: false},
		{input: "/", ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			name, args, ok := Parse(tt.input)
			if ok != tt.ok || name != tt.name || args != tt.args {
				t.Fatalf("Parse(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.input, name, args, ok, tt.name, tt.args, tt.ok)
			}
		})
	}
}

func TestRegistryExecute(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testCommand{name: "echo", aliases: []string{"e"}}); err != nil {
		t.Fatal(err)
	}

	result, handled, err := registry.Execute(context.Background(), nil, "/e hello")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !handled {
		t.Fatal("Execute() handled = false, want true")
	}
	if result.Message != "hello" {
		t.Fatalf("Execute() message = %q, want %q", result.Message, "hello")
	}

	_, handled, err = registry.Execute(context.Background(), nil, "hello")
	if err != nil {
		t.Fatalf("Execute(non-command) error = %v", err)
	}
	if handled {
		t.Fatal("Execute(non-command) handled = true, want false")
	}

	_, handled, err = registry.Execute(context.Background(), nil, "/missing")
	if !handled {
		t.Fatal("Execute(unknown) handled = false, want true")
	}
	if err == nil || !strings.Contains(err.Error(), "unknown command /missing") {
		t.Fatalf("Execute(unknown) error = %v, want unknown command", err)
	}
}

func TestRegistryHandlesSubagentShorthand(t *testing.T) {
	registry := NewRegistry()
	registry.SubagentAvailable = func(name string) bool { return name == "task-validator" }
	if !registry.Handles("@task-validator check changelog") {
		t.Fatal("Handles() = false for available subagent shorthand")
	}
	if registry.Handles("@CHANGELOG.md") {
		t.Fatal("Handles() = true for non-subagent path mention")
	}
}

func TestRegistryExecuteSubagentShorthandPassesTaskVerbatim(t *testing.T) {
	registry := NewRegistry()
	registry.RunSubagent = func(ctx context.Context, session *agent.AgentSession, name, task string) (Result, error) {
		return Result{Message: name + "\n" + task}, nil
	}
	registry.SubagentAvailable = func(name string) bool { return name == "task-validator" }

	result, handled, err := registry.Execute(context.Background(), nil, "@task-validator let's check @CHANGELOG.md")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !handled {
		t.Fatal("Execute() handled = false")
	}
	if result.Message != "task-validator\nlet's check @CHANGELOG.md" {
		t.Fatalf("subagent task = %q", result.Message)
	}
}

func TestRegistryDoesNotExposeSubagentSlashCommands(t *testing.T) {
	registry := NewRegistry()
	registry.SubagentAvailable = func(name string) bool { return name == "reviewer" }
	registry.RunSubagent = func(ctx context.Context, session *agent.AgentSession, name, task string) (Result, error) {
		return Result{Message: task}, nil
	}
	for _, input := range []string{"/agent reviewer check", "/subagent reviewer check"} {
		if _, handled, err := registry.Execute(context.Background(), nil, input); !handled || err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("Execute(%q) = handled %v, error %v", input, handled, err)
		}
	}
	if result, handled, err := registry.Execute(context.Background(), nil, "@reviewer check"); !handled || err != nil || result.Message != "check" {
		t.Fatalf("Execute(@reviewer) = %+v, handled %v, error %v", result, handled, err)
	}
}

func TestRegistryRegisterDuplicate(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testCommand{name: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testCommand{name: "echo"}); err == nil {
		t.Fatal("Register duplicate command succeeded, want error")
	}
	if err := registry.Register(testCommand{name: "other", aliases: []string{"echo"}}); err == nil {
		t.Fatal("Register duplicate alias succeeded, want error")
	}
}

func TestHelpCommand(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(CompactCommand{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(ContextCommand{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(ClearCommand{}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []Command{
		GoalCommand{}, GoalApproveCommand{}, GoalAnswerCommand{}, GoalEvidenceCommand{}, GoalResumeCommand{},
		GoalRetryCommand{}, GoalStatusCommand{}, GoalStopCommand{},
	} {
		if err := registry.Register(command); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Register(TasksCommand{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(LoopCommand{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(ScheduleListCommand{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(HelpCommand{Registry: registry}); err != nil {
		t.Fatal(err)
	}

	result, handled, err := registry.Execute(context.Background(), nil, "/")
	if err != nil {
		t.Fatalf("Execute(/) error = %v", err)
	}
	if !handled {
		t.Fatal("Execute(/) handled = false, want true")
	}
	for _, want := range []string{"/clear", "/compact", "/context", "/help", "/goal", "/goal:approve", "/goal:answer", "/goal:resume", "/goal:retry", "/goal:status", "/goal:stop", "/loop", "/schedule-list", "/tasks", "Available commands:"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("help output missing %q:\n%s", want, result.Message)
		}
	}
	if strings.Contains(result.Message, "/schedule register") {
		t.Fatalf("help output still lists /schedule:\n%s", result.Message)
	}
	if _, handled, err := registry.Execute(context.Background(), nil, "/goal"); !handled || err == nil || !strings.Contains(err.Error(), "requires a session with internal tasks") {
		t.Fatalf("Execute(/goal) = handled %v, error %v", handled, err)
	}
	if _, handled, err := registry.Execute(context.Background(), nil, "/loop"); !handled || err == nil || !strings.Contains(err.Error(), "schedules are not enabled") {
		t.Fatalf("Execute(/loop) = handled %v, error %v", handled, err)
	}
	if _, handled, err := registry.Execute(context.Background(), nil, "/schedule register every:1h check"); !handled || err == nil || !strings.Contains(err.Error(), "unknown command /schedule") {
		t.Fatalf("Execute(/schedule) = handled %v, error %v", handled, err)
	}
}

func TestCompactCommandRequiresSession(t *testing.T) {
	_, err := CompactCommand{}.Run(context.Background(), nil, "")
	if err == nil || !strings.Contains(err.Error(), "agent session is required") {
		t.Fatalf("CompactCommand.Run() error = %v, want session error", err)
	}
}

func TestClearCommandRequiresSession(t *testing.T) {
	_, err := ClearCommand{}.Run(context.Background(), nil, "")
	if err == nil || !strings.Contains(err.Error(), "agent session is required") {
		t.Fatalf("ClearCommand.Run() error = %v, want session error", err)
	}
}

func TestClearCommandCreatesFreshSiblingSession(t *testing.T) {
	logger := log.New()
	provider := &loopMockProvider{}
	toolRegistry := &tools.Registry{Logger: logger}
	baseDir := t.TempDir()
	session, err := agent.NewAgentSession("tui", "old-id", agent.SessionParams{
		BaseDir:          baseDir,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     toolRegistry,
		SystemPrompt:     "system",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatalf("NewAgentSession() error = %v", err)
	}

	result, err := ClearCommand{}.Run(context.Background(), session, "")
	if err != nil {
		t.Fatalf("ClearCommand.Run() error = %v", err)
	}
	if result.NewSession == nil {
		t.Fatal("NewSession is nil")
	}
	if result.NewSession.ChannelID() == session.ChannelID() {
		t.Fatalf("new channel id = old channel id %q", session.ChannelID())
	}
	if len(result.NewSession.ChannelID()) != 8 {
		t.Fatalf("new channel id length = %d, want 8", len(result.NewSession.ChannelID()))
	}
	if result.NewSession.AdapterType() != session.AdapterType() {
		t.Fatalf("adapter = %q, want %q", result.NewSession.AdapterType(), session.AdapterType())
	}
	if result.NewSession.ActiveMessageCount() != 0 {
		t.Fatalf("new session active messages = %d, want 0", result.NewSession.ActiveMessageCount())
	}
	if _, err := os.Stat(filepath.Join(baseDir, "tui", result.NewSession.ChannelID())); err != nil {
		t.Fatalf("new session directory was not created: %v", err)
	}
	if !strings.Contains(result.Message, "cleared session: old-id → "+result.NewSession.ChannelID()) {
		t.Fatalf("message = %q", result.Message)
	}
}

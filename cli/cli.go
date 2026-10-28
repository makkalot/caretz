package cli

import (
	"context"
	"fmt"
	"io"

	caretz "github.com/makkalot/caretz"
	"github.com/makkalot/caretz/acp"
	caretzapp "github.com/makkalot/caretz/internal/app"
	"github.com/makkalot/caretz/lib/conversation"
	libruntime "github.com/makkalot/caretz/lib/runtime"
	"github.com/makkalot/caretz/lib/sessiontools"
	"github.com/makkalot/caretz/lib/tools"
	"github.com/makkalot/caretz/lib/tui"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

type Options struct {
	Tools              []tools.Tool
	SystemPromptSuffix string
	InteractiveRunner  InteractiveRunner
	ExplorerRunner     ExplorerRunner
	// SlashCommands are added next to the built-in slash commands. In the
	// built-in TUI a command can take over the screen with
	// tui.ScreenRunnerFrom.
	SlashCommands []CustomSlashCommand
	// SessionIntegrations are installed on every session the agent command
	// opens, for example to give the agent tools that a slash command reads.
	SessionIntegrations []sessiontools.Factory
	// PermissionPrompts replace the built-in TUI prompt for the permission
	// capabilities they are keyed by, for example to review a payload an
	// integration asks to release with session.AskPermission.
	PermissionPrompts map[string]tui.PermissionPrompt
	// MessageRenderers draw user and tool messages they recognise, such as a
	// tagged block an integration's tool returns, in the built-in TUI and
	// prompt mode.
	MessageRenderers []tui.MessageRenderer
}

type Runtime struct {
	inner *caretzapp.Runtime
}

type AdapterSessionOptions struct {
	WorkspaceDir string
	FileSender   tools.FileSender
}

type Logger = libruntime.Logger
type InteractiveRunner = libruntime.InteractiveRunner
type InteractiveParams = libruntime.InteractiveParams
type ExplorerRunner = libruntime.ExplorerRunner
type ExplorerParams = libruntime.ExplorerParams
type InteractiveSession = libruntime.Session
type SlashCommandRegistry = libruntime.SlashCommandRegistry
type SlashCommand = libruntime.SlashCommand
type SlashCommandResult = libruntime.SlashCommandResult
type CustomSlashCommand = libruntime.CustomSlashCommand
type CommandSession = libruntime.CommandSession
type ModelChoice = libruntime.ModelChoice
type AdapterSession = conversation.Session

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) int {
	return caretzapp.RunWithOptions(ctx, args, stdout, stderr, opts.app())
}

func BuildRuntime(ctx context.Context, args []string, stderr io.Writer, opts Options) (*Runtime, int) {
	rt, code := caretzapp.BuildRuntimeWithOptions(ctx, args, stderr, opts.app())
	if rt == nil {
		return nil, code
	}
	return &Runtime{inner: rt}, code
}

func (opts Options) app() caretzapp.Options {
	return caretzapp.Options{
		Tools:               opts.Tools,
		SystemPromptSuffix:  opts.SystemPromptSuffix,
		InteractiveRunner:   opts.InteractiveRunner,
		ExplorerRunner:      opts.ExplorerRunner,
		SlashCommands:       opts.SlashCommands,
		SessionIntegrations: opts.SessionIntegrations,
		PermissionPrompts:   opts.PermissionPrompts,
		MessageRenderers:    opts.MessageRenderers,
		BuildInfo: caretzapp.BuildInfo{
			Version:   Version,
			Commit:    Commit,
			BuildDate: BuildDate,
		},
	}
}

func (r *Runtime) Context() context.Context {
	if r == nil || r.inner == nil {
		return context.Background()
	}
	return r.inner.Ctx
}

func (r *Runtime) CWD() string {
	if r == nil || r.inner == nil {
		return ""
	}
	return r.inner.CWD
}

func (r *Runtime) SessionsDir() string {
	if r == nil || r.inner == nil {
		return ""
	}
	return r.inner.SessionsDir
}

func (r *Runtime) Logger() Logger {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Logger
}

func (r *Runtime) NewAdapterSession(adapterType, channelID string, opts AdapterSessionOptions) (*caretz.Session, error) {
	if r == nil || r.inner == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	return r.inner.NewAdapterSession(adapterType, channelID, caretzapp.AdapterSessionOptions{
		WorkspaceDir: opts.WorkspaceDir,
		FileSender:   opts.FileSender,
	})
}

// NewACPServer constructs an in-process ACP server from this configured
// runtime. Use acp.Host to manage transport framing and workspace lifecycle.
func (r *Runtime) NewACPServer(opts acp.ServerOptions) (acp.Server, error) {
	if r == nil || r.inner == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	return caretzapp.NewACPServer(r.inner, opts.AllowedModels)
}

var _ acp.Runtime = (*Runtime)(nil)

package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/log"
)

type agentCommandOptions struct {
	Prompt     string `flag:"p" help:"Prompt to send to LLM" group:"Session and input"`
	PromptFile string `flag:"prompt-file" help:"Read prompt from file instead of -p" group:"Session and input"`
	SessionID  string `flag:"s" help:"Session ID to resume (generates new UUID if omitted)" group:"Session and input"`
	Adapter    string `flag:"adapter" help:"Session adapter namespace to open: cli, tui, slack, or telegram. Defaults to tui interactively and cli with -p." group:"Session and input"`
	Incognito  bool   `flag:"incognito" help:"Keep the agent session in memory instead of writing session files" group:"Session and input"`
	StartPeers bool   `flag:"start-peers" help:"Start configured peer bots that are not running; in Herdr they open in a bots tab" group:"Session and input"`
	MainPeer   string `flag:"main-peer" help:"Run this peer bot in the terminal instead of a root session; root forces a root session (overrides peers.main in config)" group:"Session and input"`
	baseFlagValues
	Output  string `flag:"output" help:"Output mode: compact or detailed (overrides config and CARETZ_OUTPUT)" group:"Output and diagnostics"`
	Theme   string `flag:"theme" help:"Color theme: ansi, catppuccin-mocha, dracula, github-dark, or tokyo-night (overrides config and CARETZ_THEME)" group:"Output and diagnostics"`
	Version bool   `flag:"version" help:"Print version information and exit" group:"General"`
	// peerName runs the session as the named peer bot (`caretz peer <name>`).
	peerName string
}

type acpCommandOptions struct {
	baseFlagValues
	Incognito     bool           `flag:"incognito" help:"Keep the agent session in memory instead of writing session files" group:"Session state"`
	AllowedModels stringListFlag `flag:"allowed-model" help:"Restrict ACP to these configured provider/model references. Can be specified multiple times." group:"Model and context"`
}

type cliEnvironment struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	options        Options
}

func newCaretzCLICommand(env cliEnvironment) *clicmd.Command {
	rootOptions := &agentCommandOptions{}
	prepareBaseFlags(&rootOptions.baseFlagValues)
	root := &clicmd.Command{
		Use: "caretz", Short: "Run the Caretz coding agent", Options: rootOptions, Args: clicmd.NoArgs,
		Run: func(ctx context.Context, result *clicmd.Result) int {
			values := result.Options.Value.(*agentCommandOptions)
			return runAgentCommand(ctx, result.Options, *values, env.stdout, env.stderr, env.options)
		},
	}
	setup := &clicmd.Command{
		Use: "caretz setup", Short: "Configure a local or hosted model provider, or sign in with a ChatGPT plan", Args: clicmd.NoArgs,
		Run: func(ctx context.Context, _ *clicmd.Result) int {
			return runSetupCommand(ctx, nil, env.stdin, env.stdout, env.stderr)
		},
	}
	acpOptions := &acpCommandOptions{}
	prepareBaseFlags(&acpOptions.baseFlagValues)
	acp := &clicmd.Command{
		Use: "caretz acp", Short: "Start the Agent Client Protocol server", Options: acpOptions, Args: clicmd.NoArgs,
		Run: func(ctx context.Context, result *clicmd.Result) int {
			values := result.Options.Value.(*acpCommandOptions)
			baseFlags := values.baseFlagValues
			baseFlags.Incognito = values.Incognito
			recordBaseFlagVisits(result.Options, &baseFlags)
			logger := log.New()
			if baseFlags.Debug {
				logger.SetVerbosity(2)
			} else if baseFlags.Verbose {
				logger.SetVerbosity(1)
			}
			rt, code := buildRuntimeFromFlags(ctx, baseFlags, logger, env.stderr, env.options)
			if code != 0 {
				return code
			}
			rt.printModelNotices(env.stderr)
			return runACPServerWithAllowedModels(ctx, rt, os.Stdin, env.stdout, env.stderr, values.AllowedModels)
		},
	}
	// A bot is a normal session that keeps running, so it takes every root flag.
	peer := &clicmd.Command{
		Use: "caretz peer NAME", Short: "Run a configured peer bot in this terminal", InheritRootOptions: true, Args: clicmd.ExactArgs(1),
		Run: func(ctx context.Context, result *clicmd.Result) int {
			values := *result.RootOptions.Value.(*agentCommandOptions)
			values.peerName = result.Args[0]
			return runAgentCommand(ctx, result.RootOptions, values, env.stdout, env.stderr, env.options)
		},
	}
	session := newSessionCLICommand(env)
	root.AddCommand(setup, newAuthCLICommand(env), newModelsCLICommand(env), session, acp, peer)
	return root
}

type sessionOptionsValue interface{ sessionValues() sessionCommandOptions }
type sessionBasicOptions struct{ sessionLocationOptions }

func (o *sessionBasicOptions) sessionValues() sessionCommandOptions {
	return sessionCommandOptions{sessionLocationOptions: o.sessionLocationOptions}
}

type sessionListOptions struct {
	sessionLocationOptions
	IncludeSubagents bool `flag:"include-subagents" help:"Include child/subagent sessions" group:"Filters"`
}

func (o *sessionListOptions) sessionValues() sessionCommandOptions {
	return sessionCommandOptions{sessionLocationOptions: o.sessionLocationOptions, IncludeSubagents: o.IncludeSubagents}
}

type sessionTrimOptions struct {
	sessionLocationOptions
	FromVersion uint64 `flag:"from-version" help:"First originator version to remove" group:"Trimming"`
}

func (o *sessionTrimOptions) sessionValues() sessionCommandOptions {
	return sessionCommandOptions{sessionLocationOptions: o.sessionLocationOptions, FromVersion: o.FromVersion}
}

func newSessionCLICommand(env cliEnvironment) *clicmd.Command {
	parent := &clicmd.Command{Use: "caretz session", Short: "List, explore, inspect, repair, or trim saved sessions"}
	makeAction := func(action, use, short string, validator clicmd.ArgsValidator, options sessionOptionsValue) *clicmd.Command {
		return &clicmd.Command{
			Use: use, Short: short, Options: options, Args: validator,
			Run: func(_ context.Context, result *clicmd.Result) int {
				values := result.Options.Value.(sessionOptionsValue).sessionValues()
				values.ConfigRootSet = result.Options.Changed("config-root")
				return runSessionCommand(action, result.Args, values, env.stdout, env.stderr, env.options)
			},
		}
	}
	list := makeAction("list", "caretz session list", "List saved sessions", clicmd.NoArgs, &sessionListOptions{})
	explore := makeAction("explore", "caretz session explore ID", "Open the session explorer", clicmd.ExactArgs(1), &sessionBasicOptions{})
	inspect := makeAction("inspect", "caretz session inspect ID", "Inspect whether a session needs repair", clicmd.ExactArgs(1), &sessionBasicOptions{})
	repair := makeAction("repair", "caretz session repair ID", "Repair an interrupted session", clicmd.ExactArgs(1), &sessionBasicOptions{})
	trim := makeAction("trim", "caretz session trim ID", "Delete a session tail from an originator version", clicmd.ExactArgs(1), &sessionTrimOptions{})
	parent.AddCommand(list, explore, inspect, repair, trim)
	return parent
}

func formatBuildInfo(info BuildInfo) string {
	version := strings.TrimSpace(info.Version)
	if version == "" {
		version = "dev"
	}
	commit := strings.TrimSpace(info.Commit)
	if commit == "" {
		commit = "unknown"
	}
	buildDate := strings.TrimSpace(info.BuildDate)
	if buildDate == "" {
		buildDate = "unknown"
	}
	return fmt.Sprintf("caretz %s (commit %s, built %s)", version, commit, buildDate)
}

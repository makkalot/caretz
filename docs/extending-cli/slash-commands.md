---
title: "Slash commands"
weight: 30
description: "Add your own /commands, agent tools, and full-screen views to the TUI."
---

# Add Slash Commands

Your executable can add its own `/commands` to caretz's interactive TUI. They
sit next to the built-in ones, show up in `/help` and autocomplete, and can
prompt the agent. This one turns `/standup` into a request for a summary of
the day's work:

```go
package main

import (
	"context"
	"os"

	"github.com/makkalot/caretz/cli"
)

type standup struct{}

func (standup) Name() string        { return "standup" }
func (standup) Description() string { return "Summarize today's changes for a standup" }
func (standup) Usage() string       { return "/standup" }
func (standup) Aliases() []string   { return nil }

func (standup) Run(ctx context.Context, session cli.CommandSession, _ string) (cli.SlashCommandResult, error) {
	_, err := session.Call(ctx, "Summarize what changed in this workspace today in three bullets.")
	return cli.SlashCommandResult{}, err
}

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, cli.Options{
		SlashCommands: []cli.CustomSlashCommand{standup{}},
	}))
}
```

A command gets the session's directories and channel ID, `Call` to prompt the
agent, and `RunVerificationCommand` to run a shell command in the session's
sandbox without it showing up in the conversation. A name that clashes with a
built-in command stops startup with an error.

## Give the agent tools for your command

When the agent has to answer back to your command, for example by leaving notes
that the command shows later, put those tools in a session integration and pass
its factory in `cli.Options.SessionIntegrations`. Caretz installs it on every
session the CLI opens and carries it over to the sessions that `/clear` starts.
The factory receives a `sessiontools.SessionEventGateway`. The gateway also has
`SessionDir()` and `WorkspaceDir()`, so the integration can share files with
your command. Integration tools follow the normal `--tools` and
`--disable-tool` policy. Their names must not clash with built-in or
`cli.Options.Tools` tools. Subagents and other child sessions never get
integration tools.

The `*AgentSession` behind the gateway also has `GetContextMessages()`,
`AskPermission(ctx, request)`, `ModelProvider(ctx, ref, reasoning)` (another
configured model, or the current one for an empty ref) and `LLMSessionID()`.
Assert to an interface with the methods you need. Before asking, check
`sessiontools.ApprovalAvailable(ctx)` so a request never waits for an answer
nobody can give.

## Ask for approval your way

To let the user review what an integration is about to release, put it in
`PermissionRequest.Preview` and set `NoSessionGrant` when every request
differs. Then register a `tui.PermissionPrompt` for the request's capability
in `cli.Options.PermissionPrompts`. Your prompt draws on the terminal in place
of the built-in Allow/Deny line. It can hand off to `$EDITOR` with
`Terminal.RunExternal`, and return an edited payload in
`Metadata[eventing.PermissionMetadataPayload]`.

## Render your messages

A `tui.MessageRenderer` in `cli.Options.MessageRenderers` gets each user and
tool message first and can draw content it recognises itself, for example a
tagged answer block your tool returns. It also gets the tool's name: claim
only your own tools' results, because a file the model reads or a page it
fetches can contain your tag too. A claimed tool message stays visible in
compact mode, which otherwise hides tool output. `tui.NewMarkdownRenderer`
renders Markdown in the active theme.

## Take over the screen

In the built-in TUI a command can borrow the terminal for a full-screen view.
`tui.ScreenRunnerFrom(ctx)` returns nil in prompt mode, so check it before you
draw. `tui.RunFullScreen` drives a `tui.Component` on the alternate screen and
restores the conversation when it returns. `tui.NewStyle` colors text with the
active `/theme`. For code, `github.com/makkalot/caretz/lib/syntax` gives you
the highlighter caretz uses for diffs and fenced code.

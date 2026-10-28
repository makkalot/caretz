---
title: "Herdr"
description: "Agent-aware panes that restore your sessions."
---

# Run Caretz in Herdr

Caretz automatically becomes an agent-aware pane when you run its interactive
TUI inside [Herdr](https://herdr.dev/). There is nothing to install or enable in
Caretz: start Herdr in a project, then run `caretz` in one of its panes.

Herdr will show Caretz as:

- `idle` while it is ready for input;
- `working` while a prompt or command is running;
- `blocked` while Caretz is waiting for a permission decision.

That state powers Herdr's sidebar, notifications, and agent waiting commands.
Caretz reports changes in the background, so a missing or stopped Herdr server
does not delay or fail the agent session.

## Restore the same conversation

Caretz gives Herdr the saved session ID and the command needed to reopen it.
When Herdr restores its panes after a server restart, the Caretz pane returns to
the same conversation and adapter. An explicitly selected `--config-root` or
`CARETZ_CONFIG_ROOT` is preserved as well.

Custom resume commands require Herdr 0.9.2 or newer. Older Herdr versions can
still display Caretz's live state. Incognito sessions report state but do not
offer a resume command because they have no saved session.

Running `/clear` updates Herdr to restore the newly created session. Quitting
Caretz releases the pane immediately instead of leaving stale agent state.

## Bots get their own panes

If the workspace defines [peer bots](peers.md), starting `caretz --start-peers`
in a Herdr pane also opens a `bots` tab with one pane per bot that is not
already running; a plain `caretz` leaves the bots alone. You can
[split bots across several tabs](peers.md#run-it) and pick rows or columns for
each tab. Each
pane runs `caretz peer <name>` with the flags you started `caretz` with. Herdr's
agent list shows each bot by its name, with its working, idle, or blocked state,
so you can see at a glance which bot is busy or waiting for an approval.
Restoring the pane reopens the same bot with the same flags. Focus a bot's pane
to talk to it directly.

## Current boundaries

The integration applies to Caretz's built-in interactive terminal UI. One-shot
`caretz -p` runs, ACP sessions, and applications supplying a custom interactive
runner do not report lifecycle state. Caretz also is not currently a built-in
Herdr `agent start --kind` value; launch `caretz` normally in a Herdr pane and it
will identify itself.

If Caretz does not appear, verify that it was launched directly inside a Herdr
pane and that the pane inherited `HERDR_ENV`, `HERDR_PANE_ID`,
`HERDR_BIN_PATH`, and `HERDR_SOCKET_PATH`.

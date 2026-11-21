---
title: "Zed and ACP editors"
description: "Use caretz as the agent inside Zed or any ACP client."
---

# Zed and Other ACP Editors

Caretz speaks the [Agent Client Protocol](https://agentclientprotocol.com/), so
editors such as Zed can use it as their coding agent. You get the same agent,
sandbox, sessions, and slash commands as in the terminal, inside the editor's
agent panel.

## Set Up Zed

Configure a model first with `caretz setup`; Zed's own model-provider settings
are separate and caretz doesn't use them. Then add caretz as an external agent
in Zed's `settings.json`, using the absolute path to the binary:

```json
{
  "agent_servers": {
    "Caretz": {
      "type": "custom",
      "command": "/Users/me/bin/caretz",
      "args": ["acp"],
      "env": {}
    }
  }
}
```

Open the agent panel, start a new Caretz thread, and give it a task. Zed
sends the project folder as the workspace, so `AGENTS.md`, skills, and
`.caretz/config.json` from that project apply. If your Zed version uses a
different key for external agents, follow Zed's external-agent documentation;
the command is always `caretz acp`.

## What You Get in the Editor

- **Model, reasoning, and mode pickers.** The session's options let you switch
  the model, its reasoning effort, and between **Build** and **Plan** mode.
- **Slash commands** such as `/models`, `/compact`, and `/goal` work in the
  prompt.
- **Saved sessions.** Threads are stored under `.caretz/sessions/acp/` and can
  be listed and reopened from the editor.
- **[Peer bots](peers.md).** In a workspace that defines bots, the agent can
  hand them work with `SendMessage`. A bot's reply shows up in the thread as
  a new message and the agent answers it, even when you haven't sent anything.
- **The same sandbox.** Commands run sandboxed exactly as in the terminal.
  Approving a command to [run outside the sandbox](sandboxing/escalation.md)
  works too, but without a terminal `sudo` can't ask for a password.

## Pass Options

The editor's stop action (`session/cancel`) cancels the active turn and its
background jobs and async work. Jobs from earlier completed turns remain.
Caretz saves the interruption before the next prompt; see
[stopping a turn](sessions/recovery.md#stop-a-running-turn).

Any `caretz acp` flag goes in `args` after `acp`. For example, allow the
package index and keep the agent on local models:

```json
{
  "agent_servers": {
    "Caretz (local)": {
      "type": "custom",
      "command": "/Users/me/bin/caretz",
      "args": [
        "acp",
        "--allowed-model", "llama.cpp/qwen3.6-35b-a3b",
        "--network-allow", "pypi.org",
        "--profile", "coding"
      ],
      "env": {}
    }
  }
}
```

`--allowed-model` limits which models the editor may pick; model switches from
the picker, slash commands, and subagents stay within that list. Run
`caretz acp --help` for every flag.

ACP clients can't answer caretz's terminal prompts. When a hosted model would
start in a directory no [model rule](config/model-rules.md) covers, caretz
prints a warning and continues, and switching to a model a rule blocks is
refused. Write `modelRules` for the projects you open in the editor, or send
`/allow-model provider/model` from the editor's prompt to allow one there.

## Turns You Didn't Start

A bot's reply, a schedule, or a finished background job can start a turn while
you're idle. Its message and the agent's answer stream into the thread like a
normal turn. No `session/prompt` response ends such a turn, so caretz brackets
every turn, prompted or not, with two extra updates that clients can use to
show and clear a busy state:

```text
{"sessionUpdate":"caretz/turn_started","turnId":"…","source":"peer","startedAt":"2026-11-21T10:14:03Z"}
{"sessionUpdate":"caretz/turn_ended","turnId":"…","source":"peer","startedAt":"…","stopReason":"end_turn"}
```

- `source` is `prompt` for a turn a `session/prompt` started, otherwise what
  started it, such as `peer` or `schedule`. A prompted turn also carries
  `promptRequestId`, the JSON-RPC id of its `session/prompt`.
- `turn_started` follows the turn's user message. For a prompt,
  `turn_ended` comes right before the prompt response.
- `stopReason` is `end_turn`, `cancelled`, or `error` (with an `error` field).

The updates are live only; history doesn't replay them. Instead, the results
of `session/new`, `session/load`, and `session/resume` carry `activeTurn`:
`null` when nothing runs, or the running turn's `turnId`, `source`,
`startedAt`, and `promptRequestId`. A client that opens a session mid-turn,
for example after a restart, can show it busy and match the prompt response
by `promptRequestId`.

The message that starts a turn nobody typed shows the text meant for people,
and its `_meta.caretz.origin` says where it came from, live and in history:

```text
{"type":"text","text":"Found two issues in spool.go…","_meta":{"caretz":{"origin":
  {"source":"peer","sourceId":"<message id>","from":"reviewer","messageId":"<message id>",
   "inReplyTo":"<your message id>","status":"succeeded"}}}}
```

`inReplyTo` and `status` appear only on a reply. The model still reads the
message with its framing.

A turn you didn't start waits for any running prompt, and a prompt sent
during one waits for it. When no prompt is running, the editor's stop action
cancels it. Nobody can approve anything during it, so a command that needs
approval is refused.

## When It Doesn't Work

Add `-v` after `acp` in `args` to write
`.caretz/sessions/acp/<session-id>/debug.log`, and run `dev: open acp logs` in
Zed to see the JSON-RPC exchange. More in
[Terminal and ACP troubleshooting](troubleshooting/terminal-and-acp.md).

To host ACP inside your own Go application instead of starting a subprocess,
see [Host ACP in process](extending-cli/acp-in-process.md).

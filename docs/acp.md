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

## When It Doesn't Work

Add `-v` after `acp` in `args` to write
`.caretz/sessions/acp/<session-id>/debug.log`, and run `dev: open acp logs` in
Zed to see the JSON-RPC exchange. More in
[Terminal and ACP troubleshooting](troubleshooting/terminal-and-acp.md).

To host ACP inside your own Go application instead of starting a subprocess,
see [Host ACP in process](extending-cli/acp-in-process.md).

---
title: "Background subagents"
weight: 25
description: "Keep working while subagents run, and resume runs that were cut short."
---

# Background Subagents

A [subagent](subagents.md) can search, review, run tests, or dig through git
history in the background while you and the main agent keep working. Ask for
it in plain words:

```text
Run the tester in the background on ./internal/config/... and find when
ParseConfig last changed, and meanwhile explain the config loader to me.
```

The agent starts the run and gets control back right away. When the subagent
finishes, its result arrives as a new message and the agent reports it to you.
Up to four background runs work at once, and each run stops after 15 minutes.

Any subagent can run in the background, with the same tools and permissions
its agent file gives it in the foreground. A run that should only look, such
as a tester or reviewer, sets `plan: true`:

```text
---
description: Runs tests and digs through git history.
mode: subagent
plan: true
---
Run tests, git log, and grep, and report what you find. Never fix anything.
```

It has no `Edit` or `Write`, and every `Bash` command runs in a sandbox with
the workspace mounted read-only, so `go test`, `git log`, and `grep` work
while `git commit` or a code generator fails. Commands still get a private
writable temp directory, and Go's build cache in `.caretz/scratch` stays
writable so tests don't rebuild from scratch.

An agent without `plan: true` changes files in the background just as it
would in the foreground. It shares the workspace with you, the main agent,
and other runs, so give each writer its own files or packages, for example
one fixer on `./internal/config` while a `plan: true` reviewer reads the rest.

No one is there to answer a background run's approval prompts, so a request
for a network host, a path outside the workspace, or running unsandboxed is
denied at once and the subagent works around it. Hosts and paths you already
allow in config or approved for the session still work.

In one-shot mode (`-p`), Caretz waits for background runs to finish and
report before it exits.

## See and Stop Runs

A background run's tool calls don't scroll through your chat; you see only
when it starts and finishes. Run `/bg` in an interactive session to list this
session's subagent runs, newest first, with their status, child session ID,
and result. `/bg log` replays a run's recent tool calls, results, and answer
the way the chat shows a live turn, in the same compact or detailed mode: the
last 20 entries unless you give a number. To stop one, use `/bg cancel`:

```text
/bg
/bg log searcher
/bg log searcher 50
/bg cancel searcher
```

`/bg log` and `/bg cancel` take a name or a child session ID; `/bg log` with a
name shows that agent's latest run. To cancel one of several runs of the same
agent, pass its ID. A cancelled run keeps the work it has done so far. To
stop a subagent running in the foreground, press **Esc**.

## Resume an Interrupted Run

A run that was cancelled, failed, timed out, or cut short because the session
closed or Caretz itself died can be continued in its own conversation, so the
subagent keeps what it already learned:

```sh
caretz session list
caretz -s 7f31a221 -p "resume the searcher where it stopped"
```

If Caretz died while a subagent ran in the foreground, the session first needs
`caretz session repair 7f31a221`; see
[recover an interrupted turn](../sessions/recovery.md). Reopening a session in
the terminal names its interrupted subagents. The agent
resumes a run with the same model and settings it had. It resumes a run you
cancelled only when you ask for it.

## Tools the Agent Uses

Once a session has at least one subagent, the main agent gets two tools on
top of `CallSubagent`:

| Tool | What it does |
| --- | --- |
| `ListSubagents` | With `available: true`, list the subagents it can run and what each is for; otherwise list runs, or show one run's full result |
| `CancelSubagent` | Stop a background run |

`CallSubagent` takes `background: true` to start a run in the background, and
`resume` with a child session ID to continue an earlier run. There is no tool
to wait for a background run: its result always arrives as a new message, so
the chat never blocks on one. Subagents themselves don't get the control
tools.

## Limits of Background Bash

- A background subagent's `Bash` can't start background jobs of its own.
- In a `plan: true` run, tools that write caches into the workspace, such as
  `node_modules/.cache`, fail in the read-only sandbox. Point their cache at
  `$TMPDIR` in the command.

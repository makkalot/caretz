---
title: "Run one command outside"
weight: 60
description: "Approve a single blocked command, such as sudo, on the host."
---

# Run One Command Outside the Sandbox

Ops work sometimes needs a single command with full host access, such as
restarting a service, editing a file in `/etc`, or running `sudo`, while
everything else stays contained. Start Caretz with `--ask-sandbox-off`, or use
the [`ops` profile](../profiles/ops.md), which turns it on:

```sh
caretz --ask-sandbox-off -p "postgres on this laptop is stuck; restart it with brew services"
```

When the sandbox blocks a command, Caretz shows what was refused and asks:

- **Allow once** reruns that command on your terminal, outside the sandbox.
  `sudo` asks for your password as usual; Caretz never sees it.
- **Allow for session** also lets this exact command run outside the sandbox
  later without asking.
- **Deny** (the default) returns the sandboxed result.

Judge the command itself, not the reason the model gives. The same offer
applies to commands you type with `!`, such as `!sudo systemctl status ollama`.
Caretz waits as long as it takes for an answer; for unattended runs,
`--permission-timeout 5m` denies any prompt left unanswered that long.

## When Caretz Asks

Caretz stops before the model sees the result and asks when the output shows
that the sandbox refused something:

- a path the sandbox wouldn't let it write;
- a path it hid (on Linux, unmounted paths look missing, so a
  `No such file or directory` for a path that exists on the host counts);
- `sudo`, which can't gain privileges inside the sandbox;
- an unreachable Docker daemon or system bus whose socket exists on the host.

Path refusals count only when the command fails, so a command that succeeds
but prints refusal text, such as a search through logs, keeps its result. An
unreachable Docker daemon or system bus counts even when the command
succeeds, because `docker rmi x; echo done` or `docker images | head` hides
the failure behind a later command's exit status. The same goes for `sudo`
when the command runs `sudo` and the output has sudo's own refusal line, so
`sudo -n true; echo "exit=$?"` asks you, while `grep -r "no new privileges"`
over logs doesn't.

The model writes the command and so its output, so judge the command itself,
not the stated reason. The model can't request a rerun and isn't told one
exists until you approve one: an approved rerun's result starts with a note
that it ran outside the sandbox, so the model doesn't mistake host access it
saw for sandbox access.

## What an Approval Covers

**Allow for session** covers this exact command; a different command, even the
same program with different arguments, prompts again. That holds for a
hidden Docker daemon or system bus too: the refusal comes from output the
model's command printed, so it never approves other commands. To give
sandboxed commands Docker, use `--allow-docker` instead. **Deny** is
also what happens when no one can be asked, for example in a subagent or a
non-interactive run.

In the interactive terminal, an approved command runs on your terminal while
Caretz steps aside. `sudo` asks for your password as usual, Caretz never sees
it, and Ctrl-C stops only that command. The rerun gets the sandbox's configured
environment on top of your own, but not Caretz's API keys. When you approve
from somewhere without a terminal, such as ACP, the command runs without one
and `sudo` can't ask for a password.

The sandboxed attempt has already run when you're asked, so check for partial
side effects. Blocked network destinations still use the narrower network
approval first. Plan mode never escalates, and neither do background commands.
In session mode, an approved command runs as a separate process outside the
session supervisor. It doesn't see the supervisor's background jobs or private
`/tmp`, and it can't change them.

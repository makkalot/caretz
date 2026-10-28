---
title: "Repeat with /loop"
weight: 10
description: "Run a prompt every few minutes with /loop."
---

# Repeat a Prompt with /loop

`/loop` runs a prompt every five minutes, or at the interval you choose:

```text
/loop Check the repository for new failing tests and summarize them
/loop 1m Run the tests and report failures
/loop --interval=1h Review open work and suggest the next step
```

Caretz confirms the job ID and the next run time. Use `/schedule-list` to see
every schedule in the session with its last run, status, and any error:

```text
/schedule-list
```

## Run a Command Instead of a Prompt

When a shell command can do the check, start the body with `!`. Caretz runs
the command in the sandbox without starting a subagent, then gives the model
one turn to read the exit code and output and react:

```text
/loop 10m !go test ./...
/loop 1h !./scripts/check-deploy.sh
```

This costs far fewer tokens than a prompt. When you ask for a schedule in plain
words, the model is told to prefer a command whenever one does the job. Command
schedules always run in the sandbox and never ask to leave it. When the sandbox
is disabled, Caretz refuses to register them and refuses to run any that were
registered earlier.

## Stop a Loop

To stop a loop, ask Caretz to cancel it by ID:

```text
Cancel schedule sched_ab12cd34
```

## Intervals

The interval is a Go duration such as `90s`, `15m`, or `2h`. The shortest
interval is one minute.

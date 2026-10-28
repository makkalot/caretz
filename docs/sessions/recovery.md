---
title: "Recover an interrupted turn"
weight: 30
description: "Inspect and repair a session left broken by a crash."
---

# Recover an Interrupted Turn

A crash can leave a tool call without its result, or a finished tool turn
without its final answer:

```text
[previous messages][assistant tool call][x missing tool result]
[previous messages][assistant tool call][real tool result][x missing final assistant response]
```

Caretz won't resume that session until it's fixed. Check what's wrong (this
only reads), repair it, then continue:

```sh
caretz session inspect 7f31a221
caretz session repair 7f31a221
caretz -s 7f31a221
```

Do not edit the JSONPL files by hand.

## Stop a Running Turn

Press **Esc** or **Ctrl-C** to cancel the current model request, tool, or
subagent immediately. Caretz waits for cleanup and saves an interrupted turn
before accepting your next prompt. Completed edits remain; an interrupted
operation may have partial effects. Captured command output and stopped
background jobs are recorded so the next turn knows what was cut short.

Stopping also cancels background commands and async work launched by that
turn. Jobs from earlier completed turns and schedules remain. See
[background job ownership](../sandboxing/dev-servers.md#process-cleanup).
An approved interactive program that owns the terminal keeps its native keys;
use Ctrl-C there.

If saving the interruption fails, Caretz reports the error. Use the inspection
and repair commands above before resuming that session.

## How Repair Works

`session inspect` reports the last safe event, pending tool calls, any later
affected messages, and the synthetic recovery messages it proposes.

For an unfinished tail, repair creates a valid controlled-interruption ending:

```text
[previous messages][assistant tool call][repair tool result][interrupted assistant response]
```

For an older incomplete turn followed by later input, repair preserves that
later history by inserting recovery events at the broken position:

```text
[previous messages][assistant tool call][repair tool result][interrupted assistant response][later user message]
```

This second case is a context rewrite repair, not an append-only logical repair.
Caretz locks the session, validates the proposed history, backs up the original
as `context.jsonpl.<timestamp>.<recovery-id>.bak`, and atomically replaces
`context.jsonpl`. The audit `events.jsonpl` is not rewritten. Running repair on
an already clean session is harmless.

## Delete a Broken Tail Intentionally

`session trim` is an explicit escape hatch, not the normal repair path. It
removes every event at and after an originator version from both logs:

```sh
caretz session trim 7f31a221 --from-version 42
```

Caretz creates timestamped `.bak` copies first and prints their paths. Even with
backups, this changes the authoritative audit history. Prefer `inspect` and
`repair`; trim only when you intentionally want to discard that tail.

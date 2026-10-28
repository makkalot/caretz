---
title: "Plan mode"
weight: 10
description: "Explore and plan without changing anything."
---

# Plan Mode

Plan mode lets Caretz read the project and run read-only commands, but not edit
anything:

```sh
caretz --plan -p "trace the checkout flow and propose how to add gift cards"
caretz --plan
```

In an interactive session, press **Shift+Tab** to switch between plan and build
mode. Agree on a plan with the workspace locked, then switch and ask Caretz to
implement it with the conversation still in context. The footer shows the mode,
model, context usage, and prompt-cache hit rate:

```text
──────────────────────────────────────────────────────────
> trace the checkout flow and propose how to add gift cards
──────────────────────────────────────────────────────────
  ⏸ plan · gpt-5.6 · ctx 42.0k/400.0k (10%) · cache 87% · (shift+tab to toggle)
```

In plan mode, Edit, Write, and SendFile stay listed but their calls fail, and
each Bash command runs in a one-off sandbox with the workspace mounted
read-only. Your own `!` commands follow the same rule, so `cd` and `export`
don't carry over between them.

## What a Switch Affects

Switching mode applies to the next tool call Caretz makes. Work that is
already running keeps the mode it started in, including subagents and
background Bash jobs; press **Esc** to cancel a running turn immediately before switching.
See [stopping a turn](../sessions/recovery.md#stop-a-running-turn) for what is saved.
`/goal` always
runs in build mode, because starting a goal asks for changes.

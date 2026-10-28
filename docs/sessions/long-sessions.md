---
title: "Long sessions"
weight: 20
description: "Compaction and task tracking keep long work coherent."
---

# Long Sessions

When the context window fills, Caretz summarizes older turns and keeps recent
ones word for word. This can repeat any number of times, and the full history
stays on disk. Check or compact an interactive session directly:

```text
/context
/compact
```

Most users only need to configure the model's real context limit. To tune the
budgets, see [Compaction budgets](../config/compaction.md).

## How Compaction Works

Caretz estimates usage against the configured context window. It reserves room
for the next request and response, then summarizes older complete turns when
space becomes tight.

- A later compaction carries the previous summary forward, together with the
  active request, current task state, and newer conversation.
- During long tool chains, Caretz can summarize completed tool-call boundaries
  while preserving the active user request.
- If a provider reports a smaller context limit than configured, Caretz
  rebuilds the summary request against that limit and retries.

This design has supported hours-long local sessions around a 60k context window
during Caretz's development. That is field experience rather than a guarantee:
summary quality, tool calling, server limits, and configured budgets still
matter.

Compaction never deletes the audit history. It appends summary events and
changes the active model view reconstructed from `context.jsonpl`.

## Keep Multi-Step Work on Track

Internal task tracking is enabled by default. For multi-step work, the model can
record the current step, completed work, and a concrete next action. That state
is persisted separately from ordinary conversation messages, restored with the
session, and included as the authoritative work state when Caretz builds a
compaction summary. A task list therefore survives both compaction and restart.

This is especially useful for smaller models: leave tasks enabled for long or
multi-step jobs so progress survives context compression. For a narrow,
single-purpose agent, remove the task tools and guidance with `--no-tasks`.

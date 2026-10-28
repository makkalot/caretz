---
title: "Storage and privacy"
weight: 40
description: "Where sessions live, the event log, adapters, and private history."
---

# Session Storage and Privacy

Keep session history outside the workspace:

```sh
caretz --config-root ../private-caretz-state
```

That root's session data remains hidden even when a broader read or write root
overlaps it. This does not hide history already in `.caretz/sessions`. To share
selected private history, review it and copy the desired material into the
workspace. A config root inside the workspace follows the local read-only
policy; symlinks to external storage remain private. Loaded config JSON files
stay hidden in either case.

`--config-root DIR` or `CARETZ_CONFIG_ROOT` moves `config.json`, `sessions/`, and
config-root agents and skills to `DIR`. `--workspace` changes where the default
`.caretz` root is discovered. `--incognito` keeps the session in memory; it
cannot be resumed after the process exits.

## Adapters

One-shot prompts use the `cli` namespace and interactive sessions use `tui`:

```text
.caretz/sessions/
  cli/<session-id>/
  tui/<session-id>/
  acp/<session-id>/
```

To open a session from another namespace, name its adapter:

```sh
caretz --adapter cli -s 7f31a221
caretz session list --adapter cli
```

`--adapter` requires `-s` when it differs from the mode's default. ACP sessions
are resumed by their ACP client instead. For explore, inspect, repair, and trim,
Caretz finds an ID automatically when it exists in exactly one adapter
namespace; otherwise pass `--adapter TYPE`. These commands read storage
directly, so they do not need model configuration or credentials.

## The Event Log

Caretz records activity as append-only JSON-lines events instead of repeatedly
replacing one large conversation document. Each event has a type, timestamp,
originator, and monotonically increasing version. New activity is cheap to
persist, a session can be reconstructed deterministically after a restart, and
explorers and external applications can consume the same event stream.

```text
.caretz/sessions/cli/7f31a221/
  events.jsonpl     # user-facing and application audit events
  context.jsonpl    # messages and tools used to rebuild model context
  runtime.json      # model, reasoning, and profile settings restored with this session
```

The explorer merges the two event logs into one timeline. `runtime.json` keeps
each session on the model and runtime profile it was using, even if another
session changes the workspace defaults. Explicit `--model`/`OPENAI_API_MODEL`
and `--profile`/`CARETZ_PROFILE` selections still override the saved choices
when reopening a session. Normal event writes append to both logs.
[Repair](recovery.md) is the deliberate exception.

Go applications can subscribe to these events as they happen; see
[Embedding: sessions](../embedding/sessions.md).

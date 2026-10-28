---
title: "Config files, flags, and env"
weight: 30
description: "Where config lives, what overrides what, and one-off overrides."
---

# Config Files, Flags, and Environment

Try a provider for one run without saving anything:

```sh
caretz --api-key "sk-..." \
  --base-url https://api.openai.com/v1 \
  --model gpt-5
```

The equivalent local run is:

```sh
caretz --api-key noset \
  --base-url http://127.0.0.1:8080/v1 \
  --model qwen-code \
  --context-window 60000
```

## Locations and Precedence

Caretz reads configuration from:

- `~/.config/caretz/config.json` for global defaults.
- `.caretz/config.json` for workspace settings such as `activeModel` and
  profiles. It may not contain a `provider` block.
- `config.json` inside `--config-root DIR` or `CARETZ_CONFIG_ROOT` when an
  external config root is selected.

Explicit CLI flags override environment variables, which override project
configuration, which overrides home configuration. An external config root
replaces `.caretz` as the project config location. A relative config root is
resolved against the workspace.

`--workspace DIR` changes the workspace before config, skills, subagents, and
`AGENTS.md` are discovered.

## Environment Overrides

Environment variables are supported when process-level configuration is more
convenient, but they are optional and are not required for normal setup. The
hosted form is:

```sh
export OPENAI_API_KEY="sk-your-key"
export OPENAI_API_BASE_URL="https://api.openai.com/v1"
export OPENAI_API_MODEL="gpt-5"
export OPENAI_REASONING="medium"
export CARETZ_CONTEXT_WINDOW="200000"
export CARETZ_OUTPUT="compact"
export CARETZ_THEME="ansi"
```

For a local model, use the same variables with the loopback endpoint:

```sh
export OPENAI_API_KEY="noset"
export OPENAI_API_BASE_URL="http://127.0.0.1:8080/v1"
export OPENAI_API_MODEL="qwen-code"
export CARETZ_CONTEXT_WINDOW="60000"
```

Compaction can also be tuned with `CARETZ_RESERVED_TOKENS`,
`CARETZ_KEEP_RECENT_TOKENS`, and `CARETZ_SUMMARY_TOKENS`.

`OPENAI_API_KEY` defaults to `noset`. It only applies when the endpoint comes
from `--base-url` or `OPENAI_API_BASE_URL`, or when no provider is configured.
A configured provider always uses its own key from
[credentials.json](api-keys.md), unless you pass `--api-key`.
`OPENAI_REASONING` is optional and can
be overridden with `--reasoning`. Caretz picks the model from `--model` first,
then `OPENAI_API_MODEL`, then `activeModel`.

Sandbox and tool settings live in profiles rather than model configuration;
see [Permanent sandbox settings](../sandboxing/permanent-config.md) and
[Runtime profiles](../profiles/_index.md). Run `caretz --help` for the
complete flag list.

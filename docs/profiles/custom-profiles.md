---
title: "Your own profiles"
weight: 30
description: "Define profiles, inheritance, and which setting wins."
---

# Your Own Profiles

Put profiles in `~/.config/caretz/config.json` or `.caretz/config.json`. Every
profile inherits `general`, so you list only what changes:

```json
{
  "profile": "coding",
  "profiles": {
    "general": {
      "output": {"mode": "compact"},
      "tasks": {"enabled": true}
    },
    "coding": {
      "skills": ["go-development"],
      "agents": ["reviewer"]
    }
  }
}
```

There is no `extends` chain. Omitted fields inherit, an empty list clears an
inherited list, and `["*"]` selects all available resources.

## Example: Lean by Default

```json
{
  "profile": "simple",
  "profiles": {
    "simple": {
      "systemPrompt": "You are a coding assistant.",
      "disabledTools": ["CallSubagent"],
      "skills": ["*"],
      "agents": [],
      "sandbox": {
        "sessionExec": true,
        "allowDocker": true,
        "env": ["GOPROXY", "GOFLAGS=-mod=readonly"]
      }
    }
  }
}
```

The `simple` profile removes both the agent catalog and the `CallSubagent` tool
while retaining every discovered skill. Switch to `--profile ops` for one
process when you need infrastructure access.

## Settings and Precedence

Operational settings belong under a profile: `systemPrompt`, `tools`,
`disabledTools`, `toolConfig`, `skills`, `agents`, `sandbox`,
`execution.docker`, `tasks`, `goal`, [`schedules`](../schedules/models.md),
[`peers`](../peers.md#run-it), `output`, `plan`, `incognito`, `request`, and `modelDiscovery`. Legacy root-level `sandbox`, `execution`, `tools`, `tasks`,
and `output` are rejected with a migration message.

Profile resolution is deliberately interleaved:

1. Built-in `general`, then the selected built-in profile.
2. Home `general`, then the selected home profile.
3. Project `general`, then the selected project profile.
4. Environment variables.
5. Explicit CLI arguments.

Every explicit CLI argument wins, including false booleans such as
`--plan=false`. Tool policy flags (`--tools` and `--disable-tool`) replace their
configured lists. Grant flags (`--read-root`, `--writable-root`,
`--network-allow`, and `--sandbox-env`) append to the resolved profile lists, which makes one-off
access convenient without repeating profile defaults. A scalar flag changes
only its corresponding field.

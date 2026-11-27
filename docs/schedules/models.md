---
title: "Models and context"
weight: 30
description: "Run scheduled prompts on a cheaper model with a smaller context."
---

# Pick the Model for Scheduled Runs

A check that runs every ten minutes rarely needs your best model. Use a
profile's `schedules` block to run every scheduled prompt on a cheaper or
local model, and to cap its context window so long runs compact early:

```json
{
  "profiles": {
    "general": {
      "schedules": {
        "model": "local/qwen-code",
        "reasoning": "low",
        "limit": {"context": 64000, "reservedTokens": 8000}
      }
    }
  }
}
```

Each field is optional. Without `model`, scheduled runs use the session's
current model, and `limit` still applies to that model. Caretz reads the block
when each run starts, so changing it and reopening the session also changes
schedules you already registered. `/schedule-list` shows the model each job
will use.

## When the Model Is Down

If the scheduled model's endpoint can't be reached, say your local server is
off, the run starts over on `fallback` and then on the session's current
model, instead of failing:

```json
{
  "profiles": {
    "general": {
      "schedules": {
        "model": "local/qwen-code",
        "limit": {"context": 64000},
        "fallback": {"model": "chatgpt/gpt-6.1-sol", "reasoning": "low"}
      }
    }
  }
}
```

Each attempt is a fresh child with the task from the start, using its own
model's configured limits; `limit` applies only to `model`. The run's
`timeout` covers every attempt. You get one notice per session for each
unreachable model. Only a failed connection moves on: an HTTP error or a
timeout still fails the run.

## Rules

- `model` is a configured `provider/model` ref. [Model rules](../config/model-rules.md)
  apply: a blocked model makes the run fail rather than switching to another.
  `model` and `fallback.model` are checked at startup, so an unknown or
  blocked ref fails right away.
- `reasoning` needs `model`, and `fallback.reasoning` needs `fallback.model`.
- `limit` takes the same keys as a provider model's limits: `context`,
  `reservedTokens`, `keepRecentTokens` and `summaryTokens`. An unset key keeps
  the model's value. `context` may shrink the model's window but not exceed it,
  and `reservedTokens` must stay below `context`; a run that breaks either
  rule fails with an error in the conversation.
- A project or profile layer that sets `schedules` replaces the whole block
  from earlier layers.

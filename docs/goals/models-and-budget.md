---
title: "Models and budget"
weight: 20
description: "A different model per role, and token and time limits."
---

# Models and Budget

Plan and verify with a strong model while a cheaper or local model writes the
code:

```text
/goal --planner-model anthropic/claude-opus-5-5 --planner-reasoning high --worker-model local/qwen-code Add a CSV export for the filtered report
```

When a cheap worker keeps stalling, retry with a stronger one:

```text
/goal:retry --worker-model anthropic/claude-opus-5-5
```

`--model` sets all three roles, `--verifier-model` picks the model for both the
review and the judge, and a `--<role>-reasoning` value without a model applies
to the current model. Every model is resolved before planning starts, so a typo
fails at once. The choice is saved with the goal, so answers, approval and
`/goal:resume` keep it after a restart. `/goal:approve` and `/goal:retry` take
the same flags.

## Give Long Goals a Bigger Budget

A goal stops as `over_budget` after 10 million tokens (completion plus uncached
prompt tokens across all roles) or 120 minutes by default. Raise the limits, or
set role defaults, in a profile; `0` disables the token limit:

```json
{
  "profiles": {
    "general": {
      "goal": {
        "maxTokens": 25000000,
        "maxDuration": "4h",
        "planner": {"model": "anthropic/claude-opus-5-5", "reasoning": "high"},
        "worker": {"model": "local/qwen-code"},
        "verifier": {"model": "anthropic/claude-opus-5-5", "reasoning": "high"}
      }
    }
  }
}
```

A role you leave out uses the session's current model. `/goal:status` and the
final message show what a goal spent, for example
`Spent: 1.2M/25.0M tokens, 38/240 min`.

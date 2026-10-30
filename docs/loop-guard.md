---
title: "Loop guard"
weight: 10
description: "Catch a turn that keeps hitting the same error and point it elsewhere."
---

# Loop Guard

Stop paying for a turn that runs the same failing test, makes the same edit
and fails again. The loop guard notices when one failure keeps repeating and
asks a judge model whether the turn is stuck. If it is, the agent gets one
specific note telling it to change approach.

```sh
caretz --loop-guard active -p "make the integration tests pass"
```

When the same error comes back a third time, the next tool result the agent
reads ends with a note like this:

```text
[loop guard] This turn appears to be stuck in a failure loop. The same failure has occurred 3 times: Bash: <path>/db_test.go:N: undefined: testDB. Stop retrying the same fix. Diagnosis: testDB is declared in a file with a build tag the test run doesn't set.
Try instead: run go test with -tags integration, or move testDB out of the tagged file.
```

The guard redirects once per turn. If the turn still looks stuck after that,
you get a `[loop-guard]` notice saying so, and the turn keeps running until
you stop it. The guard never ends a turn on its own.

## Try It in Shadow Mode First

`shadow` runs the judge and logs every verdict but never changes what the
agent sees, so you can check its calls before trusting it. Set the mode and
the judge in a [profile](profiles/_index.md):

```json
{
  "profiles": {
    "general": {
      "loopGuard": {
        "mode": "shadow",
        "repeatThreshold": 3,
        "judgeTimeout": "15s",
        "judge": {"model": "anthropic/claude-opus-5-5", "reasoning": "high"}
      }
    }
  }
}
```

Each judge call shows up in the terminal as it happens:

```text
[loop-guard] judging: repeat: 3× Bash: <path>/db_test.go:N: undefined: testDB
[loop-guard] stuck (0.90) → shadow mode, no redirect
  reason: testDB is declared in a file with a build tag the test run doesn't set.
  try instead: run go test with -tags integration, or move testDB out of the tagged file.
```

`--output detailed` adds the judge's model, latency and tokens. Each call is
also saved as a `LoopGuard.Judged` event in the session's `events.jsonpl`.

To try a judge without editing config, name it on the command line:

```sh
caretz --loop-guard shadow --loop-guard-model anthropic/claude-opus-5-5 --loop-guard-reasoning high
```

`--loop-guard-model` replaces the configured judge, including its reasoning
setting. `--loop-guard-reasoning` on its own changes the configured judge's
reasoning. The judge model is checked at startup, so an unknown or blocked
model fails right away. `CARETZ_LOOP_GUARD` and
`--loop-guard` override the mode (`off`, `shadow` or `active`). The guard is
off by default.

## How It Decides

Checking each tool result is cheap and makes no model calls. The guard
compares failures after removing the parts that change from run to run: temp
directories, line numbers, timestamps, durations, addresses and IDs. A Bash
command counts as failed when it exits non-zero. The judge is called only
when one of these fires:

- the same failure has now happened `repeatThreshold` times (default 3);
- at least 8 of the last 12 tool calls failed.

The judge sees a short summary of the turn (the task, the recent calls, the
repeated failures and the files edited), not the whole transcript. It has
been told that working through different errors counts as progress. After a
"not stuck" verdict it isn't asked again until the failure repeats another
`repeatThreshold` times.

The turn waits for the judge, up to `judgeTimeout`. A timeout, an error or an
unreadable reply counts as "not stuck", and the turn carries on as if the
guard were off. Without a `judge` model the session's own model judges. A
different model is better, because it is less likely to share the blind spot
that got the agent stuck. Subagents, `/goal` workers and scheduled runs
inherit the guard from the session that starts them.

The note is added to the end of a tool result rather than sent as a new
message. The conversation keeps its usual shape, and resumed sessions and the
prompt cache are not affected.

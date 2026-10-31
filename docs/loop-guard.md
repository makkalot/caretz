---
title: "Loop guard"
weight: 10
description: "Catch a turn that is looping or off track and point it elsewhere."
---

# Loop Guard

Stop paying for a turn that runs the same failing test, makes the same edit
and fails again, or wanders off the task. The loop guard notices repeated
failures, and checks in every 20 tool calls regardless, by asking a judge
model whether the turn is stuck. If it is, the agent gets one specific note
telling it to change approach, and if that doesn't work, the turn ends with a
report instead of burning more tokens.

```sh
caretz --loop-guard active -p "make the integration tests pass"
```

When the same error comes back a third time, the agent's next request ends
with a message like this:

```text
[loop guard] Automated message from caretz, not from the user. A reviewer model checked this turn's tool activity and judged it stuck or off track. The same failure has occurred 3 times: Bash: <path>/db_test.go:N: undefined: testDB.
Diagnosis: testDB is declared in a file with a build tag the test run doesn't set.
Try instead: run go test with -tags integration, or move testDB out of the tagged file.
The user's request for this turn: "make the integration tests pass"
Stop repeating what isn't working and get back to that request. Before your next tool call, say in one sentence what you will do differently. This note stays at the end of the conversation for the rest of this turn.
```

The guard redirects once per turn. If the judge still finds the turn stuck
at a later check, the guard ends it: the agent is told to stop, its next
request goes without tools, and it replies with what it changed, what is
unfinished, and what it needs from you. You get a `[loop-guard]` notice
saying so. Reply to carry on from there.

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
        "checkEvery": 20,
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
also saved as a `LoopGuard.Judged` event in the session's `events.jsonpl`,
including the exact summary the judge was shown, so you can check a verdict
against its evidence.

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
- at least 8 of the last 12 tool calls failed;
- `checkEvery` tool calls (default 20) have run since the last check, failed
  or not. This catches a turn going in circles or drifting from the task
  without erroring. `0` turns it off; `--loop-guard-check-every N` sets it
  for one run.

The judge sees a short summary of the turn, not the whole transcript:

- the task, plus the previous request and answer when the task is a short
  follow-up such as "continue";
- the turn's size so far: tool calls, failures, time, tokens, calls per tool,
  and files touched;
- the agent's last few messages, so it can compare what the agent says it is
  doing with what it actually does;
- earlier verdicts this turn, the last 12 calls, the most repeated failures
  with one raw output example each, calls repeated with the same arguments,
  and the files edited.

The summary includes command lines, agent messages and raw error output. A
judge on a different provider receives all of that. It has been told that
working through different errors, or a long turn doing distinct relevant
work, counts as progress. After a "not stuck" verdict, a failure needs to repeat another
`repeatThreshold` times, or another `checkEvery` calls need to run, before the
judge is asked again.

The guard only sees tool calls. A model that spends a long time reasoning
between calls without making them won't trigger it.

The turn waits for the judge, up to `judgeTimeout`. A timeout, an error or an
unreadable reply counts as "not stuck", and the turn carries on as if the
guard were off. Without a `judge` model the session's own model judges. A
different model is better, because it is less likely to share the blind spot
that got the agent stuck. Subagents, `/goal` workers and scheduled runs
inherit the guard from the session that starts them.

The note is a separate message, not text appended to a tool result. Models
are trained to distrust instructions that show up inside tool output. It is
sent at the end of each of the turn's remaining requests and is not saved in
the session's context, so turn boundaries, resumed sessions and the cached
prompt prefix are unchanged. The `LoopGuard.Judged` event keeps the note's
text.

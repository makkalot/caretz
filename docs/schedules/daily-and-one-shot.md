---
title: "Daily and one-shot runs"
weight: 20
description: "Run a prompt at a time of day, or once later."
---

# Daily and One-Shot Runs

Ask in plain language for a run at a time of day, or for a single run later.
Caretz registers the schedule for you:

```text
Every day at 09:00, summarize yesterday's commits and open TODOs
Every day at 18:30 Europe/Berlin, check whether the nightly build passed
In one hour, check whether the deploy finished and tell me the result
```

Daily times use your local timezone unless you name one. Check and cancel these
schedules the same way as loops: `/schedule-list`, then
`Cancel schedule <id>`.

## How Schedules Run

- Each run starts a fresh subagent with the scheduled prompt, so its tool calls
  stay out of your conversation; Caretz then reports a short summary of the
  result.
- Schedules are saved with the session. They run only while an interactive
  session is open, not in `caretz -p`, and resume when you reopen the session.
- If Caretz was closed through several due times, the schedule runs once for
  the latest one rather than catching up on each. A one-shot run that came due
  while Caretz was closed runs when you reopen the session.
- A session holds up to 100 schedules, and a scheduled run can't create more
  schedules.

## Timeouts and Failed Runs

A run gets 30 minutes by default, and each model step in it (the subagent,
and the turn that reports the result) gets 15. A command gets the time the
model step leaves, 15 minutes by default. Ask for more when you set up the
schedule, or later, up to an hour per run:

```text
Every day at 02:00, run ./scripts/nightly.sh with a 50 minute timeout
Give sched_1a2b3c4d a 45 minute timeout and a 20 minute model timeout
```

When a run fails, its error says what went wrong. For a timeout, that's which
limit ran out: `run timeout of 30m exceeded`, `model timeout of 15m exceeded`,
or `command timeout of 15m exceeded`. Caretz retries a failed or timed-out run
up to three times, after 1, 2, and 4 minutes. The next run is told how the
previous one failed, so it can try something different.

You can also change what a schedule runs without recreating it. It keeps its
ID and cadence:

```text
Change sched_1a2b3c4d to run only the integration tests
```

To switch a schedule between a command and a prompt, cancel it and ask for a
new one.

---
title: "Schedules"
description: "Run a prompt every few minutes, every day, or once at a set time."
---

# Run Tasks on a Schedule

Schedules let Caretz keep working while you do something else: watch the test
suite, write a morning summary, or check back on a deploy in an hour.

```text
/loop 10m Run the tests and report new failures
Every day at 09:00, summarize yesterday's commits
/schedule-list
```

Each run happens in its own subagent, and Caretz reports a short summary of the
result in your conversation. A check that a shell command can do runs as a
[command](loop.md#run-a-command-instead-of-a-prompt) instead: no subagent,
one model turn to read the output. Schedules belong to the session: they run while it is open and
pick up again when you [resume it](../sessions/_index.md).

- [Repeat with /loop](loop.md): run a prompt every few minutes or hours.
- [Daily and one-shot runs](daily-and-one-shot.md): ask for a time of day or
  a single run later, and see how schedules behave when Caretz is closed.
- [Models and context](models.md): run scheduled prompts on a cheaper model
  with a smaller context window.

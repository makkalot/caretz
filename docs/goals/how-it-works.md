---
title: "How goals work"
weight: 30
description: "Checks, the reviewer and judge, frozen evidence, and records."
---

# How Goals Work

A planner turns your goal into evidence and tasks, workers implement them one
at a time, and separate checks confirm the result. Neither the worker nor the
model grading it is trusted on its own.

## Tasks and Checks

Plans have two kinds of tasks. A *check* task only writes verification, such as
a test or script, and a *work* task changes the product. A work task is never
graded by a check it wrote itself: its check either exists already or is
written by an earlier check task. Every check is a command (an argv, or a
shell line when a pipeline is essential) that passes when it exits 0. Planning
only confirms that each check parses and its executable exists; it does not run
checks. When a goal replans, the planner sends changes to the plan (add,
revise or drop a task) rather than a new plan, so finished tasks can never be
redone or lost.

After approval, Caretz runs one task at a time and immediately runs that task's
check:

- A failing check retries the task with the check output, as long as each
  attempt fails differently. The same failure twice sends the task back to the
  planner, which sees how many attempts it took.
- A broken check (missing or non-executable command, or a malformed
  invocation) goes back to the planner at once.
- A check slower than two minutes needs `timeout_seconds`; a timeout counts as
  a failure.

## Review and Judge

Once the tasks pass, Caretz reruns the approved goal evidence and each distinct
task check to catch regressions. Only when every check passes, an independent
reviewer reads the files the goal changed (with read-only tools) and checks the
logic: whether each criterion is really implemented, with the code it cites,
whether the new tests exercise it, and any defects rated blocker, major or
minor. Files that were already modified before the goal started are kept out
of the review's diff.

A judge then makes the final decision from the check results and the review.
It must accept or dismiss, with a reason, every blocker and major finding, and
cannot finish the goal while accepting one. Any gap or accepted finding returns
to the planner with a structured failure report. If the same failures repeat
across two passes, the goal stops as stalled instead of looping.

## Evidence the Worker Cannot Weaken

Evidence files, and the test files a test runner in the evidence runs (for
example every `*_test.go` and `testdata/` file for `go test`), are frozen when
the run starts or when the check task that writes them finishes. The sandbox
then makes them read-only for work tasks, so a worker cannot edit a test to
make it pass; changes to tests belong in check tasks. If a frozen file changes
anyway, Caretz pauses and asks you to review the change before verifying.

Some failures cannot be fixed by changing code, for example when the sandbox
denies a listening socket, network access, or a write outside the workspace.
The sandbox reports what it refused, so Caretz pauses with the blocked command
and asks you to grant access or revise the evidence instead of replanning
around the restriction. After you answer, it reruns the check. A worker that
needs a decision or a permission pauses the goal in the same way.

## Records

Every decision a goal makes, from the approved plan to each check result and
answer, is recorded as an event in the session's `events.jsonpl`. That log is
the goal's only record: `/goal:resume`, `/goal:status` and a reopened session
rebuild the goal from it, so an interrupted run continues exactly where it
stopped. Goals recorded by an older Caretz cannot be resumed or retried; start
a new goal instead.

`state.md` is a readable view of the same goal, regenerated after every step
with the plan, evidence, model assignment, short conclusions, failure reasons
and the tail of each failing check's output. Read it to follow a run; deleting
it loses nothing. Per-task attempt details live under `tasks/` in the session
directory.

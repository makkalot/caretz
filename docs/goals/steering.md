---
title: "Answer, retry, stop"
weight: 10
description: "Respond to questions and change course mid-goal."
---

# Answer, Retry, and Stop

When a decision needs you, Caretz prints the question and the next command:

```text
Which rows should the CSV contain?
Next: /goal:answer <your answer>
```

Reply normally or with `/goal:answer Use the currently filtered rows`. Other
commands:

```text
/goal:status     what it is doing and what it needs
/goal:resume     continue after an interruption
/goal:stop       end a paused run
```

## Change Course

When the plan has gone wrong, tell the planner what to do differently:

```text
/goal:retry keep the old command as a deprecated alias instead of deleting it
/goal:retry drop the criterion about restoring other files; never revert my uncommitted changes
```

Retry works on a finished or stopped goal and on one paused for your answer or
approval. It revises the remaining work, keeps completed tasks as history, and
waits for `/goal:approve`. The planner sees your feedback on this retry and on
every later replan, and may revise the evidence when your feedback is about it.
Every replan opens with what changed:

```text
### Changes from plan v3 to v4 (evidence v1 to v2)

**Goal criteria**
- − Unrelated prior uncommitted edits are restored.

**Tasks**
- ~ T1 Remove the legacy /work slash-command implementation
  - next action: − Restore the unrelated paths, then remove the command
  - next action: + Remove the command; leave other uncommitted files alone
```

A bare `/goal:retry` keeps the approved evidence and just plans the remaining
work again. To change only what counts as done, use `/goal:evidence --edit
"describe the new evidence"`. Hand edits to `state.md` are not imported.

---
title: "Goals"
description: "Describe an outcome; caretz plans, does, and proves it."
---

# Work Toward a Goal with Independent Checks

Describe the outcome you want, and Caretz plans the work, does it, and proves
it is done with real commands and an independent reviewer instead of just
claiming it.

```text
/goal Add a CSV export for the filtered report
```

`/goal` needs internal tasks, so it doesn't work with `--no-tasks`.

## Approve the Plan

Caretz first explores the project with read-only tools and shows the plan and
the exact commands it will use as evidence:

```text
### Evidence

- **E1** `go test ./...` — export and filtering behave as specified
```

Approve it, or ask for a change first:

```text
/goal:approve
/goal:approve --edit "Check the filtered-row case too"
```

Use `/goal --yes Add a CSV export for the filtered report` to start without
asking. Workers can't edit the evidence tests to make them pass.

For a one-shot CLI run, the approval message includes the session ID. Reopen
that session with `-s` and run the printed `/goal:approve` command.

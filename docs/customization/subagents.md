---
title: "Subagents"
weight: 20
description: "Hand focused jobs to agents with their own prompt and model."
---

# Subagents

A subagent is one Markdown file; the filename is its name:

```text
.caretz/agents/
  reviewer.md
```

```markdown
---
description: Review code changes for correctness and missing tests.
mode: subagent
---

Review the requested change carefully. Do not edit files. Return findings in
severity order with file and line references.
```

Call it in a session:

```text
@reviewer review the current diff
```

Caretz runs the child, stores it as a child session, feeds its result back to
the parent, and lets the parent produce the user-facing response. Subagent
orchestration is sequential. For multi-step work with independent checks, see
[Goals](../goals/_index.md).

## Optional Fields

`description`, `mode: subagent`, and a non-empty prompt body are required.
Supported optional frontmatter fields are:

```yaml
model: local/qwen
reasoning: high
temperature: 0.2
permission:
  edit: deny
  bash: deny
```

- `model` selects a configured `provider/model` for that subagent.
- `reasoning` overrides its reasoning effort.
- `temperature` is passed to the model when supported.
- `permission.edit: deny` disables both `Edit` and `Write`.
- `permission.bash: deny` disables `Bash`.

Other permission values currently have no effect. Child sessions inherit the
parent workspace, context limits, sandbox policy, and applicable tools, then
apply their denied tools.

Agent directories are read-only to the session, and only agents that existed
when the session started can run. Edits you make to an existing agent apply on
its next run; a new agent file needs a new session.

Focused subagents can work better with smaller local models because their
prompt and responsibility stay narrow. Select a local `model`, write a concrete
prompt, and deny tools the role does not need.

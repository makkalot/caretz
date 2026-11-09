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

You don't have to name it. Ask "check my latest change for bugs" and the main
agent can look up your subagents with `ListSubagents`, read each
`description`, and hand the job to `reviewer` itself. Write `description` as
"when to use me", since that line is all the main agent sees.

Caretz runs the child, stores it as a child session, feeds its result back to
the parent, and lets the parent produce the user-facing response. Any subagent can also
[work in the background](background.md) while you keep
talking, and any run cut short can be resumed where it stopped. For
multi-step work with independent checks, see [Goals](../goals/_index.md).

## Example: a Second-Opinion Advisor

A read-only agent on a different, stronger model gives you a second opinion on
hard bugs, plans, and diffs, without letting it touch your files:

```markdown
---
description: Senior read-only advisor on a stronger model. Use for hard debugging, reviewing a plan or architecture, and reviewing changes. Pass it the file paths, symptoms, and diffs, since it does not see this conversation.
mode: subagent
model: openai/gpt-5.4
reasoning: high
plan: true
---

You are a senior advisor. Think the problem through, read the code you need,
and cite file:line evidence. You never edit files. Return a concise
recommendation, the reasoning behind it, and the risks you see.
```

Save it as `.caretz/agents/advisor.md`, then ask "ask the advisor to review
the last commit", or let the main agent call it when a task matches.

## Optional Fields

`description`, `mode: subagent`, and a non-empty prompt body are required.
Supported optional frontmatter fields are:

```yaml
model: local/qwen
reasoning: high
plan: true
permission:
  edit: deny
  bash: deny
```

- `model` selects a configured `provider/model` for that subagent.
- `reasoning` overrides its reasoning effort.
- `plan: true` runs the subagent in plan mode: no `Edit`, `Write`, or
  `SendFile`, and `Bash` runs in a read-only sandbox. Use it for reviewers
  and testers, especially in the [background](background.md).
- `permission.edit: deny` disables both `Edit` and `Write`.
- `permission.bash: deny` disables `Bash`.

Each frontmatter value must stay on one line. Caretz skips an agent file it
can't parse, prints a warning naming the file and line at startup, and keeps
the other agents. A `temperature` field is accepted but not sent to the model.

Other permission values currently have no effect. Child sessions inherit the
parent workspace, context limits, sandbox policy, and applicable tools, then
apply their denied tools.

Agent directories are read-only to the session, and only agents that existed
when the session started can run. Edits you make to an existing agent apply on
its next run; a new agent file needs a new session.

Focused subagents can work better with smaller local models because their
prompt and responsibility stay narrow. Select a local `model`, write a concrete
prompt, and deny tools the role does not need.

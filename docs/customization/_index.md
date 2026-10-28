---
title: "Instructions and skills"
description: "Teach caretz your project's rules with plain Markdown."
---

# Project Instructions, Skills, and Subagents

Teach Caretz your project's rules, give it reusable playbooks, and hand
focused jobs to specialist agents. They are all plain Markdown files.

Put rules that apply to every request in `AGENTS.md` at the workspace root:

```markdown
# Project instructions

- Run `go test ./...` after Go changes.
- Keep the default binary free of optional adapter imports.
- Ask before changing a public wire format.
```

Caretz includes this content in the system prompt. `--system-prompt` and
`--system-prompt-file` replace the built-in base prompt, but discovered skills,
`AGENTS.md`, available-tool guidance, and the working-directory section are
still appended.

Caretz reads one `AGENTS.md` from the workspace root. It does not currently
merge parent or nested `AGENTS.md` files.

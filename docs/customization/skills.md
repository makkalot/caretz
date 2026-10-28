---
title: "Skills"
weight: 10
description: "Reusable playbooks loaded only when a task needs them."
---

# Skills

A skill is a folder with a `SKILL.md`. Caretz shows the model each skill's
description and loads the full instructions only when the task matches:

```text
.caretz/skills/
  release-check/
    SKILL.md
    checklist.md
```

```markdown
---
name: release-check
description: Verify a release candidate and produce a concise release report.
---

# Release check

1. Read `checklist.md` from this skill directory.
2. Run the repository's normal verification commands.
3. Report failures with the command and relevant output.
```

The `name` and `description` fields are required, and `name` must exactly
match the containing directory. Invalid skills are skipped and reported in
verbose logs. Put skills for every project in `~/.config/caretz/skills/`.
Skills you already have in `.claude/skills/` or `.agents/skills/` work without
copying; see [Where caretz looks](discovery.md).

## How Skills Load

Caretz initially gives the model a catalog containing each skill's name,
description, and `SKILL.md` path. When the description matches the task, the
model reads the complete skill instructions with the `Read` tool. Disabling
`Read` leaves the catalog visible but prevents loading skill bodies.

Skills are instructions, not executable plugins. Supporting scripts or
templates can live inside the skill directory and are readable because
discovered resource directories are automatically added to the sandbox's
readable roots.

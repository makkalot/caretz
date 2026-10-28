---
title: "Where caretz looks"
weight: 30
description: "Discovery order, precedence, and third-party skills."
---

# Where Caretz Looks

Caretz discovers skills and agents in this order, with later locations
overriding an earlier resource of the same name:

1. Global: `~/.config/caretz/skills/` and `~/.config/caretz/agents/`
2. Explicit config root: `<config-root>/skills/` and `<config-root>/agents/`
3. Claude-compatible workspace skills: `<workspace>/.claude/skills/`
4. Codex-compatible workspace skills: `<workspace>/.agents/skills/`
5. Caretz workspace resources: `<workspace>/.caretz/skills/` and `<workspace>/.caretz/agents/`

The config-root layer is added when `--config-root` or `CARETZ_CONFIG_ROOT` is
explicitly set. The workspace is the current directory unless `--workspace`
selects another existing directory.

The Claude and Codex compatibility layers apply to skills only. Caretz reads
them from the workspace root; it does not yet scan home directories or parent
directories for third-party skills. A `.caretz/skills/` definition wins when
the same skill name also exists in `.claude/skills/` or `.agents/skills/`.
Compatibility with these directories does not imply support for every Codex or
Claude extension.

## Third-Party Skills

Follow the source repository's installation and update instructions, including
its npm or npx command when provided. Check that the resulting skill directory
is in one of the locations above, and keep any supporting files with its
`SKILL.md`. You can also copy a reviewed skill into `.caretz/skills/` or write
your own there.

We encourage you to read third-party skills and their supporting scripts before
using them. Creating and maintaining your own skills makes it easier to
understand their instructions and adapt them to your workflow.

Caretz no longer provides skill or agent installation and upgrade commands.
Previously installed definitions continue to load from their existing
directories. Old installation metadata and Git caches can remain on disk;
Caretz does not use them to load definitions or manage updates.

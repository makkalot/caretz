---
title: "Skills and sessions"
weight: 40
description: "Skills or agents not found, sessions that won't reopen."
---

# Skills and Sessions

## Skills or Subagents Are Not Found

Run with `-v` to see discovered skills and skipped invalid skill messages.
Verify:

- A skill is `<root>/skills/<name>/SKILL.md`.
- Its frontmatter has `name` and `description`, and `name` matches the directory.
- A subagent is `<root>/agents/<name>.md`.
- Its frontmatter has `description` and `mode: subagent`. Agents with `mode: peer` are [peer bots](../peers.md): they don't appear in `@name` completion, and you start them with `caretz peer <name>`.
- The file has a non-empty prompt body.
- You launched Caretz from the intended workspace or supplied `--workspace`/`--config-root`.

Workspace skills use this precedence: `.caretz` overrides `.agents`, which
overrides `.claude`; all three override config-root and global skills with the
same name. See [Where caretz looks](../customization/discovery.md).

## Session Cannot Be Reopened

First make sure the adapter namespace is correct. One-shot sessions are `cli`;
interactive sessions are `tui`:

```sh
caretz session list
caretz --adapter cli -s 7f31a221
```

If Caretz reports an unfinished assistant/tool turn, do not edit JSONPL by hand.
Use:

```sh
caretz session inspect 7f31a221
caretz session repair 7f31a221
```

Repair backs up and rewrites `context.jsonpl`, inserting recovery events at the
correct point so later messages are preserved. See
[Recover an interrupted turn](../sessions/recovery.md).

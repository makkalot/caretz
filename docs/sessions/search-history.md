---
title: "Search past sessions"
weight: 10
description: "Ask caretz to mine its own earlier work."
---

# Let Caretz Search Its Own Work

Sessions live in `.caretz/sessions/` inside the workspace, so Caretz can read
them for you; you do not have to read JSON logs yourself:

```sh
caretz -p "look through my recent .caretz/sessions/cli sessions, summarize the \
decisions we made about authentication, and name the source sessions"
```

Other useful prompts:

```text
Compare the approaches tried in my last three sessions and tell me what failed.
Find the session where we discussed the migration rollback plan.
Read my previous session, explain what failed, and continue fixing the code.
```

Project-local history is readable and read-only through file tools and
sandboxed Bash. Session history can contain prompts, command output, paths,
and model responses. Treat it as project data, and see
[Storage and privacy](storage.md) to keep it outside the workspace.

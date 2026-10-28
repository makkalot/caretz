---
title: "Sessions"
description: "Pick up where you left off and see how a result was produced."
---

# Sessions

Every session is saved, so you can stop and come back later without explaining
the project again, and see exactly how a result was produced.

```sh
# Start a review and note the printed session ID.
caretz -p "map the authentication flow and identify risky assumptions"

# Continue later.
caretz -s 7f31a221 -p "now implement the two safest improvements"

# See the messages, tool calls, results, and compactions behind the work.
caretz session explore 7f31a221
```

## Find Past Work

```sh
caretz session list
caretz session list --include-subagents
```

Listings show the adapter, ID, date, event and response counts, and first
prompt. Child sessions created by subagents and `/goal` are hidden unless you
pass `--include-subagents`. The explorer prints a chronological audit summary
that answers questions such as which command changed a file, what survived the
last compaction, or where a subagent's work entered the parent session.

Use `--incognito` for a session you don't want saved. `/clear` starts a new
sibling session without deleting the one you came from.

Because the reconstructed conversation keeps a stable history and only adds
new work, continued turns are friendly to provider prompt caching.

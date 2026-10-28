---
title: "Compaction budgets"
weight: 40
description: "Tune how much context is kept verbatim and summarized."
---

# Compaction Budgets

The context limit is enough for most setups. Caretz derives the remaining
budgets when they are omitted: it reserves room for the next response, keeps a
recent-history window verbatim, and allocates space for the rolling summary.
Set them explicitly only when the model needs different tradeoffs:

```json
{
  "activeModel": "local/qwen-code",
  "provider": {
    "local": {
      "options": {
        "baseURL": "http://127.0.0.1:8080/v1"
      },
      "models": {
        "qwen-code": {
          "limit": {
            "context": 60000,
            "reservedTokens": 8192,
            "keepRecentTokens": 9000,
            "summaryTokens": 4500
          }
        }
      }
    }
  }
}
```

If a smaller model loses details too early, increase `keepRecentTokens`; if
requests run too close to the server limit, increase `reservedTokens`.

See [Long sessions](../sessions/long-sessions.md) for how compaction works.

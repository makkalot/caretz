---
title: "Local models"
description: "Point caretz at a model running on your own machine."
---

# Run caretz with a Local Model

Caretz is local-first. It was built and tested mostly on 27B Qwen models
running on a home RTX 3090, so small context windows and smaller models are
the main target, not an afterthought.

## Connect Your Local Server

Start llama.cpp, Ollama, or LM Studio, then:

```sh
caretz setup
```

Choose **Local provider**, pick your server, and caretz tests the connection
before saving it. Caretz never downloads model servers or starts them for you;
it talks to the OpenAI-compatible endpoint you already run. See
[Bring your own server](own-server.md) for endpoints, context sizes, and
configuring it by hand.

## Strip the Agent Down

Every tool and prompt line costs context. For a narrow job, keep only what it
needs:

```sh
caretz \
  --system-prompt "Inspect this Go project and report test failures concisely." \
  --tools Read,Grep,Bash \
  --no-tasks \
  --incognito \
  -p "Run the relevant tests"
```

For longer implementation work, omit `--no-tasks`: the task list survives
[compaction](../sessions/long-sessions.md) and keeps small models on track.

---
title: "caretz docs"
linkTitle: "Overview"
description: "Install, configure, operate, customize, and integrate caretz."
---

caretz is a small, sandboxed coding assistant in Go that works well with
models you run on your own hardware. Each page here is short and does one job;
use the sidebar to dig deeper, or ask caretz itself to read these docs for you.

## New to caretz?

Install caretz:

{{< install-tabs >}}

Then connect a model (a local server or a hosted provider) and give it a task:

```sh
caretz setup
caretz -p "explain this repository"
```

If `caretz` isn't found, add `~/bin` to your `PATH`; see
[Install](installation/).

<div class="docs-cards">
  <a class="doc-link" href="installation/"><span>Install</span><small>Install script or build from source.</small></a>
  <a class="doc-link" href="local-models/"><span>Run a local model</span><small>Use llama.cpp, Ollama, or LM Studio on your GPU.</small></a>
  <a class="doc-link" href="cli/"><span>Use caretz</span><small>Sessions, one-shot tasks, plan mode.</small></a>
  <a class="doc-link" href="config/"><span>Connect a hosted model</span><small>OpenAI-compatible providers.</small></a>
</div>

## Common tasks

- [Let a task reach the network](sandboxing/network/)
- [Continue yesterday's session](sessions/)
- [Hand over a goal and have it proven done](goals/)
- [Run a check every few minutes or every morning](schedules/)
- [Turn on web search](tools/web/)
- [Give caretz your own tools](tools/custom-cli/)
- [Teach caretz your project's rules](customization/)
- [Use your ChatGPT plan instead of API credits](config/chatgpt/)
- [Keep cloud models out of private folders](config/model-rules/)
- [Run one command with sudo](sandboxing/escalation/)
- [Embed the agent in a Go app](embedding/)

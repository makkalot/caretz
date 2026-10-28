---
title: "caretz"
description: "A small, sandboxed, almost dependency-free Go coding assistant with local-first models, durable sessions, skills, tools, and an embeddable core."
---

caretz is a small Go coding assistant with sandboxed tools, persisted sessions,
context compaction, task tracking, skills, subagents, ACP, and an embeddable
core. Local models are a first-class path, including smaller models running on
home hardware, while hosted OpenAI-compatible providers remain easy to connect.
It is almost dependency-free: the core needs only the Go standard library and
`golang.org/x`, so it builds offline and has a tiny supply-chain surface.
It starts no servers, has no telemetry, and talks only to the endpoints you
configure; with a local model, nothing leaves your machine.

[Run a local model](docs/local-models/), [read the documentation](docs/), or visit the
[GitHub repository](https://github.com/makkalot/caretz).

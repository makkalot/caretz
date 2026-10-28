---
title: "Dependencies"
description: "Why caretz depends on almost nothing beyond the Go standard library."
---

# Dependencies

Caretz is almost dependency-free. Beyond the Go standard library it pulls in
only three `golang.org/x` modules, maintained by the Go team. That is a
deliberate design choice, and it buys you a few things:

- **A tiny supply-chain surface.** A coding agent reads your code and runs
  commands on your machine. Many agent harnesses ship with hundreds of
  transitive npm packages, any of which can be compromised; caretz has almost
  nothing third-party to compromise.
- **Code you can actually audit.** The whole dependency tree fits in one
  `go.mod`, so reviewing what runs is a realistic afternoon, not a project.
- **Offline builds.** Copy the source to a machine with no network, such as a
  home GPU box, then build and run it against a local model.
- **Stability.** Fewer moving parts means fewer surprise upgrades changing how
  the agent behaves.

The root module currently depends on:

- the Go standard library;
- `golang.org/x/net`;
- `golang.org/x/sys`;
- `golang.org/x/term`.

Benchmark tooling and its benchmark-only dependencies live in the separate
`bench` module. Build and test commands are in [Contributing](contributing.md).

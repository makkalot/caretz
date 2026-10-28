---
title: "Contributing"
description: "Build, test, lint, and the worktree workflow."
---

# Contributing

Building requires Go 1.25.8 or later, Git, and Make:

```sh
git clone https://github.com/makkalot/caretz.git caretz
cd caretz
make setup
make build
make test
make lint
```

`make setup` downloads and tidies the Go modules and installs the pinned
`golangci-lint` binary into `./bin`; `make lint` uses that project-local binary
so lint results do not depend on a developer's PATH. `make install` copies the
binary to `~/bin/caretz`.

After changing documentation, run `make doccheck`. It checks every example
against the real code.

## Worktrees for feature work

To start isolated feature work, create a sibling worktree with a guided prompt:

```sh
make worktree
```

This creates `feat/<name>` from `main` under `../caretz-worktrees/`. Use
`make worktree-from-branch` to move the clean, currently checked-out feature
branch into that layout, or `make worktree-from-remote` to fetch and track a
branch from `origin`. Each command previews the operation before making changes;
variables such as `NAME`, `BRANCH`, `BASE`, and `WORKTREE_ROOT` can supply or
override its defaults.

## Benchmarks

Benchmark tooling lives in the separate `bench` module with its own targets:

```sh
make build-bench
make test-bench
```

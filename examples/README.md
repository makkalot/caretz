# Caretz Examples

Small, complete apps that embed caretz. Each one shows a different public
embedding API, so you can copy the one closest to what you are building.

| Example | What you get | Public API it shows |
|---|---|---|
| [git-caretz-review](git-caretz-review/README.md) | `git caretz-review main` reviews your branch before you open a PR | `cli.Run` with custom tools and a system prompt suffix |
| [coderun](coderun/README.md) | A browser app: describe a small Python app, watch the agent write and run it | `caretz.NewClient`, sessions, context-event streaming, permission prompts |
| [vault](vault/README.md) | A personal assistant over your Markdown/Obsidian notes, with a daily plan and weekly review | `cli.BuildRuntime`, adapter sessions, built-in schedules |

All three examples are implemented and include tests. Each `README.md` explains
how to run it; `plan.md` records the design and public API limitations.

## Conventions

- **Public API only.** Examples import only `github.com/makkalot/caretz`,
  `.../cli`, `.../acp`, and `.../lib/*`. They never import `internal/*` and
  never call `Session.AgentSession()`. If something can't be done with the public
  API, the example's `plan.md` lists it under "API gaps".
- **Standard library only.** No third-party Go modules. Web UIs are vanilla
  HTML/CSS/JS embedded with `embed.FS`, so there's no JS build step.
- **Separate modules.** Each example has its own `go.mod` with
  `replace github.com/makkalot/caretz => ../..`, so it always builds against
  this checkout and its dependencies stay out of caretz's `go.mod`.
- **Sandboxed by default.** Shell commands and file writes go through caretz's
  sandboxed tools. The only exception is `git-caretz-review`, which runs a fixed,
  read-only `git` argv for its diff tools.

## Build an Example

```text
cd examples/<name>
go build ./...
```

The Git review example also provides its own Makefile:

```sh
cd examples/git-caretz-review
make build
make test
make install
```

The binary builds to the example's `bin/git-caretz-review` and installs to `~/bin`.
Set `INSTALL_DIR=/your/bin` on the install target to choose another location.

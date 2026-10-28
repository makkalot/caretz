# AGENTS.md

This file provides guidance to agents when working with code in this repository.

## Project Overview

**caretz** is an LLM-powered coding assistant that uses an agentic loop with tool calls, implemented in Go.

## Commands

```sh
make setup        # Download and tidy Go module dependencies and install lint tooling
make build        # Compile binary to ./bin/caretz
make run PROMPT="your prompt"   # Build and run with a prompt
make run PROMPT="hello" V=v     # Run with info-level logging
make run PROMPT="hello" V=vv    # Run with debug-level logging
make test         # Run all tests
make test-sandbox-darwin  # Run sandbox tests with native macOS integration
make test-sandbox-linux   # Run sandbox tests with Linux integration in Docker
make lint         # Run gofmt + go vet + golangci-lint
make doccheck     # Verify documentation examples, CLI/config references, links, and Hugo
make install      # Build and install to ~/bin/caretz
```

Run a single test:
```sh
go test ./... -run TestIsPathSafe
```

The binary reads config from `~/.config/caretz/config.json` and optional project overrides from `.caretz/config.json`; when `--config-root` or `CARETZ_CONFIG_ROOT` is set, project config comes from that root's `config.json` instead. CLI flags override environment variables, which override project config, which overrides home config. A workspace `.caretz/config.json` is untrusted and may not contain a `provider` block (load error); providers come from home config or a `--config-root` config. `OPENAI_API_KEY` (default `"noset"`) is used only for ad hoc endpoints (`--base-url`/`OPENAI_API_BASE_URL`, or no provider configured); a configured provider gets only its stored key, and an explicit `--api-key` always wins. Provider API keys live only in `~/.config/caretz/credentials.json` (0600, managed by `caretz auth`), keyed by provider name; any `apiKey` value other than the legacy `"noset"` in a `config.json` is a load error. Skills are loaded from `~/.config/caretz/skills`, optional config-root `skills/`, and workspace `.caretz/skills` with later locations overriding earlier ones.

## Debugging Caretz Sessions

When asked to debug a caretz session, use compact message-flow notation so the
problem and repair are clear. Show:

- the current broken flow;
- the exact issue;
- the possible repaired flow;
- whether the fix is controlled-interruption append, context rewrite repair, or
  explicit tail deletion.

Use this notation:

```text
[prev messages][assistant tool call][x missing tool result][later user message]
[prev messages][assistant tool call][repair tool result][interrupted assistant response][later user message]
```

Common cases:

```text
missing tool result:
[prev messages][assistant tool call][x missing tool result]
fix:
[prev messages][assistant tool call][repair tool result][interrupted assistant response]

missing final assistant response:
[prev messages][assistant tool call][real tool result][x missing final assistant response]
fix:
[prev messages][assistant tool call][real tool result][interrupted assistant response]

old unclean session before later user:
[prev messages][assistant tool call][x missing tool result][later user message]
fix:
[prev messages][assistant tool call][repair tool result][interrupted assistant response][later user message]
```

For old unclean sessions, explain that repair backs up and rewrites `context.jsonpl`
with recovery events inserted in the correct place. Do not describe this as
append-only logical repair.

## Documentation

### Style

Readers skim and rarely read a page top to bottom; anyone who needs every
detail will ask an agent to dig. So:

- Write around end-user outcomes and realistic examples, not as a reference
  manual. Lead with what a feature makes possible, then the command.
- One job per page. Shape: `# Title` → one or two sentences on what it gets you
  → a working example → a few sentences. Rules, edge cases, and internals go
  in a lower `##` section of the same page, or in a separate sibling page
  (e.g. `sandboxing/network.md` keeps "Allowlist Syntax" and "How the Proxy
  Works" at the bottom). Aim for 20–100 lines per page.
- Don't repeat content across pages; link to the page that owns it.
- When editing, look for existing capabilities that are easy to undersell and
  make them discoverable.

### Structure

The docs follow a sidebar-of-small-pages layout (like hunk.dev/docs):

- Each topic is a section folder, `docs/<section>/`, with an `_index.md`
  overview (what it is plus the most common command) and one small page per
  task. Standalone topics without subpages stay as `docs/<name>.md`
  (`installation.md`, `acp.md`, `herdr.md`, ...).
- Every page starts with front matter used only by the Hugo site, then a
  normal H1 for GitHub readers:

  ```text
  ---
  title: "Network access"     # sidebar label; keep it short
  weight: 20                  # order within the section (10, 20, ...)
  description: "One line for the section's page cards."
  ---

  # Network Access
  ```

  `_index.md` pages need `title` and `description`, but no `weight`.
- Sidebar groups and their order live in `site/data/docs_groups.yaml`. A new
  section or standalone page must be added there; pages inside a section are
  picked up automatically, ordered by `weight`. An unknown path fails the
  Hugo build. The same order drives the prev/next links.
- Cross-link with relative `.md` paths (`../sandboxing/escalation.md`,
  `../profiles/_index.md`) so links work on GitHub; the site's link render
  hook rewrites them. Never link to site URLs from `docs/`.
- When adding a user-facing area, also update the README's documentation list
  and, if it's a common task, the list on `site/content/docs/_index.md`.
- Install commands appear in three places: `README.md`, `docs/installation.md`,
  and the install tabs partial `site/layouts/partials/install-tabs.html` (used
  by the home page and, via the `install-tabs` shortcode, the docs overview).
  Change them together. Use tabs only in site-only files, because GitHub
  renders `docs/` and the README as plain Markdown.
- Docs layout lives in `site/layouts/docs/` and
  `site/layouts/partials/docs-*.html`, and its styles in the "Docs" block at the
  end of `site/assets/css/site.css`. The global CSS reset zeroes heading and
  `pre` margins, and `.prose` centers itself with auto margins, so docs-specific
  spacing and grid fixes belong in the `.docs-*` rules. Check desktop and
  narrow (≤500px) widths after layout changes.

### Verification

After changing `README.md`, `AGENTS.md`, `docs/**/*.md`, or Hugo site Markdown,
run `make doccheck`. It compiles Go examples, formats extracted Go, checks shell
syntax, validates CLI flags, make targets, tool names, environment variables,
and config JSON against the implementation, parses documented skills and
subagents, builds Hugo, and checks links and anchors.

- Use a runnable fence such as `go`, `sh`, or `json` only when the example is
  valid against the current implementation. Use `text` for output samples,
  front matter, and aspirational commands in plans.
- Extracted blocks are named from the path and fence position:
  `docs/sandboxing/network.md` → `docs_sandboxing_network__bNN`, and
  `docs/config/_index.md` → `docs_config__index__bNN`. A page's first fence is
  `b02`.
- Full Go programs are compile-checked automatically. Go fragments need a
  wrapper entry in `KINDS` in `.doccheck/gocompile.py`; unregistered fragments
  are silently skipped. Moving or inserting fences renumbers blocks, so
  re-check those entries (see `.doccheck/README.md`).
- Every `markdown` fence is checked as AGENTS.md content unless it comes from
  `docs/customization/skills.md` (SKILL.md) or `subagents.md`. Don't add
  `markdown` fences elsewhere.
- JSON that isn't caretz config (for example Zed's `settings.json` in
  `docs/acp.md`) must be listed as a third-party block in the `bin-cfgcheck`
  invocation in `.doccheck/run_all.sh`.
- Before stating what a feature does, confirm it in the code or with
  `caretz <cmd> --help`; don't document behavior from memory.

## Architecture

The default binary entrypoint lives in `cmd/caretz` and delegates to `internal/app`.
The reusable agent implementation lives under `internal/*`, with shared runtime
types under `lib/*`.

1. **`cmd/caretz`** — tiny process wrapper that calls `internal/app.Run`.
2. **`internal/app`** — parses flags/env, builds runtime dependencies, starts prompt mode, TUI mode, session listing, and session explorer.
3. **`internal/agent`** — the agent loop. Sends messages to the LLM, handles tool calls, persists session/context events, and compacts context.
4. **`internal/goal`** — the `/goal` engine: an event-sourced state machine whose events in `events.jsonpl` are the only goal state (`state.md` is a derived view), with three roles (planner, worker, verifier) and one token/time budget. `internal/slashcmd/goal*.go` adapts it to sessions, subagents and the sandbox.
5. **`internal/tools`** — built-in tools: local file tools, sandboxed Bash, search/fetch, local image analysis, and file sending.
6. **`internal/sandbox`** — OS-specific sandbox runners and policy handling.

## Key Constraints

- Use the sandbox and path safety helpers in `internal/tools`/`internal/sandbox` rather than adding direct file or command execution paths.
- Keep macOS and Linux sandbox tests at parity. Behavior both backends must
  share goes in `internal/sandbox/conformance_test.go`, one test body that runs
  on both OSes; record intentional differences in `sandboxBehavior`, never as
  `runtime.GOOS` branches. Every test in a `*_darwin_test.go` or
  `*_linux_test.go` file needs a `// parity: <capability>` tag with a
  counterpart on the other OS (or an OS-exclusive entry with a reason in
  `sandboxCapabilities`); `TestSandboxTestParity` enforces this from either OS.
  Run `make test-sandbox-darwin` and `make test-sandbox-linux` after sandbox
  changes.
- no commits made without approval.
- Commits are authored as the user only; do not add `Co-Authored-By` or other
  agent attribution trailers to commit messages.
- Edit files only with the agent's file edit/write tools so each change can be
  reviewed and approved. Never modify files through shell or scripting
  (`python`, `sed -i`, `perl -i`, heredoc redirection, etc.).

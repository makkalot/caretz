# .doccheck — keep the docs honest

The docs are full of runnable things: shell commands, Go snippets, JSON config
files, skill frontmatter, make targets, env vars, links. None of that is checked
by `go test`, so it rots silently — a flag gets renamed, a config key disappears,
and the docs keep teaching it for months.

`.doccheck` is a self-contained verification framework that treats every code
example in `README.md`, `AGENTS.md`, `docs/**/*.md` (section folders included),
and the Hugo site markdown as
a test. Nothing is hand-maintained: expected flags, config schema, parsers, and
prompts are all taken from the **real code and binaries**, so doc drift fails
loudly instead of shipping.

## Usage

```sh
# everything: builds ./bin/caretz first (stage 4 needs it), then all 8 stages
make doccheck

# run the suite directly (build ./bin/caretz first)
.doccheck/run_all.sh

# pin a specific Go toolchain (default: go on PATH)
GO=/path/to/go .doccheck/run_all.sh

# run a single stage
python3 .doccheck/extract.py          # re-extract blocks from markdown
python3 .doccheck/gocompile.py        # compile-check Go examples
python3 .doccheck/cli_check.py        # check caretz commands/flags/targets
python3 .doccheck/links.py            # check relative links + anchors
go run ./.doccheck/cfgcheck .doccheck/blocks   # check JSON config examples
go run ./.doccheck/rescheck .doccheck/blocks   # check skill/subagent examples
```

Requirements: `python3`, a Go toolchain on PATH (or via `GO`), `hugo` for
stage 8. CI runs this as a separate `doccheck` job in
`.github/workflows/ci.yml`, outside the main `make ci` pipeline.

## What gets checked

`run_all.sh` runs eight stages; any stage can also be run standalone:

| # | Stage | How it validates |
|---|-------|------------------|
| 1 | Go examples compile | Full `package main` programs compile as-is; fragments are wrapped in a minimal legal host and type-checked against the real caretz packages. |
| 2 | gofmt | Extracted `.go` blocks must be gofmt-clean. |
| 3 | Shell syntax | Extracted `.sh` blocks pass `bash -n`. |
| 4 | CLI vocabulary | Every `caretz ...` command, flag, subcommand action, `--tools` value, make target, and env var is checked against `bin/caretz --help`, flag registrations in `internal/app/main.go`, the Makefile, tool `Name:` literals, and env vars regexed from source. |
| 5 | JSON config examples | Decoded into the real `config.Config` with unknown fields rejected, then run through the actual `config.Load()` + `Resolve()` startup path. |
| 6 | Skill / subagent / AGENTS.md examples | Parsed with the real runtime parsers (`subagents.Parse`, `skills.ParseFrontmatter`, ...); the documented skill layout must actually be discovered and surface in the built system prompt. |
| 7 | Links & anchors | Relative links and GitHub-style heading anchors across all markdown; Hugo page-relative links resolve against generated `site/public`. |
| 8 | Hugo build | `hugo --minify` must succeed. |

## How it works

**Extraction.** `extract.py` scans every fenced code block in the doc sources
and writes each one to `blocks/` under a name that encodes its origin:

```
docs_sandboxing_network__b04.sh  →  docs/sandboxing/network.md, 4th fenced block, ```sh
docs_config__index__b02.json     →  docs/config/_index.md, 2nd block
README__b05.go                   →  README.md, 5th block
```

It also writes `inventory.json` (source file, line number, language, block
index for every block), which is how failures get reported back at exact doc
locations. Everything downstream keys off these filenames.

**Ground truth from real code.** Each stage derives its expectations from the
codebase itself rather than a fixture list:

- `gocompile.py` builds each Go block in a scratch module (`gomod/<name>/`)
  whose `go.mod` `replace`s the caretz module with a relative path back to this
  repo — so a doc example that references a renamed field or a removed method
  fails to compile.
  Fragments (a struct literal, a statement block) are wrapped per a registry:
  `clientopts` embeds the code as `caretz.ClientOptions{...}` fields;
  `client_stmts` / `session_stmts` / `audit_stmts` embed it as a function body
  with `client` / `session` / event types pre-declared in scope.
- `cfgcheck/` (a tiny Go program built against `internal/config`) relies on
  `DisallowUnknownFields` — the only way to catch invented config keys, which
  Go's decoder would otherwise silently ignore — then exercises the same
  `Load()`/`Resolve()` path the CLI runs at startup.
- `rescheck/` (built against `internal/skills` + `internal/subagents`) runs the
  documented frontmatter through the real parsers and asserts the documented
  behavior (permission denies, model/reasoning/temperature fields, skill
  discovery, system-prompt content) actually happens.
- `cli_check.py` harvests root flags from live `--help` output, subcommand
  flags from source, make targets from the Makefile, and tool names / env vars
  from the tree — then lint-checks every command example in shell blocks and
  inline `` `caretz ...` `` prose spans against that vocabulary.

**Where output goes.** `blocks/`, `inventory.json`, `gomod/`, `gocache/`,
`bin-cfgcheck`, and `bin-rescheck` are all generated artifacts — safe to
delete; the next run recreates them.

## Adding new doc examples

What happens depends on the block's language:

| Block | On next run |
|---|---|
| `.sh` / `bash` | Checked automatically (syntax + CLI vocabulary). No registration. |
| `.go`, full program (starts with `package`) | Compiled automatically. |
| `.go`, fragment | Skipped with a loud `SKIP (no wrapper registered)` — add its block name to `KINDS` in `gocompile.py`, with a new `WRAPPERS` entry if it's a new fragment shape. |
| `.json`, caretz config | Checked automatically against the real schema. |
| `.json`, third-party schema | Fails until whitelisted in the `bin-cfgcheck` invocation in `run_all.sh`. |
| `.yaml` / `.markdown` | Handled by name-pinned cases in `rescheck/main.go` — add a matching case there, otherwise it fails. |
| `text` and others | Extracted only, not checked. |

**Block numbers are positional.** `b04` means "4th fenced block in that file".
Insert a block in the middle of a doc and every later block in that file is
renumbered — which breaks the hardcoded names in `KINDS`, `rescheck/main.go`,
and the external-JSON whitelist. Prefer appending new blocks at the end of a
file, or update the registries in the same change.

## Known quirks

- **Most stages run through `tail`** — but `run_all.sh` sets `pipefail`, so a
  checker failure still fails the whole run. Standalone invocations print the
  full checker output; the run only shows the last lines plus the `rc`.
- **Stage 4 needs `bin/caretz`.** `make doccheck` handles this automatically;
  when invoking `run_all.sh` directly, build first (`make build`) or the stage
  errors while probing `--help`.
- **Go examples and `extract.py` regenerate on every run.** If you edit a file
  under `blocks/` or `gomod/` by hand, the next run overwrites it.
- **`gocompile.sh`, `flags.py`, `fmtprobe.*`, and some `gomod/` dirs are
  leftovers** from earlier iterations (note the `docs_extending_cli__b03`
  underscore-naming vs the current dash-naming). The Python pipeline plus the
  two Go checkers are the current implementation.

## Layout

```
run_all.sh          entry point: runs all 8 stages, exits 1 on failure (pipefail set)
extract.py          markdown → blocks/ + inventory.json
gocompile.py        stage 1: compile Go examples (fragment wrappers + KINDS registry)
cli_check.py        stage 4: CLI/make/tool/env vocabulary vs real sources
links.py            stage 7: relative links + anchors (+ Hugo relURL refs)
cfgcheck/main.go    stage 5 checker, built to bin-cfgcheck against internal/config
rescheck/main.go    stage 6 checker, built to bin-rescheck against internal/skills+subagents
gomod/go.mod        scratch module; replace path points at repo root (committed)
blocks/             extracted code blocks (generated)
inventory.json      block provenance: file, line, lang, index (generated)
gomod/<name>/       scratch modules for Go example compilation (generated)
gocache/            GOCACHE for the above (generated)
```

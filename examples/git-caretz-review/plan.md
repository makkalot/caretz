# Plan: git-caretz-review

## Goal

A `git-caretz-review` binary. When it's on `PATH`, `git caretz-review [base] [caretz flags...]`
runs a read-only, single-shot code review of `base...HEAD` and prints markdown
findings to stdout. It demonstrates `cli.Run` with custom tools.

## Rules (apply to every example)

- Import only public caretz packages: `github.com/makkalot/caretz/cli`,
  `github.com/makkalot/caretz/lib/tools`, `github.com/makkalot/caretz/lib/llm`.
  No `internal/*`.
- Standard library only; no third-party modules.
- Own module: `examples/git-caretz-review/go.mod`, module
  `github.com/makkalot/caretz/examples/git-caretz-review`, with
  `require github.com/makkalot/caretz v0.0.0` and
  `replace github.com/makkalot/caretz => ../..`. Use the same `go` version as the
  root `go.mod` (currently 1.25.8).
- Edit files only with file edit tools. No commits without approval.

## Public API Used

- `cli.Run(ctx, args, stdout, stderr, cli.Options) int` — `cli/cli.go:69`
- `cli.Options{Tools, SystemPromptSuffix}` — `cli/cli.go:24`
- `tools.Tool{Definition, Handler}` — `lib/tools/tools.go:7` (alias of the
  internal struct: `Definition llm.ToolDefinition`, `Handler ToolHandler`)
- `tools.IsPathSafe(cwd, path)` — `lib/tools/tools.go:18`
- `llm.ToolDefinition`, `llm.ToolCall` — `lib/llm`
- Reference pattern: `docs/extending-cli/_index.md` (DeployStatus tool)

## Files

```text
examples/git-caretz-review/
  go.mod
  main.go        arg split, prompt build, cli.Run
  gittools.go    GitDiff and GitLog tools + runGit helper
  prompt.go      review rubric (SystemPromptSuffix) and prompt template
  main_test.go   arg split + tool tests against a temp git repo
  README.md
```

## Steps

1. **Argument split** (`splitArgs(args []string) (base string, rest []string)`):
   - If the first arg doesn't start with `-`, it's `base`. Default `main`; fall back to
     `master` if `git rev-parse --verify main` fails.
   - Reject caretz args the example owns: `-p`, `-prompt-file`, `-s`, `-adapter`,
     and subcommands (`session`, `acp`, ...). Print a clear error and exit 2.
     Everything else (e.g. `-model`, `-v`, `-workspace`) passes through.
   - Show its own `-h` usage before delegating.
2. **Preflight** (read-only git, fixed argv via `exec.CommandContext("git", ...)`):
   - `git rev-parse --show-toplevel` → repo root; `os.Chdir` there so the caretz
     workspace is the repo.
   - `git merge-base <base> HEAD`. If it fails, print "unknown base" and exit 2.
   - `git diff --stat <base>...HEAD`. If it's empty, print "nothing to review" and exit 0.
3. **Tools** (`gittools.go`):
   - `runGit(ctx, root string, args ...string) (string, error)`: fixed `git`
     binary, `cmd.Dir = root`, cap output at ~200 KB with a truncation note, and
     set `GIT_PAGER=cat` and `GIT_OPTIONAL_LOCKS=0` (so no index lock or write).
   - `GitDiff`: params `{ "path": string (optional) }`. Validate `path` with
     `tools.IsPathSafe(root, path)` and reject values that start with `-`. Runs
     `git diff --no-color --no-ext-diff <mergeBase>..HEAD -- <path>`. `base`/`mergeBase`
     come from closure state, not from the model.
   - `GitLog`: no params. Runs `git log --no-color --format='%h %s%n%b' <mergeBase>..HEAD`.
   - Both tool definitions use a JSON-schema `Parameters` map like the DeployStatus doc example.
4. **Prompt** (`prompt.go`):
   - `SystemPromptSuffix`: reviewer persona, rubric (correctness > security >
     data loss > tests; skip style), output format: `## Findings` numbered as
     `[high|medium|low] path:line — problem. Fix: ...`, then `## Looks good`,
     and "If nothing is wrong, say so". Tell it to use GitDiff per file and Read
     for surrounding context.
   - `-p` text: base name, the `--stat` output, and "Review this branch."
5. **Run**: build final args
   `append([]string{"-p", prompt, "-plan", "-incognito",
   "-tools", "Read,Grep,Find,Ls,GitDiff,GitLog"}, rest...)` and call
   `os.Exit(cli.Run(ctx, final, os.Stdout, os.Stderr, cli.Options{Tools: ..., SystemPromptSuffix: ...}))`.
   - Before writing this, confirm with `caretz --help` that `-plan` disables mutation
     tools and makes the Bash sandbox read-only, and that `-tools` accepts custom tool
     names. Bash is left out on purpose; the git tools cover what it would be used for.
   - `-incognito` keeps review sessions out of the user's session list. Make it
     overridable with a `--keep-session` flag owned by the example.
6. **Optional `--markdown-out FILE`** (owned flag): `cli.Run` writes to an
   `io.Writer`, so tee stdout into a buffer and write the file afterwards, outside the
   repo by default (e.g. `$TMPDIR`). Never post to GitHub; the user does that.
7. **Tests** (`main_test.go`): `splitArgs` table test. In a `t.TempDir()` git repo
   with two commits, `GitDiff`/`GitLog` return the expected text, and `path`
   values like `../x`, `-p`, `/etc/passwd` are rejected. No LLM calls in tests.

## API Gaps / Open Questions

- None blocking. `cli.Run` owns stdout, so to capture output the example tees it
  (step 6). Confirm that prompt-mode output goes only to the `stdout` writer passed in.
- Check whether custom tools in `-tools` are matched case-sensitively.

## Verification

- `cd examples/git-caretz-review && go build ./... && go vet ./... && go test ./...`
- `gofmt -l .` is empty.
- Manual: `go build -o ~/bin/git-caretz-review . && cd <this repo> && git caretz-review main -v`
  against a local or configured model. The review cites real changed files, and
  `git status --porcelain` is identical before and after.
- `git caretz-review does-not-exist` exits 2 with a clear message.

## Follow-ups (not part of this example)

`make examples` target, CI build job, `docs/examples.md` page (+
`site/data/docs_groups.yaml`, README docs list), `make doccheck`.

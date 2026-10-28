# git caretz-review

Get a code review of your branch before you open a pull request, without
leaving the terminal. Put the binary on your `PATH` and git picks it up as a
subcommand.

From the example directory, build, test, and install to `~/bin`:

```sh
cd examples/git-caretz-review
make build
make test
make install
PATH="$HOME/bin:$PATH" git caretz-review main
```

The build produces `bin/git-caretz-review`. Override the install location with
`make install INSTALL_DIR=/your/bin`. Tests make no model calls.
Configure a provider with `caretz setup` before running a review.

```text
$ git caretz-review main
Reviewing 4 commits, 7 files changed against main...

## Findings
1. [high] internal/agent/compaction.go:212 — summary tokens can go negative
   when keepRecent > contextWindow; the clamp runs after the subtraction.
2. [medium] cli/cli.go:131 — NewAdapterSession ignores an empty channel ID
   and opens a shared session.

## Looks good
- New tests cover the retry path.
```

It runs the regular caretz CLI in prompt mode. Your caretz config, provider, and
credentials apply as usual, and you can still add caretz flags, for example
`git caretz-review main --model qwen-code -v`.
The default base is `main`, falling back to `master` when `main` cannot be
resolved. Use `--workspace DIR` to select another repository, and `--keep-session`
to save the review in caretz's session history. Otherwise the session stays in
memory. `--help` works outside a Git repository.

## Approach

This example shows **`cli.Run`**: build your own executable on top of the full
caretz CLI and add only what's specific to your use.

- `main.go` reads its own arguments (the base branch, default `main`) and
  passes caretz flags to `cli.Run`. Prompt, session, plan, tool allowlist, and
  task flags are reserved so the review stays read-only.
- It turns them into a prompt-mode run: `-p "<review instructions>"`,
  `-plan` (file mutation tools off, Bash sandbox read-only), and a
  `-tools` allowlist of read-only tools plus its own.
- `cli.Options.Tools` adds two read-only tools:
  - `GitDiff{path?}`: the diff against the base, for the whole branch or one
    file, so the model can request diffs one file at a time instead of
    loading everything into context.
  - `GitLog`: the commit list for the branch, so the review knows the intent.
- `cli.Options.SystemPromptSuffix` sets the review rubric: correctness bugs
  first, each finding as `[severity] file:line — problem`, and no style nits.

## Review scope and safety

The tools review committed changes from the merge base to a pinned `HEAD`.
Uncommitted and untracked changes are excluded from GitDiff; the regular file
tools still see the working tree. An empty committed diff exits without calling
the model. Findings are written to stdout; startup progress goes to stderr.

`-plan` removes Write and Edit; the allowlist also excludes Bash and all other
execution tools. The two Git tools use fixed argv without a shell, disable
external diff/text conversion commands, treat paths literally, and check paths
with `tools.IsPathSafe`. Each Git result is capped at 200 KiB with an explicit
truncation notice. The reviewer cannot run tests.

`--keep-session` lets caretz write session state as usual; it does not enable
repository editing tools. No review is posted to GitHub.

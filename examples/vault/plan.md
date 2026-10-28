# Plan: vault

## Implementation status

Implemented with public APIs. See [README.md](README.md) for commands and
verified limitations. The design below is retained as background; these findings
supersede its open questions:

- `BuildRuntime` accepts `--plan`; ask forces it on.
- Serve channels include a hash of the canonical vault path, and serve forces
  persistent sessions. Cadence changes require removal and registration.
- Weekly reviews use a daily schedule with a weekday guard; demo uses one-minute
  loops for both jobs. Current dates are determined at execution time.
- The CLI/config API has no arbitrary hidden-path setting. `.obsidian/` avoidance
  is a prompt convention, and the sample config does not claim to hide it.
- The sandbox requirement in this example is intentional; the scheduler's
  `ErrNeedsSandbox` applies specifically to command schedules.
- `lib/schedules` now exposes integration factories publicly. The proposed
  `Session.InstallIntegration` method is still absent on the public session.

## Goal

A `vault` binary with three subcommands (`capture`, `ask`, `serve`) that acts as
a personal assistant over a folder of Markdown notes. `serve` keeps a session
open with two schedules: a daily plan and a weekly review. It demonstrates
`cli.BuildRuntime`, adapter sessions, and caretz's built-in schedules, all
through the public API.

## Rules (apply to every example)

- Import only public caretz packages: `github.com/makkalot/caretz` (for the
  `*caretz.Session` type), `.../cli`, `.../lib/eventing`, `.../lib/types`.
  No `internal/*`, and no `Session.AgentSession()`.
- Standard library only.
- Own module `github.com/makkalot/caretz/examples/vault`, with
  `replace github.com/makkalot/caretz => ../..`, using the root `go.mod`'s go version.
- The example never writes notes itself; the agent does, through caretz's file tools.
  The only files the example writes are under `testdata/` (checked in).
- Edit files only with file edit tools. No commits without approval.

## Why `cli.BuildRuntime` and not `caretz.NewClient`

Schedules are installed on adapter sessions by the runtime
(`internal/app/runtime.go:228`, inside `NewAdapterSession`). With
`caretz.NewClient`, the only way to install them is
`AgentSession().InstallIntegration(...)`, which isn't public. So this example uses
`cli.BuildRuntime` + `rt.NewAdapterSession`, which also gives it the user's caretz
config and credentials for free. (API gap, see below.)

## Public API Used

- `cli.BuildRuntime(ctx, args, stderr, cli.Options{SystemPromptSuffix})` → `*cli.Runtime, code` — `cli/cli.go:73`
- `rt.Context()`, `rt.CWD()`, `rt.NewAdapterSession(adapter, channelID, cli.AdapterSessionOptions{WorkspaceDir})` — `cli/cli.go:99-137`
- `eventing.NewLogMessage(originatorID, text)` + `session.AppendAuditEvent(e)` +
  `session.ProcessUserEvent(ctx, e)` — the input pattern from
  `docs/extending-cli/custom-adapter.md` (`lib/eventing/event_types.go:243`, `caretz.go:473,515`)
- `session.SubscribeContextEvents(eventing.LLMAgentResponseCreated, ...)` — `caretz.go:501`
- `session.WorkspaceDir()`, `session.Close()`
- Scheduler tools the agent calls: `RegisterSchedule`, `ListSchedules`,
  `UpdateSchedule`, `RemoveSchedule` (`internal/schedules/integration.go:95-194`,
  read for reference only). User docs: `docs/schedules/`.

## Files

```text
examples/vault/
  go.mod
  main.go            subcommand dispatch, BuildRuntime, open session
  commands.go        capture / ask / serve
  prompt.go          SystemPromptSuffix (vault conventions) + per-command prompt templates
  commands_test.go   arg parsing + prompt template tests (no LLM)
  testdata/vault/    sample vault (see step 6)
  README.md
```

## Steps

1. **Arg handling** (`main.go`): `vault <capture|ask|serve> [caretz flags] [text...]`.
   - Take the subcommand first. Split the rest into caretz flags (args starting with
     `-` and their values) and free text. Simplest rule: free text must come after a
     `--`, or be the last arg, as documented in usage. Pass the flags to
     `cli.BuildRuntime(ctx, flags, os.Stderr, cli.Options{SystemPromptSuffix: vaultConventions})`.
   - If `-workspace` is missing, use the caretz default (cwd). Print a warning if the
     workspace has no `.md` files at all.
   - Confirm `BuildRuntime` accepts `-workspace`, `-model`, `-config-root`, `-v`, `-incognito`
     (`docs/extending-cli/custom-adapter.md` says prompt-mode/session flags are not accepted).
2. **Vault conventions** (`prompt.go`, `SystemPromptSuffix`):
   daily notes are at `Daily/YYYY-MM-DD.md` (create them with an `# YYYY-MM-DD` heading);
   todos are `- [ ] text #todo`; link related notes with `[[Note Name]]` when one clearly
   matches; never delete or rewrite existing user text, only append or add sections; never
   touch `.obsidian/`; reviews are at `Reviews/YYYY-Www.md`; cite paths when answering.
   Include today's date and weekday in each prompt. Don't rely on the model knowing them.
3. **capture**: channel `capture-<unix-nanos>` (one-off). Prompt: "Capture this into
   today's daily note following the vault conventions: <text>". Send it with the
   `NewLogMessage` → `AppendAuditEvent` → `ProcessUserEvent` pattern, print
   `response.Content`, then `Close()`.
4. **ask**: same as capture, with the prompt "Answer from the notes in this vault only.
   Search with Grep/Find, read the relevant notes, cite paths. If the notes don't say,
   say so. Question: <text>". Read-only in spirit: tell it not to edit. Check whether
   `-plan` can be passed through `BuildRuntime` to enforce this; if it can, add it for `ask`.
5. **serve**: fixed channel `serve` so the session (and its schedules) is resumed each start.
   - Open the session and subscribe to `LLMAgentResponseCreated` (check that scheduled-run
     summaries arrive on the context stream; see `docs/schedules/_index.md` "reports a
     short summary ... in your conversation"). Print each one with a timestamp.
   - On first start, send a setup prompt: "Make sure exactly these schedules exist
     (use ListSchedules, then RegisterSchedule/UpdateSchedule): (1) every day at 08:00 —
     read open #todo items and today's note, write a `## Plan` section into today's
     daily note; (2) every Sunday at 18:00 — summarize the week's daily notes into
     Reviews/YYYY-Www.md with Done / Still open / Themes." Running it every time is
     fine; it's idempotent because of ListSchedules.
   - Owned flags: `--daily HH:MM`, `--weekly "Sun HH:MM"` to change the times, and
     `--demo` that registers a 1-minute loop instead (for testing). Check the
     `RegisterSchedule` parameters (cadence/time format, `MinInterval` in `lib/schedules`)
     and how weekly cadences are expressed. If there's no weekly cadence, use a
     daily schedule that does nothing on days other than Sunday and record that under API gaps.
   - Block until SIGINT, then `session.Close()`.
   - Schedules need the sandbox (`schedules.ErrNeedsSandbox`). Print a clear error if
     the user runs with the sandbox off.
6. **Sample vault** (`testdata/vault/`): `Daily/` with 3 notes from the past
   week containing a few `#todo` items, `Home/Kitchen.md`, `Writing ideas.md`,
   `Inbox.md`, and an `.obsidian/app.json` stub (to prove it's ignored). There's a
   `.caretz/config.json` with the recommended sandbox settings: hide `.obsidian`, no
   network. Check the keys against `docs/config/` and `docs/sandboxing/` and validate
   with `make doccheck`'s cfgcheck if referenced from docs.
7. **Tests**: arg split table test; prompt templates include the date and the text;
   the setup prompt mentions both schedules. No LLM calls.

## API Gaps / Open Questions

- **Schedules on `caretz.NewClient` sessions.** There's no public way to install
  the scheduler (only through internal `AgentSession().InstallIntegration`).
  Suggested follow-up: `func (s *Session) InstallIntegration(f sessiontools.Factory) error`
  on the public `Session`, plus a public way to build the schedules factory (e.g.
  `schedules.NewIntegrationWithDefaults` returning a `sessiontools.Factory`, which
  needs checking). With that, this example could use `NewClient` directly.
- **Weekly cadence.** Confirm that `RegisterSchedule` supports day-of-week (step 5).
- **Concurrent writes.** `capture` while `serve` is running uses a different session on
  the same files. That's acceptable for a demo; note it in the README if it causes problems.

## Verification

- `cd examples/vault && go build ./... && go vet ./... && go test ./...`; `gofmt -l .` is empty.
- Manual, on a copy of the sample vault (`cp -r testdata/vault /tmp/v`):
  - `vault capture -workspace /tmp/v -- "buy milk"` → today's daily note has `- [ ] Buy milk #todo`.
  - `vault ask -workspace /tmp/v -- "what worktop did I pick?"` → answers "oak" and cites `Home/Kitchen.md`.
  - `vault serve -workspace /tmp/v --demo` → within ~2 min a scheduled summary prints
    and the daily note gets a `## Plan`. Restart `serve` → `ListSchedules` shows no
    duplicates.
  - Ask it to write `/tmp/outside.md` → blocked. `.obsidian/app.json` is unchanged.
- The original `testdata/vault` is never modified by the manual run.

## Follow-ups (not part of this example)

`make examples`, CI build job, `docs/examples.md` (+ `site/data/docs_groups.yaml`,
README docs list), `make doccheck`, and the public `Session.InstallIntegration` API.

# Let scheduled prompt runs stop blocking the chat

## Context

A scheduled `prompt` job runs as a background subagent. `executeAsyncEvent`
(`internal/agent/async.go:227`) wraps the whole run in
`withExecutionContext`, which holds the parent session's `executionMu` — the
same lock a user turn takes. In Telegram, a message sent while a scheduled
prompt runs waits until the child agent finishes (up to `RunTimeout` = 15 min).

The child does not need that lock: it is its own `AgentSession` with its own
context. Only the final "Background task completed … report to the user" turn
writes into the parent's conversation. `runAsyncCommand`
(`internal/agent/async.go:259`) already follows that shape: work unlocked,
then `finishOwnedWork(ctx)`, then a short locked report turn.

Two more things serialize background work today:
- The child shares the parent's persistent sandbox shell
  (`NewChildSessionWithOptions` → `ShareSessionExecutionFrom`,
  `internal/agent/agent.go:743`). `sandbox.Session.Run` holds its mutex for a
  whole command (`internal/sandbox/session.go:188`), and cwd/env changes leak
  between the child and the user's turn.
- The per-session executor goroutine (`startAsyncExecutor`,
  `internal/agent/async.go:143`) calls `executeAsyncEvent` inline, so one long
  prompt run delays every other schedule (including command schedules) in the
  same chat.

## Changes (caretz, `internal/agent/async.go`)

1. **Run the child unlocked, lock only the report turn.** In
   `executeAsyncEvent`, call `a.runAsyncSubagent(ctx, request)` directly for
   `AsyncRunSubagent` (no `withExecutionContext`), like the command case.
   Inside `runAsyncSubagent`, after the child finishes and the
   `SubagentInvocationCompleted` audit event is appended, do what
   `runAsyncCommand` does:
   `finishOwnedWork(ctx)` then
   `a.withExecutionContext(a.lifecycleCtx, func() error { _, err := a.callUntilStopUnlocked(a.lifecycleCtx, report, nil); return err })`.
   Foreground mode keeps the lock for its whole run.

2. **Give the background child its own shell.** Pass
   `IsolatedExecution: true` in the `SubagentChildOptions` built in
   `runAsyncSubagent` (the goal worker precedent, `internal/slashcmd/goal.go:545`).
   `runSessionBash` refreshes filesystem/network grants from the registry on
   every command (`internal/tools/session_exec.go:49-52`), so runtime
   `/add-path` and `/add-net` grants still apply — confirm in a test.

3. **Don't let one run hold up the session's executor.** In
   `startAsyncExecutor`, start each `executeAsyncEvent` in its own goroutine
   tracked by `a.asyncWG` (`a.asyncWG.Add(1)` before, `Done` in the
   goroutine). The global `asyncRunCapacity` (4) still bounds concurrency, and
   `Close` already waits on `asyncWG`. The scheduler's own "previous
   occurrence still running" skip keeps one run per job.

Reads of parent state during the unlocked child run (`a.systemPrompt`,
`ToolRegistry()`, subagent model resolver, audit append/publish) are already
used from the async goroutine today or are store-level safe; check
`systemPrompt`/`toolRegistry` mutation sites (`/clear`, model switch,
`InstallIntegration`) for races and read them once under
`integrationMu`/existing accessors if needed. `go test -race` will tell.

## Tests (`internal/agent`, next to existing async tests)

- A user turn (`ProcessUserEvent`) completes while a background subagent run
  is blocked mid-LLM-call (fake provider that waits on a channel); the report
  turn runs after the user turn and the parent context ends with both, in
  order.
- Two async requests in one session run concurrently (second starts before
  the first's fake LLM call is released).
- The background child's Bash runs in a separate sandbox session from the
  parent's (cwd change in child not visible to parent), and a filesystem root
  granted on the parent after the child starts is honoured by the child.
- Cancelling the turn scope (`turnscope`) still cancels an owned subagent run.

## Verification

- `go test -race ./internal/agent/... ./internal/schedules/... ./internal/app/... -count=1`
- `make test` and `make lint` in caretz.
- caretplus: `go build ./... && go test ./...` (go.work already points at `../caret`).
- Manual: `make run` TUI, `/loop 1m write a haiku about the time`, type a
  message while the run is going — it answers immediately; the background
  result arrives afterwards as its own turn. Then the same over Telegram after
  `make install-systemd-remote HOST=… ADAPTERS=--telegram`.

## Notes

- Follows the debugging notation in AGENTS.md: the parent flow becomes
  `[user msg][assistant reply][background report turn]` instead of the user
  message queuing behind the whole child run; no context repair involved.
- No commits without approval.

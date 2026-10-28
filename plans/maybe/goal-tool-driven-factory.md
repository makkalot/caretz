# Plan: `Goal` tool — let the driving model run goals (software factory)

## Context

`/goal` today is human-driven: a person starts it, approves the plan, answers
questions, retries. The goal engine (`internal/goal`) is the valuable part for
a "software factory": a driver model can start, watch, retry and chain goals,
but **cannot declare work done** — only evidence commands and the independent
verifier can. This plan exposes the engine as a session tool so the main model
drives goals, in two phases:

- **Phase 1** — model-driven goals, one running at a time in the current
  workspace, non-blocking for the driver, with an evidence-approval policy and
  factory budgets.
- **Phase 2** — one git worktree + branch per goal, parallel goals, merge step.

Decisions taken: evidence approval is **configurable, default `human`**;
unattended sandbox/network approvals **pause the goal with a question**;
**phased** delivery.

The decision to make with this document: is Phase 1 worth building
(~4 work items below), and is Phase 2 in scope at all.

## Findings that shape the design

- The adapter (`internal/slashcmd/goal*.go`) takes everything from one
  `*agent.AgentSession` (`GoalEvents`, `AppendAuditEvent`, `SessionDir`,
  `TasksInScope`, `RecordCommandOutcome`, `NewSubagentChildSession`). A goal can
  therefore live in a **child session** just by passing that session in.
  `goal.Replay` keeps only the latest goal per log → one session per goal.
- **Do not reuse the async executor for the goal run.** `executeAsyncEvent`
  holds the driver's `executionMu` for the whole run
  (`internal/agent/async.go:137`) and processes requests serially; a multi-hour
  goal would block every user turn. It *is* fine for the short "goal stopped"
  notification turn.
- Background runs have no permission requester → `RequestPermission` returns
  `ErrPermissionApprovalUnavailable` (`internal/tools/permissions.go:88`). The
  engine already pauses on a blocked worker/check
  (`internal/goal/engine.go:453,522,600`). "Pause with a question" mostly exists;
  it needs the denial to be clearly reported in the question.
- Session tools must be on the hard-coded allow-list `isSessionToolName`
  (`internal/tools/tools.go:1046`). Goal workers already strip session tools
  (`WithoutSessionTools`, `goal.go:542`; `NewChildSessionWithOptions` strips
  again), so no recursion.
- The schedules integration (`internal/schedules/integration.go`) is the
  template: `sessiontools.Integration`, tool handlers `func(ctx, llm.ToolCall)
  (string, error)`, persisted state rebuilt from `AuditEvents()` in the
  constructor. It is installed only in the TUI (`internal/app/tui.go:58`), not
  in prompt mode or ACP.
- No git worktree creation exists anywhere (only sandbox grants for an existing
  linked worktree, `internal/app/runtime_config.go:899`).

## Phase 1

### 1. Move the adapter into `internal/goalrun` (no behaviour change)

Move the session-facing parts of `internal/slashcmd/goal*.go` (`goalStore`,
`goalRoles`, `goalExec`, `newGoalEngine`, `stepGoal` core, plan/review/check/
refs/discovery/compact helpers, `goalStatusMessage`, `goalExitCode`) into a new
package `internal/goalrun`:

- `goalrun.Runner{Limits, DefaultModels}` replaces the `goalLimits` /
  `goalDefaultModels` globals; `ConfigureGoal` builds it.
- `Runner.Step(ctx, session, goal.Input) (Outcome, error)` where
  `Outcome{State, Message, ExitCode, Published}`.
- `Runner.StartInput(ctx, session, text, models, auto)` builds `goal.Start`
  (references, git revision, dirty capture) — today inside `GoalCommand.Run`.
- Use `lib/progress.Emit` directly instead of slashcmd's `EmitProgress`.
- `slashcmd/goal*.go` keeps only parsing (`scanGoalOptions`, completion,
  `Result`) and calls `goalrun`.
- Move tests that exercise adapter internals (`goal_test.go` helpers such as
  `goalMockProvider`, `newGoalTestSession`) with the code; keep command-level
  tests in slashcmd.

### 2. Engine: record who acted and which evidence a human approved

In `internal/goal` (events/reduce/model):

- Add `Actor string` (`"user"` | `"driver"`) to `ApprovedData`, `EditData`,
  `AnsweredData`, `RetryData` (empty = user, so old logs replay unchanged).
- Add `State.HumanEvidence string` — fingerprint of `State.Evidence` set when a
  `user` actor approves, answers a tampered-evidence question, or edits
  evidence. Helper `goal.EvidenceFingerprint(Evidence) string`.
- Unit tests in `internal/goal/engine_test.go` using `newTestEngine`,
  `fakeRoles`, `memoryStore`.

### 3. `Goal` session integration (`internal/goalrun/integration.go`)

A `sessiontools.Integration` named `goals`, installed on the driver session,
exposing one tool `Goal` (add `"Goal"` to `isSessionToolName`):

```text
action: start   {goal, auto_approve?, planner?, worker?, verifier?} → {goal_id, stage}
        status  {goal_id}       → compact JSON (below)
        list    {}              → [{goal_id, title, stage, tokens_used}]
        approve {goal_id, edit?}            edit = plan edit only
        evidence{goal_id, edit}             evidence edit (policy-gated)
        answer  {goal_id, text}
        retry   {goal_id, reason}
        resume  {goal_id}
        stop    {goal_id}
```

Status JSON (from `goal.State`): `stage, question, paused_from, plan_summary,
evidence[{id, check, status}], tasks[{id, title, status, attempts,
last_outcome}], failures[{source, check, reason, tail}], verdict{done, reason,
gaps}, tokens_used, active_time, budget_line, retries_used, needs_human`.

Mechanics:

- **One child session per goal.** `start` creates it with
  `session.NewSubagentChildSession` (prefix `goal`), records a new audit event
  `GoalToolStarted{goal_id, child_session_id, title}` on the driver (new type in
  `lib/eventing`), and runs `Runner.Step` in a **goroutine owned by the
  integration** under the integration's lifecycle context — never under the
  driver's `executionMu`. Mutating actions on a goal that is currently running
  return "goal is running; use status or stop".
- **Notify the driver when a run stops** (awaiting_approval, awaiting_input,
  done, stalled, stopped, over_budget): publish an `AsyncRunRequested` in
  `foreground` mode with `Source: "goals"` and a short prompt ("Goal G1 stopped
  at stalled: <reason>. Use Goal status for details."). This takes
  `executionMu` only for that short turn, queued behind any user turn. The
  final summary is written to the driver instead of `RecordCommandOutcome` on
  the child.
- **Phase 1 concurrency:** at most one goal running per driver session;
  `start`/`retry`/`resume` refuse while another is running.
- **Unattended permissions:** goal runs get no permission requester (current
  behaviour → deny). Make the worker prompt and handoff carry the denied
  request so the existing blocked-task pause question names it
  ("Bash needs network access to proxy.golang.org; grant it and answer"). The
  driver may answer; the human grants access in sandbox config. Verify
  `engine.go:453/522/600` questions contain the denial text; add it where
  missing.
- **Evidence policy** `goal.tool.evidenceApproval: human|driver` (default
  `human`). Under `human`, tool calls are refused with a "needs human: run
  `/goal:approve --goal G1`" message when:
  - `approve` and `EvidenceFingerprint(state.Evidence) != state.HumanEvidence`
    (covers the first approval and any replan that changed evidence);
  - `evidence` (any evidence edit);
  - `answer` while `state.TamperedEvidence` is non-empty.
  `start` with `auto_approve` is downgraded to wait for approval under `human`.
- **Factory budgets** (`goal.tool` config): `maxRetries` per goal (default 3),
  `maxTotalTokens` across goals started in this driver session (sum of
  `TokensUsed`, refuse `start`/`retry` when exceeded). Per-goal `maxTokens` /
  `maxDuration` keep working as today.
- **Restart:** the constructor rebuilds the goal table from
  `GoalToolStarted` events in `AuditEvents()` (as schedules does). Running-stage
  goals show as `interrupted`; the driver calls `resume`. Needs a small agent
  helper `OpenChildSession(parent, childID)` that reopens an existing child
  directory with the subagent registry (today `NewChildSessionWithOptions`
  always mints a new ID).

### 4. Human access, install points and config

- `--goal ID` option on `/goal:approve`, `/goal:evidence`, `/goal:answer`,
  `/goal:retry`, `/goal:stop`, `/goal:status` in the driver session, routing to
  the goal's child session via the integration. These are `user` actor actions
  (satisfy the evidence policy). `/goal:status` without `--goal` in a driver
  session with tool goals lists them.
- Opt-in: new `goal.tool` block in `config.Goal` (`internal/config/config.go`
  ~:107): `enabled` (default false), `evidenceApproval`, `maxRetries`,
  `maxTotalTokens`. Install the integration only when enabled.
- Install in the TUI next to schedules (`internal/app/tui.go:58`) **and in
  prompt mode** (`internal/app/agent_command.go`), which is the headless
  factory entry point. In prompt mode the process must wait after the driver's
  turn until no goal is running and no goal notification is pending, then exit
  (with a timeout = sum of goal `maxDuration`). ACP: later.
- Requires internal tasks (same as `/goal`); `start` errors with `--no-tasks`.

### 5. Docs

- New page `docs/goals/driven-by-the-model.md` (weight 25): what it gets you,
  config snippet to enable, an example prompt ("Work through issues #12, #13 as
  goals; retry stalled ones once with what you learned"), the evidence policy,
  and `/goal:approve --goal`. Mention in `docs/goals/_index.md` and the Built-in
  Tools table in `docs/tools/_index.md`. Config JSON must pass `make doccheck`.

## Phase 2 (decide separately)

- `goalrun` creates `git worktree add .caretz/worktrees/<goal_id> -b
  goal/<goal_id>` from HEAD at `start`; the child session's registry uses
  `ToolRegistry().WithWorkspaceDir(worktree)` (`internal/tools/tools.go:99`,
  re-applies the sandbox); sandbox grants via existing
  `worktreeGitReadOnlyPaths`.
- Lift the one-running-goal limit to `goal.tool.maxConcurrent`.
- On `done`: leave the branch, and add a `merge {goal_id}` action (fast-forward
  or report conflicts) plus `discard`. Optional `gh pr create` later.
- Must run through the sandbox helpers (no direct exec), and keep macOS/Linux
  sandbox parity tests.

## Risks / open points

- Driver and user can still edit the shared workspace while a Phase 1 goal
  runs; the engine's tamper detection catches evidence changes but not other
  conflicts. Phase 2 fixes this; Phase 1 docs say so.
- Cost: a driver retry loop is bounded only by `maxRetries` and
  `maxTotalTokens`; defaults must be conservative.
- Prompt-mode waiting changes `-p` exit semantics when the integration is
  enabled; exit code should reflect the worst goal outcome (reuse
  `goalExitCode`).

## Verification

- `go test ./internal/goal/... ./internal/goalrun/... ./internal/slashcmd/...`
  — step 1 must pass the existing ~40 goal tests unchanged.
- New tests (mock provider `goalMockProvider`, `newGoalTestSession`):
  - `start` returns immediately; driver `CallUntilStop` is not blocked while
    the goal runs (assert a user turn completes during a goal run).
  - Driver receives one notification turn per stop, with `Source: "goals"`.
  - Evidence policy: driver `approve` refused before human approval, allowed
    after; refused again after a retry that changes evidence; `driver` policy
    allows all.
  - `maxRetries` / `maxTotalTokens` refusals; second `start` refused while one
    runs.
  - Restart: rebuild from audit events, goal shows `interrupted`, `resume`
    continues from the child's event log.
  - Denied permission in a goal worker produces an `awaiting_input` question
    naming the request.
- Manual end-to-end: enable `goal.tool`, run
  `make run PROMPT="Use the Goal tool to add a --version flag; approve plans yourself"`
  and confirm the human-approval refusal, then `/goal:approve --goal G1` in TUI,
  goal completes, driver reports.
- `make lint`, `make doccheck`.

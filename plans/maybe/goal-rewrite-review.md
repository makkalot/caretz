# /goal from scratch: what I'd do differently

## Context
You asked how I'd write `/goal` if I started over. This is a review of the current design (about 4.8k lines of non-test code in `internal/slashcmd/goal*.go`), not a change request. The core ideas are good. Most of the cost comes from **state shape, too many protocols and duplicated bookkeeping**, not from the algorithm.

## Keep (the parts that are right)
- The harness runs deterministic checks. Worker prose is never evidence (`checkGoalTask`, `runGoalEvidence`).
- A work task is never graded by a check it wrote. This is the work/check task split.
- Approval shows the exact commands (`previewGoalPlan`). Evidence is locked after approval (`goalEvidenceLocked`).
- A structured failure report drives replans, with a no-progress fingerprint (`goalFailureKey`).
- Env-blocked failures pause for the user instead of weakening checks.

## Change

### 1. One typed state machine instead of Status × Phase × string task states
- Today: `Status` (8 values) and `Phase` (discovery/planning/work/verify/replan) are two overlapping axes. Task status is a free string ("pending/running/attempted/done/failed/superseded"). `goalAwaitingInput = "paused"` shows the naming has drifted.
- Instead: one `type Stage int` enum covering Discovering → AwaitingApproval → Working → Verifying → Replanning → AwaitingInput → {Done, Stalled, Stopped, OverBudget}, typed `TaskStatus`, and one `transition(state, event) (state, effects)` function. Illegal transitions become compile/test failures instead of `requiredGoalState` checks spread across 8 commands.

### 2. Commands are inputs to the machine, not 8 separate entry points
- `goal`, `approve`, `answer`, `evidence`, `resume`, `retry`, `status` and `stop` each reload state, re-check status, reject legacy state, mutate fields by hand and pick an exit code (magic 3/4/5/6, used 12×).
- Instead: each command parses into an `Input{Approve|Edit|Answer|Resume|Retry|Stop}` and calls `engine.Step(input)`. Exit codes come from the resulting stage in one table. I'd also fold `goal:evidence --edit` and `goal:approve --edit` into one `Edit{target}` input.

### 3. One source of truth for persistence
- Today, state lives in five places: `goal.json`, the goal event log, `state.md`, `tasks/*.log`, plus a global `goalIncognitoStates` map keyed by `*AgentSession`. That map is never cleaned up, so it leaks. On top of that, goal tasks are mirrored into the session task store with two-way `reconcileGoalParentTasks`. `loadGoalState` even rewrites `goal.json` from events on read.
- Instead: pick **event-sourced** (events are authoritative, and the snapshot plus `state.md` are derived caches) **or** snapshot-only. Don't do both. Either project into the session task store one way (goal → tasks) or skip the mirror. Incognito mode should get an in-memory `Store` implementation, not a global map.

### 4. Drop the migration code until the feature ships
- `migrateGoalState`, `rejectLegacyGoalState`, `LoadedVersion` and schema v5 sit on an unmerged feature branch. Breaking the schema freely is cheaper until users have real goal state.

### 5. The harness owns plan identity; the planner sends a patch
- The "return the full list of unfinished tasks; keep IDs; use new-1…; omit done; pending_on may be self/ID/new-N" protocol creates `validatePlannedTaskHistory`, alias remapping in `applyGoalPlanContext`, title-based ID recovery (`byTitle`) and copying fields from the previous task. It's the biggest source of planner retries.
- Instead: the planner returns ops: `add{ref, …}`, `drop{id, reason}`, `revise{id, fields}`. The harness assigns IDs and resolves `ref`s, and done tasks are untouchable by construction.

### 6. A smaller check DSL: argv + timeout
- Every typed check (script/test/lint/build/file_exists/json_field/grep_absent/shell) compiles to a shell string anyway, and `key()` is that string. The types add prompt tokens, validation branches and planner mistakes (`runner "go test"`).
- Instead: `Check{Argv []string, Timeout, WritesBy taskID?}` plus at most `shell` as an explicit escape hatch. Keep validation as "argv[0] resolves" only.

### 7. Use the sandbox to enforce evidence protection, not hash-based detection
- `goalEvidenceFiles` guesses which files a check runs by looking for `/`, `.sh` or `.py` in its command. For `go test ./...` it freezes **nothing**, so the test files the worker could weaken go unprotected. The run is only caught after the fact, at verify.
- Instead: you already have Landlock/seatbelt work, so make the evidence paths read-only for the worker child's sandbox (deny-write list). Tampering becomes impossible instead of detected late. Keep the hash check only as a backstop.

### 8. Structured sandbox denials instead of regex on output
- `goalEnvBlockedOutput` (a regex for "operation not permitted", etc.) and `OutputFailure` → LLM classifier are heuristics. The sandbox layer knows when it denied something. Have `RunVerificationCommand` return `Denials []Denial` and classify from that.

### 9. Fewer model roles: 6 → 3
- Today there's discovery, planner, worker, checker-classifier, reviewer and judge, where the judge must accept or dismiss each reviewer finding. Discovery repeats what the planner, which has the same inspect tools, does anyway. Reviewer and judge are two passes over the same diff.
- Instead: **planner** (does its own discovery), **worker**, **verifier** (read-only tools; reviews and decides in one call). This matches the 3-role split already sketched in `plans/maybe/goal-per-role-models.md`.

### 10. One journal instead of seven prose channels
- `Log`, `Feedback`, `Discoveries`, `Failures`, `FailingChecks`, `Summary`, `ExitReason`, `Question`, `Answer` and `PendingEdit` each get capped, compacted (`compactGoalState`) and spliced into prompts differently.
- Instead: an append-only `[]Entry{Kind, Pass, TaskID, Text, Data}`. Each prompt renders the view it needs from it. This also gives you the event log for free (see #3).

### 11. One budget and one stall rule
- Today: `WorkerAttempts`, `goalMaxTaskAttempts`, `VerdictReplans`, `NoProgressPasses`, active time, plus "refunds" for invalid checks. Interacting caps are hard to reason about.
- Instead: a budget (tokens or $ plus wall time) and "same failure fingerprint twice → stalled". Per-task attempts become a planner hint, not a cap.

### 12. Package and test boundaries
- Move this into `internal/goal`: an engine that depends on small interfaces (`Roles` for LLM calls, `Exec` for checks, `Store`, `Clock`), with `slashcmd/goal.go` as a thin adapter. The former workbench built a fake `goalState` to reuse `applyGoalPlan`. The shared plan code should be its own type.
- Use `text/template` (or prompt structs) instead of 12-arg positional `fmt.Sprintf` prompts.
- Engine tests then become table-driven over `(state, input, fake role replies) → (stage, effects)` with no session, which should shrink `goal_test.go` a lot.

## Progress

Decisions (2026-10-04):
- `events.jsonpl` is the goal's only source of truth. `goal.json` is gone; `state.md` and `tasks/*.log` are views regenerated after every event.
- Events carry typed payloads (`Data`), and `goal.State` is a fold of them (`goal.Replay`/`Apply`). A transition table rejects illegal moves.
- The session task panel is a one-way projection of goal tasks.
- Incognito sessions use the in-memory event store; the global map is gone.
- Goals recorded by an older caretz load as `goal.ErrLegacy`; no migration.
- The review and judge are merged into one verifier (#9) in Phase C.

Phase A, done: #1, #2, #3, #4, #10, #12. `internal/goal` holds the model, events, reducer, plan application/validation, evidence freezing, views and the engine (`Engine.Step(Input)` over `Roles`, `Exec` and `Store` interfaces). `internal/slashcmd/goal*.go` adapts session, subagents and sandbox to those interfaces. Requests that need a plan (edit, answer, retry) are recorded only after the planner succeeds.

Phase B, done:
- #5: the planner returns `ops` (`add` with a ref, `revise`/`drop` by ID); `goal.ApplyPatch` assigns IDs, resolves refs, and refuses changes to closed tasks or leaving a failed task as is. The former workbench kept its full-list manifest (`goal.ReplaceTasks`).
- #6: `Check{Argv | Shell, WrittenBy, TimeoutSeconds}`; validation is `bash -n` plus `command -v argv[0]`.
- #8: `tools.VerificationResult.Denials` come from the network proxy (`NetworkBlockedError`) and from refusal output the sandbox policy confirms (a path outside the writable roots, a socket); an unsandboxed run reports none. This also fixed network-blocked checks stalling the goal instead of pausing it.

Phase C, done:
- #7: evidence files include the test files of a test runner (`go test`, pytest, jest/vitest/npm test, cargo test, make → Makefile). Work-task workers get `Registry.WithProtectedPaths` (read-only in file tools and the Bash sandbox) and their own session shell (`ChildSessionOptions.IsolatedExecution`). Verified on macOS seatbelt: append and rename-over a protected `_test.go` fail. The hash check stays as backstop.
- #9: three configurable roles: planner, worker, verifier. The planner does discovery (static scan and workbench in the first plan prompt). Verification stays two calls on the verifier model: the reviewer (read-only tools, focused on logic and implementation correctness, cited findings) and then the judge (no tools, accepts or dismisses every blocker/major finding, decides); events `Goal.Reviewed` and `Goal.Judged`. A first version merged them into one call; the separate review was restored because its focus on logic was worth the extra call. The ambiguous-output classifier is gone (a zero exit with failure text passes as suspect and is shown to reviewer and judge).
- #11: one budget, `goal.Limits{MaxTokens, MaxActiveTime}` (default 10M billable tokens, 120 min; profile `goal.maxTokens`/`goal.maxDuration`), and one stall rule (the same masked failure fingerprint twice: per task it sends the task to the planner, per pass it stalls the goal). Worker/replan/task-attempt caps and refunds are gone.

## If I had to prioritize (highest value / lowest risk first)
1. #7 sandbox-enforced evidence protection. This is a real correctness hole: `go test` evidence goes unprotected.
2. #3 single source of truth, and removing the incognito global.
3. #1 + #2 typed state machine with commands as inputs.
4. #5 patch-based planner protocol.
5. #9 / #10 / #11 role, journal and budget consolidation.
6. #12 package extraction (easiest once 1–3 are done).

## Verification (if we act on any of this)
- `go test ./internal/slashcmd/... ./internal/goal/...`
- Run an end-to-end goal in a scratch repo where evidence is `go test ./...` and the worker is told to "make the test pass". Confirm the worker's write to `_test.go` is denied by the sandbox (#7).
- Kill the process mid-worker, then `/goal:resume`. Confirm state comes back from one store only (#3).

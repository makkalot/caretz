# /goal per-role models

## Context
`/goal` runs discovery, planner, worker, judge and checker subagents, all on the session's current model. Users want a strong model for planning and judging and a cheaper or local one for implementation, or the reverse. Caretz already has the pieces:
- The former workbench supported `--model provider/model --reasoning X` (with completion in models.go/completion.go)
- `SubagentChildOptions{ModelRefOverride, ReasoningEffort}`, resolved by the session's `SubagentModelResolver` (internal/agent/subagent.go:188-240, internal/app/runtime_config.go:163)
- subagent Markdown files with `model:` / `reasoning:` frontmatter

`/goal` just never wires any of these.

## Proposed interface

**Three user-facing roles:**
- `planner`: discovery and planning
- `worker`: implementation
- `judge`: final verdict, plus the ambiguous-output checker
- `reviewer`: the review step added later, which defaults to the judge's model if unset

Five roles would be noise. Discovery belongs with planning, and the checker belongs with judging.

**1. Profile defaults** (persistent, usual case), a new `goal` block in the profile:
```json
"goal": {
  "planner": {"model": "anthropic/claude-opus-5-5", "reasoning": "high"},
  "worker":  {"model": "local/qwen3-coder"},
  "judge":   {"model": "openai/gpt-5", "reasoning": "high"}
}
```
Any role left unset uses the session's current model, which is today's behavior.

**2. Per-goal flags**, using the former workbench's `--model/--reasoning` style, with completion from the configured models:
```
/goal --planner-model REF --worker-model REF --judge-model REF \
      [--planner-reasoning X] [--worker-reasoning X] [--judge-reasoning X] [--yes] CONDITION
```
- Resolution order: flag, then profile `goal.<role>`, then the session model.
- `--model REF` is a shorthand that sets all three roles.

**3. The choice is saved with the goal.** Resolved refs are stored in `goal.json` (`models.{planner,worker,judge}`), so `/goal:approve`, `/goal:answer`, `/goal:resume`, and plain-text answers keep using them after a restart.
- `/goal:approve` and `/goal:retry` accept the same flags to change roles. The typical case is escalating the worker after a stall: `/goal:retry --worker-model anthropic/claude-opus-5-5`.
- A planner change takes effect at the next plan.

**4. Visible.**
- The approval message and `state.md` show a line like `Models: planner opus-5.5 (high) · worker qwen3-coder · judge gpt-5 (high)`.
- Each child already emits "subagent X using model …" progress.

**5. Fail fast.** Each ref and reasoning value is resolved once when `/goal` starts, or when flags change. An unknown model fails before discovery spends tokens.

Not proposed: per-role agent files such as `.caretz/agents/goal-worker.md`. They also invite prompt overrides that would undermine the harness rules. Profile plus flags cover the need.

## Implementation
- **internal/config/config.go**
  - Add `Goal GoalConfig` to `Profile` with `GoalRole{Model, Reasoning string}` for Planner/Worker/Judge.
  - Add `mergeGoal` next to `mergeTasks` and call it in the profile merge.
- **internal/agent/subagent.go**
  - Add exported `(*AgentSession).ValidateSubagentModel(ctx, ref, reasoning) error`, wrapping `resolveSubagentModel`, for fail-fast checks.
- **internal/slashcmd/goal_models.go** (new)
  - `goalModels{Planner, Worker, Judge goalRoleModel}`, plus option defs, parsing via the existing `parseCommandArgs` (args.go), merging (flags over defaults), validation, the display line, and `roleFor(kind)`: discovery/planner → planner, judge/checker → judge.
- **internal/slashcmd/goal.go**
  - Give `GoalCommand`, `GoalApproveCommand` and `GoalRetryCommand` the fields `Defaults goalModels`, `Choices []config.ModelChoice` and `CurrentRef`, matching the existing command construction pattern.
  - Parse the flags, keeping `--yes`.
  - `newGoalChild` passes `ModelRefOverride/ReasoningEffort` for its role.
  - Move the worker from `NewChildSessionWithOptions` to `NewSubagentChildSession` with its existing registry, the same pattern as the existing child-session registry helper, so the override applies.
  - Add `Complete` methods reusing `optionCandidates`, `modelRefs` and `reasoningValues`.
- **internal/slashcmd/goal_state.go**
  - Add `Models goalModels` to `goalState`, render it in `goalStatusMessage` (approval) and `renderGoalState`.
- **internal/app/slash_command_registry.go**
  - Build the goal commands with the profile's `Goal` defaults and `modelsCommand.Choices/CurrentRef`.
  - The plain-text answer path (slashcmd/registry.go:88) needs nothing, because models come from state.
- **docs/profiles.md** (the `goal` block) and **docs/cli.md** (the flags and the retry-escalation example).

## Verification
- **Unit tests:**
  - flag parsing and precedence (flag > profile > session)
  - `--model` shorthand
  - a reasoning value without a model is rejected
  - an unknown ref fails before discovery
- **goal_test.go**, with a fake `SubagentModelResolver` that records requests: one `/goal --yes --planner-model a/p --worker-model a/w --judge-model a/j` run resolves discovery and planner to `a/p`, the worker to `a/w`, and the judge to `a/j`.
- **Persistence:** the models survive close/reopen and `/goal:resume`, and `/goal:retry --worker-model a/x` changes only the worker.
- **Approval message** shows the Models line.
- **Checks:** `go test ./...` and `./bin/golangci-lint run ./...`.
- **Manual:** set a profile `goal` block, run `/goal` in the TUI, and confirm the per-role "using model" progress lines and the approval Models line.

## Status (2026-10-04): implemented

Adapted to the three roles of the rewrite: planner, worker, verifier. The verifier model runs both the reviewer and the judge, so there is no separate reviewer setting.
- Profile: `goal.planner|worker|verifier {model, reasoning}` next to `goal.maxTokens`/`goal.maxDuration` (`slashcmd.ConfigureGoal`).
- Flags on `/goal`, `/goal:approve` (also with `--edit`) and `/goal:retry`: `--model`, `--reasoning`, `--<role>-model`, `--<role>-reasoning`, with completion. Options are scanned only at the start of the input, so goal text keeps its quotes.
- Models live in goal events (`CreatedData`, `ApprovedData`, `EditData`, `RetryData`) and `State.Models`; every role call reads them from state.
- Fail fast: `AgentSession.ValidateSubagentModel` resolves every chosen model before planning.
- The worker now uses `NewSubagentChildSession` with `IsolatedExecution` so its model override and protected sandbox both apply.

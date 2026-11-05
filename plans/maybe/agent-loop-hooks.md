# Agent loop hooks (interception layer)

## Context
Caretz has an **event bus** (facts after the fact: `ContextSessionStore` sync handlers, async subscriptions, audit events) but no **interception layer** (decision points that can steer the next step). That gap is why the loop guard (`internal/agent/loop_guard.go`) is wired directly into `AgentLoop.run` (`internal/agent/agent.go`):

- `newTurnLoopGuard(messages, summary)` at turn start
- `loopGuard.observeResponse(...)` after each model response
- `loopGuard.afterToolResult(..., lastInBatch)` after each tool result (both error and ok paths)
- `loopGuard.request(messages)` to append the sticky note as the last user message
- `loopGuard.wrappingUp()` to drop `toolDefs` and refuse stray tool calls

Any future "watch the turn and steer it" feature (budget guards, policy checks, user hooks) would have to repeat that wiring.

### Why not a sync event observer
The observe side *could* move to `AddEventHandler` on `LLMUserMessageCreated` / `LLMAgentMessageCreated` / `LLMToolCallResultCreated`, but:
- the act side (inject note, strip tools) needs to change the next request, which observers never see; the loop still needs a hook;
- `HandleFunc(e)` has no `ctx`, so a judge call there can't be cancelled with the turn;
- a handler error is returned from `AppendToolResult` and logged as a persistence failure;
- turn boundaries, summary turns, compaction and the previous exchange must be rebuilt from events (and possibly replay on resume, unverified);
- a minute-long LLM call hidden inside `Append…` is hard to see.

## Reference: Claude Code hooks
Handlers on fixed lifecycle points, JSON in, typed decision out. Not general request/response middleware.

| Point | Effect |
|---|---|
| `UserPromptSubmit` | block, or add `additionalContext` |
| `PreToolUse` | allow / deny / ask, `updatedInput`, reason shown to model |
| `PostToolUse` | `block` + reason, or `additionalContext` next to the result (no rewrite of built-in tool output) |
| `Stop` / `SubagentStop` | block stopping, with reason → model continues |
| `SessionStart`, `PreCompact`, `Notification` | context / notify |

They cannot rewrite the raw LLM request (messages, system prompt, tool list) or the model's response. Steering goes through a few narrow, typed effects.

## Proposed interface
```text
// internal/agent/hooks.go (sketch)
type TurnInfo struct { Task, PreviousPrompt, PreviousAnswer string; Model, BaseURL string; Summary bool }

type PreToolUseResult  struct { Deny bool; Reason string }
type PostToolUseResult struct { AdditionalContext string }

type Hook interface {
    TurnStart(ctx, TurnInfo)
    AfterModelResponse(ctx, TurnInfo, llm.Response, totalTokens int64)
    PreToolUse(ctx, TurnInfo, llm.ToolCall) PreToolUseResult
    PostToolUse(ctx, TurnInfo, llm.ToolCall, result string, isErr, lastInBatch bool) PostToolUseResult
}

// in-process only, not exposed to user shell hooks
type RequestShaper interface {
    BeforeRequest(ctx, TurnInfo, []llm.Message, []llm.ToolDefinition) ([]llm.Message, []llm.ToolDefinition)
}
```
- The loop calls a `HookChain` at fixed points and passes `ctx` (cancellation works).
- The chain owns the failure policy: recover panics, log errors, never fail the turn. Individual hooks don't repeat it.
- Hooks are installed per session and inherited by child/sibling sessions (like `SetLoopGuard` today).
- Events stay facts for rendering and audit; hooks keep emitting them (`LoopGuard.Judged`, `StatusUpdated`).

## Loop guard on top
```text
TurnStart           → loopguard.New + SetPrevious, model filter (AppliesTo)
AfterModelResponse  → Guard.ObserveResponse
PostToolUse         → Guard.Observe; judge on lastInBatch
redirect note       → option A: PostToolUse.AdditionalContext (in history)
                      option B: BeforeRequest sticky last message (today's behavior)
wrap-up             → option A: PreToolUse deny "reply with your final answer"
                      option B: BeforeRequest strips toolDefs (today's behavior)
```
`run()` no longer mentions the loop guard.

## Other candidates for hooks
Things in `run()` today that are steering or policy rather than core loop mechanics.

| Candidate | Today | Hook | Notes |
|---|---|---|---|
| Wrap-up enforcement | `wrappingUp()` refuses stray calls in the tool dispatch (`agent.go` execution loop) | `PreToolUse` deny + reason | Disappears from `run()` once the loop guard is a hook. |
| Disabled-tool check | `!toolRegistry.IsToolEnabled(tc.Name)` branch in the dispatch | `PreToolUse` deny "tool X is disabled" | One place for tool policy (future allow/deny lists, user shell hooks). The registry still refuses on its own. |
| `AskUser` injection | `withAskUser(ctx, …)` appends the tool per request when `QuestionAskerAvailable(ctx)` (`questions.go`) | `BeforeRequest` (tool list) | Installed by interactive sessions only (TUI/ACP); prompt mode, schedules and subagents don't install it, so the ctx probe goes away. |
| Plan/build mode notice | `withModeNotice` appends `<caretz-mode …>` to the user message (`plan_mode.go`, via `convertToContextEvent`) | new `UserPromptSubmit` → additional context | Must stay **stored with the user message**: resume/retry match the stored copy by content (`modeNoticeVariants`). Not a `BeforeRequest` rewrite. Enforcement (read-only tools/sandbox) stays in the registry. |
| Turn token/time budget | none mid-turn; `/goal` checks `overBudget()` only between steps (`goal/engine.go`) | `AfterModelResponse` with `totalTokens` | Needs an effect to act: shared wrap-up, or "abort turn with reason". |
| "Not done yet" check | none (goal verifier runs as a separate step) | new `Stop` (block finishing with a reason) | Optional, cheap inline check (e.g. "said done but no tests ran"); does not replace the verifier role. |

Not hooks:
- **`UpdateTasks` / `ListTasks`** are special-cased in the dispatch because they need session state. A `PreToolUse` deny can't return a successful result, and a "handled, here's the result" effect would turn hooks into a tool dispatcher. Better: register them as session-bound tools in the registry.

Stays in the core loop:
- compaction (mid-loop, context-size retry, empty-response); at most a `PreCompact` notify point;
- empty-response and context-size retries;
- the permission flow (`AsPermissionError` → `RequestPermission` → grant → retry); it runs after `PreToolUse` and is driven by tool errors. A future `ask` result could route into the same `RequestPermission`;
- persistence (`Append…`), recovery results on cancel, `stopRequested`/ctx;
- streaming, reasoning/retry events, token calibration, logging (already event observers).

Effects these candidates add to the sketch:
```text
type WrapUp struct { Reason string }          // shared: drop tools / deny calls, model must answer
AfterModelResponse(...) AfterResponseResult   // { WrapUp *WrapUp; Abort string }
UserPromptSubmit(ctx, TurnInfo, prompt) UserPromptResult  // { AdditionalContext string } stored with the message
Stop(ctx, TurnInfo, llm.Response) StopResult  // { Block bool; Reason string }
```

Resulting hook map:
```text
TurnStart           loop guard, budget guard
UserPromptSubmit    plan/build mode notice
BeforeRequest       AskUser injection, loop guard note/strip   (in-process only)
AfterModelResponse  loop guard observe, turn budget
PreToolUse          disabled-tool policy, wrap-up deny, user policy hooks
PostToolUse         loop guard observe/judge
Stop                optional "not done yet"
```

## Open questions
- **Narrow effects only vs. in-process `BeforeRequest`.** Narrow (Claude Code style) is simpler and safe to expose to user shell hooks, but loop-guard notes go into history (persisted, resumable, cache-friendly; no longer "always last"). `BeforeRequest` keeps current behavior exactly, but it's a powerful seam. Leaning: narrow effects + in-process-only `BeforeRequest`.
- **Blocking vs. async judge.** With hooks the judge can run in a goroutine and land a note one or two iterations later: no turn latency, but a staler verdict. `e2e/features/catching_a_stuck_turn.feature` assumes the note is on the very next request.
- **User shell hooks from config** (`hooks` in `.caretz/config.json`)? Workspace config is untrusted, so hooks would likely be home or config-root only, and run through the sandbox.
- Also add `Stop` (block finishing with a reason) and `UserPromptSubmit`? `UserPromptSubmit` is needed for the mode notice; `Stop` only for the optional "not done yet" check.
- **Wrap-up as a shared effect vs. loop-guard-only.** The budget guard needs the same "stop using tools, answer now" action; a typed `WrapUp` keeps one mechanism. Or is "abort turn with reason" enough for budgets?
- **Multiple hooks wanting wrap-up/notes in one iteration**: first wins, merge notes, or order by priority?

## Steps (if adopted)
1. Add `HookChain` + `Hook` interface, and call it at the five loop points with no behavior change.
2. Port the loop guard to a `Hook` (keep `BeforeRequest` for the note and wrap-up first). Existing loop guard unit and e2e tests must pass unchanged.
3. Decide on narrow effects for the note and wrap-up; update the e2e features if the behavior changes.
4. Move the low-risk candidates: disabled-tool check and wrap-up deny (`PreToolUse`), then `AskUser` injection (`BeforeRequest`).
5. Move `UpdateTasks`/`ListTasks` into the registry as session-bound tools (separate from hooks).
6. Add `UserPromptSubmit` and move the mode notice last; it touches resume matching, so resume/retry tests must pass unchanged.
7. Optional: turn budget hook (`AfterModelResponse` + wrap-up/abort), `Stop` hook.
8. Optional: config-driven shell hooks, docs page under `docs/customization/`.

# Make Esc stop the current turn immediately

Status: implemented. Linux sandbox integration verification is pending because
the Docker daemon was unavailable during implementation.

## Summary

The first Esc will cancel the current turn, including its model request, running tools, subagents, and background commands launched by that turn. Caretz will save the interruption and return to the prompt so the user can redirect it.

Previously completed edits remain in place. An interrupted operation may have partial effects, and the model is told what was cut short on the next turn.

## Implementation changes

- **Unify TUI cancellation:** Esc and Ctrl-C cancel the active turn context immediately and display `stopping…`. Remove the TUI’s graceful-stop mode switch; retain `CallUntilStop` and `ErrGracefulStop` handling for existing library callers. Slash commands already cancel on Esc through `runRawCommand`; prompts now follow the same path.
- **Prevent further work:** check cancellation after model responses and compaction, before every tool invocation, and before permission retries. A response arriving during cancellation must not start another tool or model request.
- **Place cancellation checks only at persistence boundaries:** never check between writing a final response to the context store (`agent.go` `AppendAgentResponse`) and writing it to the audit log (`processUserEventUntilStop`). Otherwise:

  ```text
  ctx:   [user][assistant final response]
  audit: [user][x missing agent response]
  ```

  `finalizeInterruptedTurn` sees the terminal response and writes no audit event, so an ACP `ProcessUserEvent` retry runs the turn again. Rule: if cancellation is observed before a model response is persisted, discard the response and finalize a controlled interruption; once it is persisted to the context, complete the audit append regardless of cancellation. After that point the turn has succeeded; skip post-response compaction when cancelled (next turn’s preflight compaction covers it) instead of reporting the turn as cancelled.
- **Record interruptions on early paths:** today `callUntilStopUnlocked` appends the user message to the audit log before the first context check, and preflight compaction errors return before the user message reaches the context. Neither records an interruption:

  ```text
  audit: [prev][user msg][x no response]
  ctx:   [prev]                     (user msg missing)
  ```

  Check cancellation before appending the user audit event, so a turn cancelled before it starts persists nothing. Once the audit event exists, a cancellation (including during preflight compaction) appends the user message to the context and then the interrupted assistant response:

  ```text
  [prev][user msg][interrupted assistant response][next user message]
  ```

- **Tell the model what was cut short:** on cancellation, keep a tool’s partial stdout/stderr in its recovery tool result instead of discarding it, rather than the generic “interrupted before a result was produced” text. Use reason-specific interrupted-response text for `user_cancelled` (“stopped by the user”), and list background jobs killed by the cancellation, including jobs whose earlier `Background command started. PID …` result is already in context. Otherwise the next turn assumes those jobs are still running.
- **Track turn ownership:** attach an internal cancellation scope to each turn and propagate it through tools and child agents. Register background commands by supervisor request ID. On cancellation, kill only process groups owned by that scope, including commands whose launch races with cancellation. Normal turn completion preserves background work; earlier turns’ jobs and schedules remain running.
- **Define ownership of async work:** async runs (`AsyncRunSubagent`, `AsyncRunCommand`) requested by the cancelled turn are cancelled too, whether queued or running, and their background jobs are killed. Schedules the turn created are configuration, not running work, and remain; their later runs belong to no turn. Async-triggered turns (schedule reports, background-task reports) run on the session lifecycle context and are not cancelled by Esc.
- **Make waiting for the session cancellable:** `CallUntilStop` takes `executionMu` with a plain mutex, and async runs hold it for their whole duration (`runAsyncSubagent` holds it while the child runs). Replace it internally with a context-aware lock so Esc while waiting abandons the prompt before anything is persisted; show that the prompt is waiting for a background task. Public method signatures stay unchanged.
- **Bound command cleanup:** extend the supervisor cancellation protocol to address completed background launches as well as active requests. Send SIGKILL immediately to owned process groups and acknowledge cancellation without waiting for output pipes to close. Use a one-second cancellation acknowledgement deadline.
- **Decouple cancellation from the request stream:** `sandbox.Session.Run` holds `s.mu` for the whole command and reads the next frame as its own response, so a cleanup request would queue behind any running command or have its ack read by the wrong caller. Tag every response frame with its request ID and read them in one demultiplexing goroutine; cancel frames are written under a separate writer lock and do not take `s.mu`. A late ack for an abandoned request is discarded.
- **Don’t kill unrelated jobs on ack timeout:** today a cancellation timeout calls `stopLocked`, which kills the whole supervisor (every background job from every turn) and can wait a further 7 seconds. Instead, on the one-second deadline, report a cleanup error naming the unacknowledged request, keep the supervisor running, and leave earlier turns’ jobs untouched. Only an unreadable or closed stream marks the supervisor failed.
- **Preserve session consistency:** await cancellation and interruption persistence before accepting the next prompt. Reuse controlled-interruption append for missing tool results and the terminal assistant response:

  ```text
  [assistant tool call][repair tool result][interrupted assistant response][next user message]
  ```

  If those appends fail, the error is reported and the session is left as `[assistant tool call][x missing tool result]`; reopening it repairs `context.jsonpl` by backing it up and rewriting it with recovery events inserted in place (`internal/app/context_repair.go`).

## TUI event loop

- **Every exit waits for the turn:** `runInteractiveRawEventLoop` currently returns on permission decode, prompt, and resolve errors without receiving the turn result, and its terminal read-error path waits without cancelling (it can hang on a pending permission). Every exit must `cancel()` and then wait for the result, so the prompt never returns while a turn is still finalizing or holding the session.
- **Cancel from a permission prompt:** `readButtonChoice` maps Esc and Ctrl-C to `buttonCancelled`, which `readPermissionDecision` (and cloud consent) turns into Deny. Return a distinct cancel outcome that cancels the turn and does not call `ResolvePermission`: `awaitPermission` already records the request as `cancelled` on context cancellation, and a later Deny fails with “already resolved”.
- **Drop stale permission events:** once the turn is cancelled, ignore queued `PermissionRequested` events (or skip requests no longer in `PendingPermissions()`), so no prompt appears for an already-cancelled request.

## Input behavior and compatibility

- Esc in a permission prompt cancels the whole active turn; choosing “Deny” remains a permission denial.
- Repeated Esc is harmless; no second press or grace period is required.
- Approved programs that own the terminal keep native keyboard input and use Ctrl-C. Existing full-screen navigation keeps its own Esc behavior.
- ACP already cancels immediately (`acpTurn.stopNow`). Turn-scoped job ownership lives in the agent and tool layers, so ACP `session/cancel` also kills background jobs owned by the cancelled turn. This is an intended behavior change; document it in `docs/acp.md`.
- No new CLI flags or configuration. Keep public runtime method signatures and existing session formats; ownership and supervisor protocol additions are internal.
- Killing is by process group. A job that calls `setsid` or double-forks leaves its group and is not killed; document this limit.

## Documentation

- `docs/sessions/recovery.md`: immediate stopping, what is saved, and partial effects.
- `docs/sandboxing/dev-servers.md` (background commands): job ownership and which jobs survive a stop, plus the process-group limit.
- `docs/cli/plan-mode.md` and `docs/config/model-rules.md`: align existing Esc wording.
- `docs/acp.md`: `session/cancel` kills the turn’s background jobs.

## Test plan

- Cancel blocked model requests, streaming, retry backoff, and compaction; verify no subsequent tool or request starts.
- Cancel after a final response is generated but before it is persisted, and after it is persisted; verify context and audit log agree and an ACP retry does not rerun the turn.
- Cancel before the turn starts and during preflight compaction; verify either nothing is persisted or `[user msg][interrupted assistant response]` is in both stores.
- Verify recovery tool results keep partial output and the interrupted response names killed background jobs.
- Cancel running commands and child agents; verify owned processes terminate on macOS and Linux.
- Test background-launch races, multiple owned jobs, owned async runs, and preservation of earlier-turn jobs and schedules.
- Verify cancellation acknowledgements do not wait for inherited output pipes; exercise the acknowledgement timeout with an earlier-turn job alive and verify it survives and the supervisor keeps serving requests.
- Verify cleanup is not blocked by another in-flight supervisor request and late acks are discarded.
- Verify Esc cancels permission waits without a resolve error, stale permission events are not prompted, repeated presses are safe, and the next prompt succeeds after reopening the interrupted session.
- Verify every event-loop exit path waits for the turn result.
- Press Esc while an async run holds the session; verify the waiting prompt is abandoned with nothing persisted.
- ACP: `session/cancel` with an owned background job kills it and leaves earlier jobs.
- Use synchronized tests to verify cancellation propagation within 250 ms of decoded Esc and ordinary command cancellation within one second.
- Run targeted tests, `make test`, non-rewriting lint checks, both sandbox integration targets, and `make doccheck`.

## Assumptions

- Immediate cancellation takes priority over finishing the current operation.
- Background work from a successfully completed turn may continue; stopping a later turn does not cancel it.
- The prompt returns after cleanup and persistence, rather than allowing old work to overlap the redirected turn.
- Async-triggered turns are not interrupted by Esc; only waiting for them is.

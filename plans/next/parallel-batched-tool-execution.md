# Parallel Batched Tool Execution

## Summary

Execute independent tool calls from one LLM response concurrently, collect their outcomes in indexed slots, then append tool messages and context events sequentially in the LLM's original call order.

## Implementation Changes

- Replace the sequential ordinary-tool loop in `internal/agent/agent.go` with a batch coordinator using one goroutine per ordinary tool call and a `sync.WaitGroup`.
- Each worker writes only to its own preallocated result slot containing the tool message, error, and any recovered panic; workers must not update active messages or append tool-result events.
- After all workers finish, process slots in original order:
  - Convert execution errors into tool error messages.
  - Append each message to active context.
  - Append each `LLMToolCallResult` event.
  - Preserve deterministic ordering regardless of completion order.
- Keep `UpdateTasks` and `ListTasks` out of the concurrent worker pool. Execute them synchronously when their position is reached during ordered result processing, preserving task-state dependencies and task-event ordering.
- A graceful stop received after the batch starts drains every call, persists all real results in order, and then returns `ErrGracefulStop` before another LLM request. It will no longer skip later calls in the batch.
- Parent-context cancellation continues to propagate to all workers. After they exit, use the existing interrupted-turn recovery path rather than concurrently writing cancellation results.
- Capture worker panics and re-raise them on the coordinator goroutine after the batch drains, avoiding an unrecovered goroutine panic while retaining existing panic semantics.
- No configuration or public data-model changes. Custom tool handlers may now be invoked concurrently and therefore must be safe when the LLM batches independent calls.

## Test Plan

- Prove two blocking ordinary tools both start before either is released.
- Make the second call finish first and verify context events, active messages, and the next LLM request still contain results in call order.
- Verify mixed success and error results are persisted deterministically.
- Verify multiple task calls remain sequential and `ListTasks` observes prior `UpdateTasks` calls in the same batch.
- Update the batched graceful-stop test to expect every started call's real result followed by the interrupted assistant response, with no recovery result for drained calls.
- Retain coverage for single-call behavior and hard context cancellation.
- Run `go test ./internal/agent` and `go test -race ./internal/agent`.

## Assumptions

- The LLM's existing instruction to batch only independent calls is the concurrency contract for ordinary, write, Bash, custom, web, and subagent tools.
- Concurrency is unbounded within one model-produced batch; no new semaphore or configuration option is introduced.
- Only result persistence is serialized. External effects of ordinary tools may complete in any order.

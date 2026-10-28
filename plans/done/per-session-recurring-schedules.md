# Per-Session Scheduling

## Summary

Add durable schedules owned by individual agent sessions. Schedule definitions and run state live exclusively in the session audit log, while each firing becomes a normal user/assistant conversation turn. V1 runs only while the built-in ANSI TUI is open; schedules restore when that session is reopened.

Supported schedules:

- `every:<Go duration>` — recurring, minimum `1m`.
- `at:<RFC3339 timestamp>` — one-shot and automatically removed after its execution attempt.
- When caretz reopens, the latest missed recurring occurrence runs once and older misses are coalesced as skipped. An overdue one-shot runs once after reopening.

## Key Changes

- Add an `internal/schedules` state machine that folds audit events such as registered, removed, run-started, run-finished, and run-skipped into active per-session jobs.
- Persist job ID, schedule specification, prompt, creation time, next-run time, last-run time, last status, and last error. Store timestamps in UTC and generate opaque IDs such as `sched_<random>`.
- Restore schedule state from `events.jsonpl` when opening a session. Retain the latest overdue recurring occurrence for immediate execution and record one coalesced `skipped` result for any older misses; retain overdue `at:` jobs for immediate execution. Retry an occurrence interrupted by a process failure unless a newer occurrence supersedes it.
- Start timers only after the owning frontend is ready. The interactive UI transfers scheduler ownership on `/clear`, while applications restore schedules for their active sessions at startup. Frontends cancel timers cleanly on shutdown. Incognito schedules remain memory-only.
- Extend the ANSI input loop with an external wake path so a due job clears/suspends the prompt, preserves the user’s draft, runs on the TUI execution path, and redraws afterward without interleaved terminal output.
- Serialize scheduled and interactive turns within the session. A recurring occurrence due while the same job is running is skipped and advances the cadence; other due jobs wait for the session execution path.
- Add process-wide scheduled-run capacity of 4, a 15-minute timeout beginning when execution starts, cancellation handling, and panic recovery. Persist terminal statuses as `succeeded`, `failed`, `timed_out`, `panicked`, `cancelled`, or `skipped`.
- Mark scheduled execution through `context.Context`. `RegisterSchedule` rejects calls from a scheduled run, including propagated subagent calls; listing and removal remain allowed.
- Apply hardcoded limits: 100 active jobs per session and 32 KiB per prompt. Validate before appending any audit event and return stable, specific errors for malformed schedules, intervals below `1m`, past `at:` timestamps, empty/oversized prompts, capacity exhaustion, unknown IDs, and forbidden nested registration.
- Preserve recurring cadence from its original anchor. One-shot jobs self-remove after success, failure, timeout, panic, or cancellation.

## Interfaces

- Model tools available only in schedule-enabled ANSI TUI sessions:

  - `RegisterSchedule {"schedule":"every:24h","prompt":"Check the repository and summarize interesting changes"}`
  - `ListSchedules {}`
  - `RemoveSchedule {"id":"sched_ab12cd34"}`

- Add one slash command:

  - `/schedule register every:24h Check the repository and summarize interesting changes`
  - `/schedule register at:2026-09-06T09:00:00+03:00 Remind me to review the release`
  - `/schedule list`
  - `/schedule remove sched_ab12cd34`

- Application users manage schedules through normal conversation, backed by the same model tools. Scheduled results and concise failure notices arrive as new messages rather than replies to the message that created the schedule.

- Lists are ordered by next-run time and include ID, schedule, abbreviated prompt, next run, last run, last status, and last error when present.
- Schedule metadata and lifecycle events never enter `context.jsonpl`. The prompt and response from an actual firing do enter normal conversation context, preserving continuity between checks.

## Test Plan

- State-fold tests for registration, removal, run lifecycle, restart restoration, corrupt event handling, and one-shot auto-removal.
- Parser and validation tests for both schedule forms, RFC3339 offsets, Go durations, exact `1m` boundary, past timestamps, empty/oversized payloads, capacity limits, and unknown IDs.
- Deterministic clock tests proving recurring cadence, latest-occurrence replay without recovery bursts, interrupted-run recovery, overdue one-shot execution, and skip-if-running behavior.
- Runtime tests for the global semaphore, per-run timeout, cancellation, panic recovery, status persistence, and forbidden registration from scheduled contexts and their subagents.
- Agent integration tests verifying tool advertisement/dispatch only for schedule-enabled sessions and confirming audit-only metadata plus normal context turns for firings.
- ANSI TUI tests verifying `/schedule` UX, restored jobs, safe idle interruption, preservation/redraw of partially typed input, `/clear` lifecycle transfer, and clean shutdown.
- Adapter tests verifying startup restoration, per-channel serialization, proactive result delivery, failure notices, deduplication, and clean shutdown.
- Run `go test ./...`, `make test`, and `make lint`.

## Assumptions

- V1 does not execute schedules in one-shot CLI prompt mode, ACP, or embedded clients.
- Caretz must remain running in the owning interactive frontend for on-time execution; persisted schedules resume when their owning session is reopened.
- No full cron expressions, calendars, time-zone defaults, job editing, manual run command, configurable overlap policy, retry policy, or queue mode are included.

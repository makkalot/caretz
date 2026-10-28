# `/goal` — Harness Specification (v4)

A goal is a user-owned completion condition. The harness keeps working until an
independent judge confirms the condition holds against evidence the user approved,
or a guardrail fires.

High level: **discover → plan (with the user) → execute → validate → loop.**

| Stage    | Component                          | Runs in                          |
|----------|------------------------------------|----------------------------------|
| Discover | Discovery subagent                 | Isolated subagent (can be slow)  |
| Plan     | Planner                            | Isolated read-only subagent      |
| Execute  | Executor per task + checker        | Isolated agent / call            |
| Validate | Harness runs evidence, judge rules | Harness code + isolated call     |

The harness is the only component that renders messages to the user. Discovery, planner,
executor, checker, and judge calls are isolated and report to the harness in JSON. The
planner decides what to present or ask, while the harness turns that decision into the
visible proposal, question, or replan notice.

---

## 1. Commands

```
/goal <condition>              discovery → planner proposes evidence + plan → awaiting_approval
/goal <condition> --yes        same, but auto-approve (one-shot CLI, /loop, scripts)
/goal:approve                  start the run (works after session resume)
/goal:approve --edit "<text>"  planner revises evidence/plan, harness re-presents
/goal:answer <text>            answer the planner's pending question (plain reply also works)
/goal:evidence --edit "<text>" change evidence mid-run or after done; reopens at planner
/goal:resume                   continue a compatible interrupted running goal
/goal:retry                    restart a compatible saved terminal goal from approved state
/goal:status                   print status line, plan table, last 3 log lines
/goal:stop                     abort → exit stopped
```

Non-interactive without `--yes` → exit `awaiting_approval`, non-zero code. Never hang.
`/goal:retry` does not import hand-edited `state.md`; plan changes use
`/goal:approve --edit`, and evidence changes use `/goal:evidence --edit`.
`/goal:resume` continues a persisted `running` phase without charging wall time spent
between processes. `/goal:retry` reopens a compatible `done`, `stalled`, `budget`, or
`stopped` run, preserves its approved goal/evidence/plan, resets run-scoped guards, and
continues directly; unresolved questions still require `/goal:answer`.

---

## 2. States and exits

```
awaiting_approval → running ⇄ paused(question) → done | stalled | budget | needs_human | stopped
```

- `paused` and `awaiting_approval` consume no iteration or time budget.
- In non-interactive mode, `paused` maps to exit `needs_human` with the question.
- Every exit writes one `# Exit` line to state.md.
- Exit codes: `3` awaiting approval, `4` needs human, `5` stalled/budget, `6` stopped.

---

## 3. Roles and ownership

| Role      | Runs where                         | Sees                                        | May                                          | May not                                   |
|-----------|------------------------------------|---------------------------------------------|----------------------------------------------|-------------------------------------------|
| Harness   | code                               | everything                                  | run all commands, write state.md             | —                                         |
| Discovery | isolated subagent, read-only tools | repo, goal text                             | explore, report runners/facts/candidate checks | edit files, talk to user, decide evidence |
| Planner   | isolated subagent, read-only tools | bounded conversation excerpt (initial plan), discovery report, injected state + delta | propose evidence + plan + questions | do the work, edit files, address the user directly, mark done, change goal |
| Executor  | isolated agent                     | its task + injected state contents          | edit its files, run cmds                     | write state.md, ask user, claim pass      |
| Checker   | harness + optional small call      | task, check command output                  | classify one task's check                    | see executor prose, edit code             |
| Judge     | harness + isolated call            | goal, evidence output, regression, log tail | end the goal                                 | run commands (v1), investigate, replan    |

Invariants:
- Goal text is immutable once approved. Evidence changes only with user approval.
- Only the checker sets a task `ok`; only the validation stage sets `done` (the
  all-command fast path or the judge).
- Checker and judge never see worker prose — only command output produced by the harness.
- The harness is the only writer of state.md.
- Only the harness writes user-visible goal messages; planner output is structured JSON.

Checker and judge use the same model as the worker in v1, as separate calls with fresh
context. `judge()` and `checker()` take `model` as a parameter so a smaller model is a
config change later.

---

## 4. Discovery — subagent, first step

Discovery can take minutes (reading CSVs, probing runners, dry-running scripts), so it
runs in an isolated subagent and returns a compact report. The main session only ever
sees the report, never the exploration.

**Step 1 — Static scan (harness code, no model).** `package.json` scripts, `Makefile`
targets, `pyproject`/pytest config, `scripts/verify_*`, available binaries.

**Step 2 — Discovery subagent (read-only tools, JSON out).**
Input: goal text verbatim, static scan, any `@paths` the user referenced.
Job: understand the inputs and the target, find how things are verified today, and
propose *candidate* checks. It does not decide evidence — that's the planner's call with
the user.

```jsonc
{
  "runners": ["python", "scripts/parse_statement.py"],
  "facts": [
    "8 CSVs in cvs/saving (jan26..aug26)",
    "parsed output lives in bank-data/parsed/savings/, schema_version 1",
    "no test runner configured"
  ],
  "candidate_checks": [
    {"check":{"type":"script","path":"scripts/verify_savings.py"}, "covers":"counts/totals per month", "gaps":"category correctness", "status":"missing"},
    {"check":{"type":"shell","cmd":"cmp loop cvs/saving vs bank-data/raw"}, "covers":"raw copies identical", "gaps":"parsed content", "status":"valid"}
  ],
  "open_questions": ["Should existing 2026-01 JSON be overwritten or kept?"]
}
```

Harness validates every candidate (§6.2) before handing the report to the planner, so
the planner never proposes a broken check to the user. `facts` seed `# Discoveries`.

Budget: discovery has its own time cap (e.g. 10 min). On timeout it returns what it has
with `"partial": true`; the planner mentions that to the user.

---

## 5. Planner — isolated, state-driven planning

The planner runs in an isolated read-only subagent. Durable context comes from the compact
goal record and `state.md`, not from a growing planner transcript. For the initial plan,
the harness also injects a bounded excerpt of the main conversation so references such as
"our system" or "one file per task" retain their meaning. Replans receive only state and
the typed delta since the previous plan.

### 5.1 Initial plan — mandatory approval

Input: goal, discovery report, bounded conversation excerpt, and current state.
The planner does three things in one turn, then stops:

1. **Choose evidence** from the candidate checks (may adjust or combine), with covers/gaps.
2. **Draft the plan**: tasks with checks from the menu (§6); if evidence is `missing`,
   T1 creates it.
3. **Ask** the discovery `open_questions` plus anything it needs, batched, with defaults.

```
Goal fc438119 — proposed evidence (the judge will run these):
  E1  scripts/verify_savings.py   [will be created by T1]
      covers: one JSON per CSV, counts/totals match, uncategorized 0
      does not cover: category correctness
  E2  raw copies byte-identical to cvs/saving
Plan v1: T1 write verify scripts → T2–T9 process jan..aug (one task per file)
Question: existing 2026-01 JSON — overwrite or keep?  [default: overwrite]
/goal:approve · /goal:approve --edit "..." · or just reply
```

Status `awaiting_approval`, including when the proposal contains questions. A plain reply
is treated as answers/edits; the planner revises and re-presents until `/goal:approve`.
Only approval starts execution. `--yes` accepts safe defaults; a question without a default
exits `needs_human` in non-interactive mode.

Structured output to the harness (which renders the human-readable proposal):
```jsonc
{
  "version": 1,
  "evidence": [{"id":"E1","check":{...},"covers":"...","gaps":"...","status":"missing"}],
  "tasks": [Task],
  "changes": ["initial plan"],
  "questions": [{"question":"...","options":["overwrite","keep"],"default":"overwrite"}]
}
```

### 5.2 Replans during the run

Replans also run in fresh isolated planner subagents, but they **auto-apply** — no approval
— unless the planner raises a question or proposes an evidence change. The user sees a
one-line harness-rendered notice (`plan v2: add T1b drop pandas`).

**Triggers:** judge verdict not done; `check_invalid`; task exhausted attempts; executor
`blocked` / `scope_changed`; user answer. Not after every successful task.

**Input is a compact delta, not transcripts** — this keeps the main context from growing
with every cycle:
```
GOAL (immutable) · EVIDENCE (with status) · PLAN TABLE + statuses
NEW SINCE LAST PLAN: discoveries · typed task events · judge feedback · answers
```
Typed events: `check_invalid Tn: <reason>` (check broken, task untouched),
`Tn exhausted (2/2): <reason>`, `blocked Tn`, `scope_changed Tn`.

**Output:** same schema as 5.1, plus optional `needs_human` / `evidence_concern`.

**Rules:** edit, don't rewrite; keep `ok` tasks; list every change; every task has a
check from the menu; tasks are ordered by `depends_on` and run one at a time. May not mark
done, edit the goal, change evidence without asking, or drop a failing task without
stating in `changes` why the goal no longer needs it (harness rejects otherwise).

### 5.3 Context hygiene

- The planner never receives executor prose or full check output — only one-line reasons.
- Per-attempt detail stays in `tasks/Tn.log`.
- Planner calls do not accumulate a shared transcript. Each call receives the current
  compact state plus only the new typed delta; the bounded conversation excerpt is used
  only for the initial proposal.

---

## 6. Checks (task checks and evidence share one vocabulary)

### 6.1 Check types — the planner picks, the harness renders

The planner never writes freeform bash by default.

```jsonc
{"type":"script",      "path":"scripts/verify_month.py", "args":["--month","2026-02"]}
{"type":"test",        "runner":"pytest", "path":"tests/import"}
{"type":"lint",        "runner":"eslint", "path":"src/"}
{"type":"build"}
{"type":"file_exists", "path":"bank-data/parsed/savings/savings_statement_2026-02.json"}
{"type":"json_field",  "file":"...json", "field":"summary.uncategorized_count", "equals":0}
{"type":"grep_absent", "pattern":"db.query(", "glob":"src/**/*.ts"}
{"type":"shell",       "cmd":"..."}      // escape hatch; flagged in approval view
```

Only runners found in discovery are offered. When no suitable check exists, the planner
makes the first task "write scripts/verify_X.py" and later tasks use it.

Checks must print a compact summary (one line per item) and exit 0/1. Output too long to
read is a defect in the check.

### 6.2 Check validation (harness, before any executor starts)

1. Render the check to a command.
2. `bash -n` for syntax; `command -v` for the executable.
3. Dry run: on an unfinished task it must **fail cleanly** (exit 1 with meaningful output).
   Exit 126/127/2, "not found", tracebacks in the check tool → invalid.
4. Checks depending on a tool another task creates are marked `pending-Tn` and validated
   once Tn passes.
5. Invalid → back to the planner as `invalid_check Tn: <stderr>`. Max 2 repairs, then
   `needs_human`.

---

## 7. Execute: executor → checker → retry

Sequence per task: **executor(T) → harness runs check(T) → classify → record → next.**
Never bulk-check at the end of a batch.

**Constraint (v1): strictly sequential.** One executor at a time, in the shared
workspace, in `depends_on` order (ties broken by plan order). No parallel executors, no
worktrees. The next task starts only after the previous one is recorded (`ok`, or
escalated to the planner). This guarantees every executor sees all discoveries from every
earlier task, and no two agents touch the workspace at once.

### 7.1 Executor

Input: task JSON, accumulated discoveries (injected as text — never a path to state.md),
previous failure reason on retry.

Output (final message, JSON only):
```jsonc
{"task_id":"T2","result":"ok|blocked|scope_changed","notes":"≤2 lines","discoveries":["..."]}
```
Harness appends discoveries to state.md; notes go to `tasks/T2.log`. Ask-user tools are
removed while a goal is active.

### 7.2 Checker

1. Harness runs the rendered check, captures exit code + last 200 lines.
2. **Fast path (no model):** exit 0 → `passed`; exit 1 with ≤5 lines → `failed`, reason =
   last line; exit 126/127/2 or "command not found" → `check_invalid`.
3. **Model path:** otherwise (long output, traceback, ambiguous exit) a fresh call
   classifies from the output alone.

```jsonc
{"task_id":"T2","attempt":1,"status":"passed|failed|check_invalid","reason":"one line, concrete","by":"harness|model"}
```

### 7.3 Outcomes

| Checker result    | Effect                                                                 |
|-------------------|------------------------------------------------------------------------|
| `passed`          | task `ok`                                                              |
| `failed`          | attempt charged; retry same task with reason (max 2 attempts), then escalate to planner as `exhausted` |
| `check_invalid`   | attempt **not** charged; task untouched; check goes to planner for repair |
| executor `blocked` / `scope_changed` | stop the execution cycle and go to the planner immediately |

---

## 8. Validate: harness + judge

**Trigger:** once per cycle, when no ready tasks remain.

**Harness gathers evidence (the judge runs nothing in v1):**
1. Every approved evidence item.
2. Regression: every task's check again (catches T5 breaking T2).
3. Inspection items, if any, via a read-only call with view/grep/screenshot only; findings
   must cite file/line.

**Fast path:** all exits 0 and no inspection items → `done` without a model call.

**Otherwise, judge call** (fresh context): goal, evidence results, regression results,
plan table, last 10 log lines.
```jsonc
{"done":false,"reason":"2026-06 uncategorized_count 2 (E1)","evidence_checked":["E1 exit 1","E2 exit 0","regression 10/10"]}
```

The judge **confirms, it does not debug.** It names the gap; the planner and executors
find why. Anything it wants beyond declared evidence goes in the exit line as a suggestion,
never as a reason to replan.

**On a gap:** reason appended to `# Judge feedback`; loop re-enters at the planner.
`judge_replans` counts separately (cap 3). If tasks are all green and the judge keeps
failing, exit `stalled: judge/plan disagreement` — the evidence and the plan encode
different definitions of done, and a human should look at the evidence.

---

## 9. Pauses and user input

The planner is the only role that decides a question, but the harness renders it. No
subagent communicates directly with the user.

Executor `blocked` and `evidence_concern` from any agent go through the planner, which
decides whether it's a real human decision or a plan fix.

Question format (renders as buttons):
```jsonc
{"question":"...","options":["rewrite all","add shim","abort"],"default":null,"context":"T3 discovery"}
```

During execution, the harness writes `# Pending question` and sets status `paused`. The
user replies (or uses `/goal:answer`); any message not starting with `/` counts as the
answer. The harness appends `Answer: ...` to Discoveries, sets `running`, and the planner
replans with it. During the initial proposal, questions remain under `awaiting_approval`:
plain replies revise and re-present the proposal, and `/goal:approve` alone starts work.

- The planner attaches a safe `default` where one exists; with `--yes` or in
  non-interactive mode, defaults auto-apply and are logged. No default → exit `needs_human`.
- Batch questions into one pause.
- Unanswered pauses stay paused; remind on `/goal:status`.
- Answers feed the plan, never the goal.

---

## 10. Evidence repair

| Problem                 | Detected by                            | Fix path                                             |
|-------------------------|----------------------------------------|------------------------------------------------------|
| Evidence broken         | harness (same rules as §6.2)           | `evidence_invalid` → isolated planner proposes replacement or a task that creates it, asks the user |
| Evidence wrong          | user after `done`                      | `/goal:evidence --edit` → reopens at planner         |
| Evidence vs plan conflict | judge fails while tasks green        | `stalled` exit names the conflict                    |
| Evidence contradicts goal | planner/executor                     | `evidence_concern` → pause with question             |

Evidence changes only with user approval; the planner proposes, the user decides. Every change bumps `evidence.version` and resets the
judge-replan counter.

---

## 11. state.md

**Location:** the harness-owned Caretz session directory alongside the session event and
context stores (`<session>/state.md`, `<session>/goal.json`, and
`<session>/tasks/<task-id>.log`). Executors receive state contents in their prompt, never
a path. Caretz already hides the session/config root from workspace tools; retain that
backstop and disable ask-user/session tools in goal children.

`goal.json` uses schema version 4. Existing pre-v4 goal runs are deliberately incompatible:
resume, retry, and status return a clear legacy-state error, while starting a new `/goal`
may replace the old goal artifacts. There is no legacy execution engine or migration.

**Format rules:** one line per task; criteria stated once; no worker prose; append log
lines only on state change; short IDs; per-attempt detail in `tasks/Tn.log`.

```markdown
# Goal fc438119
<verbatim goal>

status: running   cycle: 2   plan: v2   evidence: v1   budget: 3/25 iters, 27/120 min

# Evidence
- E1 script verify_savings.py --src cvs/saving --out bank-data/parsed/savings   [valid]
- E2 shell  raw copies byte-identical to cvs/saving                              [valid]

# Plan v2   (changes: add T1b drop pandas; T5–T9 depend on T1b)
| id  | task                         | check                | deps | status | att |
|-----|------------------------------|----------------------|------|--------|-----|
| T1  | Write verify scripts         | py_compile           | —    | ok     | 1   |
| T1b | Drop pandas from verify      | no-pandas + compile  | T1   | ok     | 1   |
| T2  | Process jan26 → 2026-01.json | verify_month 2026-01 | T1   | ok     | 2   |
| …   |                              |                      |      |        |     |

# Discoveries
- T1: CSV dates day-first → --dayfirst
- T2: jan CSV has blank-memo row → --keep-empty

# Pending question
(none)

# Log
- c1 T2 fail (att 1): MISMATCH transaction_count
- c1 T2 ok (att 2)
- c1 T5 check_invalid: verify_month.py cannot import pandas → planner
- c2 judge: done

# Exit
done — c2, 2 replans, 27 min
```

---

## 12. Control loop

```python
def goal_command(text, yes=False):
    st = State.init(goal=text)
    report = discovery_subagent(text, static_scan())          # isolated, can be slow
    report.candidate_checks = validate_checks(report.candidate_checks)
    st.add_discoveries(report.facts)

    proposal = planner_subagent(st, report, conversation_excerpt)
    st.apply(proposal)
    if yes: st.accept_defaults(); return run(st)
    return st.set("awaiting_approval")                         # harness renders; user replies / approves

def run(st):
    delta = None
    while True:
        if guard := st.guards(): return st.exit(*guard)       # budget, stop, replan caps

        if delta:
            plan = planner_subagent(st, delta)                 # fresh call, compact delta, auto-applies
            if plan.questions or plan.needs_human or plan.evidence_concern:
                return st.pause(plan)
            if bad := validate_checks(plan): delta = bad; continue
            st.apply(plan)

        delta = execute_ready(st)            # executor → check → retry; escalation or None
        if delta: continue

        ev = harness_run(st.evidence) + harness_run(st.task_checks())
        v  = fast_verdict(ev) or judge(st, ev)
        if v.done: return st.exit("done", v.reason)
        if st.judge_replans_exceeded(): return st.exit("stalled", v.reason)
        delta = f"judge: {v.reason}"
```

`/goal:approve` and answers resume with `run(st)`.

---

## 13. Build order

1. **Checker fix:** harness runs checks; checker reads output only; fast path by exit code;
   `passed | failed | check_invalid`.
2. **Per-task check + retry:** check right after each executor; 2 attempts; `check_invalid`
   uncharged and routed to planner.
3. **Discovery subagent:** static scan + read-only subagent, candidate checks validated,
   time cap with partial results.
4. **Isolated planner:** read-only subagent, bounded conversation excerpt for the initial
   proposal, evidence + plan + questions in one JSON response, `awaiting_approval`,
   `--yes`, and compact state deltas for fresh-call replans.
5. **Check vocabulary + validation:** typed checks, `bash -n`, dry run, `pending-Tn`.
6. **Judge:** harness-run evidence + regression, fast path, bounded judge replans.
7. **state.md:** session-scoped harness-owned location, schema v4, injected contents,
   compact format, per-task logs, and explicit rejection of legacy goal runs.
8. **Pauses:** planner questions, defaults, non-interactive mapping.
9. **Evidence repair:** versioning, `/goal:evidence --edit`, `evidence_concern`.
10. **Compatibility and UX:** adapt `/goal:resume` and `/goal:retry`, intercept plain
    answers while a goal is waiting, reject pre-v4 state clearly, and update help,
    autocomplete, CLI documentation, and recovery guidance.

Out of scope for v1: parallel executors and worktrees. Sequential execution keeps
discoveries flowing to every task and avoids merge handling; revisit once the loop is stable.

Deliver the complete v1 in this order. Steps 1–2 fix the current planner thrash and should
land first for review, but the redesign is complete only when all ten stages, command
compatibility, documentation, and end-to-end tests are in place.

---

## 14. Acceptance and tests

- State-machine coverage includes every transition and exit code, active-time accounting,
  interruption/resume, terminal retry, stop, approval edits, and pre-v4 rejection.
- Discovery covers runner detection, referenced paths, partial timeouts, invalid candidates,
  missing checks, and checks pending on a creator task.
- Every typed check has rendering and validation tests, including unavailable executables,
  bad syntax, meaningful exit-1 failures, ambiguous output, and flagged shell checks.
- End-to-end tests prove strict executor → check ordering, dependency order, two-attempt
  retry, uncharged invalid checks, discovery propagation, blocker replanning, and that
  worker prose never reaches checker or judge inputs.
- Approval and pause tests cover batched initial questions, plain replies, `/goal:answer`,
  `--yes` defaults, missing defaults, evidence edits, and session resume.
- Final validation tests cover the command-only fast path, regression failures, inspection
  citations, judge gaps, evidence/plan disagreement, and replan caps.
- Persistence tests cover atomic `goal.json`/`state.md` updates, compact rendering,
  per-task logs, exactly one exit entry, and denial of goal artifact access to children.
- Completion requires `go test ./...`, `make lint`, and `make doccheck` to pass.

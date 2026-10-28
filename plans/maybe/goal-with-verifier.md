# `/goal` — Harness Specification

A goal is a user-owned, immutable completion condition. The harness keeps working
until an independent judge confirms the condition holds, or a guardrail fires.

```
/goal <condition>        start
/goal:status             print state.md summary
/goal:stop               abort, exit = stopped
```

---

## 1. Roles

| Role      | Model     | Sees                          | May                                   | May not                          |
|-----------|-----------|-------------------------------|---------------------------------------|----------------------------------|
| Planner   | main      | goal, state.md                | add/split/drop/reorder tasks          | mark tasks done, change goal     |
| Executor  | main      | task, state.md                | edit code, run cmds, append notes     | mark own task done, ask user     |
| Checker   | small     | task, done_when output        | mark task ok/failed                   | edit code                        |
| Judge     | small     | goal, state.md tail, evidence | end the goal                          | edit code, replan                |

Executor and Judge/Checker must be separate model calls with separate prompts.

---

## 2. Data contracts

```jsonc
// Goal (immutable after intake)
{
  "goal": "string",                 // user's condition, verbatim
  "evidence_cmds": ["string"],      // commands judge runs, e.g. "npm test", "eslint ."
  "budget": { "max_iters": 25, "max_minutes": 120, "max_replans": 5 }
}

// Task
{
  "id": "T3",
  "title": "string",
  "files": ["src/api/**"],
  "done_when": "pytest tests/api -q",   // shell cmd; exit 0 = pass
  "depends_on": ["T2"],
  "parallel_ok": false,
  "status": "todo | running | ok | failed | blocked",
  "attempts": 0
}

// Plan (planner output)
{ "version": 3, "tasks": [Task], "changes": ["split T3 -> T3a/T3b: 14 callers found"] }

// Outcome (executor output, last message)
{ "task_id": "T3", "result": "ok | blocked | scope_changed", "notes": "string" }

// CheckResult (checker output)
{ "task_id": "T3", "passed": true, "reason": "string" }

// Verdict (judge output)
{ "done": false, "reason": "string", "evidence_checked": ["npm test: 3 failing"] }
```

Exit codes: `done | stalled | budget | needs_human | stopped`.

---

## 3. `state.md` (shared handoff)

Single file at `.goal/state.md`. Every agent reads it first; executor and harness append.

```markdown
# Goal
<verbatim goal>
evidence: npm test; eslint .

# Plan (v3)
- [x] T1 ...   done_when: ...
- [ ] T3 ...   done_when: ...   depends: T2

# Discoveries
- T2: db.query signature changed; 14 callers in src/api/**

# Task log
- T1 ok (checker: exit 0)
- T2 ok (attempt 2)

# Judge feedback
- cycle 1: not done — 3 tests failing in tests/api

# Exit
(written once at end) done | stalled | budget | needs_human | stopped — reason
```

---

## 4. Control loop

```python
def run_goal(goal_text):
    goal = intake(goal_text)                    # §6
    st   = State.init(goal)
    for it in range(goal.budget.max_iters):
        if stop_requested(): return st.exit("stopped")
        if over_time(goal): return st.exit("budget")

        plan = planner(goal, st)                # §5.3
        if plan.needs_human: return st.exit("needs_human", plan.question)
        st.apply_plan(plan)
        if st.replans > goal.budget.max_replans: return st.exit("stalled", "replan cap")

        for task in st.ready_tasks():           # deps satisfied, status todo/failed, attempts<2
            out = executor(task, st)            # §5.4
            st.log_outcome(out)
            if out.result in ("blocked", "scope_changed"): break   # replan early
            chk = checker(task, run(task.done_when))                # §5.5
            st.mark(task, chk)

        ev  = {c: run(c) for c in goal.evidence_cmds}
        v   = judge(goal, st.tail(), ev)        # §5.2
        st.log_verdict(v)
        if v.done: return st.exit("done")
        if stalled(st): return st.exit("stalled", "no progress 3 cycles")
    return st.exit("budget", "iteration cap")
```

`stalled(st)`: true when repo hash unchanged AND no task moved to `ok` for 3 consecutive cycles.

While a goal is active: remove ask-user / end-turn-to-user tools from executor.
If no output for 30 min, surface `/goal:status` to the user (do not stop).

---

## 5. Prompts

### 5.1 Worker system suffix (Phase 1, no planner)
```
ACTIVE GOAL: {goal}
Work toward the goal. Do not ask the user questions; if truly blocked, end your
turn with a line starting "BLOCKED:" and explain. Do not claim the goal is met —
an independent judge decides that.
```

### 5.2 Judge
```
You are a strict verifier. Decide only whether the GOAL is currently satisfied.

GOAL: {goal}
EVIDENCE (command outputs, ground truth):
{evidence}
RECENT TRANSCRIPT / STATE (agent claims, may be wrong):
{state_tail}

Rules: trust evidence over claims. If any part of the goal is unverified, done=false.
Reason must name the concrete gap (file, test, error), not advice.
Output JSON only: {"done": bool, "reason": str, "evidence_checked": [str]}
```

### 5.3 Planner
```
You maintain the task plan for a goal. Edit the existing plan; do not rewrite it.

GOAL (immutable): {goal}
CURRENT PLAN: {plan_json}
NEW INFORMATION SINCE LAST PLAN:
{discoveries} {task_log_delta} {judge_feedback}

Rules:
- Keep tasks with status ok unchanged.
- Add/split/drop/reorder tasks only when justified by new information; list each
  change in "changes".
- Every task has: files, a shell done_when (exit 0 = pass), depends_on.
- Tasks are small (one module/dir) and independently checkable.
- Mark parallel_ok=true only for tasks with disjoint files and no deps.
- If the goal is ambiguous or requires a destructive/irreversible action, output
  {"needs_human": true, "question": "..."} instead.
Output JSON only: Plan schema.
```

### 5.4 Executor preamble
```
You are executing one task toward a larger goal. First read .goal/state.md.

TASK: {task_json}
Constraints: touch only files in task.files unless unavoidable — if you must,
say so in notes. Run task.done_when yourself before finishing but do NOT mark
the task done; a checker will.
Before your final message, append to .goal/state.md under "Discoveries" anything
a later agent needs (changed signatures, env vars, surprises).
Final message is JSON only: {"task_id": str, "result": "ok|blocked|scope_changed", "notes": str}
Use "scope_changed" if the task is larger/different than described; "blocked" if
you need a human decision.
```

### 5.5 Checker
```
Decide whether this task passed its acceptance check.

TASK: {task_json}
COMMAND: {done_when}
EXIT CODE: {code}
OUTPUT (last 200 lines):
{output}

Exit 0 with no errors in output = passed. Anything else = failed; reason names
the failing test/error.
Output JSON only: {"task_id": str, "passed": bool, "reason": str}
```

---

## 6. Goal intake

1. Ask the model: "Propose up to 3 shell commands that verify this goal. If none
   exist, say so." → `evidence_cmds`.
2. If no commands: reject with "Goal must be checkable. Try: <suggested rewrite>."
   (Or ask the user to confirm the model's proposed rewrite.)
3. Store goal + evidence_cmds; goal text is never edited afterwards.

---

## 7. Invariants

- Goal is immutable; only the plan changes.
- Only the checker sets task `ok`; only the judge sets exit `done`.
- Judge/checker run commands; they never trust prose.
- Every exit writes one `# Exit` line to state.md.
- Executors never talk to the user; `blocked` → planner → `needs_human`.
- Replan only after a cycle, a checker double-fail, or blocked/scope_changed.

---

## 8. Build order

1. Phase 1: goal_state + worker suffix + evidence + judge + loop + 3 commands.
2. Phase 2: state.md, planner, executor, checker, ready_tasks scheduling.
3. Phase 3: stall detector, caps, tool removal, check-in, intake validation.
4. Phase 4: worktrees for parallel_ok tasks; merge, then re-run checker.

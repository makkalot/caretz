---
name: make-plan
description: Write an implementation plan, either for the work discussed so far in the current conversation or for instructions the user gives directly. Use this whenever the user asks to "make a plan", "plan this", "write up a plan", "how would we implement this", or wants a plan before any code changes. Asks the user about unclear parts with the AskUser tool instead of guessing.
---

# Make a plan

The output is a plan, given in the chat, that the user can review and later hand
to an agent to carry out: grounded in the real code, specific about what changes,
and free of open guesses. Planning is read-only: don't create or modify any files.

## 1. Work out what to plan

- **Instructions given with the request** (e.g. "plan adding retries to the HTTP
  client"): plan exactly that.
- **No instructions**: plan the work discussed in the current conversation. Go back
  through it and collect the goal, the decisions already made, constraints the user
  stated, and anything that was tried or rejected. Don't re-ask what the
  conversation already settled.

If you can't tell what the plan is for at all, ask with `AskUser` before doing
anything else.

## 2. Research before asking

Research the relevant code first so your questions are informed and the plan
points at real places. Research needs to find:

- The files, functions, and types the change touches, with `path:line`.
- Existing helpers, patterns, and tests to reuse rather than invent.
- Constraints and conventions from the project's agent/contributor instructions
  if present (`AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md`, README).
- How the project builds, tests, and lints (Makefile, package.json scripts, CI
  config), so the verification steps use real commands.

### Do the research in a subagent

Research often takes several rounds of searching and reading. Run it in a
subagent so all that file output stays out of the main conversation, and keep
only the findings:

1. Call `ListSubagents` and pick one whose description fits read-only code
   research (e.g. a researcher, explorer, or investigator). Prefer a read-only
   one; never pick one that edits files.
2. Hand it the job with `CallSubagent`. It doesn't see this conversation, so the
   task must stand on its own: the goal, the decisions already made, the
   specific questions above, and what to return: a concise report of findings
   with `path:line` references, reusable helpers, the build/test commands, and
   anything surprising or contradictory. Ask for findings, not file dumps.
3. If the findings raise new questions, send a focused follow-up (resume the
   same run if possible, so it keeps what it already read) instead of reading
   the files yourself. Independent areas can go to separate subagent calls.
4. Read a file yourself only to confirm a specific detail the plan depends on.

If no suitable subagent is listed, or subagents are disabled, do the research
yourself but keep it lean: search before reading, and read only the parts of
files you need. Mention in your reply that adding a read-only research subagent
would keep future planning sessions lighter.

Anything the code or a sensible convention answers is not a question. Decide it
and state the decision in the plan.

## 3. Ask about what's genuinely unclear

Use the `AskUser` tool for decisions that are the user's to make and change what
the plan says: scope (what's in or out), behavior choices with real trade-offs,
user-facing naming or UX, compatibility and migration, which of two viable designs
to take.

- Batch up to 4 questions in one call; ask a follow-up round only if the answers
  open new questions.
- Each question must stand on its own; the user may see only the picker. Name the
  thing you're asking about and why it matters in a short sentence.
- Give 2–4 concrete options, put the one you recommend first, and mark it
  recommended. Don't add an "Other" option; the user can always type one.
- Don't ask whether the plan is ready or whether to proceed.

If something is still uncertain after asking (or wasn't worth asking), record it
under **Open questions** with the assumption you made.

## 4. Present the plan

Reply with the plan in the chat, using this shape and keeping it as short as the
change allows:

```text
# <Outcome-focused title>

## Context
Why this change is needed: the problem, what prompted it, and the intended
outcome. Point at the current code with path:line.

## Decisions
The choices made, including the user's AskUser answers, each with a one-line
reason.

## Changes
Numbered steps, grouped by file or module. For each: what changes and why,
the functions/types involved, and existing helpers to reuse.

## Verification
How to prove it works end to end: tests to add, the project's test/lint
commands to run, and any manual check.

## Open questions
Remaining unknowns with the assumption taken. Omit the section if there are
none.
```

Don't start implementing until the user asks.

---
name: pr-review-tasks
description: Turn the review comments I left on a GitHub pull request into a local Markdown task list, one task per actionable comment, each with the problem, a proposed solution, and validation steps. Use this whenever the user wants to convert PR review comments, review feedback, or "my comments on the PR" into tasks, a todo list, a checklist, or an action plan, or asks to "go through my PR comments", "make tasks from the review", or "plan fixes for the review" — even if they don't say "skill" or "markdown".
---

# PR review comments → local task file

The goal is a Markdown file the user can work through (or hand to Claude later) where every
instruction-like review comment becomes a self-contained task: what's wrong, how to fix it,
and how to prove the fix works. Questions and chatter are left out — they need a conversation,
not a task.

## 1. Fetch the comments

Run the bundled script from the repository root (needs `gh` installed and authenticated):

```bash
python <skill-dir>/scripts/fetch_pr_comments.py [PR_NUMBER_OR_URL] [--author LOGIN] [--include-resolved]
```

- No PR argument → uses the PR for the current branch.
- No `--author` → uses the currently authenticated `gh` user (the user's own comments).
- Resolved threads are skipped by default since they're usually already handled.

If the user named a PR or author, pass it through. If `gh` is missing or unauthenticated,
tell the user (`gh auth login`) rather than guessing at comments.

The script returns JSON with the PR metadata and a list of comments, each carrying
`kind` (inline / review / conversation), `path`, `line`, `diff_hunk`, `body`, `url`,
`is_outdated`, and `thread_replies`.

## 2. Classify each comment

Decide for each comment whether it's an **instruction** (keep) or not (skip). Judge intent,
not punctuation:

**Instruction — make a task:**
- Imperatives and directives: "Rename this to…", "Extract this into a helper", "Add a test for the empty case".
- Soft directives: "should", "needs to", "let's", "please", "we must", "nit:", "consider…".
- Requests phrased as questions where the answer is obviously "yes, do it":
  "Can we move this to config?", "Could you add error handling here?", "Shouldn't this be async?" — these are instructions.
- GitHub ```` ```suggestion ```` blocks — the solution is literally provided; use it.

**Skip — no task:**
- Genuine questions seeking information or the author's reasoning: "Why did you choose X?",
  "What happens if the list is empty?", "Is this used anywhere else?"
- Praise, acknowledgements, "LGTM", "+1", "nice".
- Purely informational remarks with no requested change.

**Mixed comments:** if a comment has both a question and an instruction, keep the
instruction part as a task and ignore the question part. If a single comment contains
several distinct instructions, split it into separate tasks.

**Context from replies:** check `thread_replies`. If the PR author already said it's done,
or the discussion concluded the change isn't needed, mention that in the task's notes
rather than silently dropping it — the user wrote the comment and should decide.

When genuinely unsure whether something is an instruction, include it and add
`> ⚠️ Possibly a question — confirm this is wanted.` at the top of the task.

## 3. Investigate before writing solutions

For each kept comment with a `path`, open the file at that location in the local checkout
(use `diff_hunk` to orient if the line has moved; `is_outdated: true` means the code has
changed since the comment — locate the equivalent code and say so). Look at callers, tests
and related code as needed. A proposed solution grounded in the actual code is far more
useful than a generic one, so don't skip this step. For review-level or conversation
comments without a path, search the codebase for what they refer to.

Do **not** make the code changes — this skill only plans. The output is the task file.

## 4. Write the task file

Default path: `pr-review-tasks/PR-<number>.md` in the repo root (create the folder).
Use a different path if the user asks for one. Suggest adding the folder to `.gitignore`
if it isn't already ignored, but don't edit `.gitignore` without being asked.

Use this structure:

```markdown
# PR #<number>: <title>

Source: <pr url> · Branch: `<branch>` · Reviewer: @<author> · Generated: <YYYY-MM-DD>

**<N> tasks** from <M> comments (<K> skipped as questions/non-actionable)

---

## [ ] 1. <Short imperative title, e.g. "Validate input before parsing date">
<!-- comment-id: <id> -->

**Location:** `<path>:<line>` · [view comment](<url>)

**Problem**
> <the original comment, quoted verbatim>

<One or two sentences restating the problem in concrete terms, based on the code you read —
what is wrong or missing and why it matters.>

**Proposed solution**
<Concrete steps: which file/function to change and how. Include a short code sketch when it
makes the change clearer. If the comment contained a ```suggestion block, use it here.
If there are reasonable alternatives, list the recommended one first and mention the other briefly.>

**Validation**
- [ ] <Specific, checkable item — e.g. "Unit test: `parseDate('')` throws `InvalidDateError`">
- [ ] <Command to run — e.g. "`npm test -- date.spec.ts` passes">
- [ ] <Behavioral/manual check if relevant — e.g. "Submitting the form with an empty date shows the inline error">
- [ ] <Regression guard if relevant — e.g. "`grep -r oldName src/` returns nothing">

**Notes** *(only if needed: outdated comment, author replies, uncertainty)*

---
```

Guidelines for good tasks:
- **Problem** keeps the user's own words verbatim — that's the source of truth. The restatement
  adds precision; it must not change the meaning.
- **Validation** must be verifiable, not "make sure it works". Prefer: a test to add or run
  (use the project's actual test command — check `package.json`, `Makefile`, `pyproject.toml`, etc.),
  an observable behavior, a lint/type check, or a grep that should return nothing.
  2–4 items is typical; nits like renames may need just one or two.
- Order tasks by file then line, so related work sits together. Review-level and conversation
  comments go last.
- Number tasks sequentially; the checkbox in the heading lets the user tick off whole tasks.

End the file with a short skipped list so nothing feels lost:

```markdown
## Skipped (questions / non-actionable)
- [`<path>:<line>`](<url>) — "<first ~10 words of the comment>…"
```

## 5. Re-running on the same PR

If the task file already exists, don't overwrite the user's progress. Read it, use the
`<!-- comment-id: … -->` markers to find which comments already have tasks, keep existing
tasks (including their checked state and any edits) unchanged, and append tasks only for new
comments, continuing the numbering. Update the counts line and the skipped list.

## 6. Report back

Tell the user briefly: the file path, how many tasks were created, how many comments were
skipped, and anything that needs their attention (outdated comments, uncertain classifications,
threads where the author disagreed).

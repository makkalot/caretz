# Tell the main model which subagents it has

## Context

The main model never learns which subagents exist or what they are for.

- The `CallSubagent` tool is always offered (it is a session tool, `internal/agent/agent.go`), but its description is generic: "Run a configured .caretz/agents/<agent>.md subagent…" (`internal/tools/tools.go`, `CallSubagent`).
- The only system-prompt mention is one guideline: "If a loaded skill says to call @name … use CallSubagent" (`internal/skills/skills.go`, `toolGuidelines`).
- `ListSubagents` (`internal/agent/subagent_control.go`) lists *runs*, not the agents you can call.
- Agent descriptions are read in two places, and neither reaches the model:
  - startup discovery (`internal/app/runtime.go`, `configuredAgents`), which keeps only the names;
  - `@` autocomplete (`internal/app/path_autocomplete.go`), which is TUI only.
- A wrong guess fails with `subagent "x" not found; agents added after the session started need a new session` (`subagents.LoadFromSnapshot`), which does not list the valid names.

As a result, an agent's required `description:` frontmatter has no effect on the model. The model only delegates when the user types `@name` or a skill or AGENTS.md names the agent.

### Why a tool, not the system prompt
A catalog in the system prompt would leak into sessions that cannot call `CallSubagent`. Unnamed background runs (`newAsyncChild`) reuse the parent's `systemPrompt` but have `CallSubagent` disabled by `SubagentToolRegistry`, so they would see agents they cannot run.

`ListSubagents` is a main-session-only tool:
- `installSubagentControlTools` returns early for child sessions (`a.childSession`);
- it is installed only when the session has agents;
- it is built from the session's own agent snapshot.

The list is therefore always one the session can act on. The cost is that the model has to call the tool to find out, so its description must make that the obvious first step (step 2).

Out of scope: a built-in oracle agent. Users can already write `agents/oracle.md` with `model:`, `reasoning:`, and `plan: true`. That becomes a docs recipe instead (step 4).

## Approach

### 1. `ListSubagents` gets an `available` mode
In `internal/agent/subagent_control.go`:
- Add a boolean `available` parameter: "List the subagents you can run with CallSubagent, with what each is for."
  - `available: true` returns the catalog.
  - No arguments keeps today's behavior (runs).
  - `child_session_id` keeps showing one run, and is rejected together with `available`.
  - Parse `available` with the existing `lenientBool`, since local models send `"true"`.
- New `AgentSession.AvailableSubagents() ([]subagents.Definition, []error)`:
  - Iterate `a.subagentPaths()`, the snapshot `loadEnabledSubagent` uses, so the list matches what can run.
  - Drop names the profile disallows. Reuse the same allow check as `loadEnabledSubagent`, factored into `a.subagentAllowed(name)` so the two can't drift.
  - `LoadFile` each path. Broken files are left out of the list and reported in one trailing line (`skipped: foo.md (frontmatter missing description)`), so the model doesn't call an agent that will fail.
  - Sort by name.
- Output, plain text, one line per agent:
  ```text
  Subagents you can run with CallSubagent (each starts fresh and does not see this conversation; pass the full task and context):
  - code-reviewer: Review code changes for correctness and missing tests. [model openai/gpt-5.4, plan]
  - researcher: Find where a behavior is implemented and report file:line evidence.
  ```
  - The bracket shows `model` if set, `reasoning` if set, and `plan` if read-only, so the model can choose a read-only reviewer over an editing agent. Omit it when there is nothing to show.
  - Descriptions come from workspace files, which are untrusted: collapse whitespace and newlines and truncate to ~300 characters.
  - Cap at 100 agents with an `…and N more` line. It is an on-demand result, not a per-request cost, so the cap can be generous.
  - If the profile filters out every agent, return `No subagents are enabled in this session.`

### 2. Make the model actually call it
The tool only helps if the model uses it before deciding not to delegate. Update the descriptions the main session sees:
- `ListSubagents`: "List subagents. With available=true, lists the subagents you can delegate to with CallSubagent and what each is for — check it before starting a substantial task, and before calling CallSubagent with a name the user did not give. Without arguments, lists this session's subagent runs, newest first, including interrupted runs from before a resume."
- `CallSubagent` stays generic. Its static description also feeds the system prompt's "Available Tools" section, which background children reuse, so it must not mention `ListSubagents` (they don't have it).
  - Instead, `installSubagentControlTools` replaces the main session's `CallSubagent` definition with one that adds: "Call ListSubagents with available=true to see valid agent names." This uses the same `WithSessionTool` path, and the handler stays the same. Children keep the stock definition, and it is disabled for them anyway.
- No change to the system prompt, so background children see nothing new.
- Note: the system prompt's "Available Tools" section is built before the session installs its control tools, so `ListSubagents` is not in that section. The tool definitions sent with each request are what the model uses. Accept this rather than rebuilding the prompt.

### 3. Better not-found error
- `subagents.LoadFromSnapshot`: when the name is missing, say how to find the valid names, and list them when the list is short:
  `subagent "x" not found; available: code-reviewer, researcher; agents added after the session started need a new session`.
  - Over 20 names: `…; call ListSubagents with available=true for the full list`.
- `loadEnabledSubagent`: when the profile blocks a name, keep the current error and add the enabled names the same way.
- Never list agents the profile filters out. Pass the allowed set in, or filter in `loadEnabledSubagent` before formatting.

### 4. Docs
- `docs/customization/subagents.md`:
  - Say that the main model can list your agents with `ListSubagents` and delegates when a task matches, so write `description` as "when to use me".
  - Note that the list is fixed at session start.
  - Add a short "Second-opinion advisor" recipe (the oracle idea): `model:` on a different provider, `reasoning: high`, `plan: true`, and a prompt that reviews, debugs, and plans without editing.
  - Fix the inaccurate claim that `temperature` "is passed to the model when supported". It is parsed but never used. Either say it is ignored, or remove it from the example.
- `docs/customization/background.md` (or the tools page that documents `ListSubagents`): document the `available` parameter.
- Run `make doccheck`; it validates tool names. The recipe's frontmatter goes in a `markdown` fence, which is allowed in `subagents.md`.

## Critical files
- `internal/agent/subagent_control.go` (`available` mode, descriptions, `CallSubagent` override)
- `internal/agent/subagent.go` (`subagentAllowed`, `AvailableSubagents`, `loadEnabledSubagent` error)
- `internal/subagents/subagents.go` (`LoadFromSnapshot` error)
- `docs/customization/subagents.md`, plus the page that documents `ListSubagents`

## Verification
- Unit tests (`internal/agent`):
  - `ListSubagents` with `available: true` (and `"true"`) lists agents sorted, with descriptions and the model/reasoning/plan bracket.
  - The profile filter hides disallowed agents; `"*"` and nil keep all of them; a fully filtered profile returns the "No subagents" line.
  - A broken agent file is left out and reported on the skipped line.
  - Long or multi-line descriptions are collapsed and truncated; the cap line appears past 100.
  - No arguments still lists runs; `child_session_id` still shows one run; combining it with `available` errors.
  - The main session's `CallSubagent` definition mentions `ListSubagents`; a child's registry has neither `ListSubagents` nor an enabled `CallSubagent`.
  - An unnamed background child's system prompt is unchanged (no agent names, no `ListSubagents` mention).
- Unit tests (`internal/subagents`, `internal/agent`): the not-found and profile-blocked errors list only enabled names, and switch to the `ListSubagents` hint past 20.
- e2e: extend `e2e/features/teaching_caretz_my_project.feature` (or a sibling story). The fake LLM calls `ListSubagents` with `available: true`, sees the agent's description, then calls it by name.
- `make test`, `make lint`, `make doccheck`.
- Manual check: with `.caretz/agents/code-reviewer.md` present, `make run PROMPT="review my uncommitted changes" V=vv`.
  - The model calls `ListSubagents` with `available: true`.
  - It then delegates to `code-reviewer` without being told its name.
  - If it skips the list call, tune the `ListSubagents` description wording.

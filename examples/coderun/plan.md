# Plan: coderun

## Implementation status

Implemented with an embedded UI and fake-model HTTP integration tests.
The original design below records the scope. Confirmed API behavior:

- Existing session IDs restore history when `State.Dir` is configured.
- Context subscriptions provide complete messages, tool events, reasoning, and
  retries. `Session.SetAssistantDeltaHandler` exposes provisional assistant text;
  the app keeps a snapshot under the project mutex and sends changes every 250 ms.
  Request boundaries, retry clear signals, completed messages, and cancellation
  discard provisional text. The UI also shows elapsed time and tool commands.
- Permission and status events use audit subscriptions; permission polling is
  unnecessary. Turn completion uses a small per-project state revision, checked
  every 250 ms by connected browsers. Pending requests are included on reconnect.
- `SystemPrompt` replaces the default base prompt; caretz still adds its tool
  instructions and the session workspace.
- The file helper is `tools.IsPathSafe(filePath, cwd)`, in that argument order.
  The viewer also uses `os.Root` to contain opens during filesystem changes.
- Stop closes the stop channel and cancels the turn context, interrupting model
  requests as well as tool execution. Server shutdown waits for active turns.
- The configured projects root is shared: this demo does not isolate projects
  from each other. A path permission may grant access beyond that root.
- Sandbox-off escalation is disabled. Network permission cards authorize proxy
  destinations while keeping Bash sandboxed; `-allow-pypi` preapproves PyPI hosts.
- The CLI command box uses the public `Session.RunCommandWithPermissions` API
  without calling the model. It shares the turn lock, cancellation, and proxy
  permission cards, and returns bounded stdout/stderr plus exit status. It is
  not a terminal; stdin can be supplied with a pipe. The last command result
  survives browser reconnects in memory, until replaced by the next operation.

## Goal

A single-binary web app (`go run .`) where a user creates projects, chats with
an agent that writes small Python apps into a per-project workspace, runs them,
and sees files and output live. It demonstrates the `caretz` library API:
`NewClient`, sessions, context-event subscriptions, permission prompts, and
cancellation.

## Rules (apply to every example)

- Import only public caretz packages: `github.com/makkalot/caretz`,
  `.../lib/eventing`, `.../lib/llm`, `.../lib/tools`, `.../lib/types`.
  No `internal/*`, and no `Session.AgentSession()` (it returns an internal type).
- Standard library only. HTTP is `net/http` with Go 1.22+ patterns
  (`mux.HandleFunc("POST /api/projects/{id}/messages", ...)`). The UI is
  vanilla HTML/CSS/JS in `web/`, served via `embed.FS`. No npm, no CDN.
- Own module `github.com/makkalot/caretz/examples/coderun`, with
  `replace github.com/makkalot/caretz => ../..`, using the root `go.mod`'s go version.
- The server never invokes `os/exec`. Code runs through caretz's sandboxed Bash
  tool or the public sandboxed command API.
- Edit files only with file edit tools. No commits without approval.

## Public API Used

- `caretz.NewClient(ctx, caretz.ClientOptions)` — `caretz.go:167`
  - `ClientOptions{Model, WorkspaceDir, SystemPrompt, ToolMode, Sandbox, State, EnabledTools, DisableTasks}` — `caretz.go:109`
  - `ModelOptions{BaseURL, APIKey, Model, ContextWindow, Timeout}` — `caretz.go:25`
  - `SandboxOptions{WritableRoots, NetworkAllowlist, AskSandboxOff}` — `caretz.go:47`
  - `StateOptions{Dir}` — `caretz.go:75`
- `Client.NewSession(id, caretz.SessionOptions{Adapter, WorkspaceDir, PermissionTimeout})` — `caretz.go:131,325`
- `Session.CallUntilStopWithPermissions(ctx, prompt, stop)` — `caretz.go:445`
- `Session.RunCommandWithPermissions(ctx, command)` — model-free command
  execution in the configured sandbox, with proxy permissions and cancellation.
- `Session.PendingPermissions()` / `Session.ResolvePermission(caretz.PermissionResolution)` — `caretz.go:452,459`
  - `eventing.PermissionRequest` fields: `RequestID, ToolName, Capability, Target, Command, Reason, ExpiresAt, Details` — `lib/eventing/event_types.go:126`
  - decisions: `eventing.PermissionDecisionAllow`, `...AllowSession`, `...Deny` — `lib/eventing/event_types.go:116`
- `Session.SubscribeContextEvents(types...)` → `eventing.Subscription` (`Events() <-chan *types.Event`, `Close()`) — `caretz.go:501`, `lib/eventing/event_loop.go:30`
- Event type constants in `lib/eventing/event_types.go:18-71`. Expected to be needed:
  `LLMUserMessageCreated`, `LLMAgentMessageCreated`, `LLMToolCallResultCreated`,
  `LLMAgentResponseCreated`, `LLMAgentInterruptedResponseCreated`,
  `PermissionRequested`, `PermissionResolved`, `StatusUpdated`. Before relying on
  them, check which ones go to the *context* stream and which to the *audit* stream
  (`SubscribeAuditEvents`, `caretz.go:494`) by reading where they're emitted in
  `internal/agent`. Reading is fine; importing is not.
- `Session.Messages()` for history on page reload — `caretz.go:529`
- `tools.IsPathSafe(path, root)` — `lib/tools/tools.go:18`

## Files

```text
examples/coderun/
  go.mod
  main.go          flags/env, NewClient, http.Server, graceful shutdown
  projects.go      project registry: create/list, id → *caretz.Session, per-project turn state
  handlers.go      HTTP handlers
  events.go        SSE fan-out: context subscription + permission polling → JSON frames
  prompt.go        system prompt + default Run command
  web/index.html   layout: projects | chat | files | output
  web/app.js       fetch + EventSource, render frames, permission cards
  web/style.css    light/dark via prefers-color-scheme, works at ≤500px
  server_test.go   handler tests with a fake model server
  README.md
```

## Steps

1. **Config** (`main.go`): flags `-addr` (default `127.0.0.1:8080`), `-data`
   (default `./data`), `-model`, `-base-url`, `-api-key`, `-context-window`, and
   `-allow-pypi`. Env fallbacks are `OPENAI_API_BASE_URL`, `OPENAI_API_MODEL`, and
   `OPENAI_API_KEY`, matching caretz's names. Bind to loopback by default.
2. **Client**: `caretz.NewClient` with:
   - `WorkspaceDir: <data>/projects` (each session overrides it with its own dir)
   - `State: caretz.StateOptions{Dir: <data>/state}`
   - `Sandbox: caretz.SandboxOptions{WritableRoots: [<data>/projects], AskSandboxOff: false,
     NetworkAllowlist: allowPypi ? ["pypi.org", "files.pythonhosted.org"] : nil}`
   - `EnabledTools`: Read, Write, Edit, Ls, Find, Grep, Bash (no web, no subagents);
     `DisableTasks: true`
   - `SystemPrompt` (`prompt.go`): "You build small, self-contained Python 3 apps.
     Entry point is main.py. Prefer the standard library. After writing code, run it
     with `python3 main.py` (pass sample input via stdin if it reads input) and
     fix errors. Keep a short README.md." Check whether `SystemPrompt` replaces or
     extends the default (`systemPromptWithWorkspace`, `caretz.go:683`) and word it accordingly.
3. **Projects** (`projects.go`):
   - `Project{ID, Name, Dir, CreatedAt}`, persisted as `<data>/projects.json`
     (write a temp file, then rename).
   - IDs are a slug of the name plus a short random suffix; validate them against
     `^[a-z0-9-]{1,64}$` before any filesystem use.
   - The session is opened lazily per project with `client.NewSession(id, SessionOptions{
     Adapter: "coderun", WorkspaceDir: dir, PermissionTimeout: 10 * time.Minute})`.
     Check whether reopening an existing ID resumes history from `State.Dir`. If it
     doesn't, record that under API gaps.
   - Per-project turn state: `running bool`, `stop chan struct{}`, guarded by a mutex.
     Only one turn at a time per project; a second POST gets `409`.
4. **HTTP API** (`handlers.go`), all JSON:
   - `GET /` → embedded `web/`
   - `GET /api/projects`, `POST /api/projects {name}`
   - `POST /api/projects/{id}/messages {text}` → start a goroutine running
     `CallUntilStopWithPermissions(ctx, text, stop)` and return `202` right away.
     On completion, publish a `turn_done` frame (or `turn_error` with the message).
   - `POST /api/projects/{id}/run` → execute `python3 main.py` directly through
     `RunCommandWithPermissions`, with no model call. Shares the same command
     result handling as the custom command box.
   - `POST /api/projects/{id}/commands {command}` → model-free sandboxed command,
     returning `202`. Completion state includes stdout, stderr, exit code and
     timeout status. Uses the same single-operation slot, Stop, and permission UI.
   - `POST /api/projects/{id}/cancel` → close `stop` (use a `sync.Once`).
   - `GET /api/projects/{id}/permissions` and
     `POST /api/projects/{id}/permissions/{reqID} {decision}` → `ResolvePermission`.
     Accept only allow / allow_session / deny.
   - `GET /api/projects/{id}/files` → walk dir (skip dotfiles, virtual environments,
     dependency folders, caches, build output and temporary files; cap 500 visible entries);
     `GET /api/projects/{id}/files/{path...}` → `tools.IsPathSafe(path, dir)`, cap 256 KB,
     `text/plain` only (use `tools.IsBinaryContent` to refuse binaries).
   - `GET /api/projects/{id}/history` → `Messages()` mapped to `{role, text}`.
5. **SSE** (`events.go`): `GET /api/projects/{id}/events`
   - Headers `text/event-stream`, `Cache-Control: no-cache`; flush after each frame.
   - On connect, `SubscribeContextEvents(<types>)` and close it when the request ends.
   - Map each `*types.Event` to a frame
     `{kind: "user"|"assistant"|"tool_call"|"tool_result"|"permission"|"status"|"done", ...}`.
     Decode payloads using the payload structs or JSON shapes from `lib/eventing`. Keep
     the mapping in one function with a table test.
   - Permissions: if `Permission.Requested` reaches the context or audit
     subscription, use it. Otherwise poll `PendingPermissions()` every 500 ms during a
     turn and send new/removed requests as diffs. Write down which one works.
   - Send a heartbeat comment every 15 s.
6. **UI** (`web/`):
   - Three columns on desktop, stacked tabs at ≤500px. Uses CSS tokens with light and dark themes.
   - Chat: streams assistant text, shows tool calls as collapsible rows (`Write main.py`,
     `Bash python3 main.py`), has a Stop button while a turn runs, and disables the input during a turn.
   - Permission card: tool, capability, target/command, reason, expiry countdown,
     and Approve / Approve for session / Deny buttons.
   - Files: tree plus a read-only viewer that refreshes on `tool_result` frames for Write/Edit.
   - Output: Run button and the last run's output.
   - About 300 lines of JS. No frameworks.
7. **Shutdown**: on SIGINT, stop all running turns, then `client.Close()`, then `srv.Shutdown`.
8. **Tests** (`server_test.go`): start an `httptest` fake OpenAI-compatible
   `/chat/completions` server that returns scripted responses (one Write tool
   call, then a final message). Point `ModelOptions.BaseURL` at it, set
   `Sandbox.Disabled: true` in tests only, then check that POST messages → SSE frames
   arrive, the file appears in the files listing, path traversal on the files endpoint
   returns 400, and a concurrent POST returns 409. Before writing the fake, look at
   how the root package tests fake the model (`caretz_test.go`).

## API Gaps / Open Questions

- **Running code directly.** The public `Session.RunCommandWithPermissions` API
  now provides model-free shell commands through the configured sandbox. It
  serializes with model turns and prompts for blocked proxy destinations.
  Both Run main.py and the command box run directly without a model turn.
  Arguments and piped stdin are supplied by the user in the command box.
- **Which stream carries what.** Confirm the context vs audit split for permission and
  status events (step 5).
- **Session resume.** Confirm `NewSession` with an existing ID and `State.Dir` restores history.
- **Streaming granularity.** Assistant deltas now use the public
  `Session.SetAssistantDeltaHandler`. Tool calls appear after the model finishes
  their arguments; Bash output arrives when the tool finishes. Models without
  streaming responses still show elapsed time while the server waits.

## Verification

- `cd examples/coderun && go build ./... && go vet ./... && go test ./...`; `gofmt -l .` is empty.
- Manual with a real model: `go run . -data /tmp/coderun-demo`, then create
  "water-tracker" and ask for "a CLI that logs water intake to a JSON file".
  Confirm that `main.py` appears, Run shows output, and Stop interrupts a turn.
- Sandbox: ask it to `pip install requests` without `-allow-pypi` → a permission
  card appears. Deny → the agent reports it was blocked. Ask it to write `../escape.txt`
  → it's refused.
- UI at desktop width and at ≤500px, in light and dark mode.

## Follow-ups (not part of this example)

`make examples`, CI build job, `docs/examples.md` (+ `site/data/docs_groups.yaml`,
README docs list), `make doccheck`, and the possible public `lib/sandbox` runner.

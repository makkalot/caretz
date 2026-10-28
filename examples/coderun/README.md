# coderun

A small web app where anyone can build a Python app by describing it. Type
"a CLI that tracks my daily water intake", watch the agent write `main.py`, then
press **Run** to see the output. Everything happens in a sandboxed folder for
each project.

From this checkout, start the server with an OpenAI-compatible model:

```sh
cd examples/coderun
export OPENAI_API_KEY=your-key
make run ARGS='-model gpt-5'
```

Open **http://127.0.0.1:8080**, create a project, and describe your app. Select a
file to preview it; **Run main.py** executes `python3 main.py` directly. To try a CLI
yourself, enter a command in **Try a CLI command** and press **Execute command**.
**Stop** cancels the active request or command and any pending permission.
Projects and conversation history are restored from `./data` after a restart.

Commands run directly in the project workspace without a model turn. Try your
app's arguments, or pipe input into it:

```text
python3 main.py --help
python3 main.py add 250
printf '250\n' | python3 main.py
```

Output shows stdout, stderr, and exit status when the command finishes. Commands
have a two-minute timeout and share an execution slot with chat. This is a
command runner, not an interactive terminal: supply input with a pipe. Manual
commands do not enter the agent's conversation history; the last result remains
available on reconnect until another operation replaces it.

```text
$ go run . -addr :8080 -data ./data
coderun listening on http://localhost:8080

Browser:
  [Projects]        [Chat]                              [Files]     [Output]
  > water-tracker   you: a CLI that tracks water...     main.py     $ python3 main.py
    pomodoro        agent: Writing main.py (Write)      README.md   Logged 250ml. Today: 1.25L
                    agent: Running it (Bash)
                    ┌ Permission: network → pypi.org ┐
                    │        [Approve]  [Deny]       │
                    └────────────────────────────────┘
```

## Approach

This example shows the **`caretz` Go library**: your server owns the HTTP and UI
layers, and caretz runs the agent inside your process. It doesn't read caretz's
config files; the server passes model and sandbox settings to it directly.

- **One `caretz.Client`** for the process, created with `caretz.NewClient`.
  The model comes from flags/env, and `State.Dir` stores sessions on disk so
  projects survive a restart.
- **One session per project** via `client.NewSession(projectID, ...)`, with
  `WorkspaceDir` set to `data/projects/<id>`. The agent writes only there.
- **Streaming**: `Session.SetAssistantDeltaHandler` supplies live assistant text,
  sent to the browser every 250 ms while it changes. Context subscriptions supply
  model reasoning, retry notices, completed messages, tool calls, and results.
  The page shows elapsed time and the current tool command or file path.
  Permission and status updates come from `Session.SubscribeAuditEvents`.
  Reconnecting browsers receive history, partial assistant text, turn state,
  and pending permissions. Models that do not stream show elapsed time until
  their response arrives.
- **Permissions in the UI**: when `pip` reaches an unapproved destination, the
  filtering proxy raises a network permission request. Approving it opens that
  destination through the proxy while the command stays sandboxed. The server reads
  `Session.PendingPermissions()`, the page shows an Approve/Deny card, and the
  click goes back through `Session.ResolvePermission`.
- **Stop button**: each turn runs with `CallUntilStopWithPermissions`, and Stop
  closes its stop channel and cancels its context to interrupt an in-flight call.
- **Run buttons**: **Run main.py** executes `python3 main.py`; **Execute command**
  runs the command you entered. Both use `Session.RunCommandWithPermissions`
  through the configured sandbox without calling the model. Both use the
  existing permission cards; the server never invokes `os/exec`.

## Safety

The sandbox's writable root is the projects directory (shared across projects).
Network destinations require approval through the filtering proxy;
`-allow-pypi` preapproves the two PyPI hosts. Sandbox-off escalation is disabled,
so a blocked command cannot request an unsandboxed retry.
Approving a path permission can still allow access beyond
the configured roots. The file viewer is read-only, path-checked with
`tools.IsPathSafe`, and opened through `os.Root`. It refuses symlinks, hidden
files, binary content, and files larger than 256 KB. The file list skips virtual
environments, dependency folders, Python bytecode, build output, temporary files,
and pip caches (including macOS `Library/Caches`). These files remain on disk.
The list shows up to 500 visible files. This is a local or demo app: it has no user accounts, so don't
expose it to the internet as-is.

To try network approvals, start without `-allow-pypi` and ask:

```text
Create a .venv in this project and install requests using .venv/bin/python -m pip.
```

Approve `pypi.org:443` and `files.pythonhosted.org:443` when requested, or deny
them to keep access blocked. A fresh environment and an uncached package make
the requests visible. **Approve** allows the destination for the command;
**Approve for session** also allows it for later commands in that session.
The process stays inside the sandbox in both cases.

## Configuration and checks

Use `-data` to choose storage, `-addr` to choose the listen address (default
`127.0.0.1:8080`), and `-allow-pypi` to preapprove `pypi.org` and
`files.pythonhosted.org` through the proxy. Model flags `-model`, `-base-url`, and `-api-key` fall
back to `OPENAI_API_MODEL`, `OPENAI_API_BASE_URL`, and `OPENAI_API_KEY`.
`-context-window` defaults to 200000 tokens. The server does not load caretz
configuration files. Python 3 and the platform's caretz sandbox prerequisites
must be installed to run generated apps.

From this directory, run the tests with a fake model; no API key is needed:

```sh
make build
make test
```

Set `CARETZ_RUN_SANDBOX_INTEGRATION=1` when running `make test` to also verify
proxy approval and denial using the native sandbox and a local HTTP destination.

The binary builds to `bin/coderun`. Use `make install` to copy it to `~/bin`,
or set `INSTALL_DIR=/your/bin`. `make clean` removes the build; saved projects
and sessions stay in the data directory. Set `GO=/path/to/go` to select a Go
toolchain.

See [plan.md](plan.md) for implementation notes and public API limitations.

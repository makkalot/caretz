---
title: "Tools and sandbox"
weight: 30
description: "Choose tools, add your own, and configure the sandbox."
---

# Tools and Sandbox

Built-in tools are enabled by default. To disable them:

```go
ToolMode: caretz.ToolModeNone
```

Use `EnabledTools` to keep a selected built-in subset, or `ToolModeNone` when
the application supplies every tool itself. Fewer tool definitions reduce the
fixed request overhead for a smaller model.

Custom tools can be added with `ClientOptions.Tools`; see
[Add tools to your Go app](../tools/custom-go.md). Sandbox roots are controlled
with `ClientOptions.Sandbox`; when no roots are set, the workspace is readable and
writable. Bash remains sandboxed unless you explicitly set `Sandbox.Disabled`.

## Shared Session Execution

To let Bash calls in one live session share temporary files and background
processes, enable the opt-in supervisor:

```go
Sandbox: caretz.SandboxOptions{
	SessionExec:   true,
	AllowLoopback: true,
	HiddenPaths:   []string{"/path/to/application-secrets.json"},
	Env:           []string{"GOPROXY", "CI=true"},
},
```

Child agents share their parent's executor. Each session returned by
`NewSession` owns a separate executor, and `Session.Close` or `Client.Close`
stops its remaining process groups. Keep the sandbox enabled for untrusted
commands; setting both `SessionExec` and `Disabled` retains supervision and
shared `$TMPDIR` state but runs commands with the embedding process's native
permissions. Linux sandbox sessions can share services on their private
loopback interface. `AllowLoopback` lets restricted macOS sessions bind local
TCP servers while proxy-aware HTTP(S) clients still require permission for the
exact destination port; use unrestricted networking for raw TCP clients.

## Hidden and Read-Only Paths

`HiddenPaths` masks embedding-host configuration or credential files from
sandboxed commands; caretz's CLI supplies its loaded home and project
configuration paths automatically. Linux masks paths that exist at command
startup and skips absent paths. Persistent Linux execution refreshes masks
between commands by recreating the supervisor when necessary, losing background
jobs and temporary state. A sensitive path created during a running command
can remain visible until refresh; create it before execution when this window
is unacceptable. macOS hidden-path rules also deny paths created later.

Persistent history under `<State.Dir>/sessions` is readable and read-only when
inside the active workspace, and hidden when outside it. The default
`<workspace>/.caretz/sessions` is also read-only, even when the current client
stores its own state elsewhere. Storage reached through a symlink is classified
by its resolved location. Caretz's persistence can still append events; agents
can inspect failures and change source files without editing the history.
`SandboxOptions.ReadOnlyPaths` adds write restrictions for other paths without
granting read access. Explicit `HiddenPaths` override readability, and write
approvals cannot override read-only paths. Disabled Bash retains native access;
built-in file tools continue enforcing these restrictions.

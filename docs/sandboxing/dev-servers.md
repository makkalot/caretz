---
title: "Dev servers and background jobs"
weight: 40
description: "Keep a server running between commands, and how cleanup works."
---

# Dev Servers and Background Jobs

Commands in a session share one sandbox, so a server started in the background
keeps running while Caretz exercises it, and stops when the session ends:

```sh
caretz -p "start the test server in the background, then exercise its API"
```

Caretz reports how to watch and stop it:

```text
Background command started.
PID: 12345
Log: $TMPDIR/caretz/processes/12345.log
Stop: kill -TERM -- -12345
```

Use `--session-exec=false` to run each command on its own instead.

## What Commands Share

Each Bash invocation still uses a fresh `bash -c`, so `cd`, shell variables,
aliases, and shell options do not leak into later calls. The commands do share
the session's temporary storage, network state, localhost services, and
background processes. Foreground timeouts work as usual. A background request
cannot specify `timeout_seconds`, and one live session can run at most eight
background jobs. Linux shares the private namespace's literal `/tmp`. macOS
uses a session-specific `$TMPDIR`; literal `/tmp` remains visible but is not
writable. Sandbox-disabled runs retain their native temporary-directory
behavior. Parent and child agents intentionally share this state. Separate
top-level sessions do not.

Sockets work on both platforms as long as they live somewhere the command can
write: `$TMPDIR`, the workspace, or a writable root. Sockets elsewhere, such as
the Docker socket or an SSH agent, still need their explicit grants. On macOS
the private temporary directory lives under `/private/tmp` rather than the long
per-user `/var/folders` path, so nested socket paths stay below the 104-byte
Unix socket limit.

## Process Cleanup

Esc or Ctrl-C stops the current turn and kills background command process
groups launched by that turn, including its subagents and requested async work.
Jobs from earlier successfully completed turns keep running. Schedules remain
configured, and their later runs are independent of the stopped turn.
An async report already running in the session is also independent: Esc cancels
your waiting prompt rather than that report.

Turn cancellation kills by process group. A process that calls `setsid` or
double-forks and leaves that group may survive a stop.

Caretz terminates remaining process groups when the session is cleared or
closed. With sandboxing enabled on Linux, the supervisor is PID 1 inside a
private PID namespace and dies with Caretz, so processes remain contained even if
they daemonize and leave their original process group, and even if Caretz itself
is killed or crashes. macOS has no equivalent PID namespace: normal background
services are stopped, but a deliberately detached daemon may survive session
cleanup. Process cleanup on macOS, and on either platform when sandboxing is
disabled, is therefore best effort.

With `--session-exec=false`, each sandboxed command runs on its own, and a job it
leaves behind, such as `nohup ./counter.sh &`, keeps running across later
commands. Caretz stops these jobs when it exits, including when its terminal is
closed. On Linux this also catches jobs that detach with `setsid`. On macOS,
only jobs that stay in their command's process group are stopped. If Caretz is
killed outright or crashes, these jobs keep running. Commands you
approved to run outside the sandbox are not stopped, so start a service you
want to keep that way, or from your own terminal.

Persistent processes also extend network exposure; unrestricted networking can
maintain outbound connections and listen on the host network. On macOS,
loopback listeners use the host network namespace and are reachable from the
host for as long as they are running.

Session execution contains immediate command activity, but workspace writes
remain a trust bridge. Review scripts, dependency hooks, generated binaries,
and configuration before running changed workspace content outside Caretz.

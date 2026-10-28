---
title: "Sandboxing"
description: "What commands can do by default, and how to grant more."
---

# Sandboxing

Caretz runs every shell command in an OS sandbox, so you can let it work
unattended without worrying about the rest of your machine. Out of the box,
commands can:

- read and write the current workspace;
- read installed tools, compilers, and dependencies;
- use their own temporary storage.

They cannot write elsewhere, reach the network, or talk to Docker until you
allow it. When a task needs one of those, grant exactly that:

```sh
caretz --network-allow pypi.org --writable-root /tmp/caretz-output
```

## Defaults

The sandbox applies to `Bash` and to command-backed tools such as `Find` and
`Grep`. Caretz's file tools also perform their own path checks. The model API,
web search, and fetch tools run in the Caretz process and do not use the Bash
network policy.

| Capability | Default |
| --- | --- |
| Sandbox | Enabled |
| Workspace | Readable and writable |
| Additional filesystem roots | None, except discovered skill and agent resource directories are added read-only |
| Restricted network | No direct network access |
| HTTP/HTTPS allowlist | Empty |
| Docker daemon | Not enabled |
| Bash timeout | 120 seconds; a tool call can request up to 600 seconds |
| Captured command output | Capped at 1 MiB by the sandbox runner; the Bash tool returns a smaller head-and-tail view |
| Persistent execution | Enabled; `--session-exec=false` or the `ops` profile runs each command on its own |

Plan mode (`caretz --plan`, or Shift+Tab in an interactive session) makes
mutation tool calls fail and runs each Bash command in a one-off sandbox that
mounts the workspace and every configured root read-only.

On Linux the sandbox is a strong boundary. On macOS it is a guardrail against
mistakes, not a boundary for hostile code; see [Linux](linux.md) and
[macOS](macos.md). Windows is not supported yet.

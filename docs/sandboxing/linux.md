---
title: "Linux"
weight: 80
description: "Namespaces and seccomp, and fixing Linux sandbox errors."
---

# Linux Sandbox

On Linux, Caretz isolates commands with namespaces and seccomp: they cannot see
your other processes, and anything they start dies with the session.

## How It Isolates

The persistent supervisor is PID 1 inside private user, mount, PID, network,
IPC, UTS, and cgroup namespaces. Caretz constructs a Bubblewrap-style mount
namespace. System paths needed to execute normal commands, including `/usr`,
`/bin`, `/lib`, `/etc`, `/run`, and `/opt`, are exposed read-only; `/tmp` is a
fresh in-memory filesystem. The workspace and explicitly writable roots are
bind-mounted writable. A seccomp filter blocks dangerous operations such as
mounting filesystems, loading kernel modules, changing system time, creating
device nodes, and rebooting. Caretz skips the seccomp layer when it detects
that it is already running inside a container, where the outer runtime may
restrict those operations. Caretz uses Bubblewrap's additional
`--disable-userns` hardening when available and warns at startup when an older
version cannot provide it.

## Troubleshooting

Caretz requires the kernel/user-namespace capabilities needed by its
Bubblewrap-style sandbox. Container environments may also restrict namespace or
seccomp operations. Use `-vv` and inspect the Bash error before changing
policy:

```sh
caretz -vv -p "run the tests"
```

With default settings, sandboxed Bash has no direct network; add only the
required destinations with [`--network-allow`](network.md). Unmounted paths
look missing inside the sandbox, so `No such file or directory` for a path that
exists on the host usually means it needs a [read or write root](files.md).

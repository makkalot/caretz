---
title: "ops"
weight: 20
description: "Maintain the machine: packages, services, disk cleanup."
---

# Maintain the Machine with `ops`

Use the built-in `ops` profile when the job is the machine itself rather than
a project: installing or removing software, reclaiming disk space, or fixing a
service.

```sh
caretz --profile ops -p "free up disk space: remove old Homebrew versions and caches"
caretz --profile ops -p "make ollama listen on my tailscale address"
```

Commands stay sandboxed but can see the real network, processes, and package
manager directories. Anything more, like `sudo` or `/etc`, asks you first and
then runs on your terminal; see
[Run one command outside](../sandboxing/escalation.md).

## What It Grants

- Write access to the Homebrew prefix and its caches and logs, plus
  `~/.local`, `~/.cache`, `~/.cargo`, `~/go`, and `~/.npm` when they exist.
- The real machine's view: the host network, so `ip addr`, `ss`, and
  `tailscale` see real interfaces and ports, and the host's processes and
  daemon sockets, so `ps` and `tailscale status` work. On Linux, the system
  D-Bus is reachable when Caretz doesn't run as root, so `systemctl status`,
  `systemctl list-units`, and `loginctl` answer from the real host; anything
  that changes state through it still needs polkit's approval. Sockets that
  control the host outright, such as the user bus, user systemd, and container
  engines, stay hidden. GPU device nodes are visible too, so `nvidia-smi`,
  `tegrastats`, and ROCm tools report the real hardware. Outbound traffic
  isn't filtered. Commands run one at a time rather than in a persistent session,
  because session execution gives commands a private process list.
- The same SSH, SSH agent, and GitHub CLI grants as [`coding`](coding.md), read
  access to macOS log directories, and an operations-focused prompt.

Everything else (system services, `/etc`, `sudo`, Docker, `apt`) stays out of
reach until you approve a command.

## Customize It

Settings under `profiles.ops` merge over the built-in:

```json
{
  "profiles": {
    "ops": {
      "sandbox": {"allowDocker": true}
    }
  }
}
```

This machine's `ops` sessions then reach the host Docker daemon without asking;
Docker access is effectively root on the host. `"sandbox": {"askSandboxOff": false}`
turns escalation off, and `"writableRoots": []` removes the package-manager
grants. Leave `sessionExec` off for `ops`: a persistent session gets a private
process list and hides the host's `ps` and daemon sockets.

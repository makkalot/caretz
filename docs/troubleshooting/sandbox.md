---
title: "Sandbox errors"
weight: 30
description: "Permission denied, blocked network, and disabling the sandbox."
---

# Sandbox Errors

Use `-vv` and inspect the Bash error before changing policy. Platform-specific
startup problems are covered in [Linux](../sandboxing/linux.md) and
[macOS](../sandboxing/macos.md).

## Network Blocked

With default settings, sandboxed Bash has no direct network. Add only the
required destinations:

```sh
caretz --network-allow pypi.org \
      --network-allow files.pythonhosted.org
```

An ordinary allowlist entry controls HTTP/HTTPS through Caretz's
restricted-network proxy. Use an explicit `tcp://HOST:PORT` entry for a
SOCKS-aware TCP client. Plain `ssh` needs
`-o "ProxyCommand=$CARETZ_SSH_PROXY_COMMAND"`, otherwise it fails with
`Operation not permitted` instead of asking for approval. See
[Network access](../sandboxing/network.md).

## Permission Denied When Writing

The workspace is the default writable root. Add another existing directory with
`--writable-root DIR` or `profiles.general.sandbox.writableRoots`. A `readRoot`
never grants writes. In `--plan` mode Bash and mutation tools remain read-only
even when writable roots are configured.

## Disabling the Sandbox

`CARETZ_DISABLE_SANDBOX=1` makes Bash run with the Caretz process's normal OS
permissions. This is a last-resort diagnostic or explicitly trusted execution
mode, not a fix for a missing root or network host. Unset it after testing.

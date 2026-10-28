---
title: "Permanent settings"
weight: 70
description: "Save grants in config, or turn the sandbox off."
---

# Permanent Settings

Put the grants you always need under the `general` profile in your home or
workspace config:

```json
{
  "profiles": {
    "general": {
      "sandbox": {
        "readRoots": ["../shared-docs"],
        "writableRoots": ["/tmp/caretz-output"],
        "networkAllowlist": ["*:80", "*:443"],
        "sessionExec": true,
        "allowLoopback": true,
        "unrestrictedNetwork": false,
        "allowDocker": false
      }
    }
  }
}
```

`--read-root`, `--writable-root`, and `--network-allow` add to these values for
one run; other flags override them. [Runtime profiles](../profiles/_index.md)
let you keep separate policies for coding, ops, and CI.

## Turn the Sandbox Off

For a trusted run or last-resort diagnosis:

```sh
CARETZ_DISABLE_SANDBOX=1 caretz -p "your task"
```

Shell commands then run with your normal permissions. It does not merely relax
one sandbox rule. Caretz's file tools retain their path checks, and `--plan`
still disables mutation tools, but shell commands are no longer contained.
Prefer a narrower grant, or [`--ask-sandbox-off`](escalation.md) when only an
occasional command needs the host. Unset it after testing.

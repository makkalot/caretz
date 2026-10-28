---
title: "macOS"
weight: 90
description: "sandbox-exec, its limits, and fixing macOS sandbox errors."
---

# macOS Sandbox

On macOS, Caretz uses `sandbox-exec` to restrict files and network. It is best
effort: a guardrail against mistakes, not a boundary for hostile code.

## How It Isolates

Caretz generates a `sandbox-exec` profile that denies filesystem reads and
writes by default. It grants read access to the workspace, configured roots,
required system paths, installed tool roots such as `/opt`, and a private
temporary directory. Apple developer-tool shims may also replace `xcrun_db`
through a randomly named `xcrun_db-*` file in the system temporary directory,
so Caretz grants those names without exposing other temporary files.
Writes are otherwise limited to that private directory, the workspace, and
configured writable roots. `/Applications`, `/opt`, and `/nix`
remain broad read-only compatibility grants.

`sandbox-exec` provides only best-effort process and filesystem restrictions:
it has no equivalent private PID, mount, or network namespaces. Do not treat
the macOS mode as a boundary for fully hostile code or sensitive same-user
data.

## Troubleshooting

macOS must provide `/usr/bin/sandbox-exec`. If the OS refuses the generated
profile, Caretz reports `macOS refused to apply sandbox profile`. Nested
sandbox environments may prevent macOS from applying another Seatbelt profile.

Restricted commands cannot read arbitrary home files, write literal `/tmp`,
connect directly to localhost, or use the internet without an explicit grant.
Grant required paths explicitly, use `$TMPDIR`, select `--allow-loopback` for
local development servers and integration tests, and select
`--unrestricted-network` for broader direct networking:

```sh
caretz --allow-loopback -p "start the API server and run the integration tests"
```

# Bring macOS Sandbox Policy Close to Linux Parity

Status: implemented.

## Summary

Harden macOS sandboxing as a strict-by-default breaking change. Match Linux's user-visible access policy for ordinary sandboxed commands: restricted commands will only read approved system paths, the workspace, and configured roots; only write approved roots and a private temporary directory; and have no network access except through Caretz's host-validated HTTP(S) allowlist proxy.

This is behavioral policy parity, not equivalent containment strength. Exact isolation parity remains impossible because `sandbox-exec` cannot provide Linux-style mount, PID, user, or network namespaces, capability dropping, or seccomp. These limitations will remain explicitly documented, and macOS will continue to be a weaker boundary against deliberately hostile code.

## Implementation Changes

- Replace the macOS read-open profile with explicit filesystem policy:
  - Deny file reads and writes globally.
  - Allow reads from the workspace, `ReadableRoots`, `WritableRoots`, the command's private temp directory, the Caretz executable, configured extra binaries, and required macOS system roots: `/System`, `/Library`, `/usr`, `/bin`, `/sbin`, `/private/etc`, timezone/dyld/select databases, and required `/dev` literals.
  - Preserve compatibility with installed applications and package managers by also allowing the existing tool roots `/Applications`, `/opt`, and `/nix`; document that these are intentionally broad read-only grants rather than minimum OS requirements.
  - Allow writes only to the private temp directory and, outside read-only mode, the workspace and `WritableRoots`.
  - Remove broad write grants for `/private/tmp`, `/private/var/folders`, `~/go`, and the user Go build cache.
  - Generate literal/subpath rules instead of regex path rules so metacharacters in paths cannot broaden access.
  - Add metadata-only access to ancestors of approved paths where Seatbelt requires it for traversal.
  - Normalize approved roots before profile generation and account for both configured and symlink-resolved forms where aliases could otherwise bypass the intended policy.
  - Apply `HiddenPaths` as final specific read/write denies in both one-shot and persistent execution. Deny both configured and resolved forms, and preserve the denial when a hidden file or directory does not exist at sandbox startup but is created later.
  - Apply hidden-path masking to one-shot Linux execution for paths that exist at startup. Skip absent paths and missing parent chains without host placeholders or parent overlays, preserving ordinary workspace writes. Recheck persistent masks between commands and recreate the supervisor when the mask topology changes. A protected path created during a running Linux command can remain visible until refresh; macOS retains future-path denial.

- Give every macOS runner isolated temporary state:
  - Create a mode-`0700` temporary directory before starting a one-shot command and remove it afterward.
  - Continue using one private directory per persistent supervisor.
  - Set `TMPDIR` to that directory; literal `/tmp` is no longer generally writable.
  - Set `HOME` to the workspace, matching the effective Linux sandbox behavior.

- Align restricted environment handling:
  - Construct a small macOS environment containing a fixed system/Homebrew `PATH`, `HOME`, `TMPDIR`, locale/terminal values, explicitly supplied actor metadata, proxy variables, and `DOCKER_HOST` only when Docker is allowed.
  - Strip API keys, credential/session variables, `DYLD_*`, `LD_*`, `MALLOC_*`, `BASH_ENV`, `ENV`, `CDPATH`, `IFS`, and internal supervisor variables from both one-shot and persistent commands.
  - Allowlist per-command `RunOptions.Env` with the same policy instead of merging arbitrary caller-supplied variables after sanitization; actor metadata remains supported, while callers cannot reintroduce blocked variables or override sandbox-controlled `HOME`, `TMPDIR`, proxy, or supervisor values.
  - Leave sandbox-disabled execution unchanged.

- Make macOS networking follow the existing public allowlist contract:
  - Extract the host/port parser and validating HTTP/HTTPS proxy into platform-neutral code.
  - On macOS, have the parent Caretz process start the proxy on a dynamically allocated loopback TCP port before applying the profile, inject that exact address and port into the profile and proxy environment, and permit no other loopback endpoint.
  - For one-shot execution, bind the proxy lifetime to the command context. For persistent execution, keep it owned by the session supervisor lifecycle and close it when the supervisor stops, fails, or is recreated.
  - Preserve the original tool's permission context for live approvals while honoring proxy-request cancellation and deadlines. Scope temporary grants, pending approvals, blocked targets, HTTP forwarding, and CONNECT tunnels to a command generation; invalidate them on completion or cancellation and reject late approvals or tunnel registration.
  - Deny all other networking, including DNS, unapproved localhost services, ports 80/443/22, and arbitrary Unix sockets.
  - Permit only the exact proxy endpoint plus explicitly recognized Docker Unix sockets when `AllowDocker` is enabled.
  - Set proxy variables without `NO_PROXY`, ensuring localhost destinations also pass through allowlist validation.
  - Keep the proxy endpoint available with an empty allowlist so later
    per-command permission approvals can use it; the empty policy forwards no
    destinations.
  - Allow `AllowLoopback` to grant localhost TCP bind/inbound without granting direct outbound access. Proxy-aware clients request live approval for the exact destination port, and the proxy continues the original request after approval.
  - Keep `UnrestrictedNetwork` as the escape hatch for SSH, non-HTTP protocols, and direct connectivity.
  - Remove the persistent macOS profile's broad localhost outbound exception.

- Improve process lifecycle containment where macOS permits it:
  - Put one-shot commands in their own process group.
  - On cancellation or timeout, terminate the complete group rather than only `sandbox-exec`.
  - Retain persistent-session group tracking and document that deliberately detached daemons can still escape cleanup because macOS has no PID namespace.

## Public Interfaces and Documentation

- Add repeatable `--sandbox-env NAME` and `--sandbox-env NAME=value` grants,
  `profiles.*.sandbox.env`, and `SandboxOptions.Env` so restricted commands can
  receive the language-toolchain and credential variables users explicitly
  choose. Caretz-owned and loader-injection variables remain reserved.
- Change existing semantics:
  - `ReadableRoots` and `HiddenPaths` become effective for macOS Bash execution.
  - `NetworkAllowlist` works on macOS using the same syntax and matching rules as Linux.
  - Restricted macOS networking defaults to fully denied.
  - Restricted macOS session commands may bind localhost servers with `AllowLoopback`; proxy-aware HTTP(S) clients require destination approval. Direct clients require `UnrestrictedNetwork`.
  - `AllowDocker` permits supported local Unix sockets only; SSH Docker contexts additionally require unrestricted networking.
- Update sandboxing, troubleshooting, embedding, and CLI-facing descriptions to explain the stricter defaults, private temp behavior, migration remedies, and the remaining `sandbox-exec` limitations.
- Add a macOS CI job that runs sandbox unit and integration tests on a native, non-nested runner.

## Test Plan

- Unit-test profile generation for system roots, workspace/read/write roots, read-only mode, hidden-path precedence, private temp access, path escaping, Docker sockets, proxy-only networking, and unrestricted networking.
- Test environment construction to ensure required values survive while secrets and injection variables are removed from both the inherited environment and `RunOptions.Env`, and sandbox-controlled values cannot be overridden.
- Native macOS integration tests:
  - Read workspace and explicitly granted roots; reject unrelated home files and sibling temp paths.
  - Write workspace/writable roots/private temp; reject system, home, and read-only writes.
  - Reject literal `/tmp` writes while programs using `$TMPDIR` succeed.
  - Hide config and session paths in one-shot and persistent modes, including paths that are absent when the sandbox starts and created later.
  - Deny external and localhost connections with an empty allowlist.
  - Allow an allowlisted HTTP/HTTPS destination only through the proxy and reject a non-allowlisted destination.
  - Verify one-shot proxy cleanup and persistent proxy cleanup across normal close, supervisor failure, and supervisor recreation.
  - Restore direct and localhost networking under `UnrestrictedNetwork`.
  - Verify timeout/cancellation kills ordinary descendant process groups.
- Preserve Linux proxy and sandbox tests, adding one-shot hidden-path coverage, missing parent chains, host-persistent sibling creation and atomic replacement, and mask refresh before persistent commands.
- Run `go test ./...`, native macOS integration tests with `CARETZ_RUN_SANDBOX_INTEGRATION=1`, `make lint`, and `make doccheck`.

## Assumptions

- Security parity takes precedence over compatibility with implicit home-directory reads, host caches, literal `/tmp`, default internet access, and restricted-mode localhost development servers.
- Parity means matching Linux's intended filesystem, environment, and network permissions for ordinary commands; it does not claim equivalent kernel-level containment against hostile code.
- `/Applications`, `/opt`, and `/nix` remain broad read-only compatibility roots; narrowing them to discovered executables and runtime dependencies is outside this change.
- The supported macOS backend remains deprecated `sandbox-exec`; replacing it with virtualization or a privileged helper is outside this change.
- Native integration verification must run outside the current containing sandbox, which rejects nested `sandbox-exec` profiles.
- Existing unrelated worktree changes remain untouched.
- Linux startup-time masking is an intentional tradeoff to preserve direct host workspace writes without additional filesystem dependencies. Supervisor refresh stops background jobs and loses temporary state. It does not prevent a protected path appearing during a currently running command from becoming visible before refresh.

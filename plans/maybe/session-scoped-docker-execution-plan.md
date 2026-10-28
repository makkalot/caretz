# Session-Scoped Docker Execution

## Summary

Add Docker as an opt-in execution backend:

```text
caretz --docker
custom-caretz-adapter --docker
```

Caretz and its credentials remain on the host. Each live agent session receives a persistent execution container used for Bash commands and background services. Interactive caretz keeps one container for its lifetime; adapters create one isolated container per chat or channel and remove it after 30 minutes of inactivity.

Docker mode fails closed: it never falls back to unsandboxed host execution.

## Key Changes

### Execution backend and lifecycle

- Introduce a session-aware command executor abstraction, with the existing native sandbox and the new Docker implementation behind it.
- Lazily create the container on the session's first Bash call.
- Reuse it for the parent agent and its subagents; never share it between unrelated sessions.
- Execute foreground commands with the existing timeout and output limits. On cancellation, kill the command's complete process group without stopping unrelated background processes.
- Stop and remove containers on session close, client or adapter shutdown, or adapter idle expiry.
- Persist conversation history and mounted workspace data, but not the container filesystem. A later message recreates a clean container.
- Label resources with opaque runtime and session identifiers. On startup, remove stale resources owned by the same caretz deployment before accepting work.
- Use the Docker CLI through an injectable runner, avoiding a Docker SDK dependency.

### Docker image and isolation

- Publish versioned multi-architecture `ghcr.io/makkalot/caretz-dev` images containing Bash, Git, Go, Node, Python, common build utilities, `tini`, and the small caretz container helper.
- Default to the image matching the host caretz release; allow override through `--docker-image` and configuration. Pull a missing default image automatically and report actionable daemon, authentication, or architecture errors.
- Run as the host UID/GID, drop all capabilities, enable `no-new-privileges`, use a read-only container root plus bounded tmpfs locations, and apply CPU, memory, PID, and file-descriptor limits.
- Never mount the Docker socket, host home, caretz configuration, session history, model credentials, bot credentials, or SSH agent.
- Treat `--allow-docker` as incompatible with `--docker` and return a configuration error.
- Keep file-oriented tools on the host with their existing path checks; only command execution moves into the container.

### Mount and network policy

- Resolve and canonicalize mounts on the host before container creation:
  - session workspace or CWD: read-write, or read-only in plan mode;
  - writable roots: read-write;
  - readable roots and runtime skill or agent resources: read-only;
  - writable wins for duplicate paths, while explicit nested read-only mounts remain restrictive.
- Preserve absolute host paths inside the container. Reject missing paths, `/`, home-wide mounts, device or socket files, and Docker-related sockets with clear errors.
- Put the execution container on a per-session internal-only Docker network.
- When `networkAllowlist` is non-empty, create a small gateway container connected to both the internal network and an outbound bridge. Set HTTP(S) proxy variables in the execution container and enforce the existing `HOST` or `HOST:PORT` allowlist in the gateway; direct outbound traffic remains impossible.
- When unrestricted networking is explicitly enabled, attach the execution container directly to a normal bridge and clearly report the weaker policy.
- Add repeatable interactive-only publishing, such as `--publish 3000` or `--publish 8080:3000`. Bind host listeners exclusively to `127.0.0.1` through the gateway. Reject publishing for non-interactive adapters in v1 and expose no ports by default.

### CLI, configuration, and background commands

- Add:
  - `--docker`
  - `--docker-image IMAGE`
  - `--docker-idle-timeout DURATION`, default `30m` for adapters and disabled for interactive caretz
  - repeatable `--publish [HOST_PORT:]CONTAINER_PORT`
- Add an `execution.docker` configuration section with equivalent `enabled`, `image`, `idleTimeout`, and `publish` settings; explicit CLI values override project and home configuration normally.
- Docker mode replaces the internal Bash sandbox automatically; do not require or advertise `CARETZ_DISABLE_SANDBOX`.
- Extend Bash arguments with `background: true`. Background commands:
  - start under `tini` in a detached process group;
  - receive `/dev/null` as stdin;
  - write bounded logs under a container-private caretz runtime directory;
  - return an opaque process ID and log path immediately;
  - survive later Bash calls but stop with the session container.
- Keep v1 process control minimal: agents inspect logs and signal the returned process through ordinary Bash commands. Update the Bash tool guidance with realistic server start, check, and stop examples.
- If an idle lease recreated the container, report that fact on the next Bash result so the agent knows previous services and container-only installations were lost.

## Test Plan

- Unit-test flag and config precedence, invalid combinations, mount canonicalization and deduplication, sensitive-path rejection, label generation, container naming, and Docker command construction.
- Use a fake Docker runner to test lazy creation, concurrent session isolation, parent and subagent sharing, foreground timeout cleanup, background survival, idle expiry, shutdown cleanup, stale-resource cleanup, and fail-closed errors.
- Verify adapter channels receive different networks and containers and cannot access each other's workspace or service ports.
- Verify credentials and host environment secrets never appear in container environment or mounts.
- Add opt-in Docker integration tests covering writable and read-only mounts, internal networking, allowlisted and denied hosts, loopback-only preview publishing, background service persistence, process cancellation, and complete resource cleanup.
- Build and smoke-test the official image on Linux amd64 and arm64 and Docker Desktop on macOS; keep native sandbox tests unchanged.

## Assumptions and Defaults

- Docker is opt-in and the current native sandbox remains the default.
- Docker or a compatible Docker CLI and daemon is installed on the host.
- Adapter workspaces continue using their existing per-channel directories; shared writable roots are intentionally shared and documented as reducing isolation.
- Adapter containers expire after 30 minutes without session activity, even if a background service is still running.
- Fixed host preview publishing is interactive-only in v1.
- Empty allowlists mean no outbound container network; dependency installation requires configured hosts or explicit unrestricted networking.

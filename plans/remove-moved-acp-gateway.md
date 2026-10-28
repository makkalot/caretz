# Remove the Moved ACP Gateway from Caretz

## Summary

Remove the standalone remote ACP gateway from Caretz now that its maintained
implementation lives in Caretplus. Keep Caretz's reusable, in-process ACP host
API and all ACP/session behavior required by Caretplus.

This is a repository-ownership cleanup. It must not change the ACP wire
behavior exposed by Caretz, and it must not remove functionality used by the
embedded Caretplus gateway.

## Background

The original gateway was implemented in this branch as a separate Go module
under `integrations/gateway`. Caretplus now owns the gateway transport,
configuration, protocol documentation, fixtures, diagnostic client, and
deployment integration in its root module.

The Caretplus implementation is available in the sibling repository at
`../caretplus` and was introduced by commit `0f21a79` (`move gateway`). It uses
Caretz's public `acp.Host` and `cli.Runtime.NewACPServer` APIs rather than
starting or supervising a `caretz acp` subprocess.

## Remove from Caretz

### Standalone gateway module

Delete the complete `integrations/gateway/` tree:

- `README.md`
- `go.mod` and `go.sum`
- `cmd/caretz-gateway/`
- `cmd/caretz-gateway-client/`
- `internal/gateway/`, including its implementation and tests

This also removes the standalone module's only dependency,
`github.com/coder/websocket`. That dependency is not present in Caretz's root
module and should not be added there.

### Gateway protocol and fixtures

Delete `docs/gateway/`, including:

- `protocol.md`
- the complete `fixtures/v1/` tree

The V1 fixtures have already been copied to Caretplus and were identical when
this plan was prepared. Caretplus's `docs/gateway/protocol.md` is the newer
canonical document: it describes embedded runtimes rather than supervised ACP
child processes.

### Gateway and iOS plans

Delete these plans because ownership has moved to Caretplus:

- `plans/next/ios-acp-client-remote-caretz-gateway.md`
- `plans/next/ios-acp-client.md`

The Caretplus copies have already been updated to describe embedded ACP
runtimes and Caretplus-owned gateway configuration.

### Makefile integration

Remove the gateway-only Makefile surface:

- `GATEWAY_BINARY_PATH`
- `GATEWAY_MODULE`
- `GATEWAY_PACKAGE`
- `GATEWAY_CLIENT_BINARY_PATH`
- `GATEWAY_CLIENT_PACKAGE`
- `build-gateway` and `test-gateway` from `.PHONY`
- `./integrations/gateway` from the `tidy` module loop
- `build-gateway` and `test-gateway` from the `ci` prerequisites
- the complete `build-gateway` target
- the complete `test-gateway` target

Do not otherwise change Caretz's normal build, test, benchmark, site, or release
behavior.

### CI and release caches

Remove `integrations/gateway/go.sum` from the Go-cache dependency lists in:

- `.github/workflows/ci.yml`
- `.github/workflows/release.yml`

No other gateway-specific workflow steps were found; the gateway build and
test currently enter CI through the Makefile targets described above.

### Local build artifacts

If they exist locally, the ignored generated binaries may be removed after the
tracked cleanup:

- `bin/caretz-gateway`
- `bin/caretz-gateway-client`

These are not part of the source change and should not be committed.

## Keep in Caretz

The following are Caretz's supported embedding and ACP responsibilities and
must remain:

- `acp/host.go` and `acp/host_test.go`
- `cli.Runtime.NewACPServer` and its tests
- `internal/app/acp.go` and its tests
- `docs/extending-cli.md` documentation for the public embedding API
- workspace-safe session listing, loading, resuming, and closing
- ordered history replay and the history-complete response barrier
- provisional assistant output behavior
- prompt attachment persistence and replay
- agent-sent file persistence and download metadata
- model normalization and allowlist enforcement
- ACP logging and cancellation behavior
- supporting changes in `internal/agent`, `internal/sessions`,
  `internal/app/session_runtime.go`, `internal/app/runtime_config.go`, and
  `lib/eventing/event_types.go`

Keep the private `_caretzGateway` session request marker for now. The embedded
Caretplus gateway still injects it, and Caretz uses it to reject legacy sessions
that lack a recorded workspace. Removing or replacing that coupling requires a
separate coordinated API change in both repositories.

References to an event or integration "gateway" in generic Caretz internals,
such as `SessionEventGateway`, are unrelated abstractions and are not cleanup
candidates.

## Implementation Sequence

1. Delete `integrations/gateway/`.
2. Delete `docs/gateway/`.
3. Delete the two moved iOS/gateway plans.
4. Remove the gateway variables and targets from `Makefile`.
5. Remove the gateway module checksum entries from the CI and release cache
   keys.
6. Search the repository for stale ownership references:

   ```sh
   rg -n 'docs/gateway|integrations/gateway|build-gateway|test-gateway|caretz-gateway|GATEWAY_(MODULE|PACKAGE|BINARY)'
   ```

7. Review remaining matches for `_caretzGateway` and generic internal gateway
   interfaces; do not remove them merely because their names contain
   `gateway`.
8. Run formatting only if Go files were unexpectedly changed; this cleanup
   should normally affect only deletions, Makefile text, and workflow YAML.

## Validation

Run the normal repository checks after cleanup:

```sh
make test
make lint
make doccheck
make build
```

Also verify the public ACP packages directly:

```sh
go test ./acp ./cli ./internal/app
```

Confirm that:

- `make help` no longer advertises `build-gateway` or `test-gateway`;
- `make tidy` does not reference the deleted nested module;
- CI and release workflow cache inputs contain no deleted paths;
- Caretz's root `go.mod` and `go.sum` do not gain a WebSocket dependency;
- public ACP host and CLI-adapter tests still pass;
- no source or documentation claims that Caretz owns or ships the remote
  gateway.

Optionally validate Caretplus against this local Caretz checkout after the
cleanup:

```text
make caretz-local CARETZ_DIR=../caretz-worktrees/feat-acp-gw
make test
```

Run those commands from the Caretplus repository. The first command modifies
Caretplus's ignored local Go workspace configuration, not Caretz.

## Out of Scope

- Changing the V1 gateway protocol or fixtures in Caretplus.
- Refactoring Caretz's ACP implementation.
- Removing the `_caretzGateway` marker without a coordinated replacement.
- Moving or rewriting session, attachment, sent-file, model, or replay logic.
- Changing Caretplus service files or release packaging.
- Transplanting cross-repository Git history.

## Completion Criteria

- Caretz contains no standalone WebSocket gateway, gateway client, gateway
  protocol copy, fixture copy, or gateway-specific build/CI wiring.
- Caretz still exposes the supported in-process ACP hosting API used by
  Caretplus.
- Caretz's complete test, lint, documentation, and build checks pass.
- Caretplus can still build and test against the cleaned local Caretz checkout.

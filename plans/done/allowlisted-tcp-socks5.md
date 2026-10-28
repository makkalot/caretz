# Add Allowlisted TCP Networking Through SOCKS5

Status: implemented.

## Summary

Extend Caretz's restricted-network gateway with SOCKS5 TCP `CONNECT` support.
HTTP and HTTPS continue through the existing HTTP proxy, while SSH and other
SOCKS-aware TCP clients use `CARETZ_SOCKS_PROXY`. UDP, ICMP/ping, transparent
interception, and SOCKS authentication remain out of scope.

## Implementation Changes

- Refactor the sandbox proxy into one lifecycle-managed gateway with shared
  allowlist policy, blocked-target reporting, temporary grants, tunnel
  tracking, and shutdown.
- Retain the HTTP listener and add a separate SOCKS5 listener:
  - On Linux, bridge fixed sandbox loopback ports to separate HTTP and SOCKS
    Unix sockets.
  - On macOS, bind separate dynamic loopback listeners and allow only those
    exact TCP endpoints in the sandbox profile.
- Implement SOCKS5 version 5 with no authentication and TCP `CONNECT` for
  IPv4, IPv6, and domain-name targets. Reject `BIND`, `UDP ASSOCIATE`, malformed
  messages, and unsupported authentication.
- Resolve domain names and open destination connections on the trusted host
  side. Apply the allowlist before dialing and preserve temporary-tunnel
  cleanup at command completion.
- Keep `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY` (with lowercase
  variants) pointed at the HTTP proxy and set `CARETZ_SOCKS_PROXY` to
  `socks5h://<SOCKS endpoint>`. `ALL_PROXY` stays HTTP because clients such as
  httpx read it for HTTPS and fail without SOCKS support, and SOCKS accepts
  only `tcp://` rules.
- Extend `--network-allow` and `networkAllowlist` to accept
  `tcp://HOST:PORT` and `tcp://*:PORT`; require an explicit port for TCP rules.
- Preserve compatibility: existing `HOST[:PORT]` rules retain their current
  HTTP and HTTP-CONNECT behavior. SOCKS connections require an explicit
  `tcp://` rule, including temporary approvals.
- Make network permission targets protocol-aware and preserve them verbatim
  instead of path-normalizing them, so a blocked SOCKS request asks for
  approval of `tcp://host:port`.
- Update CLI help, configuration, sandbox, and troubleshooting documentation.
- Update the Bash tool description and generated agent system prompt to
  explain SOCKS TCP support and teach the SSH workflow:

  ```sh
  proxy=${CARETZ_SOCKS_PROXY#*://}
  ssh -o "ProxyCommand=nc -X 5 -x $proxy %h %p" user@host
  ```

  Document that the installed `nc` must support SOCKS5 proxy mode and that
  Git-over-SSH inherits the same SSH configuration.

## Test Plan

- Unit-test SOCKS negotiation, IPv4/IPv6/domain targets, successful
  bidirectional TCP relay, half-close behavior, malformed requests, and
  rejection of authentication, `BIND`, and UDP.
- Verify protocol scoping: `tcp://host:22` permits SOCKS TCP, unrelated ports
  remain blocked, and legacy HTTP/CONNECT rules behave unchanged.
- Verify blocked SOCKS targets produce `tcp://host:port` permission requests;
  approved grants apply only to that command and close temporary tunnels
  afterward.
- Verify gateway shutdown closes HTTP and SOCKS listeners and active tunnels
  for one-shot commands, persistent sessions, supervisor recreation,
  cancellation, and failure.
- Add Linux integration coverage for both loopback bridges and macOS coverage
  for exact proxy endpoint permissions with direct outbound TCP still denied.
- Test environment variables, CLI/config parsing and precedence, agent prompt
  content, and SSH guidance.
- Run `make test`, `make lint`, and `make doccheck`.

## Assumptions

- This release supports proxy-aware TCP clients only; arbitrary unmodified TCP
  applications are not guaranteed to work.
- SSH uses an explicit `ProxyCommand`; Caretz does not modify user SSH
  configuration. The agent system prompt tells the model how to construct the
  command when SSH is requested.
- UDP, QUIC/HTTP3, ICMP/ping, TUN devices, and transparent network interception
  remain blocked. `--unrestricted-network` remains the escape hatch.
- SOCKS runs without authentication because only the sandbox's narrowly
  permitted local endpoint can reach it on Linux. On macOS, other local host
  processes can reach the loopback listener and are part of the trust boundary.
- Blocked external SOCKS destinations use the existing block, approve, and
  rerun flow. `tcp://` rules authorize SOCKS only and never HTTP or HTTP CONNECT.
- Direct SSH connections fail inside the sandbox with `Operation not
  permitted` and never reach the proxy, so they cannot trigger approval. Caretz
  therefore sets `GIT_SSH_COMMAND` to a built-in SOCKS helper
  (`CARETZ_SSH_PROXY_COMMAND`, the caretz binary in a hidden connect mode) so
  Git-over-SSH reaches the proxy and blocked destinations prompt for approval.
  The helper replaces the earlier `nc -X 5` recipe, which needed a
  SOCKS-capable `nc`.

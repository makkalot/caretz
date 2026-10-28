---
title: "Network access"
weight: 20
description: "Allow specific hosts, SSH, loopback, or the whole network."
---

# Network Access

Allow just the hosts a task needs, for example a Python package index:

```sh
caretz --network-allow pypi.org \
      --network-allow files.pythonhosted.org \
      -p "add requests to the project and run the tests"
```

Commands reach these hosts through a filtering proxy that Caretz sets up
through the standard `HTTP_PROXY` variables, so `pip`, `npm`, `go`, and `curl`
work unchanged. Other common forms:

```sh
caretz --network-allow '*.example.com:443'
caretz --network-allow '*:443'
caretz --network-allow 'tcp://git.example.com:22'
```

When a command hits a blocked host in an interactive session, Caretz asks
whether to allow it, so you rarely need to predict the list up front.

## Git and SSH

`git clone`, `fetch`, and `push` to SSH remotes go through the proxy
automatically; approve `tcp://github.com:22` when asked. For plain `ssh`, route
it through the same helper:

```sh
ssh -o "ProxyCommand=$CARETZ_SSH_PROXY_COMMAND" user@host
```

Without it, `ssh` fails with `Operation not permitted` instead of asking for
approval.

## Local Servers and Unrestricted Network

Local dev servers and integration tests need loopback:

```sh
caretz --allow-loopback -p "start the API server and run the integration tests"
```

The built-in `coding` profile already turns this on. When a tool cannot use a
proxy at all (non-HTTP protocols, some database clients), drop network
isolation while keeping the filesystem sandbox:

```sh
caretz --unrestricted-network
```

`--unrestricted-network` does not create the isolated Linux network namespace.
Commands share the host's network namespace and so can still reach abstract
Unix sockets, which no file mask can hide.

Linux session commands can use services inside their private loopback
namespace. On macOS, `--allow-loopback` permits TCP listeners and direct
loopback connections. Without it, proxy-aware clients can still request
approval for an exact localhost `host:port`; direct localhost connections
remain denied. Caretz does not set `NO_PROXY` on macOS.

## Allowlist Syntax

An HTTP entry is `HOST`, `HOST:PORT`, or `*:PORT`. A TCP entry is
`tcp://HOST:PORT` or `tcp://*:PORT`; its explicit port is required. Matching is
case-insensitive and an exact host rule does not include subdomains. A leading
`*.` matches subdomains but not the base domain. When a port is omitted from a
host rule it matches any port; the proxy uses 80 and 443 as the defaults for
HTTP and HTTPS requests. An any-host rule always requires a port, so bare `*`
is rejected.

`*:80` and `*:443` also include loopback, private, link-local, and metadata
addresses on their configured ports. Because the proxy supports HTTPS
`CONNECT`, an any-host rule permits tunnels to any destination on that port;
prefer named hosts when the required destinations are known.

## How the Proxy Works

Linux starts each restricted command in a separate network namespace. macOS
uses a `sandbox-exec` network policy. With no allowlist, neither platform can
connect to the host or internet.

```text
sandboxed command -> 127.0.0.1:18080 -> private Unix socket
                  -> host/port allowlist check -> destination
```

For an allowlisted Linux run, Caretz creates private Unix sockets outside the
sandbox and bridges them from fixed HTTP and SOCKS loopback ports inside the
network namespace. On macOS, Caretz binds dynamic loopback ports and permits only
those exact endpoints in the generated profile. The upper- and lower-case
`HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY` variables point to the restricted
HTTP proxy, so HTTP clients keep working without SOCKS support.
`CARETZ_SOCKS_PROXY` points to a separate SOCKS5 proxy that resolves destination
names on the trusted host. SOCKS connections require an explicit
`tcp://HOST:PORT` or `tcp://*:PORT` allowlist entry; ordinary entries remain
scoped to HTTP and HTTP CONNECT.

The proxy accepts absolute HTTP requests and HTTPS `CONNECT` tunnels only. A
blocked host receives `403 Forbidden`. This is destination gating, not TLS
inspection: after an allowed `CONNECT`, encrypted traffic passes through the
tunnel. Programs must honor the proxy environment variables; arbitrary TCP or
UDP protocols remain unavailable in the isolated namespace. Restricted runners
keep this proxy endpoint available even when the configured allowlist is empty
so an approved command can receive temporary network access. The empty policy
forwards no destinations, and temporary grants are removed after the command
finishes.

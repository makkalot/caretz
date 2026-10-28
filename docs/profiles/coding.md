---
title: "coding"
weight: 10
description: "Package registries, loopback, GitHub and SSH logins."
---

# The `coding` Profile

```sh
caretz --profile coding
```

`coding` replaces the neutral base prompt with Caretz's coding prompt and opens
what development usually needs, without broad network access.

## Network and Credentials

It permits common development hosts such as GitHub, PyPI, npm, Go, crates.io,
Maven, NuGet, and container registries through the restricted network proxy.
It enables local TCP listeners and connections for development and integration
tests without granting access to external network destinations. It also copies
common matching credentials and tool settings when present, including
`GH_TOKEN`, `GITHUB_TOKEN`, `NPM_TOKEN`, Python index URLs, Go proxy/private
module settings, and package-registry tokens. On macOS it additionally copies
Homebrew authentication/settings and Xcode SDK selection variables. Variables
that are absent from the host remain absent.

When the host's GitHub CLI config directory exists, the profile also mounts it
read-only and sets `GH_CONFIG_DIR`, so `gh` can reuse an existing login even
when no token variable is exported. On systems such as macOS where `gh` keeps
the token in a keyring, Caretz retrieves it before entering the sandbox and
passes it as `GH_TOKEN` for the coding session only. The token remains in
memory and is not written to the profile or session history.

## Git Worktrees

When the workspace is a linked Git worktree, the `coding` profile also grants
write access to the parent repository's shared `.git` directory, so `git add`,
`git commit`, and branch updates work the same way they do in a normal
checkout. The parent working tree itself stays out of reach. Repository
`hooks`, `info`, and `config` remain read-only because Git outside the sandbox
would act on changes to them; commands that rewrite repository configuration,
such as `git branch --set-upstream-to`, fail inside the sandbox. Other profiles
do not receive this automatic grant.

## SSH

The `coding` profile also reads your existing `~/.ssh` directory and forwards
an existing `SSH_AUTH_SOCK` agent through its exact socket path. This lets SSH
reuse keys, host verification, and loaded agent identities. Agent access
permits signing with loaded keys. Other profiles do not inherit these SSH
grants. SSH destinations require an explicit `/addnet tcp://HOST:PORT` or
`--network-allow tcp://HOST:PORT` grant, or approval when Caretz asks. Git
routes SSH through the sandbox proxy automatically; plain `ssh` needs the
`ProxyCommand` described in [Network access](../sandboxing/network.md#git-and-ssh).

`~/.ssh` stays read-only, so SSH cannot record the key of a host it has not
seen before. For a new host, list a writable file first in
`UserKnownHostsFile`; SSH still trusts your existing `known_hosts` and saves
the new key in the first file:

```sh
ssh -o "ProxyCommand=$CARETZ_SSH_PROXY_COMMAND" \
    -o "UserKnownHostsFile=$TMPDIR/known_hosts ~/.ssh/known_hosts" \
    -o StrictHostKeyChecking=accept-new \
    -T git@github.com
```

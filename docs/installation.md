---
title: "Install"
description: "One command for Linux and macOS, or build from source."
---

# Installation

Both options put the `caretz` executable in `~/bin/caretz` on Linux or macOS.
Then run `caretz setup` to connect a model.

## Release Installer

```sh
curl -fsSL https://raw.githubusercontent.com/makkalot/caretz/main/install.sh | sh
```

The installer detects Linux or macOS and the `amd64` or `arm64` architecture,
downloads the matching release archive, verifies its SHA-256 checksum, and
installs the `caretz` executable.

To install a specific release or use another destination, set
`CARETZ_VERSION` or `CARETZ_INSTALL_DIR` on the `sh` command:

```sh
curl -fsSL https://raw.githubusercontent.com/makkalot/caretz/main/install.sh | CARETZ_VERSION=vX.Y.Z sh
curl -fsSL https://raw.githubusercontent.com/makkalot/caretz/main/install.sh | CARETZ_INSTALL_DIR="$HOME/.local/bin" sh
```

Prebuilt archives and checksums are also available on the
[GitHub Releases page](https://github.com/makkalot/caretz/releases/latest) if
you'd rather download and check them yourself.

## Build from Source

If you'd rather not pipe a script into your shell, build it yourself. You need
Go 1.25.8 or later, Git, and Make:

```sh
git clone https://github.com/makkalot/caretz.git
cd caretz
make install
```

`make install` builds caretz and copies it to `~/bin/caretz`; `make build`
only writes `./bin/caretz`. The core depends on nothing beyond the Go standard
library and `golang.org/x`, so the source is quick to audit and can be built on
an offline machine; see [Dependencies](dependencies.md).

## Add `~/bin` to Your PATH

If `caretz` isn't found after installing, add `~/bin` to your `PATH` in your
shell configuration:

```sh
export PATH="$HOME/bin:$PATH"
```

## Windows

Windows is not currently supported. Windows users can run caretz under WSL;
review [Sandboxing](sandboxing/_index.md) before changing the default sandbox
policy.

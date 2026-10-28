---
title: "Credentials and environment"
weight: 30
description: "Pass tokens and environment variables to commands."
---

# Credentials and Environment

Commands get a minimal environment, not your whole shell. Pass what a workflow
needs by name (copied from your environment) or with a value:

```sh
caretz --sandbox-env GOPROXY \
      --sandbox-env CI=true \
      --sandbox-env NPM_TOKEN
```

For many values, use a Docker-style env file, and keep it out of version
control if it holds secrets:

```sh
caretz --sandbox-env-file .caretz/sandbox.env
```

## Env File Format and Profiles

`NAME` copies a value only when it exists in Caretz's host environment;
`NAME=value` sets a literal value, including an empty value with `NAME=`. In an
env file, each non-comment line must be either `NAME` or `NAME=value`; blank
lines and lines whose first non-space character is `#` are ignored, and values
may contain `=` after the first separator. Profiles can provide
`sandbox.envFile`, plus `sandbox.envFileLinux` or `sandbox.envFileDarwin`:

```json
{
  "profiles": {
    "ci": {
      "sandbox": {
        "envFile": [".caretz/ci.env"],
        "envFileLinux": [".caretz/ci-linux.env"]
      }
    }
  }
}
```

## Layering

Caretz applies profile entries first, then profile env files (common, then the
current platform), then explicit `--sandbox-env-file` files, and finally
explicit `--sandbox-env` entries. Within each layer, later entries win. A
missing or invalid file stops startup with an error naming `--sandbox-env-file`
and the path. Profiles can also use `sandbox.envLinux` and `sandbox.envDarwin`;
the built-in `coding` profile uses these to carry common package-manager
credentials on both platforms and Homebrew/Xcode settings only on macOS.

Keep env files containing sensitive values out of committed configuration (add
them to `.gitignore`, restrict their permissions, and prefer host-only `NAME`
entries when possible). Explicitly named credentials are passed, so review
project profiles and avoid storing literal secrets in configuration or shell
history. Caretz always controls `HOME`, `TMPDIR`, proxy routing, Docker routing,
loader injection variables, shell startup hooks, and its internal supervisor
variables.

Go commands keep their reusable build and module caches under
`.caretz/scratch/` instead of creating `Library/` or `go/` directories at the
workspace root. You can delete this scratch directory safely to reclaim space;
Go rebuilds the caches on the next command.

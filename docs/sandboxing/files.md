---
title: "Files outside the workspace"
weight: 10
description: "Grant read or write access to other directories."
---

# Files Outside the Workspace

```sh
caretz --read-root ../shared-docs \
      --writable-root /tmp/caretz-output
```

A read root never grants write access. Both flags can be repeated. Mid-session,
grant access without restarting:

```text
/addpath ../shared-docs
/addpath --rw /tmp/caretz-output
```

Roots passed with `--read-root` and `--writable-root` must already exist; Caretz
resolves them to absolute paths and resolves symlinks before applying the
policy. When `/addpath` changes the policy while persistent execution is
running, Caretz recreates its sandbox supervisor, and the next command reports
that background jobs and private temporary state were lost.

To make grants permanent, see [Permanent settings](permanent-config.md).

## Protected Paths

Session history in the workspace, including `.caretz/sessions`, is readable but
read-only. Agents can inspect a previous failure and edit the source code,
while Caretz continues appending events from the host. Loaded config JSON files
remain hidden. Session stores outside the active workspace stay hidden, even
under a broader granted root; use an external `--config-root` for private
history. Choosing external storage does not hide old workspace history.
Explicit hidden paths take precedence over read-only paths, and writable-root
grants cannot override either restriction. These Bash restrictions require the
sandbox; disabled execution retains native permissions.

On Linux, hidden paths are masked when they exist at command startup. Missing
paths, including missing parent directories, are skipped without creating host
placeholders. Normal workspace file creation, deletion, and atomic replacement
continue to write directly to the host. Before each persistent command, Caretz
checks whether the protected paths have appeared, disappeared, changed type, or
resolved to a different target. If the masks changed, Caretz recreates the
supervisor and reports that background jobs and temporary state were lost.
A protected path created during an already running command can remain visible
until that refresh. The same startup-time rule applies to read-only session
directories: if one appears during a running command, its write restriction
is applied at refresh. Create sensitive paths before starting commands when that
visibility window is unacceptable. macOS denies hidden paths even when they
are created after the sandbox starts.

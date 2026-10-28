---
title: "Terminal and ACP"
weight: 50
description: "Keyboard input, interruption, and Zed/ACP setup."
---

# Terminal and ACP

## Terminal Input or Interruption

In the core interactive UI, `Ctrl+C` exits while the prompt is idle and cancels
only the active request while the model is running. `Ctrl+D` exits from an
empty prompt. Piped stdin uses a simpler line scanner and does not provide
raw-terminal editing or mention completion.

Some terminals report `Ctrl+Shift+R` as `Ctrl+R`. If a shortcut is intercepted
by the terminal emulator, use `/models --reasoning VALUE provider/model`
instead.

For an interrupted process that left session state incomplete, follow the
[session repair workflow](../sessions/recovery.md) rather than repeatedly
reopening it.

## ACP and Zed

For setup, see [Zed and ACP editors](../acp.md). Use an absolute path to the binary and ensure `acp` is the first argument.
Caretz's provider configuration must be available to the external-agent
process; Zed's native model-provider settings are separate.

Add `-v` after `acp` in the configured arguments to create
`.caretz/sessions/acp/<session-id>/debug.log`. In Zed, run `dev: open acp logs`
to inspect the JSON-RPC exchange. Confirm the session working directory exists
and that Zed is sending it in `session/new`.

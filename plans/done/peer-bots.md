# Peer Bots

## Summary

An agent definition with `mode: peer` becomes a long-lived bot. Each bot runs
as its own caretz process (`caretz peer <name>`) on a stable `peer/<name>` session
per workspace. Any interactive session or bot can send it work with
`SendMessage`. The reply comes back asynchronously and wakes the sender. You
attach to a bot by looking at its terminal; inside Herdr or tmux,
`caretz --start-peers` opens a pane per bot that is offline. User docs:
`docs/peers.md`.

This replaces the earlier single-process draft. That draft used a queued
`LogMessage.UserCreated` as the inbox, which conflicts with session listing, TUI
replay, and context repair, because all of them read that event as "a turn
started".

## Decisions

- **One process per bot, one instance per workspace.** An endpoint `flock`
  enforces this. The workspace path is resolved through symlinks so aliases
  share endpoints. Each git worktree has its own bots.
- **Every endpoint writes only its own logs.** Endpoints are `bot.<name>` and
  `session.<adapter>.<id>`. Messages cross processes through a signed spool in
  `<configRoot>/run/<hash(workspace)>/inbox/<endpoint>/`. A unix socket per
  endpoint carries only wake-ups.
- **Delivery is at-least-once with dedupe by message ID.** A spool file is
  removed only after `Peer.MessageReceived` and `AsyncRunRequested` are in the
  receiver's `events.jsonpl`.
- **Turns reuse the existing async executor.** A message runs as
  `AsyncRunRequested{Source: "peer", SourceID: <message id>}`, and
  `AsyncRunResult.Response` carries the final text back. Replies are sent with
  `expects_reply: false`, so nothing bounces automatically. A hop count capped
  at 4 bounds explicit chains.
- **Recovery on start:**
  - a received message that never started is requested again;
  - a completed message whose reply was not forwarded is forwarded;
  - a turn that started but never completed is answered as `interrupted`
    instead of being run again.
- **Write conflicts are accepted in v1.** `workspace: <dir>` confines a bot's
  writes to that directory and leaves the main workspace readable.
  `accept_from:` limits who may message a bot.

## Security

- The run directory is mode `0700` and its files and sockets are `0600`. Caretz
  refuses to use them if their owner or mode is wrong, and checks the uid of the
  process on the other end of each socket connection.
- Spool messages are HMAC-signed with a per-workspace key. Unsigned or tampered
  files are set aside.
- `<configRoot>/run` is on the sandbox hidden-path list. Sandboxed tools can't
  read the key or write the spool, and this holds even when the run directory
  sits inside the workspace's writable `.caretz` directory.
- Unsandboxed processes running as the same user are out of scope.

## Deferred

- Messaging from `caretz -p` and ACP sessions.
- A headless daemon with a separate attach client.
- Group chat, progress streaming, cancelling messages, and defining bots at
  runtime.
- Permission prompts during message-driven turns. They time out unless someone
  answers in the bot's pane.

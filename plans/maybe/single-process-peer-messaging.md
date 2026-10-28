# Single-Process Persistent Peer Messaging

## Summary

Add Grok Bot-style communication between configured persistent Caretz sessions
within one process. Each peer uses its own `events.jsonpl` as a durable inbox,
processes messages asynchronously, and routes one correlated result back to the
sender.

## Key Changes

- Extend `.caretz/agents/*.md` to accept `mode: peer`. Reuse the existing prompt,
  model, reasoning, permission, discovery, precedence, and profile allowlisting
  behavior.
- Add a process-local peer hub that opens configured peers as stable top-level
  sessions under a dedicated `peer/<name>` namespace, owns their workers,
  serializes turns, and closes them during shutdown.
- Add a `SendMessage` tool with `to` and `content` inputs. It validates the
  configured peer name, appends the inbound event, wakes the recipient, and
  immediately returns a durable message ID with `queued` status.
- Extend `LogMessage` with optional peer-routing metadata containing the source
  adapter/session, source peer name when applicable, and whether a reply is
  expected. Existing events remain compatible.
- Keep `LogMessage.UserCreated` as the inbox item and
  `LogMessage.AgentCreated.in_reply_to` as its completion. Never use
  `context.jsonpl` for routing.
- When the recipient finishes, route its final response into the sender's
  `events.jsonpl` as a new inbound result with `expects_reply: false`, then wake
  the sender. Further peer discussion requires another explicit `SendMessage`,
  preventing automatic reply loops.
- Auto-process messages in event order. Arrivals during an active turn remain
  queued behind it. Reconcile persisted unanswered messages and unforwarded
  replies when the hub starts.
- Include `peer` in session listing, exploration, inspection, and repair
  commands. Incognito peers remain process-local and in memory.
- Keep all session-file writes inside the owning process. Cross-process
  transport, formal task states, progress, cancellation, group chat, and
  dynamic peer creation remain deferred.

## Public Interfaces

- Agent frontmatter gains `mode: peer`; `mode: subagent` retains its existing
  fresh-child behavior.
- Add the model tool:

  ```text
  SendMessage(to: string, content: string)
  -> {message_id: string, status: "queued", to: string}
  ```

- Peer-routing metadata is optional on `eventing.LogMessage`; normal CLI, TUI,
  ACP, and adapter messages retain their current wire representation and
  behavior.
- Public embedding/runtime construction gains the ability to enable a
  configured peer hub and install its session-scoped messaging tool on root and
  peer sessions.

## Test Plan

- Parse and discover peer definitions while keeping peer agents out of
  `CallSubagent` and `@subagent` execution paths.
- Send A to B and verify the immediate queued receipt, durable B inbox event,
  automatic B processing, correlated B-to-A result, and automatic A processing.
- Verify slow B work does not block A's original turn and messages arriving at
  busy sessions process afterward in order.
- Verify unknown, disabled, and self targets fail without appending events.
- Verify routed results do not automatically bounce back to the peer.
- Restart with pending inbound messages or completed-but-unforwarded replies
  and verify exactly-once recovery.
- Verify separate peer histories, models, prompts, permissions, workspaces, and
  event versions.
- Verify session list, explore, inspect, and repair support the `peer` namespace.
- Run the full test suite, lint checks, and documentation checks.

## Assumptions

- V1 runs one Caretz process that exclusively owns all participating sessions and
  event logs.
- Peers are statically configured, eagerly registered, and opened when recovery
  or first delivery requires them.
- Communication consists of asynchronous request/result messages only;
  progress, cancellation, task status, and group conversations come later.
- Each inbound peer message produces at most one automatically routed final
  result.

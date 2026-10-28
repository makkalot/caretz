---
title: "Sessions and events"
weight: 20
description: "Keep conversations, persist them, and react to events."
---

# Sessions and Events

Use `client.Run` for simple request/response flows. Use `client.NewSession` when
you want to keep a handle to a conversation:

```go
session, err := client.NewSession("session-1", caretz.SessionOptions{})
if err != nil {
	return err
}

_, err = session.Run(ctx, "make the change")
messages := session.Messages()
```

By default, library sessions are in memory. To persist and restore sessions, set
`State.Dir`:

```go
client, err := caretz.NewClient(ctx, caretz.ClientOptions{
	Model: caretz.ModelOptions{
		BaseURL:       "http://127.0.0.1:8001/v1",
		APIKey:        "noset",
		Model:         "qwen-code",
		ContextWindow: 60000,
	},
	State: caretz.StateOptions{Dir: "/var/lib/my-app/caretz"},
})
```

Persistent library sessions are stored under `<State.Dir>/sessions/lib/`.

## React to Session Events

Sessions are event-driven. Register handlers for context or audit event types
to update a UI, send progress, collect metrics, or mirror an audit trail,
without scraping terminal output:

```go
err := session.OnAuditEvent(eventing.SubagentInvocationProgress, func(e *types.Event) error {
	// Update your UI, send a notification, or record telemetry.
	return nil
})
```

Use `OnContextEvent` for the model-facing conversation stream and
`OnAuditEvent` for application-facing history. Tooling that needs background
processing, several event types on one channel, or session-lifecycle cleanup
can use `lib/sessiontools`. See [Session storage](../sessions/storage.md) for
how the two event logs are stored.

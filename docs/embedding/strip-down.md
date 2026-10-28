---
title: "Strip it down to Bash"
weight: 10
description: "Your own prompt and only the tools your app needs."
---

# Strip It Down to Bash

You do not have to run caretz as a full coding assistant. Give it your own system
prompt and expose only the tools your application needs.

## From the CLI

Create `agent.md`:

```text
You are a build assistant. Use Bash to inspect the project and run its checks.
Keep your final answer short and report any failing command exactly.
```

Run caretz with only the sandboxed `Bash` tool. `--incognito` keeps the run in
memory, while `--no-tasks` removes the internal task tracker:

```sh
caretz \
  --system-prompt-file agent.md \
  --tools Bash \
  --no-tasks \
  --incognito \
  -p "Run the test suite and tell me what failed"
```

The CLI adds the relevant workspace and Bash guidance to your prompt. The
sandbox remains enabled by default.

## From Go

This is the equivalent minimal agent as a complete program:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/makkalot/caretz"
)

func main() {
	ctx := context.Background()
	client, err := caretz.NewClient(ctx, caretz.ClientOptions{
		Model: caretz.ModelOptions{
			BaseURL:       "http://127.0.0.1:8001/v1",
			APIKey:        "noset",
			Model:         "qwen-code",
			ContextWindow: 60000,
		},
		WorkspaceDir: ".",
		PreparedSystemPrompt: `You are a build assistant. Use Bash to inspect the
project and run its checks. Keep your final answer short and report any failing
command exactly.`,
		EnabledTools: map[string]bool{
			"Bash": true,
		},
		DisableTasks: true,
	})
	if err != nil {
		log.Fatal(err)
	}

	response, err := client.Run(
		ctx,
		"build-check",
		"Run the test suite and tell me what failed",
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(response.Content)
}
```

Compile and run it with:

```sh
go build -o minimal-agent .
./minimal-agent
```

This combination is intentionally strippable. `PreparedSystemPrompt` bypasses
the standard assembled prompt, `EnabledTools` limits the tool definitions sent
to the model, and `DisableTasks` removes the task tools and guidance. Keep tasks
enabled for longer multi-step work: their separately persisted state is included
when caretz compacts the conversation, which helps smaller models retain the
current goal and next action. See [Long sessions](../sessions/long-sessions.md).

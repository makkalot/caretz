---
title: "Add tools to your Go app"
weight: 40
description: "Register your own tools when embedding the agent."
---

# Add Tools to Your Go App

When you [embed Caretz](../embedding/_index.md), pass your tools in
`ClientOptions.Tools`. This app gives the agent only a `LookupOrder` tool, so
it can answer support questions without file or shell access:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/makkalot/caretz"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/tools"
)

func main() {
	lookupOrder := tools.Tool{
		Definition: llm.ToolDefinition{
			Name:        "LookupOrder",
			Description: "Return the status of a customer order by ID.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"order_id": map[string]any{"type": "string"},
				},
				"required": []string{"order_id"},
			},
		},
		Handler: func(_ context.Context, call llm.ToolCall) (string, error) {
			var args struct {
				OrderID string `json:"order_id"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return fmt.Sprintf("order %s shipped yesterday", args.OrderID), nil
		},
	}

	ctx := context.Background()
	client, err := caretz.NewClient(ctx, caretz.ClientOptions{
		Model: caretz.ModelOptions{
			BaseURL: "http://127.0.0.1:8001/v1",
			APIKey:  "noset",
			Model:   "qwen-code",
		},
		WorkspaceDir: ".",
		Tools:        []tools.Tool{lookupOrder},
		ToolMode:     caretz.ToolModeNone,
		DisableTasks: true,
	})
	if err != nil {
		log.Fatal(err)
	}

	response, err := client.Run(ctx, "support", "Where is order 1042?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(response.Content)
}
```

Remove `ToolModeNone` to keep the built-in tools alongside yours, or use
`EnabledTools` to keep a few of them, as in
[Strip it down to Bash](../embedding/strip-down.md).

## Built-in Tools from Go

- Set `TavilyAPIKey` or `BraveSearchAPIKey` to enable
  [web search](web.md). `NewClient` doesn't read environment variables.
- `ToolsConfig` takes the same per-tool settings as `toolConfig` in the config
  file, such as `localImage` from [Analyze images locally](local-image.md).
- Set `SessionOptions.FileSender` on a session to enable `SendFile`, so the
  agent can hand files back to your app.

Handler errors and naming rules work as in
[Add tools to the CLI](custom-cli.md#how-custom-tools-behave).

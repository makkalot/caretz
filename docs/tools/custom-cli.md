---
title: "Add tools to the CLI"
weight: 30
description: "Build your own caretz binary with extra tools."
---

# Add Tools to the CLI

Give Caretz direct access to your own systems, such as deploy status, an
internal API, or a ticket tracker, by building your own binary. It keeps every
caretz flag, session, and sandbox feature and adds your tools. Create a project:

```sh
mkdir my-caretz
cd my-caretz
go mod init example.com/my-caretz
go get github.com/makkalot/caretz
```

Save this as `main.go`. The tool takes an `environment` argument:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/makkalot/caretz/cli"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/tools"
)

func main() {
	deployStatus := tools.Tool{
		Definition: llm.ToolDefinition{
			Name:        "DeployStatus",
			Description: "Return the deployment status of one environment.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": map[string]any{
						"type":        "string",
						"description": "staging or production",
					},
				},
				"required": []string{"environment"},
			},
		},
		Handler: func(_ context.Context, call llm.ToolCall) (string, error) {
			var args struct {
				Environment string `json:"environment"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			switch args.Environment {
			case "staging", "production":
				return args.Environment + " is healthy", nil
			default:
				return "", fmt.Errorf("unknown environment %q", args.Environment)
			}
		},
	}

	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, cli.Options{
		Tools:              []tools.Tool{deployStatus},
		SystemPromptSuffix: "Use DeployStatus before answering questions about deployments.",
	}))
}
```

Build it and use it like `caretz`:

```sh
go build -o my-caretz .
./my-caretz -p "Is production healthy?"
./my-caretz --disable-tool DeployStatus
```

## How Custom Tools Behave

- `Parameters` is a JSON Schema object. The handler receives the model's
  arguments as a JSON string in `call.Arguments`.
- The returned string becomes the tool result. A returned error is shown to the
  model as `error: ...`, so it can correct its arguments and retry.
- Names must be unique and can't reuse a built-in name such as `Bash`.
- Custom tools follow `--tools`, `--disable-tool`, and profile tool lists.
- Handlers run in your process with your permissions, not in the Bash sandbox.
  Check inputs before touching files, networks, or credentials.

The [git-caretz-review](https://github.com/makkalot/caretz/tree/main/examples/git-caretz-review)
example has real-world tools of this kind. They run a fixed, read-only `git`
command and validate the model's arguments before using them.

For replacing the interactive UI or adding a transport, see
[Build on the CLI](../extending-cli/_index.md).

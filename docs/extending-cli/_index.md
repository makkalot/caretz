---
title: "Build on the CLI"
description: "Your own executable with caretz's flags, sessions, and sandbox."
---

# Build on caretz's CLI

The public `github.com/makkalot/caretz/cli` package lets your executable retain
caretz's flags, configuration, sessions, sandbox, tools, and agent runtime while
adding its own capabilities.

- `cli.Run` starts the standard caretz CLI with additional tools or UI runners.
- `cli.BuildRuntime` loads caretz's CLI configuration without starting its user
  interface, so your application can provide a different
  [transport](custom-adapter.md).

Use the direct [`caretz` library API](../embedding/_index.md) instead if your
application should own model and runtime configuration itself.

## Extend the Standard CLI

The following complete program keeps the standard caretz CLI and adds a
`DeployStatus` tool. Create a project:

```sh
mkdir my-caretz
cd my-caretz
go mod init example.com/my-caretz
go get github.com/makkalot/caretz
```

Save this as `main.go`:

```go
package main

import (
	"context"
	"os"

	"github.com/makkalot/caretz/cli"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/tools"
)

func main() {
	deployStatus := tools.Tool{
		Definition: llm.ToolDefinition{
			Name:        "DeployStatus",
			Description: "Return the deployment status for this application.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		Handler: func(context.Context, llm.ToolCall) (string, error) {
			return "production is healthy", nil
		},
	}

	code := cli.Run(
		context.Background(),
		os.Args[1:],
		os.Stdout,
		os.Stderr,
		cli.Options{Tools: []tools.Tool{deployStatus}},
	)
	os.Exit(code)
}
```

Build and run it exactly like caretz:

```sh
go build -o my-caretz .
./my-caretz -p "Check the deployment status"
./my-caretz
./my-caretz session list
```

The added tool is registered alongside the built-in tools and is subject to the
normal `--tools` and `--disable-tool` policy. For tools that take arguments,
see [Add tools to the CLI](../tools/custom-cli.md). To add your own `/commands`
or full-screen views to the TUI, see [Add slash commands](slash-commands.md).
`cli.Options` also accepts an `InteractiveRunner` and `ExplorerRunner` for
replacing those interfaces.

`cli.Run` owns every argument passed to it. If your executable defines its own
flags or subcommands, parse and remove those before passing the remaining
arguments to `cli.Run`. The
[git-caretz-review](https://github.com/makkalot/caretz/tree/main/examples/git-caretz-review)
example does this. It is a `git caretz-review main` subcommand that adds
read-only diff tools and a review rubric through `SystemPromptSuffix`, then
hands every other flag to caretz.

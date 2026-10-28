---
title: "Custom adapter"
weight: 10
description: "Your own interface on top of caretz's runtime."
---

# Build a Custom Adapter

Use `cli.BuildRuntime` when your program provides the interface but should still
use caretz's CLI flags and configuration. This complete example implements a
small line-oriented stdin adapter. It stores its session under the `stdin`
adapter namespace and gives it a dedicated workspace.

Create a project as in [Extend the standard CLI](_index.md), then save this as
`main.go`:

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/cli"
	"github.com/makkalot/caretz/lib/eventing"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx := context.Background()
	rt, code := cli.BuildRuntime(ctx, os.Args[1:], os.Stderr, cli.Options{})
	if code != 0 {
		return code
	}

	workspace := filepath.Join(rt.CWD(), "workspaces", "stdin", "local")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create workspace: %v\n", err)
		return 1
	}

	session, err := rt.NewAdapterSession("stdin", "local", cli.AdapterSessionOptions{
		WorkspaceDir: workspace,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open session: %v\n", err)
		return 1
	}

	fmt.Println("Enter a prompt, or press Ctrl+D to exit.")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}

		event := eventing.NewLogMessage("local", prompt)
		if err := session.AppendAuditEvent(event); err != nil {
			fmt.Fprintf(os.Stderr, "save prompt: %v\n", err)
			return 1
		}
		response, err := session.ProcessUserEvent(ctx, event)
		if err != nil {
			fmt.Fprintf(os.Stderr, "run agent: %v\n", err)
			return 1
		}
		if response != nil {
			fmt.Println(response.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
		return 1
	}
	return 0
}
```

Build and run it:

```sh
go build -o caretz-stdin .
./caretz-stdin
```

`BuildRuntime` accepts caretz's runtime flags, such as `--workspace`, `--model`,
`--config-root`, and verbosity flags. Unlike `cli.Run`, it does not accept
prompt-mode or session-maintenance commands because your adapter owns input and
output.

The returned runtime exposes `Context`, `CWD`, `SessionsDir`, `Logger`, and
`NewAdapterSession`. A production adapter is responsible for receiving input,
choosing stable channel IDs, creating per-channel workspaces, durably appending
incoming messages before processing them, delivering responses, retrying where
appropriate, and handling shutdown. `lib/conversation.Runtime` provides worker
and retry coordination for event-driven transports.

For a fuller adapter, see
[vault](https://github.com/makkalot/caretz/tree/main/examples/vault). It is an
assistant for a folder of Markdown notes, with `capture`, `ask`, and `serve`
commands. Adapter sessions come with caretz's scheduler tools, so `serve` gets
its daily plan and weekly review just by asking the agent to schedule them.

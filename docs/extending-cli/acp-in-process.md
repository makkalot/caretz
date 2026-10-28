---
title: "Host ACP in process"
weight: 20
description: "Embed the ACP session multiplexer instead of a subprocess."
---

# Host ACP in Process

Applications that speak ACP can embed its session multiplexer instead of
starting a `caretz acp` subprocess. An `acp.Host` manages multiple independently
configured workspace runtimes. Each workspace starts lazily and carries any
number of ACP sessions over one ordered message stream.

This complete example starts one workspace and sends `initialize`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/makkalot/caretz/acp"
	"github.com/makkalot/caretz/cli"
)

func main() {
	ctx := context.Background()
	host := acp.NewHost(ctx)
	defer host.Close()

	err := host.RegisterWorkspace("project", acp.WorkspaceConfig{
		Runtime: func(runtimeCtx context.Context) (acp.Runtime, error) {
			runtime, code := cli.BuildRuntime(
				runtimeCtx,
				[]string{"--workspace", "/srv/project", "--model", "openai/gpt-5"},
				os.Stderr,
				cli.Options{},
			)
			if code != 0 || runtime == nil {
				return nil, fmt.Errorf("build Caretz runtime: exit code %d", code)
			}
			return runtime, nil
		},
		Server: acp.ServerOptions{
			AllowedModels: []string{"openai/gpt-5"},
		},
	})
	if err != nil {
		panic(err)
	}

	endpoint, err := host.Endpoint(ctx, "project")
	if err != nil {
		panic(err)
	}
	request := json.RawMessage(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
	)
	if err := endpoint.Send(ctx, request); err != nil {
		panic(err)
	}
	fmt.Println(string(<-endpoint.Messages()))
}
```

`Send` accepts one complete JSON value and `Messages` emits complete ACP JSON
responses and notifications in server order. One consumer should continuously
drain each endpoint. A slow consumer applies backpressure; output is never
silently discarded.

Use `ReplaceWorkspace` when configuration changes and `RemoveWorkspace` when a
workspace is no longer available. Both operations cancel active turns and
close the old sessions before returning. Calling `Endpoint` after an endpoint
closes starts a fresh instance but never resends an uncertain request.

The host deliberately does not provide authentication, network transport,
replay journals, or automatic restart. Those policies belong to the embedding
application. If `AllowedModels` is non-empty, the runtime's initial model must
appear in it; model changes through ACP, slash commands, and subagents remain
restricted to the same list.

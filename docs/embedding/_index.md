---
title: "Embed in Go"
description: "Run the caretz agent directly inside your Go application."
---

# Embed caretz into Your App

Caretz can be used as a Go library when an app needs to run the agent directly
instead of shelling out to the `caretz` binary. To retain caretz's CLI flags,
configuration, and runtime while building your own executable, see
[Build on caretz's CLI](../extending-cli/_index.md).

## Copy, Compile, and Run

Create a small Go project and add caretz:

```sh
mkdir caretz-example
cd caretz-example
go mod init example.com/caretz-example
go get github.com/makkalot/caretz
```

Create a file named `main.go` with this code:

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
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "sk-...",
			Model:   "gpt-5",
		},
		WorkspaceDir: ".",
	})
	if err != nil {
		log.Fatal(err)
	}

	response, err := client.Run(ctx, "my-session", "review this project")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(response.Content)
}
```

The immediately equivalent local `Model` block is:

```go
Model: caretz.ModelOptions{
	BaseURL:       "http://127.0.0.1:8001/v1",
	APIKey:        "noset",
	Model:         "qwen-code",
	ContextWindow: 60000,
},
```

Compile and run the application after choosing the model block:

```sh
go build -o embedded-caretz .
./embedded-caretz
```

`NewClient` is explicit: it does not read caretz config files or environment
variables for you. Pass model, workspace, tools, sandbox, and state options from
your application. An embedding application may source those values from its own
config system if they should not be compiled into the binary.

For a complete app built this way, see
[coderun](https://github.com/makkalot/caretz/tree/main/examples/coderun): a
browser app where you describe a small Python program and watch the agent write
and run it. Other runnable apps are listed in
[`examples/`](https://github.com/makkalot/caretz/tree/main/examples).

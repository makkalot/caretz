# caretz

<p align="center">
  <img src="site/static/images/caretz-pixel/caretz_main.png" alt="Pixel-art Caretz mascot flying forward with a wrench" width="240">
</p>

caretz (Care Taker) is a small, sandboxed coding assistant in Go, built to work
well with the models you can run on your own hardware. Hosted
OpenAI-compatible providers work too.

## Nothing runs in the background

caretz doesn't start servers or listen for connections, and it has no
telemetry, analytics, or update checks. It talks only to the endpoints you
configure: your model provider and, if you set them up, a web search
provider and a vision model. With a local model, nothing leaves your
machine.

## Quick start

1. Install to `~/bin/caretz` (Linux and macOS). Either use the release
   installer:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/makkalot/caretz/main/install.sh | sh
   ```

   or build from source, if you'd rather not pipe a script into your shell. You
   need Go 1.25.8+, Git, and Make; there are no other dependencies:

   ```sh
   git clone https://github.com/makkalot/caretz.git
   cd caretz
   make install
   ```

2. Connect a model: llama.cpp, Ollama, LM Studio, your own server, or a hosted
   provider. Setup also offers web search (Tavily or Brave key) and a vision
   model for screenshots:

   ```sh
   caretz setup
   ```

3. Give it a task:

   ```sh
   caretz
   caretz -p "explain this repository"
   ```

If `caretz` isn't found, add `~/bin` to your `PATH`; see
[Installation](docs/installation.md), which also covers other versions and
building from source.

## Everyday use

```sh
caretz -p "explain this repository"        # one task, then exit
caretz --plan                              # look and plan, no edits (Shift+Tab toggles)
caretz -s 7f31a221                         # continue a saved session
caretz session list                        # find past sessions
caretz --profile ops                       # maintain this machine: packages, services, cleanup
```

In a session, `/goal Add a CSV export` plans the work, does it, and proves it
is done with real checks. See [Use caretz](docs/cli/_index.md).

## Why I made it

(From humans to humans version)

Why another harness there're already tons of them ?

- It all started as codecrafters project to learn how to create your own agentic loop. Since then i continued adding stuff cause it was fun to see how hard can it be ? At some point it became useful to me so i started using it for my daily tasks and my coding.
- Why not use opencode or pi ? All of them are great projects and i still use them (also the session management idea is inspired by pi (kudos)). Being developer and wondering how things work under the hood i got the itch to start one myself. Also I have some dislike about npm based projects with tons of the dependencies and their attacks.
- Is it vibe code slop ? The initial versions were written by hand but later i used AI to do all the writing of the code. All the changes reviewed by me and pushed back very frequently.

So it all started as learning exercise and then I realised that I need a harness that : 

- Has almost 0 external deps (except the `golang.org/x`)
- Is single binary and is written in Go by me :) 
- Can be used as lib and can be integrated easily in other projects.
- Can be used as a base CLI for other applications
- Works ok with small models (i did almost %90 of my testing on my 3090 with 27b qwens).
- Its code can be copied on single machine without any network, can be compiled and run against my local gpu!
- Is reliable, doesn't change all the time and I can expect similar results.
- Event driven and extensible by other tools.
- Is sandboxed by default and doesn't do nasty stuff when models are bit more stupid.
- Small cli tui with no fancy flashing garbage.
- Small agent that can be stripped to the minimum.


How do i use it daily  ? 

- as coding agent via CLI, inside Herdr, or via Zed editor ACP integration
- as a library inside small tools and automation


## What do we support

- skills
- subagents (sequential)
- [peer bots](docs/peers.md): long-lived agents you message asynchronously and can attach to
- openai compat api 
- configurable tools and a strippable prompt
- sandboxing (enabled by default, can be disabled)
- network whitelisting (disabled by default)
- repeated [context compaction](docs/sessions/long-sessions.md) for long sessions
- [internal task tracking](docs/sessions/long-sessions.md#keep-multi-step-work-on-track) that survives compaction and can be disabled
- persisted jsonpl session log data
- acp

What's not supported : 

- mcp 
- fancy ui
- anthropic models (too expensive didn't bother)
- parallel subagents

## Use it as a Go library

```sh
go get github.com/makkalot/caretz
```

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

See [Embedding caretz in Go](docs/embedding/_index.md), or
[Building on caretz's CLI](docs/extending-cli/_index.md) to make your own CLI with
caretz's flags, sandbox, and sessions. [`examples/`](examples/README.md) has
three complete apps built on these APIs: a git review subcommand, a browser
app that writes and runs Python, and an assistant for your Markdown notes.

## Documentation

The docs are split into small pages; browse them at
[caretz.dev/docs](https://caretz.dev/docs/) or in [`docs/`](docs/).

- **Start here:** [Install](docs/installation.md) ·
  [Local models](docs/local-models/_index.md)
- **Everyday use:** [Use caretz](docs/cli/_index.md) ·
  [Themes](docs/cli/themes.md) ·
  [Sessions](docs/sessions/_index.md) · [Goals](docs/goals/_index.md) ·
  [Schedules](docs/schedules/_index.md) · [Peer bots](docs/peers.md)
- **Sandboxing:** [Overview](docs/sandboxing/_index.md) ·
  [Network](docs/sandboxing/network.md) · [Files](docs/sandboxing/files.md) ·
  [Run one command outside](docs/sandboxing/escalation.md)
- **Configure:** [Configuration](docs/config/_index.md) ·
  [Use your ChatGPT plan](docs/config/chatgpt.md) ·
  [Profiles](docs/profiles/_index.md) ·
  [Instructions, skills, subagents](docs/customization/_index.md) ·
  [Tools and web search](docs/tools/_index.md) ·
  [Add your own tools](docs/tools/custom-cli.md)
- **Integrations:** [Zed and ACP editors](docs/acp.md) · [Herdr](docs/herdr.md)
- **Build on caretz:** [Embed in Go](docs/embedding/_index.md) ·
  [Build on the CLI](docs/extending-cli/_index.md) ·
  [Examples](examples/README.md) ·
  [Dependencies](docs/dependencies.md) · [Contributing](docs/contributing.md)
- **Help:** [Troubleshooting](docs/troubleshooting/_index.md)

package main

import (
	"context"
	"os"

	"github.com/makkalot/caretz/cli"
	"github.com/makkalot/caretz/internal/sandbox"
)

func main() {
	stopCleanup := sandbox.TerminateSandboxedProcessesOnExit()
	code := cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, cli.Options{})
	stopCleanup()
	os.Exit(code)
}

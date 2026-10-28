package main

import (
	"fmt"
	"os"

	"github.com/makkalot/caretz/internal/explorer"
)

func main() {
	if len(os.Args) < 2 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: check_explore SESSION_DIR")
		os.Exit(2)
	}
	sessionDir := os.Args[1]
	evts, err := explorer.ReadSessionEvents(sessionDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read events: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("events: %d\n", len(evts))
	if err := explorer.WriteSessionSummary(os.Stdout, evts, "debug", "tui"); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "render summary: %v\n", err)
		os.Exit(1)
	}
}

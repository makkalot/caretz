package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/makkalot/caretz/internal/explorer"
)

func main() {
	if len(os.Args) < 2 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: caretz-debug <session-id>")
		os.Exit(1)
	}
	cwd, _ := os.Getwd()
	sessionsDir := filepath.Join(cwd, ".caretz", "sessions")
	sessionID := os.Args[1]
	sessionDir, adapterType, err := explorer.FindSessionDir(sessionsDir, sessionID)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "find session: %v\n", err)
		os.Exit(1)
	}
	if err := explorer.StartExplorerDebug(sessionDir, sessionID, adapterType); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

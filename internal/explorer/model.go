package explorer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func StartExplorer(sessionDir, sessionID, adapterType string) error {
	events, err := ReadSessionEvents(sessionDir)
	if err != nil {
		return fmt.Errorf("failed to read session events: %w", err)
	}
	return WriteSessionSummary(os.Stdout, events, sessionID, adapterType)
}

func WriteSessionSummary(w io.Writer, events []ExplorerEvent, sessionID, adapterType string) error {
	if w == nil {
		w = io.Discard
	}
	if _, err := fmt.Fprintf(w, "Session %s/%s\n", adapterType, sessionID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Events: %d\n\n", len(events)); err != nil {
		return err
	}
	for i, event := range events {
		preview := event.Preview
		if preview == "" {
			preview = previewExplorerPayload(event.RawPayload, 160)
		}
		if _, err := fmt.Fprintf(w, "%4d  %-28s  %s\n", i+1, event.Type, preview); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "Use the event list above to inspect this session's history.")
	return err
}

func previewExplorerPayload(payload string, max int) string {
	payload = strings.Join(strings.Fields(payload), " ")
	if len(payload) <= max {
		return payload
	}
	if max <= 3 {
		return payload[:max]
	}
	return payload[:max-3] + "..."
}

func FindSessionDir(sessionsDir, sessionID string) (string, string, error) {
	return FindSessionDirWithAdapter(sessionsDir, sessionID, "")
}

func FindSessionDirWithAdapter(sessionsDir, sessionID, requestedAdapter string) (string, string, error) {
	if requestedAdapter != "" {
		dir := filepath.Join(sessionsDir, requestedAdapter, sessionID)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, requestedAdapter, nil
		}
		return "", "", fmt.Errorf("session %s not found in adapter %s", sessionID, requestedAdapter)
	}
	var matches []string
	for _, adapter := range []string{"cli", "tui", "slack", "telegram"} {
		dir := filepath.Join(sessionsDir, adapter, sessionID)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			matches = append(matches, adapter)
		}
	}
	if len(matches) == 1 {
		return filepath.Join(sessionsDir, matches[0], sessionID), matches[0], nil
	}
	if len(matches) > 1 {
		return "", "", fmt.Errorf("session %s exists in multiple adapters (%s); use --adapter", sessionID, strings.Join(matches, ", "))
	}
	return "", "", fmt.Errorf("session %s not found", sessionID)
}

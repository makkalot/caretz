package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
)

// SessionInfo holds metadata about a single session.
type SessionInfo struct {
	ID           string
	AdapterType  string
	EventCount   int
	MessageCount int // number of user-agent exchanges (responses)
	FirstPrompt  string
	CreatedAt    time.Time
	LastModified time.Time

	subagentSessionIDs []string
}

type ListOptions struct {
	IncludeSubagents bool
	Adapter          string
}

// storedFileEvent mirrors the on-disk JSON structure written by eskit's FileMemoryStore.
type storedFileEvent struct {
	OriginatorID      string      `json:"originator_id"`
	OriginatorVersion uint64      `json:"originator_version"`
	EventType         string      `json:"event_type"`
	Payload           interface{} `json:"payload"`
	CreatedAt         int64       `json:"created_at"`
}

// List scans the sessions directory and returns metadata for every discovered session.
// Results are sorted by LastModified descending (most recent first).
func List(baseDir string) ([]SessionInfo, error) {
	return ListWithOptions(baseDir, ListOptions{})
}

// ListWithOptions scans the sessions directory and returns discovered session metadata.
func ListWithOptions(baseDir string, options ListOptions) ([]SessionInfo, error) {
	adapters := []string{"cli", "tui", "slack", "telegram", "acp", "peer"}
	var sessions []SessionInfo
	subagentSessionIDs := make(map[string]bool)

	for _, adapter := range adapters {
		if options.Adapter != "" && adapter != options.Adapter {
			continue
		}
		adapterDir := filepath.Join(baseDir, adapter)
		entries, err := os.ReadDir(adapterDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to read adapter dir %s: %w", adapterDir, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessionID := entry.Name()
			sessionDir := filepath.Join(adapterDir, sessionID)
			info, err := readSessionInfo(sessionDir, adapter, sessionID)
			if err != nil {
				// Skip corrupted/unreadable sessions rather than failing the whole list.
				continue
			}
			sessions = append(sessions, *info)
			for _, childID := range info.subagentSessionIDs {
				subagentSessionIDs[childID] = true
			}
		}
	}

	if !options.IncludeSubagents {
		filtered := sessions[:0]
		for _, session := range sessions {
			if !subagentSessionIDs[session.ID] {
				filtered = append(filtered, session)
			}
		}
		sessions = filtered
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastModified.After(sessions[j].LastModified)
	})

	return sessions, nil
}

// readSessionInfo reads session metadata directly from the events.jsonpl file.
// TODO: switch to eventstore.NewFileMemoryStore once eskit provides a non-destructive
// Close() method (currently only Cleanup() exists, which deletes the file).
func readSessionInfo(sessionDir, adapterType, sessionID string) (*SessionInfo, error) {
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	stat, err := os.Stat(eventsPath)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(eventsPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	info := &SessionInfo{
		ID:           sessionID,
		AdapterType:  adapterType,
		LastModified: stat.ModTime(),
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	firstCreated := int64(0)

	for scanner.Scan() {
		line := scanner.Bytes()
		var fe storedFileEvent
		if err := json.Unmarshal(line, &fe); err != nil {
			continue
		}

		info.EventCount++

		if firstCreated == 0 || fe.CreatedAt < firstCreated {
			firstCreated = fe.CreatedAt
		}

		if info.FirstPrompt == "" && fe.EventType == eventing.LogMessageUserCreated {
			content := extractContent(fe.Payload)
			if content != "" {
				info.FirstPrompt = truncate(content, 60)
			}
		}

		if fe.EventType == eventing.LogMessageAgentCreated {
			info.MessageCount++
		}

		if fe.EventType == eventing.SubagentInvocationStarted {
			var invocation eventing.SubagentInvocation
			if decodePayload(fe.Payload, &invocation) && invocation.ChildSessionID != "" {
				info.subagentSessionIDs = append(info.subagentSessionIDs, invocation.ChildSessionID)
			}
		}
	}

	if firstCreated > 0 {
		info.CreatedAt = time.Unix(firstCreated, 0).UTC()
	} else {
		info.CreatedAt = stat.ModTime()
	}

	return info, scanner.Err()
}

func decodePayload(payload interface{}, target interface{}) bool {
	if payload == nil {
		return false
	}
	if raw, ok := payload.(string); ok {
		return json.Unmarshal([]byte(raw), target) == nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, target) == nil
}

// extractContent pulls the "content" field from a LogMessage payload.
// The payload may be stored as a raw string or as a JSON object.
func extractContent(payload interface{}) string {
	if payload == nil {
		return ""
	}
	if s, ok := payload.(string); ok {
		var lm eventing.LogMessage
		if err := json.Unmarshal([]byte(s), &lm); err == nil {
			return lm.Content
		}
		return ""
	}
	// Payload is an object (map[string]interface{})
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	var lm eventing.LogMessage
	if err := json.Unmarshal(b, &lm); err == nil {
		return lm.Content
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

package eventstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/types"
)

func TestInMemoryStoreAppendGetLogsAndDuplicates(t *testing.T) {
	store := NewInMemoryStore()
	event1 := types.NewEvent(types.NewOriginator("origin-1", 1), "Project.Created", `{"name":"one"}`)
	event2 := types.NewEvent(types.NewOriginator("origin-1", 2), "Project.Updated", `{"name":"two"}`)

	if err := store.Append(event1); err != nil {
		t.Fatalf("Append event1: %v", err)
	}
	if err := store.Append(event2); err != nil {
		t.Fatalf("Append event2: %v", err)
	}

	events, err := store.Get(types.NewOriginator("origin-1", 0), false)
	if err != nil {
		t.Fatalf("Get all: %v", err)
	}
	if len(events) != 2 || events[0].EventType != "Project.Created" || events[1].EventType != "Project.Updated" {
		t.Fatalf("events = %#v, want created then updated", events)
	}

	logs, err := store.Logs(0, 20, "Project")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(logs) != 2 || logs[0].ID != 1 || logs[1].ID != 2 {
		t.Fatalf("logs = %#v, want two ordered entries", logs)
	}

	if err := store.Append(event2); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate append error = %v, want ErrDuplicate", err)
	}
}

func TestFileMemoryStorePersistsCompatibleJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonpl")
	event1 := types.NewEvent(types.NewOriginator("origin-1", 1), "Project.Created", `{"name":"one"}`)
	event2 := types.NewEvent(types.NewOriginator("origin-1", 2), "Project.Updated", "")

	store1, err := NewFileMemoryStore(path)
	if err != nil {
		t.Fatalf("NewFileMemoryStore store1: %v", err)
	}
	if err := store1.Append(event1); err != nil {
		t.Fatalf("Append event1: %v", err)
	}
	if err := store1.Append(event2); err != nil {
		t.Fatalf("Append event2: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var stored []storedFileEvent
	for _, line := range splitNonEmptyLines(string(data)) {
		var event storedFileEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal stored line %q: %v", line, err)
		}
		stored = append(stored, event)
	}
	if len(stored) != 2 {
		t.Fatalf("stored events = %d, want 2", len(stored))
	}
	if stored[0].OriginatorID != "origin-1" || stored[0].OriginatorVersion != 1 || stored[0].EventType != "Project.Created" {
		t.Fatalf("stored[0] = %#v", stored[0])
	}
	if stored[1].Payload != nil {
		t.Fatalf("empty payload stored as %#v, want nil", stored[1].Payload)
	}

	store2, err := NewFileMemoryStore(path)
	if err != nil {
		t.Fatalf("NewFileMemoryStore store2: %v", err)
	}
	events, err := store2.Get(types.NewOriginator("origin-1", 0), false)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events after reopen = %d, want 2", len(events))
	}
	if events[0].Payload != `{"name":"one"}` || events[1].Payload != "" {
		t.Fatalf("payloads after reopen = %q, %q", events[0].Payload, events[1].Payload)
	}
	if events[0].OccurredOn.IsZero() || events[0].OccurredOn.Location() != time.UTC {
		t.Fatalf("OccurredOn after reopen = %v, want UTC timestamp", events[0].OccurredOn)
	}
}

func splitNonEmptyLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		if i > start {
			lines = append(lines, s[start:i])
		}
		start = i + 1
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOriginator(t *testing.T) {
	t.Run("NewOriginator creates valid instance", func(t *testing.T) {
		orig := NewOriginator("test-id", 1)
		if orig.ID != "test-id" {
			t.Errorf("expected ID 'test-id', got '%s'", orig.ID)
		}
		if orig.Version != 1 {
			t.Errorf("expected Version 1, got %d", orig.Version)
		}
	})

	t.Run("JSON serialization", func(t *testing.T) {
		orig := &Originator{
			ID:      "abc-123",
			Version: 42,
		}

		data, err := json.Marshal(orig)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded Originator
		err = json.Unmarshal(data, &decoded)
		if err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.ID != orig.ID {
			t.Errorf("ID mismatch: expected '%s', got '%s'", orig.ID, decoded.ID)
		}
		if decoded.Version != orig.Version {
			t.Errorf("Version mismatch: expected %d, got %d", orig.Version, decoded.Version)
		}
	})
}

func TestEvent(t *testing.T) {
	t.Run("NewEvent creates valid instance", func(t *testing.T) {
		orig := NewOriginator("entity-1", 1)
		event := NewEvent(orig, "User.Created", `{"name":"test"}`)

		if event.Originator.ID != "entity-1" {
			t.Errorf("unexpected originator ID: %s", event.Originator.ID)
		}
		if event.EventType != "User.Created" {
			t.Errorf("unexpected event type: %s", event.EventType)
		}
		if event.Payload != `{"name":"test"}` {
			t.Errorf("unexpected payload: %s", event.Payload)
		}
		if event.OccurredOn.IsZero() {
			t.Error("OccurredOn should be set")
		}
	})

	t.Run("PartitionKey extraction", func(t *testing.T) {
		tests := []struct {
			name        string
			eventType   string
			expectedKey string
		}{
			{"standard format", "User.Created", "User"},
			{"nested format", "Order.Item.Added", "Order"},
			{"no dot", "SimpleEvent", "SimpleEvent"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				event := &Event{EventType: tt.eventType}
				key := event.PartitionKey()
				if key != tt.expectedKey {
					t.Errorf("expected partition key '%s', got '%s'", tt.expectedKey, key)
				}
			})
		}
	})

	t.Run("JSON serialization", func(t *testing.T) {
		orig := NewOriginator("test-id", 5)
		event := &Event{
			Originator: orig,
			EventType:  "Project.Updated",
			Payload:    `{"status":"active"}`,
			OccurredOn: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		}

		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded Event
		err = json.Unmarshal(data, &decoded)
		if err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.EventType != event.EventType {
			t.Errorf("EventType mismatch: expected '%s', got '%s'", event.EventType, decoded.EventType)
		}
		if decoded.Originator.ID != event.Originator.ID {
			t.Errorf("Originator.ID mismatch")
		}
	})
}

func TestAppLogEntry(t *testing.T) {
	t.Run("NewAppLogEntry creates valid instance", func(t *testing.T) {
		orig := NewOriginator("entity-1", 1)
		event := NewEvent(orig, "User.Created", `{"email":"test@example.com"}`)
		entry := NewAppLogEntry(123, event)

		if entry.ID != 123 {
			t.Errorf("expected ID 123, got %d", entry.ID)
		}
		if entry.Event == nil {
			t.Error("Event should not be nil")
		}
		if entry.Event.EventType != "User.Created" {
			t.Errorf("unexpected event type: %s", entry.Event.EventType)
		}
	})

	t.Run("JSON serialization", func(t *testing.T) {
		orig := NewOriginator("test-id", 1)
		event := NewEvent(orig, "Order.Placed", `{"total":100}`)
		entry := &AppLogEntry{
			ID:    456,
			Event: event,
		}

		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded AppLogEntry
		err = json.Unmarshal(data, &decoded)
		if err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.ID != entry.ID {
			t.Errorf("ID mismatch: expected %d, got %d", entry.ID, decoded.ID)
		}
		if decoded.Event.EventType != entry.Event.EventType {
			t.Errorf("EventType mismatch")
		}
	})
}

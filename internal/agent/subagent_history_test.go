package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

func TestReadSubagentEndpointsIgnoresToolTraffic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.jsonpl")
	records := []struct {
		EventType string `json:"event_type"`
		Payload   any    `json:"payload"`
	}{
		{eventing.LLMUserMessageCreated, eventing.LLMUserMessage{Message: llm.Message{Role: llm.RoleUser, Content: "inspect this"}}},
		{"LLMToolCallResult.Created", map[string]any{"message": map[string]any{"Content": "secret tool output"}}},
		{eventing.LLMAgentResponseCreated, eventing.LLMAgentResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "final answer"}}},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	prompt, result, err := readSubagentEndpoints(path)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "inspect this" || result != "final answer" {
		t.Fatalf("readSubagentEndpoints() = %q, %q", prompt, result)
	}
}

package output

import (
	"bytes"
	"testing"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

func TestPromptOutputWritesOnlyRawFinalResponse(t *testing.T) {
	var stdout bytes.Buffer
	out := NewPromptOutput(&stdout)

	response := eventing.NewLLMAgentResponseWithReasoning("ch", "", 0, llm.Message{Role: llm.RoleAssistant, Content: "# Title\n\n**done**\n\n"}, llm.Usage{TotalTokens: 10}, 10, "", 0)
	if err := out.HandleLLMAgentResponseCreated(response); err != nil {
		t.Fatalf("HandleLLMAgentResponseCreated() error = %v", err)
	}
	empty := eventing.NewLLMAgentResponseWithReasoning("ch", "", 0, llm.Message{Role: llm.RoleAssistant, Content: "  "}, llm.Usage{}, 0, "", 0)
	if err := out.HandleLLMAgentResponseCreated(empty); err != nil {
		t.Fatalf("HandleLLMAgentResponseCreated(empty) error = %v", err)
	}

	if got, want := stdout.String(), "# Title\n\n**done**\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

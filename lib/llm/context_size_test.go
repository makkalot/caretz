package llm

import (
	"fmt"
	"testing"
)

func TestParseContextSizeErrorExtractsProviderLimits(t *testing.T) {
	err := fmt.Errorf(`request error: POST "http://127.0.0.1:8001/v1/chat/completions": 400 Bad Request {"code":400,"message":"request (157064 tokens) exceeds the available context size (60160 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":157064,"n_ctx":60160}`)

	contextErr, ok := ParseContextSizeError(err)
	if !ok {
		t.Fatal("expected context size error")
	}
	if contextErr.PromptTokens != 157064 {
		t.Fatalf("PromptTokens = %d, want 157064", contextErr.PromptTokens)
	}
	if contextErr.ContextSize != 60160 {
		t.Fatalf("ContextSize = %d, want 60160", contextErr.ContextSize)
	}
}

func TestParseContextSizeErrorWithoutProviderLimits(t *testing.T) {
	err := fmt.Errorf("request failed: context size exceeded")

	contextErr, ok := ParseContextSizeError(err)
	if !ok {
		t.Fatal("expected context size error")
	}
	if contextErr.PromptTokens != 0 {
		t.Fatalf("PromptTokens = %d, want 0", contextErr.PromptTokens)
	}
	if contextErr.ContextSize != 0 {
		t.Fatalf("ContextSize = %d, want 0", contextErr.ContextSize)
	}
}

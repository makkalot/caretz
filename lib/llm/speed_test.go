package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResponseSpeed(t *testing.T) {
	start := time.Unix(1000, 0)
	tests := []struct {
		name   string
		timing Timing
		tokens int64
		want   Speed
	}{
		{
			name:   "decode time from first token",
			timing: Timing{Start: start, FirstToken: start.Add(time.Second), End: start.Add(3 * time.Second)},
			tokens: 100,
			want:   Speed{TokensPerSecond: 50, TTFTMs: 1000},
		},
		{
			name:   "whole request without first token",
			timing: Timing{Start: start, End: start.Add(4 * time.Second)},
			tokens: 100,
			want:   Speed{TokensPerSecond: 25},
		},
		{
			name:   "no tokens",
			timing: Timing{Start: start, End: start.Add(time.Second)},
			want:   Speed{},
		},
		{
			name:   "no timing",
			tokens: 100,
			want:   Speed{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := Response{Usage: Usage{CompletionTokens: tt.tokens}, Timing: tt.timing}
			if got := resp.Speed(); got != tt.want {
				t.Fatalf("Speed() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func streamChat(t *testing.T, chunks ...string) Response {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL+"/v1", "mock-model")
	resp, err := provider.Chat("", []Message{{Role: RoleUser, Content: "hello"}}, nil, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Timing.Start.IsZero() || resp.Timing.End.Before(resp.Timing.Start) {
		t.Fatalf("timing = %+v, want start and end", resp.Timing)
	}
	return resp
}

func TestOpenAIChatStreamingTimesFromStreamedThinking(t *testing.T) {
	resp := streamChat(t,
		`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":3}}}`,
	)
	if resp.Usage.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %d, want 3", resp.Usage.ReasoningTokens)
	}
	if resp.Timing.FirstToken.IsZero() {
		t.Fatal("first token not recorded although thinking was streamed")
	}
}

func TestOpenAIChatStreamingIgnoresFirstTokenWhenThinkingHidden(t *testing.T) {
	resp := streamChat(t,
		`{"choices":[{"delta":{"content":"h"}}]}`,
		`{"choices":[{"delta":{"content":"i"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":50,"total_tokens":60,"completion_tokens_details":{"reasoning_tokens":48}}}`,
	)
	if !resp.Timing.FirstToken.IsZero() {
		t.Fatal("first token recorded although the thinking tokens never streamed")
	}
}

func TestOpenAIChatStreamingIgnoresFirstTokenForSingleChunk(t *testing.T) {
	resp := streamChat(t,
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	)
	if !resp.Timing.FirstToken.IsZero() {
		t.Fatal("first token recorded for output that arrived in one chunk")
	}
}

func TestFromOpenAIResponseUsageReadsReasoningTokens(t *testing.T) {
	var raw responseUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":10,"output_tokens":40,"total_tokens":50,"output_tokens_details":{"reasoning_tokens":30}}`), &raw); err != nil {
		t.Fatal(err)
	}
	if got := fromOpenAIResponseUsage(raw); got.CompletionTokens != 40 || got.ReasoningTokens != 30 {
		t.Fatalf("usage = %+v, want completion 40 with reasoning 30", got)
	}
}

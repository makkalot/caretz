package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIRequestShapeKeepsStableCachePrefixFirst(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "http://mock", "mock-model").(*openAIProvider)

	messages := provider.toOpenAIMessages("stable system prompt", []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	})
	messageJSON, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("failed to marshal messages: %v", err)
	}
	messageShape := string(messageJSON)

	systemIdx := strings.Index(messageShape, `"role":"system"`)
	userIdx := strings.Index(messageShape, `"role":"user"`)
	if systemIdx < 0 {
		t.Fatalf("marshaled messages missing system role: %s", messageShape)
	}
	if userIdx < 0 {
		t.Fatalf("marshaled messages missing user role: %s", messageShape)
	}
	if systemIdx > userIdx {
		t.Fatalf("system prompt must be the first provider message for cache stability: %s", messageShape)
	}

	tools := provider.toOpenAITools([]ToolDefinition{
		{
			Name:        "Read",
			Description: "Read file contents",
			Parameters:  map[string]any{"type": "object"},
		},
	})
	toolJSON, err := json.Marshal(tools)
	if err != nil {
		t.Fatalf("failed to marshal tools: %v", err)
	}
	toolShape := string(toolJSON)
	if !strings.Contains(toolShape, `"name":"Read"`) {
		t.Fatalf("marshaled tools missing Read definition: %s", toolShape)
	}
	if strings.Contains(messageShape, `"name":"Read"`) {
		t.Fatalf("tool definitions leaked into message stream: %s", messageShape)
	}
}

func TestOpenCodeHeadersUseSessionContext(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "https://opencode.ai/zen/go/v1", "mock-model").(*openAIProvider)
	ctx := WithSessionID(context.Background(), "caretz_session_hash")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint("/chat/completions"), nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error: %v", err)
	}

	provider.setOpenCodeHeaders(req, true)

	if got := req.Header.Get("X-Opencode-Session"); got != "caretz_session_hash" {
		t.Fatalf("X-Opencode-Session = %q, want caretz_session_hash", got)
	}
	if got := req.Header.Get("User-Agent"); got != openCodeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, openCodeUserAgent)
	}
}

func TestOpenCodeHeadersAreNotSentToOtherProviders(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "https://api.example.com/v1", "mock-model").(*openAIProvider)
	ctx := WithSessionID(context.Background(), "caretz_session_hash")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint("/chat/completions"), nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error: %v", err)
	}

	provider.setOpenCodeHeaders(req, true)

	if got := req.Header.Get("X-Opencode-Session"); got != "" {
		t.Fatalf("X-Opencode-Session = %q, want empty", got)
	}
	if got := req.Header.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %q, want empty", got)
	}
}

func TestOpenCodeModelDiscoveryOmitsConversationHeader(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "https://api.opencode.ai/v1", "mock-model").(*openAIProvider)
	ctx := WithSessionID(context.Background(), "caretz_session_hash")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.endpoint("/models"), nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error: %v", err)
	}

	provider.setOpenCodeHeaders(req, false)

	if got := req.Header.Get("X-Opencode-Session"); got != "" {
		t.Fatalf("X-Opencode-Session = %q, want empty", got)
	}
	if got := req.Header.Get("User-Agent"); got != openCodeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, openCodeUserAgent)
	}
}

func TestOpenAIToolSchemaDropsConflictingAnyOfForStrictProviders(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "http://mock", "mock-model").(*openAIProvider)
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent": map[string]any{"type": "string"},
			"task":  map[string]any{"type": "string"},
		},
		"anyOf": []map[string]any{
			{"required": []string{"agent", "task"}},
			{"required": []string{"subagent_type", "prompt"}},
		},
	}

	tools := provider.toOpenAITools([]ToolDefinition{{
		Name:        "CallSubagent",
		Description: "Run a subagent",
		Parameters:  parameters,
	}})
	normalized := normalizeToolParameters(parameters)
	if got := normalized["type"]; got != "object" {
		t.Fatalf("normalized root schema type = %v, want object: %#v", got, normalized)
	}
	if _, ok := normalized["anyOf"]; ok {
		t.Fatalf("normalized schema still has provider-rejected anyOf: %#v", normalized)
	}

	toolJSON, err := json.Marshal(tools)
	if err != nil {
		t.Fatalf("failed to marshal tools: %v", err)
	}
	shape := string(toolJSON)
	if !strings.Contains(shape, `"name":"CallSubagent"`) {
		t.Fatalf("marshaled tools missing CallSubagent definition: %s", shape)
	}
	if !strings.Contains(shape, `"type":"object"`) {
		t.Fatalf("serialized schema should keep required root object type: %s", shape)
	}
	if strings.Contains(shape, `"anyOf"`) {
		t.Fatalf("serialized schema should omit provider-rejected anyOf: %s", shape)
	}

	if _, ok := parameters["type"]; !ok {
		t.Fatalf("original parameters map was mutated: %#v", parameters)
	}
	branches := parameters["anyOf"].([]map[string]any)
	for _, branch := range branches {
		if _, ok := branch["type"]; ok {
			t.Fatalf("original anyOf branch was mutated: %#v", parameters)
		}
	}
}

func TestNormalizeToolParametersLeavesOrdinarySchemaUnchanged(t *testing.T) {
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
		"required": []string{"file_path"},
	}
	normalized := normalizeToolParameters(parameters)
	originalJSON, err := json.Marshal(parameters)
	if err != nil {
		t.Fatalf("failed to marshal original: %v", err)
	}
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("failed to marshal normalized: %v", err)
	}
	if string(normalizedJSON) != string(originalJSON) {
		t.Fatalf("ordinary schema changed:\noriginal:   %s\nnormalized: %s", originalJSON, normalizedJSON)
	}
}

func TestOpenAIRequestShapeIncludesReasoningEffortWhenConfigured(t *testing.T) {
	provider := NewOpenAIProviderWithOptions("test-key", "http://mock", "mock-model", OpenAIProviderOptions{
		ReasoningEffort: "xhigh",
	}).(*openAIProvider)
	if provider.ReasoningEffort() != "xhigh" {
		t.Fatalf("ReasoningEffort() = %q, want xhigh", provider.ReasoningEffort())
	}

	params := provider.chatCompletionParams("", []Message{{Role: RoleUser, Content: "hello"}}, nil)
	requestJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	if !strings.Contains(string(requestJSON), `"reasoning_effort":"xhigh"`) {
		t.Fatalf("marshaled request missing reasoning_effort: %s", requestJSON)
	}
}

func TestOpenAIRequestShapeOmitsReasoningEffortWhenEmpty(t *testing.T) {
	provider := NewOpenAIProviderWithOptions("test-key", "http://mock", "mock-model", OpenAIProviderOptions{}).(*openAIProvider)

	params := provider.chatCompletionParams("", []Message{{Role: RoleUser, Content: "hello"}}, nil)
	requestJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	if strings.Contains(string(requestJSON), "reasoning_effort") {
		t.Fatalf("marshaled request should omit reasoning_effort: %s", requestJSON)
	}
}

func TestOpenAIResponsesRequestShapeUsesReasoningSummaryAndToolCallIDs(t *testing.T) {
	provider := NewOpenAIProviderWithOptions("test-key", "http://mock", "mock-model", OpenAIProviderOptions{
		ReasoningEffort: "high",
	}).(*openAIProvider)

	params := provider.responseNewParams("system prompt", []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "Read", Arguments: `{"file_path":"main.go"}`}}},
		{Role: RoleTool, ToolCallID: "call-1", Content: "file contents"},
	}, []ToolDefinition{{Name: "Read", Description: "Read file", Parameters: map[string]any{"type": "object"}}})
	requestJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	shape := string(requestJSON)
	for _, want := range []string{
		`"instructions":"system prompt"`,
		`"summary":"auto"`,
		`"effort":"high"`,
		`"type":"function_call"`,
		`"call_id":"call-1"`,
		`"type":"function_call_output"`,
		`"name":"Read"`,
	} {
		if !strings.Contains(shape, want) {
			t.Fatalf("responses request missing %s: %s", want, shape)
		}
	}
	if strings.Contains(shape, "reasoning_summary") {
		t.Fatalf("display-only reasoning field leaked into responses input: %s", shape)
	}
}

func TestOpenAIResponsesRequestShapeKeepsEmptyRequiredContent(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "https://api.openai.com/v1", "mock-model").(*openAIProvider)
	params := provider.responseNewParams("", []Message{
		{Role: RoleSystem, Content: ""},
		{Role: RoleUser, Content: ""},
		{Role: RoleTool, ToolCallID: "call-1", Content: ""},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "Read", Arguments: "{}"}}},
	}, nil)
	requestJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	shape := string(requestJSON)
	if strings.Count(shape, `"content":""`) != 2 {
		t.Fatalf("expected empty system and user content fields: %s", shape)
	}
	if !strings.Contains(shape, `"output":""`) {
		t.Fatalf("expected empty tool output field: %s", shape)
	}
	if strings.Contains(shape, `"type":"function_call","content"`) {
		t.Fatalf("function call should not have a content field: %s", shape)
	}
}

func TestOpenAIChatRequestShapeKeepsEmptyRequiredContent(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "http://mock", "mock-model").(*openAIProvider)
	params := provider.chatCompletionParams("", []Message{
		{Role: RoleUser, Content: ""},
		{Role: RoleTool, ToolCallID: "call-1", Content: ""},
	}, nil)
	requestJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	if strings.Count(string(requestJSON), `"content":""`) != 2 {
		t.Fatalf("expected empty user and tool content fields: %s", requestJSON)
	}
}

func TestReasoningContentFromRawOpenAICompatiblePayload(t *testing.T) {
	raw := `{"role":"assistant","content":"4071","reasoning_content":"computed 177 * 23"}`
	if got := reasoningContentFromRaw(raw); got != "computed 177 * 23" {
		t.Fatalf("reasoningContentFromRaw() = %q, want reasoning_content", got)
	}
}

func TestOpenAIProviderUsesExplicitProtocolAndDefaultsToChatCompletions(t *testing.T) {
	responses := NewOpenAIProviderWithOptions("test-key", "https://opencode.ai/zen/go/v1", "gpt-5", OpenAIProviderOptions{Protocol: ProtocolResponses}).(*openAIProvider)
	if !responses.supportsResponsesAPI() {
		t.Fatal("explicit responses protocol should use Responses API")
	}
	defaultProtocol := NewOpenAIProvider("test-key", "https://api.openai.com/v1", "gpt-5").(*openAIProvider)
	if defaultProtocol.supportsResponsesAPI() {
		t.Fatal("an omitted protocol should default to Chat Completions")
	}
}

func TestOpenAIReplaySanitizesMalformedToolCallArguments(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "http://mock", "mock-model").(*openAIProvider)

	messages := provider.toOpenAIMessages("", []Message{
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{
				{
					ID:        "tc-1",
					Name:      "Edit",
					Arguments: `{"file_path":"main.go","new_text":"missing close}`,
				},
			},
		},
	})
	messageJSON, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("failed to marshal messages: %v", err)
	}

	messageShape := string(messageJSON)
	if !strings.Contains(messageShape, `_raw_arguments`) {
		t.Fatalf("malformed arguments were not wrapped for replay: %s", messageShape)
	}
	if strings.Contains(messageShape, `"arguments":"{\"file_path\":\"main.go\",\"new_text\":\"missing close}"`) {
		t.Fatalf("malformed arguments were replayed directly: %s", messageShape)
	}
}

func TestSanitizeToolArgumentsForReplay(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{
			name: "valid object",
			args: `{"command":"echo ok"}`,
			want: `{"command":"echo ok"}`,
		},
		{
			name: "empty",
			args: "",
			want: "{}",
		},
		{
			name: "non object json",
			args: `"not an object"`,
			want: `{"_raw_arguments":"\"not an object\""}`,
		},
		{
			name: "malformed json",
			args: `{"command":"unterminated}`,
			want: `{"_raw_arguments":"{\"command\":\"unterminated}"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeToolArgumentsForReplay(tt.args); got != tt.want {
				t.Fatalf("sanitizeToolArgumentsForReplay(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFromOpenAIUsagePreservesCachedPromptTokens(t *testing.T) {
	rawUsage := openAIUsage{
		PromptTokens:     100,
		CompletionTokens: 25,
		TotalTokens:      125,
	}
	rawUsage.PromptTokensDetail.CachedTokens = 40
	usage := fromOpenAIUsage(rawUsage)

	if usage.PromptTokens != 100 {
		t.Fatalf("prompt tokens = %d, want 100", usage.PromptTokens)
	}
	if usage.CachedPromptTokens != 40 {
		t.Fatalf("cached prompt tokens = %d, want 40", usage.CachedPromptTokens)
	}
	if usage.CompletionTokens != 25 {
		t.Fatalf("completion tokens = %d, want 25", usage.CompletionTokens)
	}
	if usage.TotalTokens != 125 {
		t.Fatalf("total tokens = %d, want 125", usage.TotalTokens)
	}
}

func TestOpenAIRateLimitDetection(t *testing.T) {
	err := &apiError{StatusCode: http.StatusTooManyRequests}
	if !isRateLimitError(err) {
		t.Fatal("expected 429 openai error to be treated as rate limited")
	}

	wrapped := errors.New(`request error: POST "http://mock": 429 Too Many Requests {"type":"rate_limit_error"}`)
	if !isRateLimitError(wrapped) {
		t.Fatal("expected wrapped 429 text to be treated as rate limited")
	}
}

func TestOpenAIRetryDelayCapsExponentialBackoff(t *testing.T) {
	provider := &openAIProvider{
		initialBackoff: 100 * time.Millisecond,
		maxBackoff:     350 * time.Millisecond,
	}

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 100 * time.Millisecond},
		{attempt: 2, want: 200 * time.Millisecond},
		{attempt: 3, want: 350 * time.Millisecond},
		{attempt: 4, want: 350 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := provider.retryDelay(tt.attempt); got != tt.want {
			t.Fatalf("retryDelay(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestOpenAIChatStreamingMapsDeltasToolCallsAndUsage(t *testing.T) {
	var requestShape chatCompletionParams
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestShape); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"hel","reasoning_content":"think "}}],"usage":null}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"lo","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"Read","arguments":"{\"file_"}}]}}],"usage":null}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"path\":\"main.go\"}"}}]}}],"usage":null}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL+"/v1", "mock-model").(*openAIProvider)
	var deltas strings.Builder
	resp, err := provider.Chat("", []Message{{Role: RoleUser, Content: "hello"}}, nil, func(delta string) {
		deltas.WriteString(delta)
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if !requestShape.Stream {
		t.Fatalf("stream flag was not set in request: %#v", requestShape)
	}
	if requestShape.StreamOptions == nil || !requestShape.StreamOptions.IncludeUsage {
		t.Fatalf("stream usage was not requested: %#v", requestShape.StreamOptions)
	}
	if got := deltas.String(); got != "hello" {
		t.Fatalf("deltas = %q, want hello", got)
	}
	if resp.Content != "hello" || resp.Message.Content != "hello" {
		t.Fatalf("content not mapped: %#v", resp)
	}
	if resp.ReasoningSummary != "think" {
		t.Fatalf("reasoning summary = %q, want think", resp.ReasoningSummary)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CachedPromptTokens != 4 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage not mapped: %#v", resp.Usage)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call-1" || resp.ToolCalls[0].Name != "Read" || resp.ToolCalls[0].Arguments != `{"file_path":"main.go"}` {
		t.Fatalf("tool calls not mapped: %#v", resp.ToolCalls)
	}
}

func TestOpenAIChatFallsBackWhenStreamUsageUnsupported(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var requestShape chatCompletionParams
		if err := json.NewDecoder(r.Body).Decode(&requestShape); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			if requestShape.StreamOptions == nil || !requestShape.StreamOptions.IncludeUsage {
				t.Fatalf("first attempt should request stream usage: %#v", requestShape)
			}
			http.Error(w, `{"error":{"message":"include_usage unsupported"}}`, http.StatusBadRequest)
			return
		}
		if requestShape.StreamOptions != nil {
			t.Fatalf("fallback attempt should omit stream_options: %#v", requestShape.StreamOptions)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("test-key", server.URL+"/v1", "mock-model", OpenAIProviderOptions{
		MaxAttempts:    1,
		RequestTimeout: time.Second,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}).(*openAIProvider)
	resp, err := provider.Chat("", []Message{{Role: RoleUser, Content: "hello"}}, nil, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
}

func TestOpenAIResponsesStreamingMapsReasoningToolCallsAndUsage(t *testing.T) {
	var requestShape responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %s, want /v1/responses", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestShape); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"done"}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.reasoning_summary_text.done","text":"checked"}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc-id","call_id":"call-1","name":"Read","arguments":"{\"file_path\":\"main.go\"}"}}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":7,"total_tokens":27,"input_tokens_details":{"cached_tokens":8}}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("test-key", server.URL+"/v1", "gpt-5", OpenAIProviderOptions{
		ReasoningEffort: "high",
	}).(*openAIProvider)
	resp, err := provider.chatResponsesStreaming(contextWithTimeout(t), "system prompt", []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "Read", Arguments: `{"file_path":"main.go"}`}}},
		{Role: RoleTool, ToolCallID: "call-1", Content: "file contents"},
	}, []ToolDefinition{{Name: "Read", Description: "Read file", Parameters: map[string]any{"type": "object"}}}, nil)
	if err != nil {
		t.Fatalf("chatResponsesStreaming() error: %v", err)
	}
	if !requestShape.Stream {
		t.Fatalf("responses stream flag was not set: %#v", requestShape)
	}
	if requestShape.Instructions != "system prompt" || requestShape.Reasoning.Summary != "auto" || requestShape.Reasoning.Effort != "high" {
		t.Fatalf("responses request reasoning/system shape wrong: %#v", requestShape)
	}
	if resp.Content != "done" || resp.ReasoningSummary != "checked" {
		t.Fatalf("response text/reasoning not mapped: %#v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call-1" || resp.ToolCalls[0].Name != "Read" {
		t.Fatalf("tool calls not mapped: %#v", resp.ToolCalls)
	}
	if resp.Usage.PromptTokens != 20 || resp.Usage.CachedPromptTokens != 8 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 27 {
		t.Fatalf("usage not mapped: %#v", resp.Usage)
	}
}

func contextWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatGPTPlanRequestShape(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %s, want /responses even without a configured protocol", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3}}}\n\n")
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", server.URL, "gpt-test", OpenAIProviderOptions{ChatGPTPlan: true}).(*openAIProvider)
	ctx := WithSessionID(context.Background(), "session-123")
	resp, err := provider.ChatContext(ctx, "be terse", []Message{
		{Role: RoleSystem, Content: "repo notes"},
		{Role: RoleUser, Content: "hi"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("ChatContext() error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
	if store, ok := body["store"].(bool); !ok || store {
		t.Fatalf("store = %v, want false", body["store"])
	}
	if body["stream"] != true {
		t.Fatalf("stream = %v, want true", body["stream"])
	}
	if body["prompt_cache_key"] != "session-123" {
		t.Fatalf("prompt_cache_key = %v, want the session ID", body["prompt_cache_key"])
	}
	raw, _ := json.Marshal(body["input"])
	if strings.Contains(string(raw), `"role":"system"`) || !strings.Contains(string(raw), `"role":"developer"`) {
		t.Fatalf("system items must become developer items: %s", raw)
	}
}

func TestResponsesRequestOmitsPlanFieldsByDefault(t *testing.T) {
	provider := NewOpenAIProviderWithOptions("key", "http://mock", "m", OpenAIProviderOptions{Protocol: ProtocolResponses}).(*openAIProvider)
	params := provider.responseNewParams("sys", []Message{{Role: RoleSystem, Content: "x"}}, nil)
	raw, _ := json.Marshal(params)
	shape := string(raw)
	if strings.Contains(shape, `"store"`) || strings.Contains(shape, `"prompt_cache_key"`) {
		t.Fatalf("plan-only fields leaked into a regular request: %s", shape)
	}
	if !strings.Contains(shape, `"role":"system"`) {
		t.Fatalf("regular Responses requests keep system items: %s", shape)
	}
}

func TestChatGPTPlanUsageLimitInStreamIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"limit reached\"}}}\n\n")
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", server.URL, "m", OpenAIProviderOptions{
		ChatGPTPlan: true, MaxAttempts: 3, InitialBackoff: time.Millisecond,
	})
	_, err := provider.(ContextProvider).ChatContext(context.Background(), "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("ChatContext() succeeded, want usage limit error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1: a usage limit is not a rate limit to wait out", got)
	}
	if status, ok := APIErrorStatus(err); !ok || status != http.StatusTooManyRequests {
		t.Fatalf("APIErrorStatus = %d, %v; want 429", status, ok)
	}
	if !strings.Contains(err.Error(), "ChatGPT Settings > Usage") {
		t.Fatalf("error %q should point at ChatGPT usage settings", err)
	}
}

func TestChatGPTPlanTransientStreamErrorIsRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_unavailable\",\"message\":\"try later\"}}}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", server.URL, "m", OpenAIProviderOptions{
		ChatGPTPlan: true, MaxAttempts: 3, InitialBackoff: time.Millisecond,
	})
	if _, err := provider.(ContextProvider).ChatContext(context.Background(), "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("ChatContext() error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestReadAPIErrorUsesDetailBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"detail":"System messages are not allowed"}`)
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", server.URL, "m", OpenAIProviderOptions{Protocol: ProtocolResponses, MaxAttempts: 1})
	_, err := provider.(ContextProvider).ChatContext(context.Background(), "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "System messages are not allowed") {
		t.Fatalf("error = %v, want the detail message", err)
	}
}

func TestListOpenAIModelsReadsChatGPTPlanShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"models":[
			{"slug":"gpt-6.1-sol","visibility":"list"},
			{"slug":"gpt-reserve","visibility":"hide"},
			{"slug":"gpt-5.6-luna","visibility":"list"}]}`)
	}))
	defer server.Close()

	models, err := ListOpenAIModels(context.Background(), "", server.URL, OpenAIProviderOptions{})
	if err != nil {
		t.Fatalf("ListOpenAIModels() error: %v", err)
	}
	if got := strings.Join(models, ","); got != "gpt-6.1-sol,gpt-5.6-luna" {
		t.Fatalf("models = %s, want listed models in server order", got)
	}
}

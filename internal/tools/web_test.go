package tools

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/llm"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func textResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRegistryToolDefinitions_WebSearchGatedByProviderKey(t *testing.T) {
	withoutKey := (&Registry{}).ToolDefinitions()
	if hasToolDefinition(withoutKey, "WebSearch") {
		t.Fatal("web search should not be registered without a web provider key")
	}
	if !hasToolDefinition(withoutKey, "WebFetch") {
		t.Fatal("web fetch should be registered without a web provider key")
	}

	withTavilyKey := (&Registry{TavilyAPIKey: "test-key"}).ToolDefinitions()
	if !hasToolDefinition(withTavilyKey, "WebSearch") {
		t.Fatal("expected WebSearch definition when Tavily key is set")
	}
	if !hasToolDefinition(withTavilyKey, "WebFetch") {
		t.Fatal("expected WebFetch definition when Tavily key is set")
	}

	withBraveKey := (&Registry{BraveSearchAPIKey: "brave-key"}).ToolDefinitions()
	if !hasToolDefinition(withBraveKey, "WebSearch") {
		t.Fatal("expected WebSearch definition when Brave key is set")
	}
	if !hasToolDefinition(withBraveKey, "WebFetch") {
		t.Fatal("expected WebFetch definition when Brave key is set")
	}

}

func TestRegistryToolDefinitions_WebFetchOmitsRenderJS(t *testing.T) {
	defs := (&Registry{BraveSearchAPIKey: "brave-key"}).ToolDefinitions()
	for _, def := range defs {
		if def.Name != "WebFetch" {
			continue
		}
		params, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("WebFetch properties missing: %#v", def.Parameters)
		}
		if _, ok := params["render_js"]; ok {
			t.Fatalf("WebFetch render_js property should not be in core schema: %#v", params)
		}
		return
	}
	t.Fatal("WebFetch definition missing")
}

func TestHandleToolCall_WebSearchInvalidWithoutProviderKey(t *testing.T) {
	registry := &Registry{}
	_, err := registry.HandleToolCall(makeToolCall("WebSearch", "web-1", map[string]any{
		"query": "golang",
	}))
	if err == nil || !strings.Contains(err.Error(), "invalid tool") {
		t.Fatalf("expected invalid tool error without Tavily key, got %v", err)
	}
}

func TestHandleWebSearchToolCall_TavilyProvider(t *testing.T) {
	var captured map[string]any
	registry := &Registry{
		TavilyAPIKey: "test-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", req.Method)
			}
			if req.URL.String() != tavilySearchURL {
				t.Fatalf("url = %s, want %s", req.URL.String(), tavilySearchURL)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("authorization header = %q", got)
			}
			if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return jsonResponse(200, `{
				"answer":"Use Go 1.25.",
				"results":[{"title":"Go","url":"https://go.dev","content":"The Go programming language","score":0.92}]
			}`), nil
		}),
	}

	msg, err := registry.HandleToolCall(makeToolCall("WebSearch", "web-1", map[string]any{
		"query":       "latest go",
		"max_results": 100,
		"time_range":  "week",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured["query"] != "latest go" {
		t.Errorf("query = %v", captured["query"])
	}
	if captured["max_results"] != float64(MaxWebSearchResults) {
		t.Errorf("max_results = %v, want %d", captured["max_results"], MaxWebSearchResults)
	}
	if captured["search_depth"] != "basic" {
		t.Errorf("search_depth = %v, want basic", captured["search_depth"])
	}

	if msg.Role != llm.RoleTool || msg.ToolCallID != "web-1" {
		t.Fatalf("unexpected tool message metadata: %+v", msg)
	}
	for _, want := range []string{"[WebSearch: latest go", "Answer: Use Go 1.25.", "https://go.dev", "The Go programming language"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("output missing %q:\n%s", want, msg.Content)
		}
	}
}

func TestHandleWebSearchToolCall_BraveProvider(t *testing.T) {
	registry := &Registry{
		BraveSearchAPIKey: "brave-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", req.Method)
			}
			if req.URL.Scheme+"://"+req.URL.Host+req.URL.Path != braveSearchURL {
				t.Fatalf("url = %s, want %s", req.URL.String(), braveSearchURL)
			}
			if got := req.Header.Get("X-Subscription-Token"); got != "brave-key" {
				t.Fatalf("subscription token header = %q", got)
			}
			values := req.URL.Query()
			wantQuery := "latest go site:go.dev -site:example.com"
			if got := values.Get("q"); got != wantQuery {
				t.Fatalf("q = %q, want %q", got, wantQuery)
			}
			if got := values.Get("count"); got != "20" {
				t.Fatalf("count = %q, want 20", got)
			}
			if got := values.Get("freshness"); got != "pw" {
				t.Fatalf("freshness = %q, want pw", got)
			}
			return jsonResponse(200, `{
				"web": {
					"results": [
						{
							"title": "Go",
							"url": "https://go.dev",
							"description": "The Go programming language",
							"extra_snippets": ["Build simple, secure systems."]
						}
					]
				}
			}`), nil
		}),
	}

	msg, err := registry.HandleToolCall(makeToolCall("WebSearch", "web-1", map[string]any{
		"query":           "latest go",
		"max_results":     100,
		"time_range":      "week",
		"include_domains": []string{"go.dev"},
		"exclude_domains": []string{"example.com"},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if msg.Role != llm.RoleTool || msg.ToolCallID != "web-1" {
		t.Fatalf("unexpected tool message metadata: %+v", msg)
	}
	for _, want := range []string{"[WebSearch: latest go", "https://go.dev", "The Go programming language", "Build simple, secure systems."} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("output missing %q:\n%s", want, msg.Content)
		}
	}
}

func TestHandleWebSearchToolCall_BravePreferredOverTavily(t *testing.T) {
	registry := &Registry{
		BraveSearchAPIKey: "brave-key",
		TavilyAPIKey:      "tavily-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method = %s, want Brave GET", req.Method)
			}
			if got := req.Header.Get("X-Subscription-Token"); got != "brave-key" {
				t.Fatalf("subscription token header = %q", got)
			}
			return jsonResponse(200, `{"web":{"results":[]}}`), nil
		}),
	}

	msg, err := registry.HandleToolCall(makeToolCall("WebSearch", "web-1", map[string]any{
		"query": "latest go",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg.Content, "No results found.") {
		t.Fatalf("unexpected output:\n%s", msg.Content)
	}
}

func TestHandleWebFetchToolCall_DirectHTTPWithBraveOnly(t *testing.T) {
	registry := &Registry{
		BraveSearchAPIKey: "brave-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", req.Method)
			}
			return textResponse(200, "text/html; charset=utf-8", `<html><body><h1>Hello</h1><p>Readable page</p></body></html>`), nil
		}),
	}

	msg, err := registry.HandleToolCall(makeToolCall("WebFetch", "fetch-1", map[string]any{
		"url": "http://93.184.216.34",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"source=direct-http", "Hello", "Readable page"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("output missing %q:\n%s", want, msg.Content)
		}
	}
}

func TestHandleWebFetchToolCall_TavilyExtractSuccess(t *testing.T) {
	registry := &Registry{
		TavilyAPIKey: "test-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != tavilyExtractURL {
				t.Fatalf("url = %s, want %s", req.URL.String(), tavilyExtractURL)
			}
			return jsonResponse(200, `{
				"results":[{"url":"https://example.com","raw_content":"# Title\nExtracted content"}]
			}`), nil
		}),
	}

	msg, err := registry.HandleToolCall(makeToolCall("WebFetch", "fetch-1", map[string]any{
		"url": "https://example.com",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"source=tavily-extract", "# Title", "Extracted content"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("output missing %q:\n%s", want, msg.Content)
		}
	}
}

func TestHandleWebFetchToolCall_HTTPFallback(t *testing.T) {
	requests := 0
	registry := &Registry{
		TavilyAPIKey: "test-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			switch requests {
			case 1:
				return jsonResponse(200, `{"failed_results":[{"url":"http://93.184.216.34","error":"extract unavailable"}]}`), nil
			case 2:
				return textResponse(200, "text/html; charset=utf-8", `<html><head><style>.x{}</style><script>alert(1)</script></head><body><h1>Hello</h1><p>Readable page</p></body></html>`), nil
			default:
				t.Fatalf("unexpected request %d to %s", requests, req.URL.String())
				return nil, nil
			}
		}),
	}

	msg, err := registry.HandleToolCall(makeToolCall("WebFetch", "fetch-1", map[string]any{
		"url": "http://93.184.216.34",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"source=direct-http", "Hello", "Readable page"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("output missing %q:\n%s", want, msg.Content)
		}
	}
	if strings.Contains(msg.Content, "alert") || strings.Contains(msg.Content, ".x") {
		t.Errorf("html cleanup leaked script/style content:\n%s", msg.Content)
	}
}

func TestHandleWebFetchToolCall_IgnoresLegacyRenderJSArgument(t *testing.T) {
	registry := &Registry{
		BraveSearchAPIKey: "brave-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return textResponse(200, "text/html; charset=utf-8", `<html><body><h1>Hello</h1></body></html>`), nil
		}),
	}

	msg, err := registry.HandleToolCall(makeToolCall("WebFetch", "fetch-1", map[string]any{
		"url":       "http://93.184.216.34",
		"render_js": true,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg.Content, "source=direct-http") || !strings.Contains(msg.Content, "Hello") {
		t.Fatalf("unexpected output:\n%s", msg.Content)
	}
}

func TestHandleWebFetchToolCall_BlocksPrivateHTTPFallback(t *testing.T) {
	registry := &Registry{
		TavilyAPIKey: "test-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(200, `{"failed_results":[{"url":"http://127.0.0.1","error":"extract unavailable"}]}`), nil
		}),
	}

	_, err := registry.HandleToolCall(makeToolCall("WebFetch", "fetch-1", map[string]any{
		"url": "http://127.0.0.1",
	}))
	if err == nil || !strings.Contains(err.Error(), "blocked private or local address") {
		t.Fatalf("expected private address block, got %v", err)
	}
}

func TestHandleWebFetchToolCall_RejectsUnsupportedContentType(t *testing.T) {
	requests := 0
	registry := &Registry{
		TavilyAPIKey: "test-key",
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return jsonResponse(200, `{"results":[]}`), nil
			}
			return textResponse(200, "application/octet-stream", "binary"), nil
		}),
	}

	_, err := registry.HandleToolCall(makeToolCall("WebFetch", "fetch-1", map[string]any{
		"url": "http://93.184.216.34",
	}))
	if err == nil || !strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("expected unsupported content type error, got %v", err)
	}
}

func hasToolDefinition(defs []llm.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

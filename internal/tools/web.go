package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/lib/llm"
)

const (
	DefaultWebSearchResults = 5
	MaxWebSearchResults     = 20
	DefaultWebFetchBytes    = 20 * 1024
	tavilySearchURL         = "https://api.tavily.com/search"
	tavilyExtractURL        = "https://api.tavily.com/extract"
	braveSearchURL          = "https://api.search.brave.com/res/v1/web/search"
)

type WebSearchToolArguments struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results"`
	SearchDepth    string   `json:"search_depth"`
	Topic          string   `json:"topic"`
	TimeRange      string   `json:"time_range"`
	IncludeDomains []string `json:"include_domains"`
	ExcludeDomains []string `json:"exclude_domains"`
}

type WebFetchToolArguments struct {
	URL      string `json:"url"`
	Query    string `json:"query"`
	MaxBytes int    `json:"max_bytes"`
	Format   string `json:"format"`
}

type WebSearchProvider interface {
	Search(ctx context.Context, args WebSearchToolArguments) (WebSearchResponse, error)
}

type WebSearchResponse struct {
	Answer  string
	Results []WebSearchResult
}

type WebSearchResult struct {
	Title   string
	URL     string
	Snippet string
	Score   float64
}

func webSearchToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "WebSearch",
		Description: "Search the web for current information. Returns compact result titles, URLs, snippets, and optional answer text. Use WebFetch to read a selected page.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum results to return (default: 5, max: 20)",
				},
				"search_depth": map[string]any{
					"type":        "string",
					"description": "Search depth: basic or advanced (default: basic)",
				},
				"topic": map[string]any{
					"type":        "string",
					"description": "Search topic, such as general or news",
				},
				"time_range": map[string]any{
					"type":        "string",
					"description": "Optional recency filter such as day, week, month, or year",
				},
				"include_domains": map[string]any{
					"type":        "array",
					"description": "Domains to include",
					"items":       map[string]any{"type": "string"},
				},
				"exclude_domains": map[string]any{
					"type":        "array",
					"description": "Domains to exclude",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []string{"query"},
		},
	}
}

func webFetchToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "WebFetch",
		Description: "Fetch and extract readable content from a web URL. Uses Tavily Extract when configured and falls back to direct HTTP for text/HTML pages.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "HTTP or HTTPS URL to fetch",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Optional extraction hint or question",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"description": "Maximum bytes to return (default: 20000, max: 50000)",
				},
				"format": map[string]any{
					"type":        "string",
					"description": "Preferred extracted content format: markdown or text (default: markdown)",
				},
			},
			"required": []string{"url"},
		},
	}
}

func (r *Registry) handleWebSearchToolCall(ctx context.Context, searchTool llm.ToolCall) (llm.Message, error) {
	var args WebSearchToolArguments
	if err := parseToolArgs(searchTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return llm.Message{}, fmt.Errorf("query is required")
	}

	maxResults := clampWebSearchResults(args.MaxResults)
	args.MaxResults = maxResults

	provider := r.webSearchProvider()
	if provider == nil {
		return llm.Message{}, fmt.Errorf("web search provider is not configured")
	}
	resp, err := provider.Search(ctx, args)
	if err != nil {
		return llm.Message{}, err
	}

	output := formatWebSearchOutput(args.Query, maxResults, resp)
	return llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: searchTool.ID}, nil
}

func (r *Registry) handleWebFetchToolCall(ctx context.Context, fetchTool llm.ToolCall) (llm.Message, error) {
	var args WebFetchToolArguments
	if err := parseToolArgs(fetchTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}
	args.URL = strings.TrimSpace(args.URL)
	if args.URL == "" {
		return llm.Message{}, fmt.Errorf("url is required")
	}
	maxBytes := clampWebFetchBytes(args.MaxBytes)
	format := args.Format
	if format == "" {
		format = "markdown"
	}

	var content, source string
	var err error
	if strings.TrimSpace(r.TavilyAPIKey) != "" {
		content, source, err = r.fetchViaTavilyExtract(ctx, args.URL, args.Query, format)
	}
	if err != nil || strings.TrimSpace(content) == "" {
		if ctx.Err() != nil {
			return llm.Message{}, ctx.Err()
		}
	}
	if strings.TrimSpace(content) == "" {
		fallbackContent, fallbackErr := r.fetchDirectHTTP(ctx, args.URL, maxBytes)
		if fallbackErr != nil {
			if err != nil {
				return llm.Message{}, fmt.Errorf("tavily extract failed: %v; http fallback failed: %w", err, fallbackErr)
			}
			return llm.Message{}, fallbackErr
		}
		content = fallbackContent
		source = "direct-http"
	}

	content, truncated := truncateWebContent(content, maxBytes)
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "[WebFetch: %s, source=%s]\n", args.URL, source)
	b.WriteString(content)
	if truncated {
		_, _ = fmt.Fprintf(&b, "\n[Output truncated at %d bytes]", maxBytes)
	}
	return llm.Message{Role: llm.RoleTool, Content: b.String(), ToolCallID: fetchTool.ID}, nil
}

func (r *Registry) webSearchProvider() WebSearchProvider {
	if r == nil {
		return nil
	}
	if strings.TrimSpace(r.BraveSearchAPIKey) != "" {
		return braveSearchProvider{apiKey: r.BraveSearchAPIKey, client: r.httpClient()}
	}
	if strings.TrimSpace(r.TavilyAPIKey) != "" {
		return tavilySearchProvider{apiKey: r.TavilyAPIKey, client: r.httpClient()}
	}
	return nil
}

func clampWebSearchResults(n int) int {
	if n <= 0 {
		return DefaultWebSearchResults
	}
	if n > MaxWebSearchResults {
		return MaxWebSearchResults
	}
	return n
}

func clampWebFetchBytes(n int) int {
	if n <= 0 {
		return DefaultWebFetchBytes
	}
	if n > MaxToolOutputBytes {
		return MaxToolOutputBytes
	}
	return n
}

func formatWebSearchOutput(query string, maxResults int, resp WebSearchResponse) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "[WebSearch: %s, max_results=%d]\n", query, maxResults)
	if strings.TrimSpace(resp.Answer) != "" {
		_, _ = fmt.Fprintf(&b, "Answer: %s\n\n", strings.TrimSpace(resp.Answer))
	}
	if len(resp.Results) == 0 {
		b.WriteString("No results found.")
		return b.String()
	}
	for i, result := range resp.Results {
		_, _ = fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(result.Title))
		_, _ = fmt.Fprintf(&b, "   URL: %s\n", strings.TrimSpace(result.URL))
		if result.Score > 0 {
			_, _ = fmt.Fprintf(&b, "   Score: %.3f\n", result.Score)
		}
		if snippet := strings.TrimSpace(result.Snippet); snippet != "" {
			_, _ = fmt.Fprintf(&b, "   Snippet: %s\n", TruncateLine(snippet, MaxGrepLineChars))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncateWebContent(content string, maxBytes int) (string, bool) {
	if len(content) <= maxBytes {
		return content, false
	}
	cutoff := maxBytes
	for cutoff > 0 && content[cutoff-1] != '\n' && content[cutoff-1] != ' ' {
		cutoff--
	}
	if cutoff == 0 {
		cutoff = maxBytes
	}
	return strings.TrimSpace(content[:cutoff]), true
}

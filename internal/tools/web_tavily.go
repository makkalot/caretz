package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type tavilySearchProvider struct {
	apiKey string
	client httpDoer
}

type tavilySearchResponse struct {
	Query   string `json:"query"`
	Answer  string `json:"answer"`
	Results []struct {
		Title   string  `json:"title"`
		URL     string  `json:"url"`
		Content string  `json:"content"`
		Score   float64 `json:"score"`
	} `json:"results"`
}

type tavilyExtractResponse struct {
	Results []struct {
		URL        string `json:"url"`
		RawContent string `json:"raw_content"`
		Content    string `json:"content"`
	} `json:"results"`
	FailedResults []struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	} `json:"failed_results"`
}

func (p tavilySearchProvider) Search(ctx context.Context, args WebSearchToolArguments) (WebSearchResponse, error) {
	searchDepth := args.SearchDepth
	if searchDepth == "" {
		searchDepth = "basic"
	}

	body := map[string]any{
		"query":        args.Query,
		"max_results":  args.MaxResults,
		"search_depth": searchDepth,
	}
	if args.Topic != "" {
		body["topic"] = args.Topic
	}
	if args.TimeRange != "" {
		body["time_range"] = args.TimeRange
	}
	if len(args.IncludeDomains) > 0 {
		body["include_domains"] = args.IncludeDomains
	}
	if len(args.ExcludeDomains) > 0 {
		body["exclude_domains"] = args.ExcludeDomains
	}

	var resp tavilySearchResponse
	if err := p.post(ctx, tavilySearchURL, body, &resp); err != nil {
		return WebSearchResponse{}, err
	}

	normalized := WebSearchResponse{Answer: resp.Answer}
	for _, result := range resp.Results {
		normalized.Results = append(normalized.Results, WebSearchResult{
			Title:   result.Title,
			URL:     result.URL,
			Snippet: result.Content,
			Score:   result.Score,
		})
	}
	return normalized, nil
}

func (p tavilySearchProvider) post(ctx context.Context, endpoint string, body map[string]any, target any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal tavily request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create tavily request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("tavily request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("tavily request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode tavily response: %w", err)
	}
	return nil
}

func (r *Registry) fetchViaTavilyExtract(ctx context.Context, targetURL, query, format string) (string, string, error) {
	body := map[string]any{
		"urls":           []string{targetURL},
		"extract_depth":  "basic",
		"format":         format,
		"include_images": false,
	}
	if query != "" {
		body["query"] = query
	}

	var resp tavilyExtractResponse
	provider := tavilySearchProvider{apiKey: r.TavilyAPIKey, client: r.httpClient()}
	if err := provider.post(ctx, tavilyExtractURL, body, &resp); err != nil {
		return "", "", err
	}
	if len(resp.Results) > 0 {
		content := resp.Results[0].RawContent
		if content == "" {
			content = resp.Results[0].Content
		}
		return content, "tavily-extract", nil
	}
	if len(resp.FailedResults) > 0 {
		return "", "", fmt.Errorf("extract failed: %s", resp.FailedResults[0].Error)
	}
	return "", "", fmt.Errorf("no extracted content returned")
}

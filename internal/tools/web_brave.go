package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type braveSearchProvider struct {
	apiKey string
	client httpDoer
}

type braveSearchResponse struct {
	Web struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			Description   string   `json:"description"`
			ExtraSnippets []string `json:"extra_snippets"`
		} `json:"results"`
	} `json:"web"`
}

func (p braveSearchProvider) Search(ctx context.Context, args WebSearchToolArguments) (WebSearchResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqURL, err := url.Parse(braveSearchURL)
	if err != nil {
		return WebSearchResponse{}, fmt.Errorf("parse brave search url: %w", err)
	}
	values := reqURL.Query()
	values.Set("q", braveQuery(args.Query, args.IncludeDomains, args.ExcludeDomains))
	values.Set("count", fmt.Sprintf("%d", args.MaxResults))
	if freshness := braveFreshness(args.TimeRange); freshness != "" {
		values.Set("freshness", freshness)
	}
	reqURL.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return WebSearchResponse{}, fmt.Errorf("create brave search request: %w", err)
	}
	req.Header.Set("X-Subscription-Token", p.apiKey)
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.client.Do(req)
	if err != nil {
		return WebSearchResponse{}, fmt.Errorf("brave search request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		return WebSearchResponse{}, fmt.Errorf("brave search request failed with status %d: %s", httpResp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var resp braveSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return WebSearchResponse{}, fmt.Errorf("decode brave search response: %w", err)
	}

	var normalized WebSearchResponse
	for _, result := range resp.Web.Results {
		snippet := strings.TrimSpace(result.Description)
		if len(result.ExtraSnippets) > 0 {
			extras := make([]string, 0, len(result.ExtraSnippets))
			for _, extra := range result.ExtraSnippets {
				if extra = strings.TrimSpace(extra); extra != "" {
					extras = append(extras, extra)
				}
			}
			if len(extras) > 0 {
				if snippet != "" {
					snippet += " "
				}
				snippet += strings.Join(extras, " ")
			}
		}
		normalized.Results = append(normalized.Results, WebSearchResult{
			Title:   result.Title,
			URL:     result.URL,
			Snippet: snippet,
		})
	}
	return normalized, nil
}

func braveQuery(query string, includeDomains, excludeDomains []string) string {
	parts := []string{query}
	for _, domain := range includeDomains {
		domain = strings.TrimSpace(domain)
		if domain != "" {
			parts = append(parts, "site:"+domain)
		}
	}
	for _, domain := range excludeDomains {
		domain = strings.TrimSpace(domain)
		if domain != "" {
			parts = append(parts, "-site:"+domain)
		}
	}
	return strings.Join(parts, " ")
}

func braveFreshness(timeRange string) string {
	switch strings.ToLower(strings.TrimSpace(timeRange)) {
	case "day":
		return "pd"
	case "week":
		return "pw"
	case "month":
		return "pm"
	case "year":
		return "py"
	default:
		return ""
	}
}

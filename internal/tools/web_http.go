package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

func (r *Registry) fetchDirectHTTP(ctx context.Context, rawURL string, maxBytes int) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := parsePublicWebURL(rawURL); err != nil {
		return "", err
	}

	client := r.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("stopped after 5 redirects")
				}
				return validatePublicHost(req.URL.Hostname())
			},
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create http request: %w", err)
	}
	req.Header.Set("User-Agent", "caretz-webfetch/1.0")
	req.Header.Set("Accept", "text/html,text/plain,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http fetch failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http fetch failed with status %d", resp.StatusCode)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !isTextLikeContentType(contentType) {
		return "", fmt.Errorf("unsupported content type: %s", contentType)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return "", fmt.Errorf("read http response: %w", err)
	}
	text := string(data)
	if strings.Contains(contentType, "html") || looksLikeHTML(text) {
		text = htmlToText(text)
	}
	return strings.TrimSpace(text), nil
}

func parsePublicWebURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported url scheme: %s", parsed.Scheme)
	}
	if err := validatePublicHost(parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func validatePublicHost(host string) error {
	if host == "" {
		return fmt.Errorf("url host is required")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve host: %w", err)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return fmt.Errorf("blocked private or local address: %s", ip.String())
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
}

func isTextLikeContentType(contentType string) bool {
	return strings.Contains(contentType, "text/") ||
		strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "application/xml") ||
		strings.Contains(contentType, "application/xhtml+xml") ||
		strings.Contains(contentType, "application/rss+xml")
}

func looksLikeHTML(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") || strings.Contains(lower, "<body")
}

func htmlToText(input string) string {
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return collapseWhitespace(stripHTMLTags(input))
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
			return
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				b.WriteString(text)
				b.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && isBlockHTMLNode(n.Data) {
			b.WriteByte('\n')
		}
	}
	walk(doc)
	return collapseWhitespace(b.String())
}

func isBlockHTMLNode(name string) bool {
	switch name {
	case "p", "div", "section", "article", "header", "footer", "main", "li", "br", "h1", "h2", "h3", "h4", "h5", "h6", "tr":
		return true
	default:
		return false
	}
}

func stripHTMLTags(input string) string {
	re := regexp.MustCompile(`(?s)<[^>]*>`)
	return re.ReplaceAllString(input, " ")
}

func collapseWhitespace(input string) string {
	lines := strings.Split(input, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

package tools

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/makkalot/caretz/lib/llm"
)

const (
	DefaultLocalImagePrompt         = "Describe this image in detail."
	DefaultLocalImageAuthToken      = "sk-no-key"
	DefaultLocalImageMaxTimeSeconds = 120
)

// LocalImageConfig is toolConfig.localImage. With Endpoint set, Model is the
// API model ID sent to that server with a placeholder token. Without it,
// Model is a configured provider/model reference, called with the provider's
// base URL and stored key.
type LocalImageConfig struct {
	Endpoint       string
	Model          string
	MaxTimeSeconds int
}

// LocalImageModel is where a provider/model reference sends images.
type LocalImageModel struct {
	BaseURL string
	APIKey  string
	Model   string
}

// LocalImageModelResolver resolves a provider/model reference for the current
// session, applying its model rules.
type LocalImageModelResolver func(ctx context.Context, ref string) (LocalImageModel, error)

type localImageModelResolverKey struct{}

func WithLocalImageModelResolver(ctx context.Context, resolve LocalImageModelResolver) context.Context {
	return context.WithValue(ctx, localImageModelResolverKey{}, resolve)
}

func localImageModelResolverFromContext(ctx context.Context) LocalImageModelResolver {
	resolve, _ := ctx.Value(localImageModelResolverKey{}).(LocalImageModelResolver)
	return resolve
}

type LocalImageToolArguments struct {
	FilePath string `json:"file_path"`
	Prompt   string `json:"prompt"`
}

type localImageRequest struct {
	Model    string              `json:"model"`
	Messages []localImageMessage `json:"messages"`
}

type localImageMessage struct {
	Role    string                  `json:"role"`
	Content []localImageContentPart `json:"content"`
}

type localImageContentPart struct {
	Type     string              `json:"type"`
	Text     string              `json:"text,omitempty"`
	ImageURL *localImageImageURL `json:"image_url,omitempty"`
}

type localImageImageURL struct {
	URL string `json:"url"`
}

type localImageResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func localImageToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "LocalImage",
		Description: "Analyze a readable local image file with the configured vision model. Returns the vision model's text response.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the local image file to analyze",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Optional analysis prompt. Defaults to a detailed image description.",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

func (r *Registry) localImageEnabled() bool {
	cfg, err := r.localImageConfig()
	if err != nil {
		return false
	}
	return r != nil && strings.TrimSpace(cfg.Model) != ""
}

// localImageTarget returns the chat completions URL, API model ID and bearer
// token for cfg.
func localImageTarget(ctx context.Context, cfg LocalImageConfig) (string, string, string, error) {
	model := strings.TrimSpace(cfg.Model)
	if endpoint := strings.TrimSpace(cfg.Endpoint); endpoint != "" {
		return endpoint, model, DefaultLocalImageAuthToken, nil
	}
	resolve := localImageModelResolverFromContext(ctx)
	if resolve == nil {
		return "", "", "", fmt.Errorf("local image model %q needs a configured provider; no model resolver in this session", model)
	}
	target, err := resolve(ctx, model)
	if err != nil {
		return "", "", "", err
	}
	// An empty base URL is the OpenAI default, as for the session model.
	baseURL := strings.TrimRight(cmp.Or(strings.TrimSpace(target.BaseURL), "https://api.openai.com/v1"), "/")
	return baseURL + "/chat/completions", target.Model, cmp.Or(target.APIKey, DefaultLocalImageAuthToken), nil
}

func (r *Registry) handleLocalImageToolCall(ctx context.Context, imageTool llm.ToolCall) (llm.Message, error) {
	if !r.localImageEnabled() {
		return llm.Message{}, fmt.Errorf("local image tool is not configured")
	}
	cfg, err := r.localImageConfig()
	if err != nil {
		return llm.Message{}, fmt.Errorf("invalid local image config: %w", err)
	}
	var args LocalImageToolArguments
	if err := parseToolArgs(imageTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}
	args.FilePath = strings.TrimSpace(args.FilePath)
	if args.FilePath == "" {
		return llm.Message{}, fmt.Errorf("file_path is required")
	}
	prompt := strings.TrimSpace(args.Prompt)
	if prompt == "" {
		prompt = DefaultLocalImagePrompt
	}

	filePath, cwd, err := r.resolveWorkspacePath(args.FilePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("getting workspace path: %w", err)
	}
	safe, reason := r.isPathReadableContext(ctx, filePath, cwd)
	if !safe {
		return llm.Message{}, r.outsideRootPermission(PermissionPathRead, filePath, reason, false)
	}

	endpoint, model, authToken, err := localImageTarget(ctx, cfg)
	if err != nil {
		return llm.Message{}, err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("reading image file: %w", err)
	}
	mimeType := localImageMIMEType(filePath)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data))
	reqBody := localImageRequest{
		Model: model,
		Messages: []localImageMessage{
			{
				Role: "user",
				Content: []localImageContentPart{
					{Type: "text", Text: prompt},
					{Type: "image_url", ImageURL: &localImageImageURL{URL: dataURL}},
				},
			},
		},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return llm.Message{}, fmt.Errorf("marshaling local image request: %w", err)
	}

	timeout := cfg.MaxTimeSeconds
	if timeout <= 0 {
		timeout = DefaultLocalImageMaxTimeSeconds
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return llm.Message{}, fmt.Errorf("creating local image request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := r.localImageHTTPClient(timeout).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return llm.Message{}, ctx.Err()
		}
		return llm.Message{}, fmt.Errorf("local image request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxToolOutputBytes+1))
	if err != nil {
		return llm.Message{}, fmt.Errorf("reading local image response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return llm.Message{}, fmt.Errorf("local image endpoint returned HTTP %d: %s", resp.StatusCode, truncateToolOutput(string(body)))
	}

	var parsed localImageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return llm.Message{}, fmt.Errorf("parsing local image response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return llm.Message{}, fmt.Errorf("unexpected local image response shape: missing choices")
	}
	content := parsed.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		return llm.Message{}, fmt.Errorf("unexpected local image response shape: missing choices[0].message.content")
	}
	return llm.Message{Role: llm.RoleTool, Content: truncateToolOutput(content), ToolCallID: imageTool.ID}, nil
}

func validateLocalImageConfig(raw map[string]any) error {
	cfg, err := decodeToolConfig[LocalImageConfig](raw)
	if err != nil {
		return fmt.Errorf("tools.localImage: %w", err)
	}
	if cfg.MaxTimeSeconds < 0 {
		return fmt.Errorf("tools.localImage.maxTimeSeconds must be positive")
	}
	if LocalImageConfigHasToken(raw) {
		return fmt.Errorf("tools.localImage.authToken is no longer read; set model to a configured provider/model and omit endpoint so the provider's stored key is used")
	}
	return nil
}

// LocalImageConfigHasToken reports whether a localImage tool config carries a
// real bearer token. Tokens belong in credentials.json; the empty value and
// the old placeholders hold no secret and are allowed.
func LocalImageConfigHasToken(raw map[string]any) bool {
	token, _ := raw["authToken"].(string)
	switch strings.TrimSpace(token) {
	case "", "noset", DefaultLocalImageAuthToken:
		return false
	}
	return true
}

func (r *Registry) localImageHTTPClient(timeoutSeconds int) httpDoer {
	if r != nil && r.HTTPClient != nil {
		return r.HTTPClient
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = DefaultLocalImageMaxTimeSeconds
	}
	return &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second}
}

func localImageMIMEType(filePath string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(filePath), ".")) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

func truncateToolOutput(output string) string {
	if len(output) <= MaxToolOutputBytes {
		return output
	}
	return output[:MaxToolOutputBytes] + fmt.Sprintf("\n[output truncated: %d bytes total]", len(output))
}

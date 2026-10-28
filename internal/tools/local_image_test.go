package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/llm"
)

func TestRegistryToolDefinitions_LocalImageGatedByConfig(t *testing.T) {
	withoutConfig := (&Registry{}).ToolDefinitions()
	if hasToolDefinition(withoutConfig, "LocalImage") {
		t.Fatal("LocalImage should not be registered without config")
	}

	incompleteConfig := (&Registry{ToolConfig: localImageToolConfig(LocalImageConfig{Endpoint: "http://localhost:8001/v1/chat/completions"})}).ToolDefinitions()
	if hasToolDefinition(incompleteConfig, "LocalImage") {
		t.Fatal("LocalImage should not be registered without model")
	}

	withConfig := (&Registry{ToolConfig: localImageToolConfig(LocalImageConfig{
		Endpoint: "http://localhost:8001/v1/chat/completions",
		Model:    "vision-model",
	})}).ToolDefinitions()
	if !hasToolDefinition(withConfig, "LocalImage") {
		t.Fatal("expected LocalImage definition when endpoint and model are configured")
	}

	disabled := (&Registry{
		ToolConfig: localImageToolConfig(LocalImageConfig{
			Endpoint: "http://localhost:8001/v1/chat/completions",
			Model:    "vision-model",
		}),
		DisabledTools: map[string]bool{"LocalImage": true},
	}).ToolDefinitions()
	if hasToolDefinition(disabled, "LocalImage") {
		t.Fatal("LocalImage should respect DisabledTools")
	}
}

func TestValidateConfigRejectsNegativeLocalImageTimeout(t *testing.T) {
	err := (&Registry{ToolConfig: localImageToolConfig(LocalImageConfig{MaxTimeSeconds: -1})}).ValidateConfig()
	if err == nil || !strings.Contains(err.Error(), "tools.localImage.maxTimeSeconds must be positive") {
		t.Fatalf("Registry.ValidateConfig() error = %v, want maxTimeSeconds validation error", err)
	}
}

func TestValidateConfigRejectsLocalImageAuthToken(t *testing.T) {
	cfg := localImageToolConfig(LocalImageConfig{Endpoint: "http://localhost:8001/v1/chat/completions", Model: "vision-model"})
	cfg["localImage"]["authToken"] = "sk-real"
	err := (&Registry{ToolConfig: cfg}).ValidateConfig()
	if err == nil || !strings.Contains(err.Error(), "authToken is no longer read") {
		t.Fatalf("Registry.ValidateConfig() error = %v, want authToken rejection", err)
	}
	for _, placeholder := range []string{"noset", DefaultLocalImageAuthToken} {
		cfg["localImage"]["authToken"] = placeholder
		if err := (&Registry{ToolConfig: cfg}).ValidateConfig(); err != nil {
			t.Fatalf("authToken %q rejected: %v", placeholder, err)
		}
	}
}

func TestHandleToolCall_LocalImageUsesProviderModel(t *testing.T) {
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	imagePath := filepath.Join(tmpDir, "sample.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0644); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	var captured map[string]any
	registry := &Registry{
		ToolConfig:    localImageToolConfig(LocalImageConfig{Model: "vision/qwen-vl"}),
		ReadableRoots: []string{tmpDir},
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "http://127.0.0.1:8002/v1/chat/completions" {
				t.Fatalf("url = %s", req.URL.String())
			}
			if got := req.Header.Get("Authorization"); got != "Bearer vl-key" {
				t.Fatalf("authorization header = %q", got)
			}
			if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}]}`), nil
		}),
	}
	call := makeToolCall("LocalImage", "img-1", map[string]any{"file_path": imagePath})

	if _, err := registry.HandleToolCall(call); err == nil || !strings.Contains(err.Error(), "no model resolver") {
		t.Fatalf("without resolver error = %v", err)
	}

	var resolvedRef string
	ctx := WithLocalImageModelResolver(context.Background(), func(_ context.Context, ref string) (LocalImageModel, error) {
		resolvedRef = ref
		return LocalImageModel{BaseURL: "http://127.0.0.1:8002/v1/", APIKey: "vl-key", Model: "qwen2.5-vl-7b"}, nil
	})
	if _, err := registry.HandleToolCallContext(ctx, call); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolvedRef != "vision/qwen-vl" || captured["model"] != "qwen2.5-vl-7b" {
		t.Fatalf("ref = %q, model = %v", resolvedRef, captured["model"])
	}

	// A model rule refusal from the resolver stops the call before any request.
	blocked := WithLocalImageModelResolver(context.Background(), func(context.Context, string) (LocalImageModel, error) {
		return LocalImageModel{}, errors.New("LocalImage: model \"vision/qwen-vl\" is not allowed")
	})
	if _, err := registry.HandleToolCallContext(blocked, call); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("blocked error = %v", err)
	}
}

func TestHandleToolCall_LocalImageSendsVisionPayload(t *testing.T) {
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	imagePath := filepath.Join(tmpDir, "sample.png")
	imageBytes := []byte("fake-png")
	if err := os.WriteFile(imagePath, imageBytes, 0644); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	var captured map[string]any
	registry := &Registry{
		ToolConfig: localImageToolConfig(LocalImageConfig{
			Endpoint:       "http://localhost:8001/v1/chat/completions",
			Model:          "vision-model",
			MaxTimeSeconds: 7,
		}),
		ReadableRoots: []string{tmpDir},
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", req.Method)
			}
			if req.URL.String() != "http://localhost:8001/v1/chat/completions" {
				t.Fatalf("url = %s", req.URL.String())
			}
			if got := req.Header.Get("Authorization"); got != "Bearer "+DefaultLocalImageAuthToken {
				t.Fatalf("authorization header = %q", got)
			}
			if got := req.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("content-type = %q", got)
			}
			if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return jsonResponse(200, `{"choices":[{"message":{"content":"A small test image."}}]}`), nil
		}),
	}

	msg, err := registry.HandleToolCall(makeToolCall("LocalImage", "img-1", map[string]any{
		"file_path": imagePath,
		"prompt":    "Describe visible UI issues.",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if msg.Role != llm.RoleTool || msg.ToolCallID != "img-1" || msg.Content != "A small test image." {
		t.Fatalf("unexpected tool message: %+v", msg)
	}
	if captured["model"] != "vision-model" {
		t.Fatalf("model = %v", captured["model"])
	}
	messages := captured["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	textPart := content[0].(map[string]any)
	if textPart["type"] != "text" || textPart["text"] != "Describe visible UI issues." {
		t.Fatalf("unexpected text part: %#v", textPart)
	}
	imagePart := content[1].(map[string]any)
	imageURL := imagePart["image_url"].(map[string]any)["url"].(string)
	wantPrefix := "data:image/png;base64,"
	if !strings.HasPrefix(imageURL, wantPrefix) {
		t.Fatalf("image url prefix = %q, want %q", imageURL[:min(len(imageURL), len(wantPrefix))], wantPrefix)
	}
	if got := strings.TrimPrefix(imageURL, wantPrefix); got != base64.StdEncoding.EncodeToString(imageBytes) {
		t.Fatalf("encoded image = %q", got)
	}
}

func TestHandleToolCall_LocalImageDefaultsPromptAndAuth(t *testing.T) {
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	imagePath := filepath.Join(tmpDir, "sample.jpg")
	if err := os.WriteFile(imagePath, []byte("jpeg"), 0644); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	var captured map[string]any
	registry := &Registry{
		ToolConfig: localImageToolConfig(LocalImageConfig{
			Endpoint: "http://localhost:8001/v1/chat/completions",
			Model:    "vision-model",
		}),
		ReadableRoots: []string{tmpDir},
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer "+DefaultLocalImageAuthToken {
				t.Fatalf("authorization header = %q", got)
			}
			if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return jsonResponse(200, `{"choices":[{"message":{"content":"default ok"}}]}`), nil
		}),
	}

	if _, err := registry.HandleToolCall(makeToolCall("LocalImage", "img-1", map[string]any{
		"file_path": imagePath,
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	messages := captured["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	textPart := content[0].(map[string]any)
	if textPart["text"] != DefaultLocalImagePrompt {
		t.Fatalf("default prompt = %v", textPart["text"])
	}
}

func TestHandleToolCall_LocalImageRejectsOutOfRootPath(t *testing.T) {
	registry := &Registry{
		ToolConfig: localImageToolConfig(LocalImageConfig{
			Endpoint: "http://localhost:8001/v1/chat/completions",
			Model:    "vision-model",
		}),
	}

	_, err := registry.HandleToolCall(makeToolCall("LocalImage", "img-1", map[string]any{
		"file_path": "/etc/passwd",
	}))
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected access denied error, got %v", err)
	}
}

func TestHandleToolCall_LocalImageMalformedResponse(t *testing.T) {
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	imagePath := filepath.Join(tmpDir, "sample.webp")
	if err := os.WriteFile(imagePath, []byte("webp"), 0644); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	registry := &Registry{
		ToolConfig: localImageToolConfig(LocalImageConfig{
			Endpoint: "http://localhost:8001/v1/chat/completions",
			Model:    "vision-model",
		}),
		ReadableRoots: []string{tmpDir},
		HTTPClient: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(200, `{"choices":[]}`), nil
		}),
	}

	_, err = registry.HandleToolCall(makeToolCall("LocalImage", "img-1", map[string]any{
		"file_path": imagePath,
	}))
	if err == nil || !strings.Contains(err.Error(), "unexpected local image response shape") {
		t.Fatalf("expected response shape error, got %v", err)
	}
}

func TestHandleToolCall_LocalImageHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry := &Registry{
		ToolConfig: localImageToolConfig(LocalImageConfig{
			Endpoint: "http://localhost:8001/v1/chat/completions",
			Model:    "vision-model",
		}),
	}

	_, err := registry.HandleToolCallContext(ctx, makeToolCall("LocalImage", "img-1", map[string]any{
		"file_path": "unused.png",
	}))
	if err == nil || err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func localImageToolConfig(cfg LocalImageConfig) Config {
	raw := map[string]any{}
	if cfg.Endpoint != "" {
		raw["endpoint"] = cfg.Endpoint
	}
	if cfg.Model != "" {
		raw["model"] = cfg.Model
	}
	if cfg.MaxTimeSeconds != 0 {
		raw["maxTimeSeconds"] = cfg.MaxTimeSeconds
	}
	return Config{"localImage": raw}
}

package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/config"
)

func TestSetupPresets(t *testing.T) {
	tests := []struct {
		name    string
		presets []setupPreset
		want    map[string]string
	}{
		{"local", localSetupPresets, map[string]string{"llama.cpp": "http://127.0.0.1:8080/v1", "ollama": "http://127.0.0.1:11434/v1", "lmstudio": "http://127.0.0.1:1234/v1", "local": "http://127.0.0.1:8080/v1"}},
		{"hosted", hostedSetupPresets, map[string]string{"openai": "https://api.openai.com/v1", "chatgpt": config.ChatGPTBaseURL, "openrouter": "https://openrouter.ai/api/v1", "groq": "https://api.groq.com/openai/v1", "together": "https://api.together.xyz/v1", "mistral": "https://api.mistral.ai/v1", "opencode-go": "https://opencode.ai/zen/go/v1", "custom": "https://api.example.com/v1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, preset := range tt.presets {
				if tt.want[preset.key] != preset.baseURL {
					t.Errorf("preset %q URL = %q", preset.key, preset.baseURL)
				}
				delete(tt.want, preset.key)
			}
			if len(tt.want) != 0 {
				t.Fatalf("missing presets: %v", tt.want)
			}
		})
	}
	if localSetupPresets[0].key != "llama.cpp" {
		t.Fatalf("default local preset = %q, want llama.cpp", localSetupPresets[0].key)
	}
}

func loadSetupConfig(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path, "")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func TestSetupLocalProviderChoices(t *testing.T) {
	for i, wantKey := range []string{"llama.cpp", "ollama", "lmstudio", "local"} {
		t.Run(wantKey, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"data":[{"id":"test-model"}]}`)
			}))
			defer server.Close()

			path := filepath.Join(t.TempDir(), "config.json")
			input := fmt.Sprintf("1\n%d\n\n\n%s\n\n\n\n\n", i+1, server.URL)
			var stdout, stderr bytes.Buffer
			w := &setupWizard{ctx: context.Background(), in: strings.NewReader(input), out: &stdout, errOut: &stderr}
			w.reader = bufio.NewReader(w.in)
			if err := w.run(path); err != nil {
				t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "Connection successful; found 1 model(s).") || !strings.Contains(stdout.String(), "Selected "+wantKey+"/test-model") {
				t.Fatalf("stdout=%q", stdout.String())
			}
		})
	}
}

func TestSetupManualLocalFallbackWritesConfigWithoutLeakingToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	in := strings.NewReader("1\n\n\n\nhttp://127.0.0.1:1/v1\nsecret-token\n3\nmanual/model\ny\n\n\n")
	var stdout, stderr bytes.Buffer
	code := runSetupCommand(context.Background(), nil, in, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "secret-token") {
		t.Fatalf("token leaked: %q", combined)
	}
	if !strings.Contains(stdout.String(), "Selected llama.cpp/manual/model") || !strings.Contains(stdout.String(), "Run caretz to get started.") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "could not connect") || !strings.Contains(stderr.String(), "saving an unverified provider configuration") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "caretz", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"context": 32768`) || !strings.Contains(string(data), `"manual/model"`) {
		t.Fatalf("config = %s", data)
	}
}

func TestSetupDiscoversSortedModelsAndUsesAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer hosted-secret" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"z-model"},{"id":"a-model"}]}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	input := fmt.Sprintf("1\n\n\n\n%s\nhosted-secret\n1\n\n\n", server.URL)
	var stdout, stderr bytes.Buffer
	w := &setupWizard{ctx: context.Background(), in: strings.NewReader(input), out: &stdout, errOut: &stderr}
	w.reader = bufio.NewReader(w.in)
	if err := w.run(path); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if strings.Index(stdout.String(), "a-model") > strings.Index(stdout.String(), "z-model") {
		t.Fatalf("models were not sorted: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Selected llama.cpp/a-model") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	cache, err := config.LoadModelCache(config.ModelCachePath(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := cache.Provider["llama.cpp"].Models; strings.Join(got, ",") != "a-model,z-model" {
		t.Fatalf("cached models = %q, want the list setup verified", got)
	}
	if strings.Contains(stdout.String()+stderr.String(), "hosted-secret") {
		t.Fatal("token leaked to output")
	}
}

func TestSetupConnectionFailureMessages(t *testing.T) {
	tests := []struct {
		name, body, want string
		status           int
	}{
		{"unauthorized", `{"error":{"message":"bad key"}}`, "authentication failed (HTTP 401)", http.StatusUnauthorized},
		{"forbidden", `{"error":{"message":"forbidden"}}`, "authentication failed (HTTP 403)", http.StatusForbidden},
		{"not found", `not found`, "OpenAI-compatible /models endpoint was not found", http.StatusNotFound},
		{"server error", `{"error":{"message":"model service unavailable"}}`, "model service unavailable", http.StatusServiceUnavailable},
		{"malformed", `not-json`, "did not return valid OpenAI-compatible JSON", http.StatusOK},
		{"empty", `{"data":[]}`, "no models were advertised", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			candidate := config.SetupProvider{Name: "Ollama", BaseURL: server.URL, APIKey: "secret-token"}
			w := &setupWizard{ctx: context.Background()}
			_, err := w.discover(candidate)
			if got := setupConnectionFailure(candidate, "ollama", err); !strings.Contains(got, tt.want) {
				t.Fatalf("message = %q, want %q", got, tt.want)
			} else if strings.Contains(got, "secret-token") {
				t.Fatalf("message leaked token: %q", got)
			}
		})
	}

	candidate := config.SetupProvider{Name: "Local", BaseURL: "http://127.0.0.1:8080/v1"}
	if got := setupConnectionFailure(candidate, "local", context.DeadlineExceeded); !strings.Contains(got, "timed out after 5 seconds") {
		t.Fatalf("timeout message = %q", got)
	}
	invalid := config.SetupProvider{Name: "Custom", BaseURL: "localhost:1234/v1"}
	_, err := (&setupWizard{ctx: context.Background()}).discover(invalid)
	if got := setupConnectionFailure(invalid, "local", err); !strings.Contains(got, "Base URL is invalid") {
		t.Fatalf("invalid URL message = %q", got)
	}
}

func TestSetupRetriesConnectionWithoutRepromptingDetails(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, "still starting", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"ready-model"}]}`)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	input := fmt.Sprintf("1\n\n\n\n%s\n\n\n\n\n\n", server.URL)
	var stdout, stderr bytes.Buffer
	w := &setupWizard{ctx: context.Background(), in: strings.NewReader(input), out: &stdout, errOut: &stderr}
	w.reader = bufio.NewReader(w.in)
	if err := w.run(path); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if requests != 2 || !strings.Contains(stdout.String(), "Selected llama.cpp/ready-model") {
		t.Fatalf("requests=%d stdout=%q", requests, stdout.String())
	}
}

func TestSetupDoesNotSaveUnverifiedProviderWithoutConfirmation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	in := strings.NewReader("1\n\n\n\nhttp://127.0.0.1:1/v1\n\n3\nmanual/model\nn\n")
	var stdout, stderr bytes.Buffer
	if code := runSetupCommand(context.Background(), nil, in, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "caretz", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config should not exist; stat error=%v", err)
	}
}

func TestSetupDispatchPrecedesRuntimeConstruction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunWithOptions(context.Background(), []string{"setup", "--help"}, &stdout, &stderr, Options{}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage: caretz setup") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestSetupProviderClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
	}))
	defer server.Close()
	for _, tt := range []struct {
		name, mode, endpoint, answer  string
		edit, wantLocal, cancel, warn bool
	}{
		{name: "private default", mode: "1", wantLocal: true},
		{name: "private hosted", mode: "1", answer: "2\n"},
		{name: "hosted flow private proxy", mode: "2"},
		{name: "tailscale default", mode: "1", endpoint: "http://100.100.1.2/v1"},
		{name: "public local", mode: "1", endpoint: "http://gpu.example/v1", answer: "1\n", wantLocal: true, warn: true},
		{name: "edited public default", mode: "1", endpoint: "http://gpu.example/v1", edit: true},
		{name: "edited private default", mode: "1", edit: true, wantLocal: true},
		{name: "escape", mode: "1", answer: "\x1b\n", cancel: true},
		{name: "ctrl-c", mode: "1", answer: "\x03\n", cancel: true},
		{name: "eof", mode: "1", cancel: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := tt.endpoint
			if endpoint == "" {
				endpoint = server.URL
			}
			input := tt.mode + "\n\n\n\n"
			if tt.edit {
				// Invalid URL fails immediately, allowing details to be edited.
				input += "invalid\n\n2\n\n\n"
			}
			input += endpoint + "\n\n"
			if endpoint == server.URL {
				input += "\n"
			} else {
				// Cancelled context avoids contacting a real remote server.
				input += "3\nm\ny\n"
			}
			if tt.mode == "1" {
				input += "\n"
			}
			if tt.answer != "" {
				input += tt.answer
			} else if !tt.cancel && tt.mode == "1" {
				input += "\n"
			}
			ctx := context.Background()
			if endpoint != server.URL {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			path := filepath.Join(t.TempDir(), "config.json")
			var stdout, stderr bytes.Buffer
			w := &setupWizard{ctx: ctx, in: strings.NewReader(input), out: &stdout, errOut: &stderr}
			w.reader = bufio.NewReader(w.in)
			err := w.run(path)
			if tt.cancel {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("cancel err=%v output=%s", err, stdout.String())
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("cancel saved config: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			cfg := loadSetupConfig(t, path)
			for _, p := range cfg.Provider {
				if p.Local == nil || *p.Local != tt.wantLocal {
					t.Fatalf("classification=%v want=%v", p.Local, tt.wantLocal)
				}
			}
			if strings.Contains(stderr.String(), "travels unencrypted") != tt.warn {
				t.Fatalf("warning: %s", stderr.String())
			}
			if got := strings.Count(stdout.String(), "Model location:"); (got == 1) != (tt.mode == "1") {
				t.Fatalf("classification prompts=%d", got)
			}
			if tt.mode == "1" {
				wantDefault := "Choose [2]:"
				if config.IsLocalEndpoint(endpoint) {
					wantDefault = "Choose [1]:"
				}
				location := strings.Split(stdout.String(), "Model location:")[1]
				if !strings.Contains(location, wantDefault) {
					t.Fatalf("wrong final default: %s", location)
				}
			}
		})
	}
}

func runSetupWizard(t *testing.T, path, input string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	w := &setupWizard{ctx: context.Background(), in: strings.NewReader(input), out: &stdout, errOut: &stderr}
	w.reader = bufio.NewReader(w.in)
	if err := w.run(path); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func TestSetupConfiguresWebSearchAndImageAnalysis(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"coder"},{"id":"vision"}]}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	// Provider steps, then: both search services with their keys, and another
	// model (the second listed) for image analysis.
	provider := fmt.Sprintf("1\n\n\n\n%s\n\n1\n\n\n", server.URL)
	stdout, _ := runSetupWizard(t, path, provider+"4\ntavily-secret\nbrave-secret\n3\n2\n")
	if strings.Contains(stdout, "tavily-secret") || strings.Contains(stdout, "brave-secret") {
		t.Fatalf("search key leaked: %q", stdout)
	}
	if !strings.Contains(stdout, "Image analysis uses llama.cpp/vision") || !strings.Contains(stdout, "Run caretz to get started.") {
		t.Fatalf("stdout=%q", stdout)
	}
	creds, err := config.LoadCredentials(config.CredentialsPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if creds.Search["tavily"] != "tavily-secret" || creds.Search["brave"] != "brave-secret" {
		t.Fatalf("search credentials = %v", creds.Search)
	}
	cfg := loadSetupConfig(t, path)
	if err := validateLocalImageModel(cfg, cfg.Profiles["general"].ToolConfig["localImage"]); err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveModel != "llama.cpp/coder" {
		t.Fatalf("active model = %q", cfg.ActiveModel)
	}

	// Re-running setup replaces the provider's catalog; keeping the image
	// model must re-add it so the config still loads.
	stdout, _ = runSetupWizard(t, path, provider+"y\n\n\n")
	if !strings.Contains(stdout, "Keep the current image model (llama.cpp/vision)") || !strings.Contains(stdout, "Tavily key already stored") {
		t.Fatalf("stdout=%q", stdout)
	}
	cfg = loadSetupConfig(t, path)
	if err := validateLocalImageModel(cfg, cfg.Profiles["general"].ToolConfig["localImage"]); err != nil {
		t.Fatal(err)
	}
}

func TestSetupSignsInWithChatGPT(t *testing.T) {
	t.Setenv(config.CredentialsFileEnv, "")
	path := filepath.Join(t.TempDir(), "config.json")
	attempts := 0
	var stdout, stderr bytes.Buffer
	// Hosted, ChatGPT plan, default provider key, try again after the failed
	// sign-in, the second model, then skip web search.
	w := &setupWizard{ctx: context.Background(), in: strings.NewReader("2\n2\n\n1\n2\n\n"), out: &stdout, errOut: &stderr,
		signIn: func(_ context.Context, homePath, providerKey string, _, _ io.Writer) ([]string, bool, error) {
			attempts++
			if homePath != path || providerKey != "chatgpt" {
				t.Errorf("signIn(%q, %q)", homePath, providerKey)
			}
			if attempts == 1 {
				return nil, false, errors.New("browser closed")
			}
			return []string{"gpt-6.1-sol", "gpt-6-luna"}, false, nil
		}}
	w.reader = bufio.NewReader(w.in)
	if err := w.run(path); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if attempts != 2 || !strings.Contains(stderr.String(), "Sign-in failed: browser closed") {
		t.Fatalf("attempts=%d stderr=%s", attempts, stderr.String())
	}
	if strings.Contains(stdout.String(), "Base URL") || strings.Contains(stdout.String(), "API token") || strings.Contains(stdout.String(), "Image analysis") {
		t.Fatalf("sign-in asked for API key details or image analysis:\n%s", stdout.String())
	}
	cfg := loadSetupConfig(t, path)
	provider := cfg.Provider["chatgpt"]
	if provider.Auth != config.AuthChatGPT || provider.Models["gpt-6-luna"].Protocol != "responses" {
		t.Fatalf("provider = %+v", provider)
	}
	if cfg.ActiveModel != "chatgpt/gpt-6-luna" {
		t.Fatalf("active model = %q", cfg.ActiveModel)
	}
}

func TestSetupCancelDuringOptionalStepsKeepsProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	stdout, _ := runSetupWizard(t, path, fmt.Sprintf("1\n\n\n\n%s\n\n1\n\n\n\x03\n", server.URL))
	if !strings.Contains(stdout, "provider is saved") {
		t.Fatalf("stdout=%q", stdout)
	}
	if cfg := loadSetupConfig(t, path); cfg.ActiveModel != "llama.cpp/m" {
		t.Fatalf("active model = %q", cfg.ActiveModel)
	}
}

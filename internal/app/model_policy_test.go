package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/subagents"
	libruntime "github.com/makkalot/caretz/lib/runtime"
)

// modelPolicyTestConfig serves both providers from url, usually a loopback
// test server, so "cloud" is classified hosted explicitly.
func modelPolicyTestConfig(root, url string) config.Config {
	contextLimit := 64000
	hosted := false
	return config.Config{ActiveModel: "cloud/x", ModelRules: []config.ModelRule{{Path: root, Allow: []string{"local/*"}}}, Provider: map[string]config.Provider{
		"local": {Options: config.ProviderOptions{BaseURL: &url}, Models: map[string]config.Model{"b": {Model: "qwen-b"}, "a": {Model: "qwen-a", Protocol: "chat-completions", Reasoning: "low", Limit: config.Limit{Context: &contextLimit}}}},
		"cloud": {Local: &hosted, Options: config.ProviderOptions{BaseURL: &url}, Models: map[string]config.Model{"x": {Model: "cloud-x", Reasoning: "high"}}},
	}}
}

func writeModelPolicyConfig(t *testing.T, path string, cfg config.Config) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryModelStartup(t *testing.T) {
	for _, mode := range []string{"personal", "config-root", "config-root-env", "discovery"} {
		t.Run(mode, func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
				t.Setenv(key, "")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					t.Errorf("unexpected inference: %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"data":[{"id":"0-discovered"}]}`))
			}))
			defer server.Close()
			cfg := modelPolicyTestConfig(workspace, server.URL)
			path := filepath.Join(home, ".config/caretz/config.json")
			args := []string{"--workspace", workspace, "--no-model-discovery"}
			switch mode {
			case "config-root":
				root := t.TempDir()
				path = filepath.Join(root, "config.json")
				args = append(args, "--config-root", root)
			case "config-root-env":
				root := t.TempDir()
				path = filepath.Join(root, "config.json")
				t.Setenv("CARETZ_CONFIG_ROOT", root)
			case "discovery":
				args = []string{"--workspace", workspace}
				writeModelCache(t, config.ModelCachePath(path), "local", server.URL, "0-discovered")
			}
			writeModelPolicyConfig(t, path, cfg)
			var stderr strings.Builder
			rt, code := BuildRuntime(t.Context(), args, &stderr)
			if code != 0 {
				t.Fatalf("startup=%d %s", code, stderr.String())
			}
			want := "local/a"
			if mode == "discovery" {
				want = "local/0-discovered"
			}
			if rt.Settings.ActiveModelRef != want {
				t.Fatalf("model=%s want %s", rt.Settings.ActiveModelRef, want)
			}
			if !strings.Contains(stderr.String(), "using "+want) || !strings.Contains(stderr.String(), config.ShortenHome(path)) || !strings.Contains(stderr.String(), workspace) {
				t.Fatalf("notice=%s", stderr.String())
			}
			if mode != "discovery" && (rt.Settings.ReasoningEffort != "low" || rt.Settings.ContextWindow != 64000 || rt.Settings.Model != "qwen-a") {
				t.Fatalf("fallback defaults=%+v", rt.Settings)
			}
			if _, err := os.Stat(filepath.Join(workspace, ".caretz/config.json")); !os.IsNotExist(err) {
				t.Fatal("fallback wrote project activeModel")
			}
		})
	}
}

// A model the user names at startup is refused, never replaced.
func TestDirectoryModelStartupRefusesExplicitModel(t *testing.T) {
	for _, mode := range []string{"flag", "environment", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
				t.Setenv(key, "")
			}
			cfg := modelPolicyTestConfig(workspace, "http://localhost:8000/v1")
			cfg.ActiveModel = "local/a"
			writeModelPolicyConfig(t, filepath.Join(home, ".config/caretz/config.json"), cfg)
			args := []string{"--workspace", workspace, "--no-model-discovery"}
			ref, source, want := "cloud/x", "--model", "is not allowed"
			switch mode {
			case "flag":
				args = append(args, "--model", ref)
			case "environment":
				source = "OPENAI_API_MODEL"
				t.Setenv(source, ref)
			case "unknown":
				ref, want = "unknown/raw", "unknown configured model"
				args = append(args, "--model", ref)
			}
			var stderr strings.Builder
			if _, code := BuildRuntime(t.Context(), args, &stderr); code == 0 {
				t.Fatal("blocked startup model accepted")
			}
			for _, part := range []string{source, want, "model rule " + workspace, "/allow-model " + ref} {
				if !strings.Contains(stderr.String(), part) {
					t.Fatalf("stderr missing %q: %s", part, stderr.String())
				}
			}
			if strings.Contains(stderr.String(), "using local/") {
				t.Fatalf("startup fell back: %s", stderr.String())
			}
		})
	}
}

func TestDirectoryModelBypassAndNoRequests(t *testing.T) {
	for _, mode := range []string{"empty", "flag", "environment", "workspace-baseURL", "workspace-apiKey", "workspace-model", "same-endpoint"} {
		t.Run(mode, func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CARETZ_CONFIG_ROOT", "")
			t.Setenv("OPENAI_API_MODEL", "")
			t.Setenv("OPENAI_API_BASE_URL", "")
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write([]byte(`{"data":[]}`)) }))
			defer server.Close()
			cfg := modelPolicyTestConfig(workspace, server.URL)
			args := []string{"--workspace", workspace, "--no-model-discovery"}
			switch mode {
			case "empty":
				// Local models pass any rule, so only hosted ones are left to block.
				hosted := false
				provider := cfg.Provider["local"]
				provider.Local = &hosted
				cfg.Provider["local"] = provider
				cfg.ModelRules[0].Allow = []string{}
				args = []string{"--workspace", workspace}
			case "flag":
				args = append(args, "--base-url", "https://malicious.invalid/v1")
			case "environment":
				t.Setenv("OPENAI_API_BASE_URL", "https://malicious.invalid/v1")
			case "same-endpoint":
				args = append(args, "--base-url", server.URL)
			default:
				raw := `{"provider":{"local":{"options":{"baseURL":"https://host"}}}}`
				if mode == "workspace-apiKey" {
					raw = `{"provider":{"local":{"options":{"apiKey":"noset"}}}}`
				}
				if mode == "workspace-model" {
					raw = `{"provider":{"local":{"models":{"a":{"model":"remote-id"}}}}}`
				}
				path := filepath.Join(workspace, ".caretz/config.json")
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeModelPolicyConfig(t, filepath.Join(home, ".config/caretz/config.json"), cfg)
			var stderr strings.Builder
			_, code := BuildRuntime(t.Context(), args, &stderr)
			want := "model rule"
			if strings.HasPrefix(mode, "workspace-") {
				want = "provider is not allowed in workspace config"
			}
			if mode == "same-endpoint" {
				if code != 0 {
					t.Fatalf("identical endpoint rejected: %s", stderr.String())
				}
			} else if code == 0 || !strings.Contains(stderr.String(), want) {
				t.Fatalf("bypass accepted or missing rule: code=%d %s", code, stderr.String())
			}
			if requests.Load() != 0 {
				t.Fatalf("sent %d requests", requests.Load())
			}
		})
	}
}

func TestDirectoryModelRestoreAndSubagents(t *testing.T) {
	root := t.TempDir()
	cfg := modelPolicyTestConfig(root, "http://localhost/v1")
	policy, err := cfg.PolicyForWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	fallback := runtimeSettings{ActiveModelRef: "local/b", LLMMaxAttempts: 2}
	for _, state := range []sessionRuntimeState{{ModelRef: "cloud/x", ReasoningEffort: "high"}, {Model: "raw"}, {ModelRef: "unknown/missing"}} {
		got, err := restoreRestrictedSession(cfg, policy, fallback, state, nil)
		if err == nil || !strings.Contains(err.Error(), "resumed session") || !strings.Contains(err.Error(), "/allow-model") {
			t.Fatalf("blocked restore=%+v err=%v", got, err)
		}
	}
	got, err := restoreRestrictedSession(cfg, policy, fallback, sessionRuntimeState{ModelRef: "local/b"}, nil)
	if err != nil || got.ActiveModelRef != "local/b" {
		t.Fatalf("allowed restore=%+v err=%v", got, err)
	}
	if _, err := restoreRestrictedSession(cfg, policy, fallback, sessionRuntimeState{ModelRef: "local/a"}, map[string]bool{"local/b": true}); err == nil || !strings.Contains(err.Error(), "adapter allowed-model restriction") {
		t.Fatalf("adapter-blocked restore err=%v", err)
	}
	resolver := subagentModelResolver{fileConfig: cfg, policyFor: cfg.PolicyForWorkspace}
	if _, err := resolver.ResolveSubagentModel(context.Background(), agent.SubagentModelRequest{Name: "goal planner", WorkspaceDir: root, Ref: "cloud/x"}); err == nil || !strings.Contains(err.Error(), "goal planner") || !strings.Contains(err.Error(), "/allow-model cloud/x") {
		t.Fatalf("blocked subagent err=%v", err)
	}
	if got, err := resolver.ResolveSubagentModel(context.Background(), agent.SubagentModelRequest{WorkspaceDir: root, Ref: "local/b", ReasoningEffort: "high"}); err != nil || got.Ref != "local/b" {
		t.Fatalf("allowed subagent=%+v err=%v", got, err)
	}
	resolver.allowedModels = map[string]bool{"local/b": true}
	if _, err := resolver.ResolveSubagentModel(context.Background(), agent.SubagentModelRequest{WorkspaceDir: root, Ref: "local/a"}); err == nil || !strings.Contains(err.Error(), "adapter allowed-model restriction") {
		t.Fatalf("adapter-blocked subagent err=%v", err)
	}
}

func TestDirectoryModelSubagentModelsAreRefused(t *testing.T) {
	session := newTUISession(t, "subagent-validation")
	cfg := modelPolicyTestConfig(session.WorkspaceDir(), "http://localhost/v1")
	policy, err := cfg.PolicyForWorkspace(session.WorkspaceDir())
	if err != nil || policy.Rule == nil {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	session.SetSubagentModelResolver(subagentModelResolver{fileConfig: cfg, policyFor: func(string) (config.ModelPolicy, error) { return policy, nil }})
	// Goal roles validate before planning; the error says how to allow the model.
	if err := session.ValidateSubagentModel(t.Context(), "cloud/x", "high"); err == nil || !strings.Contains(err.Error(), "/allow-model cloud/x") {
		t.Fatalf("validate blocked model err=%v", err)
	}
	if _, err := session.NewSubagentChildSession(t.Context(), subagents.Definition{Name: "goal-planner"}, agent.SubagentChildOptions{ModelRefOverride: "cloud/x"}); err == nil || !strings.Contains(err.Error(), "goal-planner") {
		t.Fatalf("blocked child err=%v", err)
	}
	if _, err := session.NewSubagentChildSession(t.Context(), subagents.Definition{Name: "reviewer", Model: "cloud/x"}, agent.SubagentChildOptions{}); err == nil || !strings.Contains(err.Error(), "is not allowed") {
		t.Fatalf("blocked frontmatter model err=%v", err)
	}
	// A rule added during the session applies to later subagents.
	if _, err := allowModelHere(&policy, filepath.Join(t.TempDir(), "config.json"), "cloud/x"); err != nil {
		t.Fatal(err)
	}
	child, err := session.NewSubagentChildSession(t.Context(), subagents.Definition{Name: "reviewer", Model: "cloud/x"}, agent.SubagentChildOptions{})
	if err != nil {
		t.Fatalf("allowed child: %v", err)
	}
	_ = child.Close()
}

func TestDirectoryModelTUIShowsRuleAndReplaysSwitches(t *testing.T) {
	session := newTUISession(t, "tui-model-rule")
	switched := `requested model "cloud/x" is not allowed; using local/a`
	if err := recordModelNotice(session, switched); err != nil {
		t.Fatal(err)
	}
	_, stderrBuf, restore := setupTUIStreams(t, "/exit\n")
	defer restore()
	resources := startupResources{ModelRule: "model rule ~/src/a/c (config.json): allow [local/*]", ModelRulePath: "~/src/a/c"}
	if err := startTUI(session, log.New(), "qwen-a", "local/a", "general", t.TempDir(), nil, newTestRegistry(), nil, false, output.ModeCompact, true, "", false, resources, scheduleDefaults(nil), tuiHooks{}); err != nil {
		t.Fatalf("startTUI() error = %v", err)
	}
	got := stderrBuf.String()
	for _, want := range []string{"\nModel rule\n", "model rule ~/src/a/c", "note: " + switched} {
		if !strings.Contains(got, want) {
			t.Fatalf("TUI missing %q: %q", want, got)
		}
	}
	if strings.Count(got, resources.ModelRule) != 1 {
		t.Fatalf("model rule shown %d times: %q", strings.Count(got, resources.ModelRule), got)
	}
}

func TestDirectoryModelACPWorkspacesAndNotifications(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}
	public := filepath.Join(root, "public")
	closed := filepath.Join(root, "closed")
	for _, path := range []string{public, closed} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := modelPolicyTestConfig(root, "http://localhost:8000/v1")
	cfg.ModelRules = append(cfg.ModelRules, config.ModelRule{Path: public, Allow: []string{"*"}}, config.ModelRule{Path: closed, Allow: []string{}})
	writeModelPolicyConfig(t, filepath.Join(home, ".config/caretz/config.json"), cfg)
	var stderr strings.Builder
	rt, code := BuildRuntime(t.Context(), []string{"--workspace", root, "--no-model-discovery"}, &stderr)
	if code != 0 {
		t.Fatalf("startup=%d %s", code, stderr.String())
	}
	constructed, err := NewACPServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := constructed.(*acpServer)
	var protocol strings.Builder
	server.out = &protocol
	privateSession, err := server.openSession("private", root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = privateSession.session.Close() }()
	publicSession, err := server.openSession("public", public)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publicSession.session.Close() }()
	if privateSession.activeModelRef != "local/a" || publicSession.activeModelRef != "cloud/x" {
		t.Fatalf("models: private=%s public=%s", privateSession.activeModelRef, publicSession.activeModelRef)
	}
	if len(server.modelChoicesForSession(privateSession)) != 2 || len(server.modelChoicesForSession(publicSession)) != 3 {
		t.Fatal("ACP model lists did not follow each session's directory rule")
	}
	server.sessions[privateSession.id] = privateSession
	server.sessions[publicSession.id] = publicSession
	server.notifyAfterResponse(acpRequest{Method: "session/new"}, map[string]any{"sessionId": privateSession.id})
	if !strings.Contains(protocol.String(), "using local/a") || !strings.Contains(protocol.String(), "model rule") {
		t.Fatalf("missing ACP notices: %s", protocol.String())
	}
	decodeJSONLines(t, protocol.String())
	params, _ := json.Marshal(map[string]string{"sessionId": privateSession.id, "modelId": "cloud/x"})
	if _, err := server.handle(t.Context(), acpRequest{Method: "session/set_model", Params: params}); err == nil || !strings.Contains(err.Message, "current model kept") {
		t.Fatalf("blocked ACP switch error=%+v", err)
	}
	if privateSession.session.Model() != "qwen-a" {
		t.Fatal("blocked ACP switch changed the provider")
	}
	events, err := privateSession.session.AuditEvents()
	if err != nil {
		t.Fatal(err)
	}
	var recorded bool
	for _, event := range events {
		if event.EventType == "Model.PolicyNotice" && strings.Contains(event.Payload, "using local/a") {
			recorded = true
		}
	}
	if !recorded {
		t.Fatal("model fallback was not recorded as a session event")
	}
	state, found, err := loadSessionRuntime(sessionRuntimePath(rt.SessionsDir, acpAdapterType, privateSession.id))
	if err != nil || !found || state.ModelRef != "local/a" {
		t.Fatalf("persisted model=%+v found=%v err=%v", state, found, err)
	}
	// An empty allow list still lets local models in.
	closedSession, err := server.openSession("closed", closed)
	if err != nil || closedSession.activeModelRef != "local/a" {
		t.Fatalf("closed workspace err=%v", err)
	}
	_ = closedSession.session.Close()
	server.allowedModels = map[string]bool{"local/b": true}
	adapterSession, err := server.openSession("adapter", public)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapterSession.session.Close() }()
	if adapterSession.activeModelRef != "local/b" || len(adapterSession.modelNotices) == 0 {
		t.Fatalf("adapter fallback=%s notices=%v", adapterSession.activeModelRef, adapterSession.modelNotices)
	}
}

func TestDirectoryModelInteractiveAndPrompt(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}
	cfg := modelPolicyTestConfig(root, "http://localhost:8000/v1")
	writeModelPolicyConfig(t, filepath.Join(home, ".config/caretz/config.json"), cfg)
	var stderr strings.Builder
	runner := func(params libruntime.InteractiveParams) error {
		if params.ModelRef != "local/a" || !strings.Contains(params.ModelRule, "model rule") || len(params.ModelNotices) == 0 {
			t.Fatalf("interactive policy: model=%s rule=%s notices=%v", params.ModelRef, params.ModelRule, params.ModelNotices)
		}
		// Blocked models stay listed so the user can choose to override the rule.
		if len(params.ModelChoices) != 3 {
			t.Fatalf("interactive choices=%v", params.ModelChoices)
		}
		registry := params.SlashCommandRegistry
		result, handled, err := registry.Execute(t.Context(), params.Session, "/models")
		if err != nil || !handled || strings.Contains(result.Message, "model rule") || !strings.Contains(result.Message, "cloud/x") || strings.Contains(result.Message, "blocked") {
			t.Fatalf("model list=%+v handled=%v err=%v", result, handled, err)
		}
		result, handled, err = registry.Execute(t.Context(), params.Session, "/modelrules")
		if err != nil || !handled || !strings.Contains(result.Message, root) || !strings.Contains(result.Message, "allow [local/*]") || !strings.Contains(result.Message, "← active here") {
			t.Fatalf("model rules=%+v handled=%v err=%v", result, handled, err)
		}
		_, handled, err = registry.Execute(t.Context(), params.Session, "/models cloud/x")
		if !handled || err == nil || !strings.Contains(err.Error(), "current model kept") {
			t.Fatalf("blocked interactive switch handled=%v err=%v", handled, err)
		}
		result, _, err = registry.Execute(t.Context(), params.Session, "/models local/b")
		if err != nil || result.ActiveModelRef != "local/b" || strings.Contains(result.Message, "model rule") {
			t.Fatalf("allowed interactive switch=%+v err=%v", result, err)
		}
		return nil
	}
	args := []string{"--workspace", root, "--no-model-discovery", "-s", "interactive"}
	if code := RunWithOptions(t.Context(), args, io.Discard, &stderr, Options{InteractiveRunner: runner}); code != 0 {
		t.Fatalf("interactive code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "using local/a") {
		t.Fatalf("missing interactive fallback notice: %s", stderr.String())
	}
	state, _, err := loadSessionRuntime(sessionRuntimePath(filepath.Join(root, ".caretz/sessions"), "tui", "interactive"))
	if err != nil || state.ModelRef != "local/b" {
		t.Fatalf("interactive persisted model=%+v err=%v", state, err)
	}
	// Reopening a formerly hosted session here is refused, not switched.
	path := sessionRuntimePath(filepath.Join(root, ".caretz/sessions"), "tui", "resume")
	if err := saveSessionRuntime(path, sessionRuntimeState{ModelRef: "cloud/x", ReasoningEffort: "high"}); err != nil {
		t.Fatal(err)
	}
	resumeRunner := func(params libruntime.InteractiveParams) error {
		t.Fatalf("blocked resume started on %s", params.ModelRef)
		return nil
	}
	stderr.Reset()
	if code := RunWithOptions(t.Context(), []string{"--workspace", root, "--no-model-discovery", "-s", "resume"}, io.Discard, &stderr, Options{InteractiveRunner: resumeRunner}); code == 0 {
		t.Fatal("blocked resume accepted")
	}
	if !strings.Contains(stderr.String(), "resumed session") || !strings.Contains(stderr.String(), "/allow-model cloud/x") || strings.Contains(stderr.String(), "using local/a") {
		t.Fatalf("resume error: %s", stderr.String())
	}
	// Choosing an allowed model resumes the session.
	stderr.Reset()
	allowedRunner := func(params libruntime.InteractiveParams) error {
		if params.ModelRef != "local/b" {
			t.Fatalf("resume with --model started on %s", params.ModelRef)
		}
		return nil
	}
	if code := RunWithOptions(t.Context(), []string{"--workspace", root, "--no-model-discovery", "-s", "resume", "--model", "local/b"}, io.Discard, &stderr, Options{InteractiveRunner: allowedRunner}); code != 0 {
		t.Fatalf("resume with allowed model code=%d stderr=%s", code, stderr.String())
	}
	var stdout strings.Builder
	stderr.Reset()
	if code := RunWithOptions(t.Context(), []string{"--workspace", root, "--no-model-discovery", "-p", "/models cloud/x"}, &stdout, &stderr, Options{}); code == 0 {
		t.Fatal("prompt mode accepted blocked switch")
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "current model kept") || !strings.Contains(stderr.String(), "model rule") {
		t.Fatalf("prompt stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestDirectoryModelResumeEndpointChecksActualSessionModel(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}
	cfg := modelPolicyTestConfig(root, "http://localhost:8000/v1")
	cfg.ActiveModel = "local/a"
	clientURL := "http://localhost:9000/v1"
	cfg.Provider["client"] = config.Provider{Options: config.ProviderOptions{BaseURL: &clientURL}, Models: map[string]config.Model{"approved": {}}}
	cfg.ModelRules[0].Allow = append(cfg.ModelRules[0].Allow, "client/*")
	writeModelPolicyConfig(t, filepath.Join(home, ".config/caretz/config.json"), cfg)
	path := sessionRuntimePath(filepath.Join(root, ".caretz/sessions"), "tui", "endpoint")
	if err := saveSessionRuntime(path, sessionRuntimeState{ModelRef: "client/approved"}); err != nil {
		t.Fatal(err)
	}
	runner := func(params libruntime.InteractiveParams) error {
		if params.ModelRef != "client/approved" {
			t.Fatalf("restored %s instead of saved client model", params.ModelRef)
		}
		return nil
	}
	var stderr strings.Builder
	args := []string{"--workspace", root, "--no-model-discovery", "--base-url", clientURL, "-s", "endpoint", "--profile", "general"}
	if code := RunWithOptions(t.Context(), args, io.Discard, &stderr, Options{InteractiveRunner: runner}); code != 0 {
		t.Fatalf("matching saved endpoint rejected: code=%d stderr=%s", code, stderr.String())
	}
	if err := saveSessionRuntime(path, sessionRuntimeState{ModelRef: "local/a"}); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := RunWithOptions(t.Context(), args, io.Discard, &stderr, Options{InteractiveRunner: runner}); code == 0 || !strings.Contains(stderr.String(), "--base-url endpoint override rejected") {
		t.Fatalf("mismatching saved endpoint accepted: code=%d stderr=%s", code, stderr.String())
	}
}

func TestAllowModelCommand(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}
	cfg := modelPolicyTestConfig(root, "http://localhost:8000/v1")
	cfg.ActiveModel = "local/a"
	homeConfig := filepath.Join(home, ".config/caretz/config.json")
	writeModelPolicyConfig(t, homeConfig, cfg)
	runner := func(params libruntime.InteractiveParams) error {
		registry := params.SlashCommandRegistry
		_, _, err := registry.Execute(t.Context(), params.Session, "/models cloud/x")
		if err == nil || !strings.Contains(err.Error(), "/allow-model cloud/x") {
			t.Fatalf("blocked switch err=%v", err)
		}
		session := params.Session.(*agent.AgentSession)
		if _, _, err := registry.Execute(t.Context(), params.Session, "/goal --yes --worker-model cloud/x make tests pass"); err == nil || !strings.Contains(err.Error(), "worker model cloud/x") || !strings.Contains(err.Error(), "/allow-model cloud/x") {
			t.Fatalf("blocked goal role err=%v", err)
		}
		if _, _, err := registry.Execute(t.Context(), params.Session, "/allow-model unknown/x"); err == nil || !strings.Contains(err.Error(), "unknown configured model") {
			t.Fatalf("unknown model err=%v", err)
		}
		if _, _, err := registry.Execute(t.Context(), params.Session, "/allow-model"); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("missing ref err=%v", err)
		}
		result, _, err := registry.Execute(t.Context(), params.Session, "/allow-model cloud/x")
		if err != nil || !strings.Contains(result.Message, "Allowed cloud/x") || !strings.Contains(result.Message, config.ShortenHome(homeConfig)) {
			t.Fatalf("allow=%+v err=%v", result, err)
		}
		result, _, err = registry.Execute(t.Context(), params.Session, "/allow-model cloud/x")
		if err != nil || !strings.Contains(result.Message, "already allowed") {
			t.Fatalf("second allow=%+v err=%v", result, err)
		}
		if err := session.ValidateSubagentModel(t.Context(), "cloud/x", ""); err != nil {
			t.Fatalf("goal role model after /allow-model: %v", err)
		}
		if result, _, err = registry.Execute(t.Context(), params.Session, "/models cloud/x"); err != nil || result.ActiveModelRef != "cloud/x" {
			t.Fatalf("switch after allow=%+v err=%v", result, err)
		}
		return nil
	}
	var stderr strings.Builder
	if code := RunWithOptions(t.Context(), []string{"--workspace", root, "--no-model-discovery", "-s", "allow"}, io.Discard, &stderr, Options{InteractiveRunner: runner}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	saved, err := config.Load(homeConfig, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ModelRules) != 1 || strings.Join(saved.ModelRules[0].Allow, ",") != "local/*,cloud/x" {
		t.Fatalf("saved rules=%+v", saved.ModelRules)
	}
	// The saved rule lets a later run start on the model.
	stderr.Reset()
	if _, code := BuildRuntime(t.Context(), []string{"--workspace", root, "--no-model-discovery", "--model", "cloud/x"}, &stderr); code != 0 {
		t.Fatalf("startup after allow code=%d stderr=%s", code, stderr.String())
	}
}

func TestAllowModelCommandNoRule(t *testing.T) {
	policy := config.ModelPolicy{Workspace: "/tmp/open"}
	cfg := modelPolicyTestConfig(t.TempDir(), "http://localhost/v1")
	rule, message, err := allowModelCommand(cfg, &policy, filepath.Join(t.TempDir(), "config.json"), "cloud/x")
	if err != nil || rule != nil || !strings.Contains(message, "already allowed") {
		t.Fatalf("rule=%v message=%s err=%v", rule, message, err)
	}
}

func TestAllowModelACP(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}
	cfg := modelPolicyTestConfig(root, "http://localhost:8000/v1")
	cfg.ActiveModel = "local/a"
	writeModelPolicyConfig(t, filepath.Join(home, ".config/caretz/config.json"), cfg)
	rt, code := BuildRuntime(t.Context(), []string{"--workspace", root, "--no-model-discovery"}, io.Discard)
	if code != 0 {
		t.Fatalf("startup=%d", code)
	}
	constructed, err := NewACPServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := constructed.(*acpServer)
	server.out = io.Discard
	acp, err := server.openSession("allow", root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = acp.session.Close() }()
	if err := acp.session.ValidateSubagentModel(t.Context(), "cloud/x", ""); err == nil {
		t.Fatal("blocked subagent model accepted")
	}
	message, err := server.allowACPModel(acp.session, "cloud/x")
	if err != nil || !strings.Contains(message, "Allowed cloud/x") {
		t.Fatalf("allow=%s err=%v", message, err)
	}
	if err := acp.session.ValidateSubagentModel(t.Context(), "cloud/x", ""); err != nil {
		t.Fatalf("subagent model after allow: %v", err)
	}
	if len(server.modelChoicesForSession(acp)) != 3 {
		t.Fatal("allowed model missing from ACP choices")
	}
}

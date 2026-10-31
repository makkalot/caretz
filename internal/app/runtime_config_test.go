package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/loopguard"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/lib/llm"
)

// writeModelCache stores models for providerKey as fetched from baseURL.
func writeModelCache(t *testing.T, path, providerKey, baseURL string, models ...string) {
	t.Helper()
	err := config.UpdateModelCache(path, func(cache *config.ModelCache) {
		cache.SetCachedModels(providerKey, baseURL, models, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func modelRefs(cfg config.Config) string {
	choices := cfg.ModelChoices()
	refs := make([]string, 0, len(choices))
	for _, choice := range choices {
		refs = append(refs, choice.Ref)
	}
	return strings.Join(refs, ",")
}

func TestCachedProviderModelsMergesCacheWithoutContactingProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("startup contacted a provider")
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "models.json")
	writeModelCache(t, cachePath, "live", server.URL+"/", "z-model", "a-model")
	// Cached from a URL the provider no longer uses, so it must be ignored.
	writeModelCache(t, cachePath, "moved", "http://old.example/v1", "stale")
	cfg := config.Config{Provider: map[string]config.Provider{
		"live":    {Options: config.ProviderOptions{BaseURL: stringPtr(server.URL)}},
		"moved":   {Options: config.ProviderOptions{BaseURL: stringPtr("http://new.example/v1")}, Models: map[string]config.Model{"fallback": {}}},
		"offline": {Options: config.ProviderOptions{BaseURL: stringPtr(server.URL)}, Models: map[string]config.Model{"fallback": {Reasoning: "high"}}},
	}}
	var stderr strings.Builder
	got := cachedProviderModels(cfg, cachePath, baseFlagValues{}, &stderr)
	if want := "live/a-model,live/z-model,moved/fallback,offline/fallback"; modelRefs(got) != want {
		t.Fatalf("refs = %q, want %q", modelRefs(got), want)
	}
	if got.ActiveModel != "live/a-model" {
		t.Fatalf("ActiveModel = %q, want deterministic first model", got.ActiveModel)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want quiet startup", stderr.String())
	}
}

func TestCachedProviderModelsWarnsOnCorruptCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cachePath, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Provider: map[string]config.Provider{
		"local": {Options: config.ProviderOptions{BaseURL: stringPtr("http://127.0.0.1:1/v1")}, Models: map[string]config.Model{"configured": {}}},
	}}
	var stderr strings.Builder
	got := cachedProviderModels(cfg, cachePath, baseFlagValues{}, &stderr)
	if modelRefs(got) != "local/configured" || !strings.Contains(stderr.String(), "caretz models --refresh") {
		t.Fatalf("refs = %q, stderr = %q", modelRefs(got), stderr.String())
	}
}

func TestProbeProvidersSendsOnlyStoredKey(t *testing.T) {
	keyServer := func(want string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer "+want {
				t.Errorf("Authorization = %q, want %q", got, "Bearer "+want)
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
		}))
	}
	keyed := keyServer("stored-key")
	defer keyed.Close()
	keyless := keyServer(config.NoAPIKey)
	defer keyless.Close()

	t.Setenv("OPENAI_API_KEY", "env-key")
	cfg := config.Config{
		ActiveModel: "keyless/m",
		Provider: map[string]config.Provider{
			"keyed":   {Options: config.ProviderOptions{BaseURL: stringPtr(keyed.URL)}},
			"keyless": {Options: config.ProviderOptions{BaseURL: stringPtr(keyless.URL)}},
		},
	}.WithCredentials(config.Credentials{Provider: map[string]config.ProviderCredential{"keyed": {APIKey: "stored-key"}}})
	for _, probe := range probeProviders(context.Background(), cfg, time.Second) {
		if probe.Err != nil {
			t.Fatalf("probe %s: %v", probe.Key, probe.Err)
		}
	}
}

func TestProviderProbeReasonSeparatesBadKeyFromDeniedAccess(t *testing.T) {
	statusServer := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"no"}}`)
		}))
	}
	unauthorized := statusServer(http.StatusUnauthorized)
	defer unauthorized.Close()
	forbidden := statusServer(http.StatusForbidden)
	defer forbidden.Close()

	cfg := config.Config{Provider: map[string]config.Provider{
		"bad":    {Options: config.ProviderOptions{BaseURL: stringPtr(unauthorized.URL)}},
		"denied": {Options: config.ProviderOptions{BaseURL: stringPtr(forbidden.URL)}},
	}}
	want := map[string]string{
		"bad":    "key rejected (HTTP 401); run caretz auth set bad",
		"denied": "access denied (HTTP 403); check key permissions or run caretz auth set denied",
	}
	for _, probe := range probeProviders(context.Background(), cfg, time.Second) {
		if got := providerProbeReason(probe); got != want[probe.Key] {
			t.Fatalf("%s reason = %q, want %q", probe.Key, got, want[probe.Key])
		}
	}
}

func TestCachedProviderModelsCanBeDisabled(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models.json")
	writeModelCache(t, cachePath, "local", "http://127.0.0.1:1/v1", "discovered")
	cfg := config.Config{Provider: map[string]config.Provider{
		"local": {
			Options: config.ProviderOptions{BaseURL: stringPtr("http://127.0.0.1:1/v1")},
			Models:  map[string]config.Model{"configured": {}},
		},
	}}
	got := cachedProviderModels(cfg, cachePath, baseFlagValues{DisableModelDiscovery: true}, io.Discard)
	if got.ActiveModel != "local/configured" {
		t.Fatalf("ActiveModel = %q, want configured fallback", got.ActiveModel)
	}
	if refs := modelRefs(got); refs != "local/configured" {
		t.Fatalf("refs = %q, want configured model only", refs)
	}
}

func TestNoModelDiscoveryFlag(t *testing.T) {
	var values baseFlagValues
	prepareBaseFlags(&values)
	command := &clicmd.Command{Use: "test", Options: &values}
	if _, err := command.Parse([]string{"--no-model-discovery"}); err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !values.DisableModelDiscovery {
		t.Fatal("DisableModelDiscovery = false, want true")
	}
}

func TestSessionExecExplicitFalseFlagIsVisited(t *testing.T) {
	var values baseFlagValues
	prepareBaseFlags(&values)
	command := &clicmd.Command{Use: "test", Options: &values}
	result, err := command.Parse([]string{"--session-exec=false"})
	if err != nil {
		t.Fatal(err)
	}
	recordBaseFlagVisits(result.Options, &values)
	if !values.SandboxFlags.SessionExecSet || values.SandboxFlags.SessionExecValue {
		t.Fatalf("flag state=%+v", values.SandboxFlags)
	}
}

func TestResolveRuntimeSettings_LLMTimeoutAndAttemptsDefaults(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: stringPtr("http://localhost:8000/v1")},
				Models:  map[string]config.Model{"qwen": {Model: "qwen"}},
			},
		},
	}

	settings, err := resolveRuntimeSettings(cfg, runtimeFlagValues{}, mapGetenv(nil))
	if err != nil {
		t.Fatalf("resolveRuntimeSettings() error = %v", err)
	}
	if settings.LLMTimeout != llm.DefaultRequestTimeout {
		t.Fatalf("LLMTimeout = %s, want %s", settings.LLMTimeout, llm.DefaultRequestTimeout)
	}
	if settings.LLMMaxAttempts != llm.DefaultMaxAttempts {
		t.Fatalf("LLMMaxAttempts = %d, want %d", settings.LLMMaxAttempts, llm.DefaultMaxAttempts)
	}
}

func TestResolveRuntimeSettings_LLMTimeoutAndAttemptsFlags(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: stringPtr("http://localhost:8000/v1")},
				Models:  map[string]config.Model{"qwen": {Model: "qwen"}},
			},
		},
	}

	settings, err := resolveRuntimeSettings(cfg, runtimeFlagValues{
		LLMTimeout:     2 * time.Minute,
		LLMTimeoutSet:  true,
		LLMMaxAttempts: 5,
	}, mapGetenv(nil))
	if err != nil {
		t.Fatalf("resolveRuntimeSettings() error = %v", err)
	}
	if settings.LLMTimeout != 2*time.Minute {
		t.Fatalf("LLMTimeout = %s, want 2m", settings.LLMTimeout)
	}
	if settings.LLMMaxAttempts != 5 {
		t.Fatalf("LLMMaxAttempts = %d, want 5", settings.LLMMaxAttempts)
	}
}

func TestResolveRuntimeSettings_LLMTimeoutZeroDisables(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: stringPtr("http://localhost:8000/v1")},
				Models:  map[string]config.Model{"qwen": {Model: "qwen"}},
			},
		},
	}

	settings, err := resolveRuntimeSettings(cfg, runtimeFlagValues{LLMTimeout: 0, LLMTimeoutSet: true}, mapGetenv(nil))
	if err != nil {
		t.Fatalf("resolveRuntimeSettings() error = %v", err)
	}
	if settings.LLMTimeout != 0 {
		t.Fatalf("LLMTimeout = %s, want disabled timeout 0", settings.LLMTimeout)
	}
}

func TestResolveRuntimeSettings_LLMValidation(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: stringPtr("http://localhost:8000/v1")},
				Models:  map[string]config.Model{"qwen": {Model: "qwen"}},
			},
		},
	}

	if _, err := resolveRuntimeSettings(cfg, runtimeFlagValues{LLMTimeout: -2, LLMTimeoutSet: true}, mapGetenv(nil)); err == nil {
		t.Fatal("expected negative timeout error, got nil")
	}
	if _, err := resolveRuntimeSettings(cfg, runtimeFlagValues{LLMMaxAttempts: -1}, mapGetenv(nil)); err == nil {
		t.Fatal("expected negative attempts error, got nil")
	}
}

func TestResolveDisableTasksPrecedence(t *testing.T) {
	cfg := config.Config{Runtime: config.Profile{Tasks: config.Tasks{Enabled: boolPtr(false)}}}

	if !resolveDisableTasks(cfg, taskFlagUnspecified) {
		t.Fatal("resolveDisableTasks() = false, want config disabled")
	}
	if resolveDisableTasks(cfg, taskFlagEnable) {
		t.Fatal("resolveDisableTasks(--tasks) = true, want enabled")
	}
	if !resolveDisableTasks(config.Config{}, taskFlagDisable) {
		t.Fatal("resolveDisableTasks(--no-tasks) = false, want disabled")
	}
}

func TestResolveLoopGuardPrecedence(t *testing.T) {
	threshold := 4
	profile := config.LoopGuard{Mode: "shadow", RepeatThreshold: &threshold, Judge: &config.GoalRole{Model: " local/big ", Reasoning: "high"}}
	env := func(value string) func(string) string {
		return func(name string) string {
			if name == "CARETZ_LOOP_GUARD" {
				return value
			}
			return ""
		}
	}

	cfg, err := resolveLoopGuard(profile, loopGuardFlags{}, env(""))
	if err != nil || cfg.Mode != loopguard.ModeShadow || cfg.RepeatThreshold != 4 || cfg.JudgeModel != "local/big" || cfg.JudgeReasoning != "high" {
		t.Fatalf("resolveLoopGuard(config) = %+v, %v", cfg, err)
	}
	if cfg, _ := resolveLoopGuard(profile, loopGuardFlags{}, env("active")); cfg.Mode != loopguard.ModeActive {
		t.Fatalf("env mode = %q, want active", cfg.Mode)
	}
	if cfg, _ := resolveLoopGuard(profile, loopGuardFlags{Mode: "off"}, env("active")); cfg.Mode != loopguard.ModeOff {
		t.Fatalf("flag mode = %q, want off", cfg.Mode)
	}
	if _, err := resolveLoopGuard(profile, loopGuardFlags{Mode: "loud"}, env("")); err == nil {
		t.Fatal("resolveLoopGuard(--loop-guard loud) error = nil")
	}
}

func TestResolveLoopGuardJudgeFlags(t *testing.T) {
	profile := config.LoopGuard{Judge: &config.GoalRole{Model: "local/big", Reasoning: "high"}}
	noEnv := func(string) string { return "" }

	// A flag model drops the reasoning chosen for the configured model.
	cfg, err := resolveLoopGuard(profile, loopGuardFlags{Model: "cloud/judge"}, noEnv)
	if err != nil || cfg.JudgeModel != "cloud/judge" || cfg.JudgeReasoning != "" {
		t.Fatalf("--loop-guard-model = %+v, %v", cfg, err)
	}
	cfg, err = resolveLoopGuard(profile, loopGuardFlags{Model: "cloud/judge", Reasoning: "low"}, noEnv)
	if err != nil || cfg.JudgeModel != "cloud/judge" || cfg.JudgeReasoning != "low" {
		t.Fatalf("--loop-guard-model --loop-guard-reasoning = %+v, %v", cfg, err)
	}
	// Reasoning alone applies to the configured model.
	cfg, err = resolveLoopGuard(profile, loopGuardFlags{Reasoning: "medium"}, noEnv)
	if err != nil || cfg.JudgeModel != "local/big" || cfg.JudgeReasoning != "medium" {
		t.Fatalf("--loop-guard-reasoning = %+v, %v", cfg, err)
	}
	if _, err := resolveLoopGuard(config.LoopGuard{}, loopGuardFlags{Reasoning: "high"}, noEnv); err == nil || !strings.Contains(err.Error(), "--loop-guard-model") {
		t.Fatalf("reasoning without any model error = %v", err)
	}
}

func TestResolveLoopGuardCheckEvery(t *testing.T) {
	noEnv := func(string) string { return "" }
	twenty, zero, negative := 20, 0, -1

	if cfg, _ := resolveLoopGuard(config.LoopGuard{}, loopGuardFlags{}, noEnv); cfg.CheckEvery != loopguard.DefaultCheckEvery {
		t.Fatalf("default CheckEvery = %d, want %d", cfg.CheckEvery, loopguard.DefaultCheckEvery)
	}
	if cfg, _ := resolveLoopGuard(config.LoopGuard{CheckEvery: &twenty}, loopGuardFlags{}, noEnv); cfg.CheckEvery != 20 {
		t.Fatalf("config CheckEvery = %d, want 20", cfg.CheckEvery)
	}
	if cfg, _ := resolveLoopGuard(config.LoopGuard{CheckEvery: &twenty}, loopGuardFlags{CheckEvery: &zero}, noEnv); cfg.CheckEvery != 0 {
		t.Fatalf("--loop-guard-check-every 0 = %d, want 0 (off)", cfg.CheckEvery)
	}
	if _, err := resolveLoopGuard(config.LoopGuard{}, loopGuardFlags{CheckEvery: &negative}, noEnv); err == nil {
		t.Fatal("negative --loop-guard-check-every error = nil")
	}
}

func TestTaskFlagsRejectConflict(t *testing.T) {
	var values baseFlagValues
	prepareBaseFlags(&values)
	command := &clicmd.Command{Use: "caretz", Options: &values}
	if _, err := command.Parse([]string{"--tasks", "--no-tasks"}); err == nil {
		t.Fatal("Parse(--tasks --no-tasks) error = nil, want conflict")
	}
}

func TestSplitFlagList(t *testing.T) {
	got := splitFlagList([]string{"Read, Write", "Grep", "Read", "  Ls  "})
	want := []string{"Read", "Write", "Grep", "Ls"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("splitFlagList() = %v, want %v", got, want)
	}
}

func TestResolveConfigRootDefaultsToProjectCaretz(t *testing.T) {
	cwd := t.TempDir()

	root, err := resolveConfigRoot(cwd, "", configRootFlagState{}, mapGetenv(nil))
	if err != nil {
		t.Fatalf("resolveConfigRoot() error = %v", err)
	}
	if root != filepath.Join(cwd, ".caretz") {
		t.Fatalf("root = %q, want project .caretz", root)
	}
}

func TestResolveConfigRootUsesEnvAndFlagPrecedence(t *testing.T) {
	cwd := t.TempDir()
	envRoot := t.TempDir()
	flagRoot := t.TempDir()

	root, err := resolveConfigRoot(cwd, "", configRootFlagState{}, mapGetenv(map[string]string{
		"CARETZ_CONFIG_ROOT": envRoot,
	}))
	if err != nil {
		t.Fatalf("resolveConfigRoot(env) error = %v", err)
	}
	resolvedEnvRoot, err := filepath.EvalSymlinks(envRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(envRoot): %v", err)
	}
	if root != resolvedEnvRoot {
		t.Fatalf("root = %q, want env root %q", root, resolvedEnvRoot)
	}

	root, err = resolveConfigRoot(cwd, flagRoot, configRootFlagState{ConfigRootSet: true}, mapGetenv(map[string]string{
		"CARETZ_CONFIG_ROOT": envRoot,
	}))
	if err != nil {
		t.Fatalf("resolveConfigRoot(flag) error = %v", err)
	}
	resolvedFlagRoot, err := filepath.EvalSymlinks(flagRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(flagRoot): %v", err)
	}
	if root != resolvedFlagRoot {
		t.Fatalf("root = %q, want flag root %q", root, resolvedFlagRoot)
	}
}

func TestResolveConfigRootAllowsMissingRoot(t *testing.T) {
	cwd := t.TempDir()
	missing := filepath.Join(cwd, "state", "caretz")

	root, err := resolveConfigRoot(cwd, missing, configRootFlagState{ConfigRootSet: true}, mapGetenv(nil))
	if err != nil {
		t.Fatalf("resolveConfigRoot() error = %v", err)
	}
	if root != missing {
		t.Fatalf("root = %q, want missing absolute root %q", root, missing)
	}
}

func TestRuntimeResourceDirsIncludeCompatibleWorkspaceSkills(t *testing.T) {
	cwd := t.TempDir()
	homeSkills := filepath.Join(t.TempDir(), "home", "skills")
	homeAgents := filepath.Join(t.TempDir(), "home", "agents")
	configRoot := filepath.Join(cwd, ".caretz")

	skillDirs := skillDirsForRuntime(homeSkills, cwd, configRoot, false)
	wantSkillDirs := []string{
		homeSkills,
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(cwd, ".caretz", "skills"),
	}
	if fmt.Sprint(skillDirs) != fmt.Sprint(wantSkillDirs) {
		t.Fatalf("skillDirs = %#v, want %#v", skillDirs, wantSkillDirs)
	}

	agentDirs := agentDirsForRuntime(homeAgents, cwd, configRoot, false)
	wantAgentDirs := []string{homeAgents, filepath.Join(cwd, ".caretz", "agents")}
	if fmt.Sprint(agentDirs) != fmt.Sprint(wantAgentDirs) {
		t.Fatalf("agentDirs = %#v, want %#v", agentDirs, wantAgentDirs)
	}
}

func TestRuntimeResourceDirsLayerConfigRootBeforeWorkspace(t *testing.T) {
	cwd := t.TempDir()
	configRoot := filepath.Join(t.TempDir(), "state")
	homeSkills := filepath.Join(t.TempDir(), "home", "skills")
	homeAgents := filepath.Join(t.TempDir(), "home", "agents")

	skillDirs := skillDirsForRuntime(homeSkills, cwd, configRoot, true)
	wantSkillDirs := []string{
		homeSkills,
		filepath.Join(configRoot, "skills"),
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(cwd, ".caretz", "skills"),
	}
	if fmt.Sprint(skillDirs) != fmt.Sprint(wantSkillDirs) {
		t.Fatalf("skillDirs = %#v, want %#v", skillDirs, wantSkillDirs)
	}

	agentDirs := agentDirsForRuntime(homeAgents, cwd, configRoot, true)
	wantAgentDirs := []string{
		homeAgents,
		filepath.Join(configRoot, "agents"),
		filepath.Join(cwd, ".caretz", "agents"),
	}
	if fmt.Sprint(agentDirs) != fmt.Sprint(wantAgentDirs) {
		t.Fatalf("agentDirs = %#v, want %#v", agentDirs, wantAgentDirs)
	}
}

// The Linux sandbox can only protect agent directories that exist.
func TestEnsureAgentsDirsCreatesMissingDirs(t *testing.T) {
	cwd := t.TempDir()
	homeAgents := filepath.Join(t.TempDir(), "home", "agents")
	dirs := agentDirsForRuntime(homeAgents, cwd, filepath.Join(t.TempDir(), "state"), true)

	ensureAgentsDirs(dirs, nil)

	for _, dir := range dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("agents dir %s not created: %v", dir, err)
		}
	}
}

func TestRuntimeReadableResourceRootsIncludesExistingSkillsAndAgents(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "config", "skills")
	agentsDir := filepath.Join(root, "config", "agents")
	missingDir := filepath.Join(root, "missing", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	got := runtimeReadableResourceRoots([]string{skillsDir, missingDir}, []string{agentsDir})
	want := []string{mustEvalSymlinks(t, skillsDir), mustEvalSymlinks(t, agentsDir)}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("runtimeReadableResourceRoots() = %#v, want %#v", got, want)
	}
}

func TestRuntimeReadableResourceRootsDeduplicatesResolvedPaths(t *testing.T) {
	root := t.TempDir()
	sharedDir := filepath.Join(root, "shared")
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		t.Fatal(err)
	}

	got := runtimeReadableResourceRoots([]string{sharedDir}, []string{sharedDir})
	want := []string{mustEvalSymlinks(t, sharedDir)}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("runtimeReadableResourceRoots() = %#v, want %#v", got, want)
	}
}

func TestSystemPromptWithWorkspaceReplacesWorkingDirectory(t *testing.T) {
	base := "system\n\nWorking directory: /old\n\nPlan mode is active."
	got := systemPromptWithWorkspace(base, "/new/workspace")
	want := "system\n\nWorking directory: /new/workspace\n\nPlan mode is active."
	if got != want {
		t.Fatalf("systemPromptWithWorkspace() = %q, want %q", got, want)
	}
}

func TestSystemPromptWithWorkspaceAddsWorkingDirectoryWhenMissing(t *testing.T) {
	got := systemPromptWithWorkspace("system", "/new/workspace")
	want := "system\n\nWorking directory: /new/workspace"
	if got != want {
		t.Fatalf("systemPromptWithWorkspace() = %q, want %q", got, want)
	}
}

func TestResolveSandboxSettings_UsesConfigDefaults(t *testing.T) {
	readRoot := t.TempDir()
	writableRoot := t.TempDir()
	cfg := config.Config{
		Runtime: config.Profile{Sandbox: config.Sandbox{
			ReadRoots:           []string{readRoot},
			WritableRoots:       []string{writableRoot},
			NetworkAllowlist:    []string{"pypi.org", "files.pythonhosted.org"},
			AllowDocker:         boolPtr(true),
			AllowLoopback:       boolPtr(true),
			UnrestrictedNetwork: boolPtr(true),
			SessionExec:         boolPtr(true),
			Env:                 []string{"GOFLAGS=-mod=readonly", "NPM_TOKEN"},
		}},
	}

	settings, err := resolveSandboxSettings(cfg, sandboxFlagState{}, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("resolveSandboxSettings() error = %v", err)
	}
	if !settings.AllowDocker {
		t.Fatalf("AllowDocker = false, want config true")
	}
	if !settings.AllowLoopback {
		t.Fatalf("AllowLoopback = false, want config true")
	}
	if !settings.UnrestrictedNetwork {
		t.Fatalf("UnrestrictedNetwork = false, want config true")
	}
	if !settings.SessionExec {
		t.Fatalf("SessionExec = false, want config true")
	}
	overridden, err := resolveSandboxSettings(cfg, sandboxFlagState{SessionExecSet: true, SessionExecValue: false, AllowLoopbackSet: true, AllowLoopbackValue: false}, nil, nil, nil, false, false)
	if err != nil || overridden.SessionExec || overridden.AllowLoopback {
		t.Fatalf("explicit false override = %#v, %v", overridden, err)
	}
	if fmt.Sprint(settings.NetworkAllowlist) != fmt.Sprint([]string{"pypi.org", "files.pythonhosted.org"}) {
		t.Fatalf("NetworkAllowlist = %#v, want config values", settings.NetworkAllowlist)
	}
	if fmt.Sprint(settings.Environment) != "[GOFLAGS=-mod=readonly NPM_TOKEN]" {
		t.Fatalf("Environment = %#v", settings.Environment)
	}
	resolvedReadRoot, err := filepath.EvalSymlinks(readRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(readRoot): %v", err)
	}
	if len(settings.ReadableRoots) != 1 || settings.ReadableRoots[0] != resolvedReadRoot {
		t.Fatalf("ReadableRoots = %#v, want %#v", settings.ReadableRoots, []string{resolvedReadRoot})
	}
	resolvedWritableRoot, err := filepath.EvalSymlinks(writableRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(writableRoot): %v", err)
	}
	if len(settings.WritableRoots) != 1 || settings.WritableRoots[0] != resolvedWritableRoot {
		t.Fatalf("WritableRoots = %#v, want %#v", settings.WritableRoots, []string{resolvedWritableRoot})
	}
}

func TestResolveSandboxSettings_SessionExecDefaultsOn(t *testing.T) {
	cases := []struct {
		name  string
		cfg   config.Config
		flags sandboxFlagState
		want  bool
	}{
		{name: "default", want: true},
		{name: "config false", cfg: config.Config{Runtime: config.Profile{Sandbox: config.Sandbox{SessionExec: boolPtr(false)}}}, want: false},
		{name: "flag false", flags: sandboxFlagState{SessionExecSet: true, SessionExecValue: false}, want: false},
		{name: "flag true over config false", cfg: config.Config{Runtime: config.Profile{Sandbox: config.Sandbox{SessionExec: boolPtr(false)}}}, flags: sandboxFlagState{SessionExecSet: true, SessionExecValue: true}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings, err := resolveSandboxSettings(tc.cfg, tc.flags, nil, nil, nil, false, false)
			if err != nil {
				t.Fatalf("resolveSandboxSettings() error = %v", err)
			}
			if settings.SessionExec != tc.want {
				t.Fatalf("SessionExec = %v, want %v", settings.SessionExec, tc.want)
			}
		})
	}
}

func TestResolveSandboxSettings_EnvironmentFileMatchesEquivalentFlags(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "sandbox.env")
	if err := os.WriteFile(envFile, []byte("HOST_TOKEN\nCI=file\nFILE_ONLY=value\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	fromFile, err := resolveSandboxSettings(config.Config{Runtime: config.Profile{Sandbox: config.Sandbox{
		EnvFiles: []string{envFile},
	}}}, sandboxFlagState{}, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("file settings error = %v", err)
	}
	fromFlags, err := resolveSandboxSettings(config.Config{}, sandboxFlagState{
		EnvironmentSet: true,
		Environment:    []string{"HOST_TOKEN", "CI=file", "FILE_ONLY=value"},
	}, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("flag settings error = %v", err)
	}
	if fmt.Sprint(fromFile.Environment) != fmt.Sprint(fromFlags.Environment) {
		t.Fatalf("file environment = %#v, flags environment = %#v", fromFile.Environment, fromFlags.Environment)
	}
}

func TestSandboxEnvironmentFilesForOSSelectsProfileVariants(t *testing.T) {
	profile := config.Sandbox{
		EnvFiles:       []string{"common.env"},
		EnvFilesLinux:  []string{"linux.env"},
		EnvFilesDarwin: []string{"darwin.env"},
	}
	if got := fmt.Sprint(sandboxEnvironmentFilesForOS(profile, "linux")); got != "[common.env linux.env]" {
		t.Fatalf("linux env files = %s", got)
	}
	if got := fmt.Sprint(sandboxEnvironmentFilesForOS(profile, "darwin")); got != "[common.env darwin.env]" {
		t.Fatalf("darwin env files = %s", got)
	}
}

func TestResolveSandboxSettings_EnvironmentFilePrecedence(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "sandbox.env")
	if err := os.WriteFile(envFile, []byte("  # ignored\n\nCI=file\nFILE_ONLY=value\nWITH_EQUALS=a=b\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg := config.Config{Runtime: config.Profile{Sandbox: config.Sandbox{
		Env:      []string{"CI=profile", "PROFILE_ONLY"},
		EnvFiles: []string{envFile},
	}}}
	settings, err := resolveSandboxSettings(cfg, sandboxFlagState{
		EnvironmentSet: true,
		Environment:    []string{"CI=explicit", "EXPLICIT_ONLY=value"},
	}, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("resolveSandboxSettings() error = %v", err)
	}
	want := []string{"CI=profile", "PROFILE_ONLY", "CI=file", "FILE_ONLY=value", "WITH_EQUALS=a=b", "CI=explicit", "EXPLICIT_ONLY=value"}
	if fmt.Sprint(settings.Environment) != fmt.Sprint(want) {
		t.Fatalf("Environment = %#v, want %#v", settings.Environment, want)
	}
}

func TestResolveSandboxSettings_EnvironmentFileFlagPrecedence(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "explicit.env")
	if err := os.WriteFile(envFile, []byte("CI=file\nFILE_ONLY=value\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	settings, err := resolveSandboxSettings(config.Config{Runtime: config.Profile{Sandbox: config.Sandbox{
		Env: []string{"CI=profile"},
	}}}, sandboxFlagState{
		EnvironmentFilesSet: true,
		EnvironmentFiles:    []string{envFile},
		EnvironmentSet:      true,
		Environment:         []string{"CI=explicit"},
	}, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("resolveSandboxSettings() error = %v", err)
	}
	want := []string{"CI=profile", "CI=file", "FILE_ONLY=value", "CI=explicit"}
	if fmt.Sprint(settings.Environment) != fmt.Sprint(want) {
		t.Fatalf("Environment = %#v, want %#v", settings.Environment, want)
	}
}

func TestResolveSandboxSettings_EnvironmentFileMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.env")
	_, err := resolveSandboxSettings(config.Config{Runtime: config.Profile{Sandbox: config.Sandbox{
		EnvFiles: []string{missing},
	}}}, sandboxFlagState{}, nil, nil, nil, false, false)
	if err == nil || !strings.Contains(err.Error(), "--sandbox-env-file") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("resolveSandboxSettings() error = %v, want flag and path", err)
	}
}

func TestResolveSandboxSettings_EnvironmentFileInvalid(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "invalid.env")
	if err := os.WriteFile(envFile, []byte("NOT VALID=value\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := resolveSandboxSettings(config.Config{Runtime: config.Profile{Sandbox: config.Sandbox{
		EnvFiles: []string{envFile},
	}}}, sandboxFlagState{}, nil, nil, nil, false, false)
	if err == nil || !strings.Contains(err.Error(), "--sandbox-env-file") || !strings.Contains(err.Error(), envFile) {
		t.Fatalf("resolveSandboxSettings() error = %v, want flag and path", err)
	}
}

func TestSandboxEnvironmentForOS(t *testing.T) {
	sandboxConfig := config.Sandbox{
		Env:       []string{"COMMON", "CI=true"},
		EnvLinux:  []string{"LINUX_ONLY"},
		EnvDarwin: []string{"DARWIN_ONLY"},
	}
	if got := fmt.Sprint(sandboxEnvironmentForOS(sandboxConfig, "linux")); got != "[COMMON CI=true LINUX_ONLY]" {
		t.Fatalf("linux environment = %s", got)
	}
	if got := fmt.Sprint(sandboxEnvironmentForOS(sandboxConfig, "darwin")); got != "[COMMON CI=true DARWIN_ONLY]" {
		t.Fatalf("darwin environment = %s", got)
	}
	if got := fmt.Sprint(sandboxEnvironmentForOS(sandboxConfig, "windows")); got != "[COMMON CI=true]" {
		t.Fatalf("other environment = %s", got)
	}
}

func TestCodingGitHubEnvironmentUsesHostCLIKeyringToken(t *testing.T) {
	calls := 0
	environment := codingGitHubEnvironment("coding", []string{"GH_TOKEN", "NPM_TOKEN"}, func(string) (string, bool) {
		return "", false
	}, func() (string, error) {
		calls++
		return "keyring-token\n", nil
	})
	if calls != 1 || environment[len(environment)-1] != "GH_TOKEN=keyring-token" {
		t.Fatalf("coding GitHub environment = %#v, token calls = %d", environment, calls)
	}
}

func TestCodingGitHubEnvironmentPreservesExplicitCredentials(t *testing.T) {
	for _, environment := range [][]string{{"GH_TOKEN"}, {"GITHUB_TOKEN=explicit"}, {"GH_TOKEN="}} {
		calls := 0
		got := codingGitHubEnvironment("coding", environment, func(name string) (string, bool) {
			if name == "GH_TOKEN" && len(environment) == 1 && environment[0] == "GH_TOKEN" {
				return "exported", true
			}
			return "", false
		}, func() (string, error) {
			calls++
			return "keyring-token", nil
		})
		if calls != 0 || fmt.Sprint(got) != fmt.Sprint(environment) {
			t.Fatalf("explicit environment changed: got %#v, calls = %d", got, calls)
		}
	}
}

func TestGitHubKeyringLookupIsCodingProfileOnly(t *testing.T) {
	calls := 0
	environment := []string{"GH_TOKEN"}
	got := codingGitHubEnvironment("general", environment, func(string) (string, bool) { return "", false }, func() (string, error) {
		calls++
		return "keyring-token", nil
	})
	if calls != 0 || fmt.Sprint(got) != fmt.Sprint(environment) {
		t.Fatalf("general environment changed: got %#v, calls = %d", got, calls)
	}
}

func TestResolveSandboxSettings_DisablesSandboxFromEnv(t *testing.T) {
	t.Setenv(disableSandboxEnv, "true")

	settings, err := resolveSandboxSettings(config.Config{}, sandboxFlagState{}, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("resolveSandboxSettings() error = %v", err)
	}
	if !settings.Disabled {
		t.Fatalf("Disabled = false, want true")
	}
}

func TestEnvTruthy(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", " yes ", "on"} {
		if !envTruthy(value) {
			t.Fatalf("envTruthy(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "unexpected"} {
		if envTruthy(value) {
			t.Fatalf("envTruthy(%q) = true, want false", value)
		}
	}
}

func TestResolveSandboxSettings_GrantFlagsAppendToConfig(t *testing.T) {
	configReadRoot := t.TempDir()
	configWritableRoot := t.TempDir()
	flagReadRoot := t.TempDir()
	flagWritableRoot := t.TempDir()
	cfg := config.Config{
		Runtime: config.Profile{Sandbox: config.Sandbox{
			ReadRoots:           []string{configReadRoot},
			WritableRoots:       []string{configWritableRoot},
			NetworkAllowlist:    []string{"config.example.com", "tcp://git.example.com:22"},
			AllowDocker:         boolPtr(true),
			AllowLoopback:       boolPtr(true),
			UnrestrictedNetwork: boolPtr(true),
			Env:                 []string{"CI=true", "GOPROXY"},
		}},
	}

	settings, err := resolveSandboxSettings(cfg, sandboxFlagState{
		AllowDockerSet:         true,
		AllowLoopbackSet:       true,
		AllowLoopbackValue:     false,
		UnrestrictedNetworkSet: true,
		ReadableRootsSet:       true,
		WritableRootsSet:       true,
		NetworkAllowlistSet:    true,
		EnvironmentSet:         true,
		Environment:            []string{"NPM_TOKEN", "CI=false"},
	}, []string{flagReadRoot}, []string{flagWritableRoot}, []string{"flag.example.com", "tcp://db.example.com:5432"}, false, false)
	if err != nil {
		t.Fatalf("resolveSandboxSettings() error = %v", err)
	}
	if settings.AllowDocker {
		t.Fatalf("AllowDocker = true, want explicit CLI false override")
	}
	if settings.AllowLoopback {
		t.Fatalf("AllowLoopback = true, want explicit CLI false override")
	}
	if settings.UnrestrictedNetwork {
		t.Fatalf("UnrestrictedNetwork = true, want explicit CLI false override")
	}
	if fmt.Sprint(settings.NetworkAllowlist) != fmt.Sprint([]string{"config.example.com", "tcp://git.example.com:22", "flag.example.com", "tcp://db.example.com:5432"}) {
		t.Fatalf("NetworkAllowlist = %#v, want config plus flag grant", settings.NetworkAllowlist)
	}
	if fmt.Sprint(settings.Environment) != "[CI=true GOPROXY NPM_TOKEN CI=false]" {
		t.Fatalf("Environment = %#v, want profile plus CLI entries", settings.Environment)
	}
	resolvedConfigReadRoot, err := filepath.EvalSymlinks(configReadRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(configReadRoot): %v", err)
	}
	resolvedFlagReadRoot, err := filepath.EvalSymlinks(flagReadRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(flagReadRoot): %v", err)
	}
	if fmt.Sprint(settings.ReadableRoots) != fmt.Sprint([]string{resolvedConfigReadRoot, resolvedFlagReadRoot}) {
		t.Fatalf("ReadableRoots = %#v, want config plus flag grant", settings.ReadableRoots)
	}
	resolvedConfigWritableRoot, err := filepath.EvalSymlinks(configWritableRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(configWritableRoot): %v", err)
	}
	resolvedFlagWritableRoot, err := filepath.EvalSymlinks(flagWritableRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(flagWritableRoot): %v", err)
	}
	if fmt.Sprint(settings.WritableRoots) != fmt.Sprint([]string{resolvedConfigWritableRoot, resolvedFlagWritableRoot}) {
		t.Fatalf("WritableRoots = %#v, want config plus flag grant", settings.WritableRoots)
	}
}

func TestAppendUniqueStringsPreservesOrder(t *testing.T) {
	got := appendUniqueStrings([]string{"a", "b"}, "b", "c", "a", "d")
	if fmt.Sprint(got) != "[a b c d]" {
		t.Fatalf("appendUniqueStrings() = %v", got)
	}
}

func TestCodingWorktreeGitRootGrantsSharedMetadata(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, "parent", ".git")
	gitDir := filepath.Join(commonDir, "worktrees", "topic")
	workspace := filepath.Join(root, "topic")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedCommonDir, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		t.Fatal(err)
	}

	if got := codingWorktreeGitRoot("coding", workspace); got != resolvedCommonDir {
		t.Fatalf("codingWorktreeGitRoot() = %q, want %q", got, resolvedCommonDir)
	}
	for _, profile := range []string{"general", "compact"} {
		if got := codingWorktreeGitRoot(profile, workspace); got != "" {
			t.Fatalf("%s profile Git root = %q, want none", profile, got)
		}
	}
}

func TestWorktreeGitReadOnlyPathsProtectHostExecutedMetadata(t *testing.T) {
	commonDir := filepath.Join(t.TempDir(), ".git")
	got := worktreeGitReadOnlyPaths(commonDir)
	for _, name := range []string{"hooks", "config", "info"} {
		if !slices.Contains(got, filepath.Join(commonDir, name)) {
			t.Fatalf("worktreeGitReadOnlyPaths() = %v, missing %s", got, name)
		}
	}
	for _, path := range got {
		for _, writable := range []string{"objects", "refs", "logs", "worktrees"} {
			if strings.HasPrefix(path, filepath.Join(commonDir, writable)) {
				t.Fatalf("worktreeGitReadOnlyPaths() protects %s, which commits must write", path)
			}
		}
	}
}

func TestCodingWorktreeGitRootSkipsNormalCheckout(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := codingWorktreeGitRoot("coding", workspace); got != "" {
		t.Fatalf("normal checkout Git root = %q, want no extra grant", got)
	}
}

func TestResolveSandboxSettings_InvalidConfigRootFails(t *testing.T) {
	cfg := config.Config{
		Runtime: config.Profile{Sandbox: config.Sandbox{
			ReadRoots: []string{filepath.Join(t.TempDir(), "missing")},
		}},
	}

	if _, err := resolveSandboxSettings(cfg, sandboxFlagState{}, nil, nil, nil, false, false); err == nil {
		t.Fatal("resolveSandboxSettings() error = nil, want invalid root error")
	}
}

func TestResolveRuntimeSettings_ModelRefOverridesEnvFromConfig(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "openai/default",
		Provider: map[string]config.Provider{
			"openai": {
				Options: config.ProviderOptions{
					BaseURL: stringPtr("https://api.openai.test/v1"),
				},
				Models: map[string]config.Model{
					"default": {Model: "gpt-default"},
				},
			},
			"llama.cpp": {
				Options: config.ProviderOptions{
					BaseURL: stringPtr("http://localhost:8001/v1"),
				},
				Models: map[string]config.Model{
					"qwen3627b_mtp_code": {
						Model:     "qwen3-627b-mtp-code",
						Reasoning: "xhigh",
						Limit: config.Limit{
							Context:          intPtrMain(32768),
							ReservedTokens:   intPtrMain(4096),
							KeepRecentTokens: intPtrMain(8192),
							SummaryTokens:    intPtrMain(3000),
						},
					},
				},
			},
		},
	}.WithCredentials(config.Credentials{Provider: map[string]config.ProviderCredential{
		"openai":    {APIKey: "openai-key"},
		"llama.cpp": {APIKey: "local-key"},
	}})
	env := map[string]string{
		"OPENAI_API_KEY":            "env-key",
		"OPENAI_API_BASE_URL":       "https://env.test/v1",
		"OPENAI_API_MODEL":          "env-model",
		"OPENAI_REASONING":          "low",
		"CARETZ_CONTEXT_WINDOW":     "12345",
		"CARETZ_RESERVED_TOKENS":    "2345",
		"CARETZ_KEEP_RECENT_TOKENS": "3456",
		"CARETZ_SUMMARY_TOKENS":     "4567",
	}

	settings, err := resolveRuntimeSettings(cfg, runtimeFlagValues{Model: "llama.cpp/qwen3627b_mtp_code"}, mapGetenv(env))
	if err != nil {
		t.Fatalf("resolveRuntimeSettings() error = %v", err)
	}

	if settings.APIKey != "local-key" {
		t.Fatalf("APIKey = %q, want config ref API key", settings.APIKey)
	}
	if settings.BaseURL != "http://localhost:8001/v1" {
		t.Fatalf("BaseURL = %q, want config ref base URL", settings.BaseURL)
	}
	if settings.Model != "qwen3-627b-mtp-code" {
		t.Fatalf("Model = %q, want config ref model", settings.Model)
	}
	if settings.ReasoningEffort != "xhigh" {
		t.Fatalf("ReasoningEffort = %q, want config ref reasoning effort", settings.ReasoningEffort)
	}
	if settings.ContextWindow != 32768 {
		t.Fatalf("ContextWindow = %d, want config ref context window", settings.ContextWindow)
	}
	if settings.ReservedTokens != 4096 {
		t.Fatalf("ReservedTokens = %d, want config ref reserved tokens", settings.ReservedTokens)
	}
	if settings.KeepRecentTokens != 8192 {
		t.Fatalf("KeepRecentTokens = %d, want config ref keep recent tokens", settings.KeepRecentTokens)
	}
	if settings.SummaryTokens != 3000 {
		t.Fatalf("SummaryTokens = %d, want config ref summary tokens", settings.SummaryTokens)
	}
}

func TestResolveRuntimeSettings_RawSlashModelFallsBackToExistingBehavior(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: stringPtr("http://localhost:8000/v1")},
				Models:  map[string]config.Model{"qwen": {}},
			},
		},
	}

	settings, err := resolveRuntimeSettings(cfg, runtimeFlagValues{Model: "openrouter/vendor/model"}, mapGetenv(nil))
	if err != nil {
		t.Fatalf("resolveRuntimeSettings() error = %v", err)
	}

	if settings.Model != "openrouter/vendor/model" {
		t.Fatalf("Model = %q, want raw slash model", settings.Model)
	}
	if settings.BaseURL != "http://localhost:8000/v1" {
		t.Fatalf("BaseURL = %q, want active config base URL", settings.BaseURL)
	}
}

func TestResolveRuntimeSettings_ExplicitAdHocProviderModel(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "opencodego/missing-active-model",
		Provider: map[string]config.Provider{
			"opencodego": {
				Options: config.ProviderOptions{
					BaseURL: stringPtr("https://opencode.ai/zen/go/v1"),
				},
				Models: map[string]config.Model{"deepseek-v4-flash": {}},
			},
		},
	}.WithCredentials(config.Credentials{Provider: map[string]config.ProviderCredential{"opencodego": {APIKey: "provider-key"}}})

	settings, err := resolveRuntimeSettings(cfg, runtimeFlagValues{Model: "opencodego/glm-5.3-flash"}, mapGetenv(nil))
	if err != nil {
		t.Fatalf("resolveRuntimeSettings() error = %v", err)
	}
	if settings.Model != "glm-5.3-flash" {
		t.Fatalf("Model = %q, want glm-5.3-flash", settings.Model)
	}
	if settings.ActiveModelRef != "opencodego/glm-5.3-flash" {
		t.Fatalf("ActiveModelRef = %q, want explicit provider/model ref", settings.ActiveModelRef)
	}
	if settings.BaseURL != "https://opencode.ai/zen/go/v1" || settings.APIKey != "provider-key" {
		t.Fatalf("provider options = (%q, %q), want configured URL and key", settings.BaseURL, settings.APIKey)
	}
}

// TestAPIKeyGoesOnlyToItsHost checks which key each combination of config,
// flags, env and model rule sends to which host: a stored key only to its
// provider, OPENAI_API_KEY only to an ad hoc endpoint, and --api-key wherever
// the request goes.
func TestAPIKeyGoesOnlyToItsHost(t *testing.T) {
	const (
		openaiURL = "https://api.openai.test/v1"
		ollamaURL = "http://gpu-box:11434/v1"
		adhocURL  = "http://adhoc.test/v1"
	)
	providers := map[string]config.Provider{
		"openai": {Options: config.ProviderOptions{BaseURL: stringPtr(openaiURL)}, Models: map[string]config.Model{"gpt": {}}},
		"ollama": {Options: config.ProviderOptions{BaseURL: stringPtr(ollamaURL)}, Models: map[string]config.Model{"llama3": {}}},
		// Only a hand-written config has a provider without a baseURL.
		"bare": {Models: map[string]config.Model{"x": {}}},
	}
	creds := config.Credentials{Provider: map[string]config.ProviderCredential{
		"openai": {APIKey: "stored-openai"},
		"bare":   {APIKey: "stored-bare"},
	}}
	envKey := map[string]string{"OPENAI_API_KEY": "env-key"}
	withEnv := func(extra map[string]string) map[string]string {
		env := map[string]string{"OPENAI_API_KEY": "env-key"}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	allowAll := &config.ModelRule{Allow: []string{"*"}}
	for _, tc := range []struct {
		name        string
		activeModel string
		flags       runtimeFlagValues
		env         map[string]string
		rule        *config.ModelRule
		wantURL     string
		wantKey     string
		wantErr     bool
	}{
		{name: "active provider ignores env key", activeModel: "openai/gpt", env: envKey, wantURL: openaiURL, wantKey: "stored-openai"},
		{name: "keyless active provider gets no env key", activeModel: "ollama/llama3", env: envKey, wantURL: ollamaURL, wantKey: config.NoAPIKey},
		{name: "keyless --model provider gets no key", activeModel: "openai/gpt", flags: runtimeFlagValues{Model: "ollama/llama3"}, env: envKey, wantURL: ollamaURL, wantKey: config.NoAPIKey},
		{name: "plain --model keeps provider endpoint and key", activeModel: "openai/gpt", flags: runtimeFlagValues{Model: "gpt-other"}, env: envKey, wantURL: openaiURL, wantKey: "stored-openai"},
		{name: "--base-url is ad hoc", activeModel: "openai/gpt", flags: runtimeFlagValues{BaseURL: adhocURL, Model: "m"}, env: envKey, wantURL: adhocURL, wantKey: "env-key"},
		{name: "OPENAI_API_BASE_URL is ad hoc", activeModel: "openai/gpt", env: withEnv(map[string]string{"OPENAI_API_BASE_URL": adhocURL}), wantURL: adhocURL, wantKey: "env-key"},
		{name: "no config uses env key", env: withEnv(map[string]string{"OPENAI_API_BASE_URL": adhocURL, "OPENAI_API_MODEL": "m"}), wantURL: adhocURL, wantKey: "env-key"},
		{name: "--api-key beats provider key", activeModel: "openai/gpt", flags: runtimeFlagValues{APIKey: "flag-key"}, env: envKey, wantURL: openaiURL, wantKey: "flag-key"},
		{name: "--api-key follows --model provider", activeModel: "openai/gpt", flags: runtimeFlagValues{Model: "ollama/llama3", APIKey: "flag-key"}, env: envKey, wantURL: ollamaURL, wantKey: "flag-key"},
		{name: "--model provider without baseURL keeps no previous host", activeModel: "openai/gpt", flags: runtimeFlagValues{Model: "bare/x"}, env: envKey, wantErr: true},
		{name: "--model provider without baseURL plus --base-url is ad hoc", activeModel: "openai/gpt", flags: runtimeFlagValues{Model: "bare/x", BaseURL: adhocURL}, env: envKey, wantURL: adhocURL, wantKey: "env-key"},
		{name: "rule maps env model onto keyless provider", activeModel: "openai/gpt", env: withEnv(map[string]string{"OPENAI_API_MODEL": "ollama/llama3"}), rule: allowAll, wantURL: ollamaURL, wantKey: config.NoAPIKey},
		{name: "rule keeps --api-key on mapped provider", activeModel: "openai/gpt", flags: runtimeFlagValues{APIKey: "flag-key"}, env: withEnv(map[string]string{"OPENAI_API_MODEL": "ollama/llama3"}), rule: allowAll, wantURL: ollamaURL, wantKey: "flag-key"},
		{name: "rule fallback uses the fallback provider's key", activeModel: "openai/gpt", flags: runtimeFlagValues{APIKey: "flag-key"}, rule: &config.ModelRule{Allow: []string{"ollama/*"}}, wantURL: ollamaURL, wantKey: config.NoAPIKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{ActiveModel: tc.activeModel, Provider: providers}.WithCredentials(creds)
			settings, err := resolveRuntimeSettings(cfg, tc.flags, mapGetenv(tc.env))
			if err == nil {
				policy := config.ModelPolicy{Workspace: t.TempDir(), Rule: tc.rule}
				settings, _, err = enforceModelSettings(cfg, policy, settings, nil)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got (%q, %q), want an error", settings.BaseURL, settings.APIKey)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if settings.BaseURL != tc.wantURL || settings.APIKey != tc.wantKey {
				t.Fatalf("got (%q, %q), want (%q, %q)", settings.BaseURL, settings.APIKey, tc.wantURL, tc.wantKey)
			}
		})
	}
}

func TestApplyExplicitSessionRuntimeOverridesKeepsRestoredProviderKey(t *testing.T) {
	selected := runtimeSettings{APIKey: "env-key", BaseURL: "http://adhoc.test/v1"}
	restored := runtimeSettings{APIKey: "stored-key", BaseURL: "https://provider.test/v1"}
	env := mapGetenv(map[string]string{"OPENAI_API_KEY": "env-key"})

	got := restored
	applyExplicitSessionRuntimeOverrides(&got, selected, baseFlagValues{}, env)
	if got.APIKey != "stored-key" {
		t.Fatalf("APIKey = %q, want restored provider key despite OPENAI_API_KEY", got.APIKey)
	}
	got = restored
	applyExplicitSessionRuntimeOverrides(&got, selected, baseFlagValues{BaseURL: "http://adhoc.test/v1"}, env)
	if got.APIKey != "env-key" || got.BaseURL != "http://adhoc.test/v1" {
		t.Fatalf("ad hoc override = (%q, %q), want env key and flag URL", got.APIKey, got.BaseURL)
	}
	got = restored
	applyExplicitSessionRuntimeOverrides(&got, runtimeSettings{APIKey: "flag-key"}, baseFlagValues{APIKey: "flag-key"}, env)
	if got.APIKey != "flag-key" || got.BaseURL != "https://provider.test/v1" {
		t.Fatalf("--api-key override = (%q, %q), want flag key on provider URL", got.APIKey, got.BaseURL)
	}
}

func TestResolveRuntimeSettings_EnvironmentModelBypassesInvalidActiveModel(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "local/missing",
		Provider:    map[string]config.Provider{"local": {Models: map[string]config.Model{}}},
	}
	env := map[string]string{
		"OPENAI_API_MODEL":    "env-model",
		"OPENAI_API_BASE_URL": "https://env.test/v1",
	}

	settings, err := resolveRuntimeSettings(cfg, runtimeFlagValues{}, mapGetenv(env))
	if err != nil {
		t.Fatalf("resolveRuntimeSettings() error = %v", err)
	}
	if settings.Model != "env-model" || settings.BaseURL != "https://env.test/v1" {
		t.Fatalf("settings = %+v, want environment model and URL", settings)
	}
}

func TestResolveRuntimeSettings_NoModelReturnsActionableError(t *testing.T) {
	_, err := resolveRuntimeSettings(config.Config{}, runtimeFlagValues{}, mapGetenv(nil))
	if err == nil || !strings.Contains(err.Error(), "set --model, OPENAI_API_MODEL, or activeModel") {
		t.Fatalf("resolveRuntimeSettings() error = %v, want model selection guidance", err)
	}
}

func TestResolveRuntimeSettings_OutputPrecedenceAndDefault(t *testing.T) {
	base := config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: stringPtr("http://localhost:8000/v1")},
				Models:  map[string]config.Model{"qwen": {}},
			},
		},
	}

	settings, err := resolveRuntimeSettings(base, runtimeFlagValues{}, mapGetenv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if settings.OutputMode != output.ModeCompact {
		t.Fatalf("default OutputMode = %q, want compact", settings.OutputMode)
	}

	base.Runtime.Output.Mode = "detailed"
	settings, err = resolveRuntimeSettings(base, runtimeFlagValues{}, mapGetenv(map[string]string{"CARETZ_OUTPUT": "compact"}))
	if err != nil {
		t.Fatal(err)
	}
	if settings.OutputMode != output.ModeCompact {
		t.Fatalf("environment OutputMode = %q, want compact", settings.OutputMode)
	}

	settings, err = resolveRuntimeSettings(base, runtimeFlagValues{Output: "detailed"}, mapGetenv(map[string]string{"CARETZ_OUTPUT": "compact"}))
	if err != nil {
		t.Fatal(err)
	}
	if settings.OutputMode != output.ModeDetailed {
		t.Fatalf("flag OutputMode = %q, want detailed", settings.OutputMode)
	}

	if settings.Theme != output.ThemeANSI {
		t.Fatalf("default Theme = %q, want ansi", settings.Theme)
	}
	base.Runtime.Output.Theme = "dracula"
	settings, err = resolveRuntimeSettings(base, runtimeFlagValues{}, mapGetenv(nil))
	if err != nil || settings.Theme != "dracula" {
		t.Fatalf("config Theme = %q, %v; want dracula", settings.Theme, err)
	}
	settings, err = resolveRuntimeSettings(base, runtimeFlagValues{}, mapGetenv(map[string]string{"CARETZ_THEME": "Tokyo-Night"}))
	if err != nil || settings.Theme != "tokyo-night" {
		t.Fatalf("environment Theme = %q, %v; want tokyo-night", settings.Theme, err)
	}
	settings, err = resolveRuntimeSettings(base, runtimeFlagValues{Theme: "github-dark"}, mapGetenv(map[string]string{"CARETZ_THEME": "tokyo-night"}))
	if err != nil || settings.Theme != "github-dark" {
		t.Fatalf("flag Theme = %q, %v; want github-dark", settings.Theme, err)
	}
	if _, err := resolveRuntimeSettings(base, runtimeFlagValues{}, mapGetenv(map[string]string{"CARETZ_THEME": "bogus"})); err == nil || !strings.Contains(err.Error(), "catppuccin-mocha") {
		t.Fatalf("invalid CARETZ_THEME error = %v, want one listing themes", err)
	}
}

func TestResolveRuntimeSettings_InvalidOutputFails(t *testing.T) {
	cfg := config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: stringPtr("http://localhost:8000/v1")},
				Models:  map[string]config.Model{"qwen": {}},
			},
		},
	}
	_, err := resolveRuntimeSettings(cfg, runtimeFlagValues{Output: "json"}, mapGetenv(nil))
	if err == nil || !strings.Contains(err.Error(), "compact or detailed") {
		t.Fatalf("resolveRuntimeSettings() error = %v, want output mode guidance", err)
	}
}

func TestResolveRuntimeSettings_ExplicitFlagsOverrideModelRef(t *testing.T) {
	cfg := config.Config{
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{
					BaseURL: stringPtr("http://localhost:8000/v1"),
				},
				Models: map[string]config.Model{
					"qwen": {
						Model:     "qwen-configured",
						Reasoning: "medium",
						Limit: config.Limit{
							Context:          intPtrMain(32768),
							ReservedTokens:   intPtrMain(4096),
							KeepRecentTokens: intPtrMain(8192),
							SummaryTokens:    intPtrMain(3000),
						},
					},
				},
			},
		},
	}

	settings, err := resolveRuntimeSettings(cfg, runtimeFlagValues{
		Model:            "local/qwen",
		APIKey:           "flag-key",
		BaseURL:          "http://flag.test/v1",
		ReasoningEffort:  "xhigh",
		ContextWindow:    65536,
		ReservedTokens:   5000,
		KeepRecentTokens: 6000,
		SummaryTokens:    2500,
	}, mapGetenv(nil))
	if err != nil {
		t.Fatalf("resolveRuntimeSettings() error = %v", err)
	}

	if settings.APIKey != "flag-key" {
		t.Fatalf("APIKey = %q, want flag API key", settings.APIKey)
	}
	if settings.BaseURL != "http://flag.test/v1" {
		t.Fatalf("BaseURL = %q, want flag base URL", settings.BaseURL)
	}
	if settings.Model != "qwen-configured" {
		t.Fatalf("Model = %q, want resolved config ref model", settings.Model)
	}
	if settings.ReasoningEffort != "xhigh" {
		t.Fatalf("ReasoningEffort = %q, want flag reasoning effort", settings.ReasoningEffort)
	}
	if settings.ContextWindow != 65536 {
		t.Fatalf("ContextWindow = %d, want flag context window", settings.ContextWindow)
	}
	if settings.ReservedTokens != 5000 {
		t.Fatalf("ReservedTokens = %d, want flag reserved tokens", settings.ReservedTokens)
	}
	if settings.KeepRecentTokens != 6000 {
		t.Fatalf("KeepRecentTokens = %d, want flag keep recent tokens", settings.KeepRecentTokens)
	}
	if settings.SummaryTokens != 2500 {
		t.Fatalf("SummaryTokens = %d, want flag summary tokens", settings.SummaryTokens)
	}
}

func TestDefaultSummaryTokensMain(t *testing.T) {
	tests := []struct {
		keepRecent int
		want       int
	}{
		{keepRecent: 1000, want: 2000},
		{keepRecent: 8192, want: 4096},
		{keepRecent: 30000, want: 6000},
	}
	for _, tt := range tests {
		if got := defaultSummaryTokens(tt.keepRecent); got != tt.want {
			t.Fatalf("defaultSummaryTokens(%d) = %d, want %d", tt.keepRecent, got, tt.want)
		}
	}
}

func TestModelSummaryNamesModelSourceAndEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		settings runtimeSettings
		source   string
		want     string
	}{
		{
			name:     "configured provider model",
			settings: runtimeSettings{ActiveModelRef: "openai/gpt-5", Model: "gpt-5", BaseURL: "https://api.openai.com/v1", Protocol: "responses", ReasoningEffort: "high", ContextWindow: 400000},
			source:   "--model",
			want:     "model openai/gpt-5 (--model) endpoint=https://api.openai.com/v1 protocol=responses reasoning=high ctx=400000",
		},
		{
			name:     "alias resolving to another id",
			settings: runtimeSettings{ActiveModelRef: "local/fast", Model: "qwen3-8b", BaseURL: "http://127.0.0.1:8080/v1"},
			source:   "config activeModel",
			want:     "model local/fast (config activeModel) id=qwen3-8b endpoint=http://127.0.0.1:8080/v1",
		},
		{
			name:     "ad hoc model without endpoint",
			settings: runtimeSettings{Model: "mock"},
			want:     "model mock endpoint=default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelSummary(tt.settings, tt.source); got != tt.want {
				t.Fatalf("modelSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModelSourcePrefersExplicitThenResumedThenConfig(t *testing.T) {
	configured := runtimeSettings{ActiveModelRef: "openai/gpt-5"}
	if got := modelSource("--model", true, configured); got != "--model" {
		t.Fatalf("explicit source = %q", got)
	}
	if got := modelSource("", true, configured); got != "resumed session" {
		t.Fatalf("resumed source = %q", got)
	}
	if got := modelSource("", false, configured); got != "config activeModel" {
		t.Fatalf("config source = %q", got)
	}
	if got := modelSource("", false, runtimeSettings{Model: "mock"}); got != "default" {
		t.Fatalf("default source = %q", got)
	}
}

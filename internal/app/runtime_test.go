package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/skills"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
)

func TestRuntimeSessionStorageAccess(t *testing.T) {
	for _, mode := range []string{"default", "local-flag", "external-flag", "external-env"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CARETZ_CONFIG_ROOT", "")
			workspace, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(workspace, ".caretz")
			args := []string{"--workspace", workspace, "--base-url", "http://localhost:8000/v1", "--model", "test-model"}
			if mode == "local-flag" {
				root = filepath.Join(workspace, "history")
			}
			if strings.HasPrefix(mode, "external") {
				root, err = filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "external-env" {
				t.Setenv("CARETZ_CONFIG_ROOT", root)
			} else if mode != "default" {
				args = append(args, "--config-root", root)
			}
			var stderr strings.Builder
			rt, code := BuildRuntime(t.Context(), args, &stderr)
			if code != 0 {
				t.Fatalf("runtime: %d %s", code, stderr.String())
			}
			history := filepath.Join(root, "sessions")
			private := strings.HasPrefix(mode, "external")
			if slices.Contains(rt.ToolRegistry.HiddenPaths, history) != private || slices.Contains(rt.ToolRegistry.ReadOnlyPaths, history) == private {
				t.Fatalf("history policy: hidden=%v readonly=%v", rt.ToolRegistry.HiddenPaths, rt.ToolRegistry.ReadOnlyPaths)
			}
			if !slices.Contains(rt.ToolRegistry.HiddenPaths, filepath.Join(root, "config.json")) {
				t.Fatal("loaded config is not hidden")
			}
			if !slices.Contains(rt.ToolRegistry.HiddenPaths, filepath.Join(os.Getenv("HOME"), ".config", "caretz", "credentials.json")) {
				t.Fatalf("credentials file is not hidden: %v", rt.ToolRegistry.HiddenPaths)
			}
			if !slices.Contains(rt.ToolRegistry.ReadOnlyPaths, filepath.Join(workspace, ".caretz", "sessions")) {
				t.Fatal("default workspace history is not readable/read-only")
			}
			server := &acpServer{rt: rt}
			for _, mode := range []string{acpModeBuild, acpModePlan} {
				registry := server.acpToolRegistryForMode(root, mode)
				if slices.Contains(registry.HiddenPaths, history) || !slices.Contains(registry.ReadOnlyPaths, history) {
					t.Fatalf("ACP workspace switch did not expose local history in %s mode", mode)
				}
			}
		})
	}
}

func TestBuildRuntimeCustomSystemPromptAndToolFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()

	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--system-prompt", "custom cli prompt",
		"--tools", "read,WRITE,ls,grep",
		"--disable-tool", "wRiTe",
	}, &stderr)
	if code != 0 {
		t.Fatalf("BuildRuntime() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	for _, want := range []string{
		"Working directory:",
		"custom cli prompt",
		"## Available Tools",
		"Read:",
		"Grep:",
		"Ls:",
	} {
		if !strings.Contains(rt.SystemPrompt, want) {
			t.Fatalf("SystemPrompt missing %q:\n%s", want, rt.SystemPrompt)
		}
	}
	for _, unwanted := range []string{skills.BaseSystemPrompt, "Write:", "Bash:", "CallSubagent:"} {
		if strings.Contains(rt.SystemPrompt, unwanted) {
			t.Fatalf("SystemPrompt should not contain %q:\n%s", unwanted, rt.SystemPrompt)
		}
	}
	defs := rt.ToolRegistry.ToolDefinitions()
	if fmt.Sprint(toolDefinitionNamesMain(defs)) != fmt.Sprint([]string{"Read", "Grep", "Ls"}) {
		t.Fatalf("tool definitions = %v, want [Read Grep Ls]", toolDefinitionNamesMain(defs))
	}
	if rt.ToolRegistry.IsToolEnabled("Write") {
		t.Fatal("Write should be disabled by --disable-tool")
	}
}

func TestBuildRuntimeDarwinPromptExplainsSandboxTemporaryDirectory(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific system prompt guidance")
	}
	t.Setenv("HOME", t.TempDir())
	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", t.TempDir(),
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--tools", "bash",
	}, &stderr)
	if code != 0 {
		t.Fatalf("BuildRuntime() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(rt.SystemPrompt, "literal /tmp is not writable inside the sandbox; use $TMPDIR") {
		t.Fatalf("SystemPrompt missing macOS temporary-directory guidance:\n%s", rt.SystemPrompt)
	}
}

func TestBuildRuntimePromptExplainsSOCKSTCPAndSSH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", t.TempDir(),
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--tools", "bash",
	}, &stderr)
	if code != 0 {
		t.Fatalf("BuildRuntime() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"tcp://HOST:PORT", "CARETZ_SOCKS_PROXY", "CARETZ_SSH_PROXY_COMMAND", "Git-over-SSH"} {
		if !strings.Contains(rt.SystemPrompt, want) {
			t.Fatalf("SystemPrompt missing %q:\n%s", want, rt.SystemPrompt)
		}
	}
}

func TestBuildRuntimeWithSystemPromptSuffix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configRoot := t.TempDir()
	var stderr strings.Builder
	rt, code := BuildRuntimeWithOptions(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--system-prompt", "operator prompt",
	}, &stderr, Options{SystemPromptSuffix: "  Telegram chat guidance  "})
	if code != 0 {
		t.Fatalf("BuildRuntimeWithOptions() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if rt.SystemPromptBase != "operator prompt\n\nTelegram chat guidance" {
		t.Fatalf("SystemPromptBase = %q", rt.SystemPromptBase)
	}
	for _, want := range []string{"operator prompt\n\nTelegram chat guidance", "## Available Tools", "Working directory:"} {
		if !strings.Contains(rt.SystemPrompt, want) {
			t.Fatalf("SystemPrompt missing %q:\n%s", want, rt.SystemPrompt)
		}
	}
}

func TestBuildRuntimeWithSystemPromptSuffixAndEmptyBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stderr strings.Builder
	rt, code := BuildRuntimeWithOptions(context.Background(), []string{
		"--config-root", t.TempDir(),
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--system-prompt", "",
	}, &stderr, Options{SystemPromptSuffix: "Telegram chat guidance"})
	if code != 0 {
		t.Fatalf("BuildRuntimeWithOptions() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if rt.SystemPromptBase != skills.BaseSystemPrompt+"\n\nTelegram chat guidance" {
		t.Fatalf("SystemPromptBase = %q", rt.SystemPromptBase)
	}
}

func TestBuildRuntimeExplicitAdHocModelOverridesInvalidActiveModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	configJSON := `{
  "activeModel": "opencodego/missing-active",
  "provider": {
    "opencodego": {
      "options": {"baseURL": "https://opencode.ai/zen/go/v1"},
      "models": {"deepseek-v4-flash": {}}
    }
  }
}`
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--no-model-discovery",
		"--model", "opencodego/glm-5.3-flash",
	}, &stderr)
	if code != 0 {
		t.Fatalf("BuildRuntime() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if rt.Settings.Model != "glm-5.3-flash" || rt.Settings.ActiveModelRef != "opencodego/glm-5.3-flash" {
		t.Fatalf("runtime settings = %+v, want explicit ad-hoc provider model", rt.Settings)
	}
}

func TestBuildRuntimeAppliesProfileAndExplicitFalseOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	configJSON := `{
  "profiles": {
    "general": {"plan": true, "incognito": true, "tools": ["Read"], "output": {"mode": "detailed"}, "request": {"timeout": "9s", "maxAttempts": 7}},
    "focused": {"tools": ["Grep"], "tasks": {"enabled": false}}
  },
  "profile": "focused"
}`
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot, "--base-url", "http://localhost:8000/v1", "--model", "test-model",
		"--plan=false", "--incognito=false", "--tools", "Read,Bash", "--output", "compact",
	}, &stderr)
	if code != 0 {
		t.Fatalf("BuildRuntime() code = %d, stderr = %s", code, stderr.String())
	}
	if rt.PlanMode || rt.Incognito {
		t.Fatalf("explicit false lost: plan=%v incognito=%v", rt.PlanMode, rt.Incognito)
	}
	if rt.Settings.OutputMode != output.ModeCompact {
		t.Fatalf("output = %v", rt.Settings.OutputMode)
	}
	if rt.Settings.LLMTimeout != 9*time.Second || rt.Settings.LLMMaxAttempts != 7 {
		t.Fatalf("request policy = %v/%d", rt.Settings.LLMTimeout, rt.Settings.LLMMaxAttempts)
	}
	if rt.ToolRegistry.IsToolEnabled("Grep") || !rt.ToolRegistry.IsToolEnabled("Read") || !rt.ToolRegistry.IsToolEnabled("Bash") {
		t.Fatalf("CLI tool replacement not enforced")
	}
	if !rt.DisableTasks {
		t.Fatal("profile tasks=false not applied")
	}
}

func TestBuildRuntimeProfilePrompts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Profile resolution should not depend on whether the host running the test
	// has the platform sandbox executable (for example, bwrap on Linux).
	t.Setenv(disableSandboxEnv, "1")
	configRoot := t.TempDir()
	args := []string{"--config-root", configRoot, "--base-url", "http://localhost:8000/v1", "--model", "test-model"}
	var stderr strings.Builder
	general, code := BuildRuntime(context.Background(), args, &stderr)
	if code != 0 {
		t.Fatalf("general BuildRuntime() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(general.SystemPrompt, skills.GeneralSystemPrompt) || strings.Contains(general.SystemPrompt, skills.BaseSystemPrompt) {
		t.Fatalf("general prompt = %q", general.SystemPrompt)
	}
	if !general.SandboxSettings.SessionExec {
		t.Fatalf("general sandbox = %+v, want session execution by default", general.SandboxSettings)
	}
	if !strings.Contains(general.SystemPrompt, "## TL;DR") {
		t.Fatalf("general prompt missing TL;DR guidance: %q", general.SystemPrompt)
	}
	stderr.Reset()
	coding, code := BuildRuntime(context.Background(), append(args, "--profile", "coding"), &stderr)
	if code != 0 {
		t.Fatalf("coding BuildRuntime() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(coding.SystemPrompt, skills.BaseSystemPrompt) {
		t.Fatalf("coding prompt = %q", coding.SystemPrompt)
	}
	if !coding.SandboxSettings.SessionExec || len(coding.SandboxSettings.NetworkAllowlist) == 0 {
		t.Fatalf("coding sandbox = %+v, want session execution and development network defaults", coding.SandboxSettings)
	}
	stderr.Reset()
	compact, code := BuildRuntime(context.Background(), append(args, "--profile", "compact"), &stderr)
	if code != 0 {
		t.Fatalf("compact BuildRuntime() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(compact.SystemPrompt, skills.CompactSystemPrompt) || strings.Contains(compact.SystemPrompt, skills.GeneralSystemPrompt) {
		t.Fatalf("compact prompt = %q", compact.SystemPrompt)
	}
	for _, want := range []string{"Omit unnecessary details", "## TL;DR", "main answer"} {
		if !strings.Contains(compact.SystemPrompt, want) {
			t.Fatalf("compact prompt missing %q: %q", want, compact.SystemPrompt)
		}
	}
	if got := fmt.Sprint(toolDefinitionNamesMain(compact.ToolRegistry.ToolDefinitions())); got != "[Bash]" {
		t.Fatalf("compact tools = %s, want [Bash]", got)
	}
	if !compact.DisableTasks || len(compact.StartupResources.Skills) != 0 || len(compact.StartupResources.Agents) != 0 {
		t.Fatalf("compact resources = %+v, disableTasks=%v", compact.StartupResources, compact.DisableTasks)
	}
}

func TestBuildRuntimeInvalidActiveModelFailsGracefully(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	configJSON := `{
  "activeModel": "local/missing",
  "provider": {"local": {"options": {"baseURL": "http://localhost:8000/v1"}, "models": {}}}
}`
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--no-model-discovery",
	}, &stderr)
	if code != 1 || rt != nil {
		t.Fatalf("BuildRuntime() = (%v, %d), want (nil, 1)", rt, code)
	}
	if !strings.Contains(stderr.String(), `error: failed to resolve config: active model "local/missing" references unknown model "missing"`) {
		t.Fatalf("stderr = %q, want graceful active-model error", stderr.String())
	}
}

func TestBuildRuntimeWithOptionsRegistersCustomToolsBeforeSystemPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()

	var stderr strings.Builder
	rt, code := BuildRuntimeWithOptions(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
	}, &stderr, Options{
		Tools: []tools.Tool{{
			Definition: llm.ToolDefinition{
				Name:        "DeployPreview",
				Description: "Create a deploy preview.",
				Parameters:  map[string]any{"type": "object"},
			},
			Handler: func(ctx context.Context, call llm.ToolCall) (string, error) {
				return "ok", nil
			},
		}},
	})
	if code != 0 {
		t.Fatalf("BuildRuntimeWithOptions() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !hasToolDefinitionMain(rt.ToolRegistry.ToolDefinitions(), "DeployPreview") {
		t.Fatal("ToolRegistry missing custom tool")
	}
	if !strings.Contains(rt.SystemPrompt, "DeployPreview:") {
		t.Fatalf("SystemPrompt missing custom tool:\n%s", rt.SystemPrompt)
	}
}

func TestBuildRuntimeEnablesLocalImageFromToolsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	configPath := filepath.Join(configRoot, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"profiles": {"general": {"toolConfig": {
			"localImage": {
				"endpoint": "http://localhost:8002/v1/chat/completions",
				"model": "llava",
				"maxTimeSeconds": 30
			}
		}}}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
	}, &stderr)
	if code != 0 {
		t.Fatalf("BuildRuntime() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !hasToolDefinitionMain(rt.ToolRegistry.ToolDefinitions(), "LocalImage") {
		t.Fatal("ToolRegistry missing LocalImage")
	}
	if rt.ToolRegistry.ToolConfig["localImage"]["endpoint"] != "http://localhost:8002/v1/chat/completions" {
		t.Fatalf("LocalImage endpoint = %q", rt.ToolRegistry.ToolConfig["localImage"]["endpoint"])
	}
}

func TestBuildRuntimeLocalImageUsesConfiguredProviderModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	t.Setenv(config.CredentialsFileEnv, filepath.Join(configRoot, "credentials.json"))
	write := func(localImageModel string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(`{
			"provider": {"vision": {
				"options": {"baseURL": "http://127.0.0.1:8002/v1"},
				"models": {"qwen-vl": {"model": "qwen2.5-vl-7b"}}
			}},
			"profiles": {"general": {"toolConfig": {"localImage": {"model": "`+localImageModel+`"}}}}
		}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	build := func() (*Runtime, int, string) {
		var stderr strings.Builder
		rt, code := BuildRuntime(context.Background(), []string{
			"--config-root", configRoot,
			"--base-url", "http://localhost:8000/v1",
			"--model", "test-model",
		}, &stderr)
		return rt, code, stderr.String()
	}

	write("vision/qwen-vl")
	if err := config.SaveProviderCredential(filepath.Join(configRoot, "credentials.json"), "vision", "vl-key"); err != nil {
		t.Fatal(err)
	}
	rt, code, stderr := build()
	if code != 0 {
		t.Fatalf("BuildRuntime() exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if !hasToolDefinitionMain(rt.ToolRegistry.ToolDefinitions(), "LocalImage") {
		t.Fatal("ToolRegistry missing LocalImage")
	}
	resolution, err := subagentModelResolver{fileConfig: rt.FileConfig}.ResolveSubagentModel(context.Background(), agent.SubagentModelRequest{Ref: "vision/qwen-vl", ValidateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.BaseURL != "http://127.0.0.1:8002/v1" || resolution.APIKey != "vl-key" || resolution.Model != "qwen2.5-vl-7b" {
		t.Fatalf("resolution = %+v, want the vision provider's endpoint, stored key and model ID", resolution)
	}

	write("vision/missing")
	if _, code, stderr := build(); code == 0 || !strings.Contains(stderr, `tools.localImage.model "vision/missing" is not a configured provider/model`) {
		t.Fatalf("unknown model: code = %d, stderr = %q", code, stderr)
	}
}

func TestBuildRuntimeRejectsInvalidToolConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	configPath := filepath.Join(configRoot, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"profiles": {"general": {"toolConfig": {
			"localImage": {
				"maxTimeSeconds": -1
			}
		}}}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	_, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
	}, &stderr)
	if code != 2 {
		t.Fatalf("BuildRuntime() exit code = %d, want 2; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid tool config: tools.localImage.maxTimeSeconds must be positive") {
		t.Fatalf("stderr = %q, want tool config validation error", stderr.String())
	}
}

func TestBuildRuntimeValidatesCustomToolConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	configPath := filepath.Join(configRoot, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"profiles": {"general": {"toolConfig": {
			"DeployPreview": {
				"environment": ""
			}
		}}}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	_, code := BuildRuntimeWithOptions(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
	}, &stderr, Options{
		Tools: []tools.Tool{{
			Definition: llm.ToolDefinition{Name: "DeployPreview"},
			Handler:    func(ctx context.Context, call llm.ToolCall) (string, error) { return "ok", nil },
			ValidateConfig: func(config map[string]any) error {
				if strings.TrimSpace(fmt.Sprint(config["environment"])) == "" {
					return fmt.Errorf("environment is required")
				}
				return nil
			},
		}},
	})
	if code != 2 {
		t.Fatalf("BuildRuntimeWithOptions() exit code = %d, want 2; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid tool config: tools.DeployPreview: environment is required") {
		t.Fatalf("stderr = %q, want custom tool config validation error", stderr.String())
	}
}

func TestBuildRuntimeSandboxDisabledUpdatesBashPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(disableSandboxEnv, "1")
	configRoot := t.TempDir()

	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--tools", "bash",
	}, &stderr)
	if code != 0 {
		t.Fatalf("BuildRuntime() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(rt.SystemPrompt, "**Bash** runs without CARETZ's internal sandbox") {
		t.Fatalf("SystemPrompt missing unsandboxed Bash wording:\n%s", rt.SystemPrompt)
	}
	if strings.Contains(rt.SystemPrompt, "**Bash** runs in a sandbox") {
		t.Fatalf("SystemPrompt still says Bash is sandboxed:\n%s", rt.SystemPrompt)
	}
	defs := rt.ToolRegistry.ToolDefinitions()
	if len(defs) != 1 || defs[0].Name != "Bash" {
		t.Fatalf("tool definitions = %v, want only Bash", toolDefinitionNamesMain(defs))
	}
	if !strings.Contains(defs[0].Description, "without CARETZ's internal sandbox") {
		t.Fatalf("Bash description = %q, want unsandboxed wording", defs[0].Description)
	}
}

func TestBuildRuntimeCustomSystemPromptFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()
	promptPath := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptPath, []byte("file system prompt\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	rt, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--system-prompt-file", promptPath,
	}, &stderr)
	if code != 0 {
		t.Fatalf("BuildRuntime() exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	for _, want := range []string{
		"Working directory:",
		"file system prompt",
		"## Available Tools",
	} {
		if !strings.Contains(rt.SystemPrompt, want) {
			t.Fatalf("SystemPrompt missing %q:\n%s", want, rt.SystemPrompt)
		}
	}
	if strings.Contains(rt.SystemPrompt, skills.BaseSystemPrompt) {
		t.Fatalf("SystemPrompt should not include default base prompt when custom prompt file is supplied:\n%s", rt.SystemPrompt)
	}
}

func TestBuildRuntimeRejectsSystemPromptFlagConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()

	var stderr strings.Builder
	_, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--system-prompt", "inline",
		"--system-prompt-file", filepath.Join(t.TempDir(), "missing.md"),
	}, &stderr)
	if code != 2 {
		t.Fatalf("BuildRuntime() exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--system-prompt and --system-prompt-file cannot be combined") {
		t.Fatalf("stderr = %q, want conflict", stderr.String())
	}
}

func TestBuildRuntimeRejectsRemovedModelConfigFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := t.TempDir()

	var stderr strings.Builder
	_, code := BuildRuntime(context.Background(), []string{
		"--config-root", configRoot,
		"--base-url", "http://localhost:8000/v1",
		"--model", "test-model",
		"--model-config", filepath.Join(t.TempDir(), "model.json"),
	}, &stderr)
	if code != 2 {
		t.Fatalf("BuildRuntime() exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined: -model-config") {
		t.Fatalf("stderr = %q, want unknown-flag error", stderr.String())
	}
}

func TestBuildRuntimeOpsProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(disableSandboxEnv, "1")
	args := []string{"--config-root", t.TempDir(), "--base-url", "http://localhost:8000/v1", "--model", "test-model", "--profile", "ops"}
	var stderr strings.Builder
	ops, code := BuildRuntime(context.Background(), args, &stderr)
	if code != 0 {
		t.Fatalf("ops BuildRuntime() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(ops.SystemPrompt, skills.OpsSystemPrompt) {
		t.Fatalf("ops prompt = %q", ops.SystemPrompt)
	}
	if !ops.SandboxSettings.AskSandboxOff || !ops.SandboxSettings.AllowSystemBus || !ops.SandboxSettings.AllowGPU || ops.SandboxSettings.SessionExec || ops.SandboxSettings.AllowDocker || !ops.SandboxSettings.UnrestrictedNetwork {
		t.Fatalf("ops sandbox = %+v", ops.SandboxSettings)
	}
	if !ops.ToolRegistry.AskSandboxOff {
		t.Fatal("ops registry does not escalate sandbox denials")
	}
	stderr.Reset()
	quiet, code := BuildRuntime(context.Background(), append(args, "--ask-sandbox-off=false"), &stderr)
	if code != 0 {
		t.Fatalf("ops override BuildRuntime() code = %d, stderr = %s", code, stderr.String())
	}
	if quiet.SandboxSettings.AskSandboxOff || quiet.ToolRegistry.AskSandboxOff {
		t.Fatal("--ask-sandbox-off=false did not override the ops profile")
	}
}

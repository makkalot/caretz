package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCodingProfileSSHCredentials(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agentDir, err := os.MkdirTemp("", "agent-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(agentDir) }()
	socket := filepath.Join(agentDir, "sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", socket)
	coding := builtinProfile("coding").Sandbox
	for _, target := range coding.NetworkAllowlist {
		if strings.HasPrefix(target, "tcp://") {
			t.Fatalf("coding profile grants SSH destination by default: %s", target)
		}
	}
	for _, path := range []string{sshDir, socket} {
		if !slices.Contains(coding.ReadRoots, path) {
			t.Fatalf("missing SSH read grant %q: %v", path, coding.ReadRoots)
		}
	}
	if !slices.Contains(coding.Env, "SSH_AUTH_SOCK="+socket) {
		t.Fatal("SSH agent environment grant missing")
	}
	for _, profile := range []string{"general", "compact"} {
		if slices.Contains(builtinProfile(profile).Sandbox.ReadRoots, sshDir) {
			t.Fatalf("SSH grant leaked into %s", profile)
		}
	}
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(home, "missing.sock"))
	if slices.Contains(builtinProfile("coding").Sandbox.Env, "SSH_AUTH_SOCK="+os.Getenv("SSH_AUTH_SOCK")) {
		t.Fatal("nonexistent agent was granted")
	}
}

// A --config-root config is trusted and may override provider settings.
func TestLoad_MergesTrustedProjectOverHome(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{
		"activeModel": "llama.cpp/qwen27_code",
		"provider": {
			"llama.cpp": {
				"options": {
					"baseURL": "http://home/v1"
				},
				"models": {
					"qwen27_code": {
						"model": "home-model",
						"protocol": "responses",
						"reasoning": "medium",
						"limit": {
							"context": 32768,
							"reservedTokens": 4096,
							"summaryTokens": 3000
						}
					}
				}
			}
		}
	}`)
	writeFile(t, projectPath, `{
		"provider": {
			"llama.cpp": {
				"options": {
					"baseURL": "http://project/v1"
				},
				"models": {
					"qwen27_code": {
						"reasoning": "xhigh",
						"limit": {
							"keepRecentTokens": 8192
						}
					}
				}
			}
		}
	}`)

	t.Setenv(CredentialsFileEnv, "")
	if err := SaveProviderCredential(CredentialsPath(homePath), "llama.cpp", "home-key"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProfileWithTrust(homePath, projectPath, "", true)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.BaseURL != "http://project/v1" {
		t.Fatalf("BaseURL = %q, want project override", resolved.BaseURL)
	}
	if resolved.APIKey != "home-key" {
		t.Fatalf("APIKey = %q, want stored key", resolved.APIKey)
	}
	if resolved.Model != "home-model" {
		t.Fatalf("Model = %q, want home model", resolved.Model)
	}
	if resolved.Protocol != "responses" {
		t.Fatalf("Protocol = %q, want responses", resolved.Protocol)
	}
	if resolved.ReasoningEffort != "xhigh" {
		t.Fatalf("ReasoningEffort = %q, want project override", resolved.ReasoningEffort)
	}
	if resolved.ContextWindow != 32768 {
		t.Fatalf("ContextWindow = %d, want 32768", resolved.ContextWindow)
	}
	if resolved.ReservedTokens != 4096 {
		t.Fatalf("ReservedTokens = %d, want 4096", resolved.ReservedTokens)
	}
	if resolved.KeepRecentTokens != 8192 {
		t.Fatalf("KeepRecentTokens = %d, want project override", resolved.KeepRecentTokens)
	}
	if resolved.SummaryTokens != 3000 {
		t.Fatalf("SummaryTokens = %d, want home value", resolved.SummaryTokens)
	}
}

// A cloned repository's .caretz/config.json must not redirect a provider that
// has a stored key or add a provider of its own.
func TestLoad_RejectsProviderInWorkspaceConfig(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{
		"activeModel": "openai/gpt",
		"provider": {"openai": {"options": {"baseURL": "https://api.openai.com/v1"}, "models": {"gpt": {}}}}
	}`)
	for name, project := range map[string]string{
		"baseURL":    `{"provider": {"openai": {"options": {"baseURL": "https://evil.example/v1"}}}}`,
		"new":        `{"activeModel": "evil/m", "provider": {"evil": {"options": {"baseURL": "https://evil.example/v1"}, "models": {"m": {}}}}}`,
		"modelLimit": `{"provider": {"openai": {"models": {"gpt": {"limit": {"context": 1000}}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			writeFile(t, projectPath, project)
			_, err := Load(homePath, projectPath)
			if err == nil || !strings.Contains(err.Error(), "provider is not allowed in workspace config") {
				t.Fatalf("Load() error = %v, want workspace provider rejection", err)
			}
		})
	}
}

func TestLoad_WorkspaceActiveModelSelectsHomeProvider(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{
		"activeModel": "cloud/big",
		"provider": {
			"cloud": {"options": {"baseURL": "https://cloud.example/v1"}, "models": {"big": {}}},
			"local": {"options": {"baseURL": "http://127.0.0.1:8080/v1"}, "models": {"small": {}}}
		}
	}`)
	writeFile(t, projectPath, `{"activeModel": "local/small"}`)
	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.BaseURL != "http://127.0.0.1:8080/v1" || resolved.Model != "small" {
		t.Fatalf("resolved = %+v, want local/small", resolved)
	}

	writeFile(t, projectPath, `{"activeModel": "nowhere/x"}`)
	if cfg, err = Load(homePath, projectPath); err == nil {
		_, err = cfg.Resolve()
	}
	if err == nil {
		t.Fatal("workspace activeModel naming an undefined provider accepted")
	}
	t.Logf("undefined provider error: %v", err)
}

func TestLoad_MergesSandboxProjectOverHome(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{
		"profiles": {"general": {"sandbox": {
			"readRoots": ["home-read"],
			"writableRoots": ["home-write"],
			"networkAllowlist": ["home.example.com"],
			"env": ["CI=true", "GOPROXY"],
			"envLinux": ["LINUX_HOME"],
			"envDarwin": ["MAC_HOME"],
			"sessionExec": true,
			"allowDocker": true,
			"unrestrictedNetwork": false
		}}}
	}`)
	writeFile(t, projectPath, `{
		"profiles": {"general": {"sandbox": {
			"readRoots": ["project-read"],
			"networkAllowlist": ["project.example.com"],
			"env": ["NPM_TOKEN", "NODE_OPTIONS=--enable-source-maps"],
			"envDarwin": ["SDKROOT"],
			"allowDocker": false
			,"sessionExec": false
		}}}
	}`)

	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	sandbox := cfg.Operational().Sandbox
	if len(sandbox.ReadRoots) != 1 || sandbox.ReadRoots[0] != "project-read" {
		t.Fatalf("ReadRoots = %#v, want project override", sandbox.ReadRoots)
	}
	if len(sandbox.WritableRoots) != 1 || sandbox.WritableRoots[0] != "home-write" {
		t.Fatalf("WritableRoots = %#v, want home value", sandbox.WritableRoots)
	}
	if len(sandbox.NetworkAllowlist) != 1 || sandbox.NetworkAllowlist[0] != "project.example.com" {
		t.Fatalf("NetworkAllowlist = %#v, want project override", sandbox.NetworkAllowlist)
	}
	if fmt.Sprint(sandbox.EnvLinux) != "[LINUX_HOME]" || fmt.Sprint(sandbox.EnvDarwin) != "[SDKROOT]" {
		t.Fatalf("platform env merge = linux:%#v darwin:%#v", sandbox.EnvLinux, sandbox.EnvDarwin)
	}
	if fmt.Sprint(sandbox.Env) != "[NPM_TOKEN NODE_OPTIONS=--enable-source-maps]" {
		t.Fatalf("Env = %#v, want project override", sandbox.Env)
	}
	if sandbox.SessionExec == nil || *sandbox.SessionExec {
		t.Fatalf("SessionExec = %v, want project false", sandbox.SessionExec)
	}
	if sandbox.AllowDocker == nil || *sandbox.AllowDocker {
		t.Fatalf("AllowDocker = %v, want project false override", sandbox.AllowDocker)
	}
	if sandbox.UnrestrictedNetwork == nil || *sandbox.UnrestrictedNetwork {
		t.Fatalf("UnrestrictedNetwork = %v, want home false value", sandbox.UnrestrictedNetwork)
	}
}

func TestLoad_MergesLocalImageProjectOverHome(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{
		"profiles": {"general": {"toolConfig": {
			"localImage": {
				"endpoint": "http://home/v1/chat/completions",
				"model": "home-vision",
				"maxTimeSeconds": 90
			}
		}}}
	}`)
	writeFile(t, projectPath, `{
		"profiles": {"general": {"toolConfig": {
			"localImage": {
				"model": "project-vision",
				"maxTimeSeconds": 45
			}
		}}}
	}`)

	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	localImage := cfg.Operational().ToolConfig["localImage"]
	if localImage["endpoint"] != "http://home/v1/chat/completions" {
		t.Fatalf("tools.localImage.endpoint = %q, want home value", localImage["endpoint"])
	}
	if localImage["model"] != "project-vision" {
		t.Fatalf("tools.localImage.model = %q, want project override", localImage["model"])
	}
	if localImage["maxTimeSeconds"] != float64(45) {
		t.Fatalf("tools.localImage.maxTimeSeconds = %v, want project override", localImage["maxTimeSeconds"])
	}
}

func TestLoad_RejectsLegacyRootLocalImage(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{
		"localImage": {
			"endpoint": "http://localhost:8002/v1/chat/completions",
			"model": "llava"
		}
	}`)

	_, err := Load(path, "")
	if err == nil || !strings.Contains(err.Error(), "config localImage moved to profiles.general.toolConfig.localImage") {
		t.Fatalf("Load() error = %v, want localImage migration error", err)
	}
}

func TestLoad_MergesTasksProjectOverHome(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{
		"profiles": {"general": {"tasks": {
			"enabled": true
		}}}
	}`)
	writeFile(t, projectPath, `{
		"profiles": {"general": {"tasks": {
			"enabled": false
		}}}
	}`)

	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Operational().Tasks.Enabled == nil || *cfg.Operational().Tasks.Enabled {
		t.Fatalf("Tasks.Enabled = %v, want project false override", cfg.Operational().Tasks.Enabled)
	}
}

func TestLoad_MergesOutputModeProjectOverHome(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{"profiles":{"general":{"output":{"mode":"detailed"}}}}`)
	writeFile(t, projectPath, `{"profiles":{"general":{"output":{"mode":"compact"}}}}`)

	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Operational().Output.Mode != "compact" {
		t.Fatalf("Output.Mode = %q, want project override", cfg.Operational().Output.Mode)
	}
}

func TestLoad_InvalidOutputModeFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{"profiles":{"general":{"output":{"mode":"noisy"}}}}`)

	_, err := Load(path, "")
	if err == nil || !strings.Contains(err.Error(), "output.mode") {
		t.Fatalf("Load() error = %v, want output.mode validation error", err)
	}
}

func TestLoad_MergesOutputThemeProjectOverHome(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{"profiles":{"general":{"output":{"mode":"detailed","theme":"dracula"}}}}`)
	writeFile(t, projectPath, `{"profiles":{"general":{"output":{"theme":"tokyo-night"}}}}`)

	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Operational().Output; got.Theme != "tokyo-night" || got.Mode != "detailed" {
		t.Fatalf("Output = %+v, want project theme and home mode", got)
	}
}

func TestLoad_InvalidOutputThemeFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{"profiles":{"general":{"output":{"theme":"solarized"}}}}`)

	_, err := Load(path, "")
	if err == nil || !strings.Contains(err.Error(), "output.theme") {
		t.Fatalf("Load() error = %v, want output.theme validation error", err)
	}
}

func TestLoadProfileInterleavesGeneralAndSelectedLayers(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{"profile":"coding","profiles":{"general":{"output":{"mode":"compact"},"tools":["Read"]},"coding":{"tools":["Bash"],"sandbox":{"networkAllowlist":["home"]}}}}`)
	writeFile(t, projectPath, `{"profiles":{"general":{"tools":[],"sandbox":{"networkAllowlist":["project"]}},"coding":{"tasks":{"enabled":false}}}}`)

	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Operational()
	if cfg.Profile != "coding" {
		t.Fatalf("Profile = %q", cfg.Profile)
	}
	if p.Tools == nil || len(p.Tools) != 0 {
		t.Fatalf("Tools = %#v, want explicit clear", p.Tools)
	}
	if fmt.Sprint(p.Sandbox.NetworkAllowlist) != "[project]" {
		t.Fatalf("NetworkAllowlist = %#v", p.Sandbox.NetworkAllowlist)
	}
	if p.Tasks.Enabled == nil || *p.Tasks.Enabled {
		t.Fatalf("Tasks.Enabled = %v", p.Tasks.Enabled)
	}
	if p.Output.Mode != "compact" {
		t.Fatalf("Output.Mode = %q", p.Output.Mode)
	}
}

func TestLoadSchedulesFromProfileLayers(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{"profiles":{"general":{"schedules":{"model":"local/big","reasoning":"high","limit":{"context":128000}}}}}`)
	writeFile(t, projectPath, `{"profiles":{"general":{"schedules":{"model":"local/small","limit":{"context":32000,"summaryTokens":2000}}}}}`)

	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Operational().Schedules
	if s == nil || s.Model != "local/small" || s.Reasoning != "" {
		t.Fatalf("Schedules = %#v, want the project block to replace home", s)
	}
	if s.Limit.Context == nil || *s.Limit.Context != 32000 || s.Limit.SummaryTokens == nil || *s.Limit.SummaryTokens != 2000 || s.Limit.ReservedTokens != nil {
		t.Fatalf("Limit = %#v", s.Limit)
	}
}

func TestLoadRejectsInvalidSchedules(t *testing.T) {
	tests := map[string]string{
		`{"reasoning":"low"}`:                       "schedules.reasoning requires schedules.model",
		`{"model":"local/a","limit":{"context":0}}`: "schedules.limit.context must be positive",
		`{"limit":{"reservedTokens":-1}}`:           "schedules.limit.reservedTokens must be positive",
	}
	for schedules, want := range tests {
		homePath := filepath.Join(t.TempDir(), "home.json")
		writeFile(t, homePath, `{"profiles":{"general":{"schedules":`+schedules+`}}}`)
		if _, err := Load(homePath, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Load(%s) error = %v, want %q", schedules, err, want)
		}
	}
}

func TestLoadPeersLayoutFromWorkspace(t *testing.T) {
	tmp := t.TempDir()
	homePath := filepath.Join(tmp, "home.json")
	projectPath := filepath.Join(tmp, "project.json")
	writeFile(t, homePath, `{"profiles":{"general":{"peers":{"tabs":[{"name":"all","bots":["a"]}]}}}}`)
	writeFile(t, projectPath, `{"profiles":{"general":{"peers":{"tabs":[{"name":"lead","bots":["coordinator"]},{"name":"impl","bots":["a","b"],"layout":"rows"}]}}}}`)

	cfg, err := Load(homePath, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Operational().Peers
	if p == nil || len(p.Tabs) != 2 || p.Tabs[1].Name != "impl" || p.Tabs[1].Layout != "rows" || fmt.Sprint(p.Tabs[1].Bots) != "[a b]" {
		t.Fatalf("Peers = %#v, want the workspace block to replace home", p)
	}
}

func TestLoadRejectsInvalidPeersLayout(t *testing.T) {
	tests := map[string]string{
		`[{"bots":["a"]}]`:                                      "peers.tabs[0].name must not be empty",
		`[{"name":"x"},{"name":"x"}]`:                           `tab "x" is listed twice`,
		`[{"name":"x","layout":"grid"}]`:                        "peers.tabs[0].layout must be rows or columns",
		`[{"name":"x","bots":[" "]}]`:                           "must not contain empty names",
		`[{"name":"x","bots":["a"]},{"name":"y","bots":["a"]}]`: `bot "a" is in both "x" and "y"`,
	}
	for tabs, want := range tests {
		homePath := filepath.Join(t.TempDir(), "home.json")
		writeFile(t, homePath, `{"profiles":{"general":{"peers":{"tabs":`+tabs+`}}}}`)
		if _, err := Load(homePath, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Load(%s) error = %v, want %q", tabs, err, want)
		}
	}
}

func TestLoadBuiltInProfiles(t *testing.T) {
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "general" || fmt.Sprint(cfg.Operational().Tools) != "[*]" {
		t.Fatalf("default = %#v", cfg)
	}

	coding, err := LoadProfile("", "", "coding")
	if err != nil {
		t.Fatal(err)
	}
	if coding.Operational().Sandbox.SessionExec == nil || !*coding.Operational().Sandbox.SessionExec {
		t.Fatalf("coding sessionExec = %v, want enabled", coding.Operational().Sandbox.SessionExec)
	}
	if coding.Operational().Sandbox.AllowLoopback == nil || !*coding.Operational().Sandbox.AllowLoopback {
		t.Fatalf("coding allowLoopback = %v, want enabled", coding.Operational().Sandbox.AllowLoopback)
	}
	for _, want := range []string{"github.com", "*.github.com", "pypi.org", "*.pythonhosted.org", "*.npmjs.org", "*.golang.org", "*.crates.io", "*.docker.io"} {
		found := false
		for _, host := range coding.Operational().Sandbox.NetworkAllowlist {
			if host == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("coding network allowlist missing %q: %#v", want, coding.Operational().Sandbox.NetworkAllowlist)
		}
	}
	for _, want := range []string{"GH_TOKEN", "NPM_TOKEN", "GOPROXY", "CARGO_REGISTRIES_CRATES_IO_TOKEN"} {
		if !slices.Contains(coding.Operational().Sandbox.Env, want) {
			t.Fatalf("coding environment missing %q: %#v", want, coding.Operational().Sandbox.Env)
		}
	}
	for _, want := range []string{"HOMEBREW_GITHUB_API_TOKEN", "DEVELOPER_DIR", "SDKROOT"} {
		if !slices.Contains(coding.Operational().Sandbox.EnvDarwin, want) {
			t.Fatalf("coding macOS environment missing %q: %#v", want, coding.Operational().Sandbox.EnvDarwin)
		}
	}

	compact, err := LoadProfile("", "", "compact")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(compact.Operational().Tools) != "[Bash]" {
		t.Fatalf("compact tools = %#v", compact.Operational().Tools)
	}
	if compact.Operational().Skills == nil || len(compact.Operational().Skills) != 0 {
		t.Fatalf("compact skills = %#v", compact.Operational().Skills)
	}
	if compact.Operational().Tasks.Enabled == nil || *compact.Operational().Tasks.Enabled {
		t.Fatalf("compact tasks = %v, want disabled", compact.Operational().Tasks.Enabled)
	}
}

func TestCodingProfileUsesExistingGitHubCLIConfig(t *testing.T) {
	home := t.TempDir()
	ghConfigDir := filepath.Join(home, ".config", "gh")
	if err := os.MkdirAll(ghConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghConfigDir, err := filepath.EvalSymlinks(ghConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	coding, err := LoadProfile("", "", "coding")
	if err != nil {
		t.Fatal(err)
	}
	sandbox := coding.Operational().Sandbox
	if !slices.Contains(sandbox.ReadRoots, ghConfigDir) {
		t.Fatalf("coding read roots missing GitHub CLI config %q: %#v", ghConfigDir, sandbox.ReadRoots)
	}
	if !slices.Contains(sandbox.Env, "GH_CONFIG_DIR="+ghConfigDir) {
		t.Fatalf("coding environment missing GitHub CLI config: %#v", sandbox.Env)
	}
}

func TestLoadRejectsLegacyOperationalFields(t *testing.T) {
	for _, field := range []string{"sandbox", "execution", "tools", "tasks", "output"} {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, fmt.Sprintf(`{%q:{}}`, field))
		_, err := Load(path, "")
		if err == nil || !strings.Contains(err.Error(), "profiles.general."+field) {
			t.Fatalf("%s error = %v", field, err)
		}
	}
}

func TestResolve_ActiveModelUsesProviderModelFormat(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{
		"activeModel": "local/qwen",
		"provider": {
			"local": {
				"options": {
					"baseURL": "http://localhost:8000/v1"
				},
				"models": {
					"qwen": {
						"name": "Qwen"
					}
				}
			}
		}
	}`)

	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Model != "qwen" {
		t.Fatalf("Model = %q, want fallback to model key", resolved.Model)
	}
}

func TestResolveModelRef_UsesConfiguredProviderModel(t *testing.T) {
	cfg := Config{
		Provider: map[string]Provider{
			"llama.cpp": {
				Options: ProviderOptions{
					BaseURL: ptr("http://localhost:8001/v1"),
				},
				Models: map[string]Model{
					"qwen3627b_mtp_code": {
						Model:     "qwen3-627b-mtp-code",
						Reasoning: "xhigh",
						Limit: Limit{
							Context:          intPtr(32768),
							ReservedTokens:   intPtr(4096),
							KeepRecentTokens: intPtr(8192),
							SummaryTokens:    intPtr(3000),
						},
					},
				},
			},
		},
		credentials: Credentials{Provider: map[string]ProviderCredential{"llama.cpp": {APIKey: "local-key"}}},
	}

	resolved, ok, err := cfg.ResolveModelRef("llama.cpp/qwen3627b_mtp_code")
	if err != nil {
		t.Fatalf("ResolveModelRef() error = %v", err)
	}
	if !ok {
		t.Fatal("ResolveModelRef() ok = false, want true")
	}
	if resolved.Model != "qwen3-627b-mtp-code" {
		t.Fatalf("Model = %q, want configured model string", resolved.Model)
	}
	if resolved.BaseURL != "http://localhost:8001/v1" {
		t.Fatalf("BaseURL = %q, want provider base URL", resolved.BaseURL)
	}
	if resolved.APIKey != "local-key" {
		t.Fatalf("APIKey = %q, want provider API key", resolved.APIKey)
	}
	if resolved.ReasoningEffort != "xhigh" {
		t.Fatalf("ReasoningEffort = %q, want xhigh", resolved.ReasoningEffort)
	}
	if resolved.ContextWindow != 32768 {
		t.Fatalf("ContextWindow = %d, want 32768", resolved.ContextWindow)
	}
	if resolved.ReservedTokens != 4096 {
		t.Fatalf("ReservedTokens = %d, want 4096", resolved.ReservedTokens)
	}
	if resolved.KeepRecentTokens != 8192 {
		t.Fatalf("KeepRecentTokens = %d, want 8192", resolved.KeepRecentTokens)
	}
	if resolved.SummaryTokens != 3000 {
		t.Fatalf("SummaryTokens = %d, want 3000", resolved.SummaryTokens)
	}
}

func TestResolveModelRef_UnknownOrInvalidRefDoesNotMatch(t *testing.T) {
	cfg := Config{
		Provider: map[string]Provider{
			"local": {
				Models: map[string]Model{"qwen": {}},
			},
		},
	}

	for _, ref := range []string{"local/missing", "missing/qwen", "openrouter/vendor/model", "raw-model"} {
		if _, ok, err := cfg.ResolveModelRef(ref); err != nil || ok {
			t.Fatalf("ResolveModelRef(%q) = ok %v, err %v; want ok false, err nil", ref, ok, err)
		}
	}
}

func TestResolveExplicitModelRef_AllowsAdHocModelForKnownProvider(t *testing.T) {
	cfg := Config{
		Provider: map[string]Provider{
			"opencodego": {
				Options: ProviderOptions{BaseURL: ptr("https://opencode.ai/zen/go/v1")},
				Models:  map[string]Model{"configured": {}},
			},
		},
		credentials: Credentials{Provider: map[string]ProviderCredential{"opencodego": {APIKey: "provider-key"}}},
	}

	resolved, ok, err := cfg.ResolveExplicitModelRef("opencodego/glm-5.3-flash")
	if err != nil {
		t.Fatalf("ResolveExplicitModelRef() error = %v", err)
	}
	if !ok {
		t.Fatal("ResolveExplicitModelRef() ok = false, want true")
	}
	if resolved.Model != "glm-5.3-flash" {
		t.Fatalf("Model = %q, want glm-5.3-flash", resolved.Model)
	}
	if resolved.BaseURL != "https://opencode.ai/zen/go/v1" || resolved.APIKey != "provider-key" {
		t.Fatalf("provider options = (%q, %q), want configured URL and key", resolved.BaseURL, resolved.APIKey)
	}
}

func TestResolveExplicitModelRef_UnknownProviderDoesNotMatch(t *testing.T) {
	cfg := Config{Provider: map[string]Provider{"local": {}}}
	if _, ok, err := cfg.ResolveExplicitModelRef("openrouter/vendor/model"); err != nil || ok {
		t.Fatalf("ResolveExplicitModelRef() = ok %v, err %v; want ok false, err nil", ok, err)
	}
}

func TestModelChoices_SortedConfiguredRefs(t *testing.T) {
	cfg := Config{
		Provider: map[string]Provider{
			"zlocal": {
				Name: "Local",
				Models: map[string]Model{
					"qwen": {Name: "Qwen", Model: "qwen-code"},
				},
			},
			"anthropic": {
				Models: map[string]Model{
					"sonnet": {},
				},
			},
		},
	}

	choices := cfg.ModelChoices()
	if len(choices) != 2 {
		t.Fatalf("ModelChoices() len = %d, want 2", len(choices))
	}
	if choices[0].Ref != "anthropic/sonnet" || choices[0].Model != "sonnet" {
		t.Fatalf("first choice = %+v, want anthropic/sonnet fallback model", choices[0])
	}
	if choices[1].Ref != "zlocal/qwen" || choices[1].ProviderName != "Local" || choices[1].ModelName != "Qwen" || choices[1].Model != "qwen-code" {
		t.Fatalf("second choice = %+v, want zlocal/qwen metadata", choices[1])
	}
}

func TestModelChoices_ReasoningEffort(t *testing.T) {
	cfg := Config{
		Provider: map[string]Provider{
			"local": {
				Models: map[string]Model{
					"inferred": {Model: "effort-model", Reasoning: "high", ReasoningValues: []string{"low", "high"}},
					"plain":    {Model: "plain-model"},
				},
			},
		},
	}

	choices := cfg.ModelChoices()
	gotReasoning := map[string]string{}
	gotValues := map[string][]string{}
	for _, choice := range choices {
		gotReasoning[choice.Ref] = choice.Reasoning
		gotValues[choice.Ref] = choice.ReasoningValues
	}
	if gotReasoning["local/inferred"] != "high" {
		t.Fatalf("reasoning effort = %q, want high", gotReasoning["local/inferred"])
	}
	if fmt.Sprint(gotValues["local/inferred"]) != fmt.Sprint([]string{"low", "high"}) {
		t.Fatalf("reasoning values = %#v, want [low high]", gotValues["local/inferred"])
	}
	if gotReasoning["local/plain"] != "" {
		t.Fatalf("plain model reasoning effort = %q, want empty", gotReasoning["local/plain"])
	}
	if gotValues["local/plain"] != nil {
		t.Fatalf("plain model reasoning values = %#v, want nil", gotValues["local/plain"])
	}
}

func TestWithDiscoveredModelsMergesOverridesAndSelectsDefault(t *testing.T) {
	cfg := Config{
		Provider: map[string]Provider{
			"openai": {
				Name: "OpenAI",
				Models: map[string]Model{
					"configured-only": {Reasoning: "low"},
					"fast": {
						Model:     "gpt-5",
						Reasoning: "high",
						Limit:     Limit{Context: intPtr(123456)},
					},
				},
			},
		},
	}

	got := cfg.WithDiscoveredModels(map[string][]string{
		"openai": {"vendor/model", "gpt-5", "gpt-5", ""},
	})
	choices := got.ModelChoices()
	refs := make([]string, 0, len(choices))
	for _, choice := range choices {
		refs = append(refs, choice.Ref)
	}
	wantRefs := []string{"openai/configured-only", "openai/fast", "openai/vendor/model"}
	if fmt.Sprint(refs) != fmt.Sprint(wantRefs) {
		t.Fatalf("refs = %#v, want %#v", refs, wantRefs)
	}
	if got.ActiveModel != "openai/configured-only" {
		t.Fatalf("ActiveModel = %q, want first sorted canonical ref", got.ActiveModel)
	}
	if cfg.ActiveModel != "" || len(cfg.Provider["openai"].Models) != 2 {
		t.Fatal("WithDiscoveredModels mutated its input")
	}

	resolved, ok, err := got.ResolveModelRef("openai/gpt-5")
	if err != nil || !ok {
		t.Fatalf("ResolveModelRef(raw discovered ID) = ok %v, err %v", ok, err)
	}
	if resolved.Model != "gpt-5" || resolved.ReasoningEffort != "high" || resolved.ContextWindow != 123456 {
		t.Fatalf("raw discovered ID did not receive configured override: %+v", resolved)
	}

	slashModel, ok, err := got.ResolveModelRef("openai/vendor/model")
	if err != nil || !ok || slashModel.Model != "vendor/model" {
		t.Fatalf("slash model resolution = %+v, ok %v, err %v", slashModel, ok, err)
	}
}

func TestLoad_MissingFilesAreEmptyConfig(t *testing.T) {
	tmp := t.TempDir()
	cfg, err := Load(filepath.Join(tmp, "missing-home.json"), filepath.Join(tmp, "missing-project.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved != (Resolved{}) {
		t.Fatalf("Resolve() = %+v, want zero value", resolved)
	}
}

func TestSaveProjectActiveModel_CreatesProjectConfig(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".caretz", "config.json")

	if err := SaveProjectActiveModel(path, "local/qwen"); err != nil {
		t.Fatalf("SaveProjectActiveModel() error = %v", err)
	}

	cfg, err := loadRawConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg["activeModel"] != "local/qwen" {
		t.Fatalf("activeModel = %q, want local/qwen", cfg["activeModel"])
	}
}

func TestSaveProjectActiveModel_PreservesExistingConfig(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".caretz", "config.json")
	writeFile(t, path, `{
		"activeModel": "local/old",
		"provider": {
			"local": {
				"options": {
					"baseURL": "http://localhost:8000/v1"
				}
			}
		},
		"custom": {
			"keep": true
		}
	}`)

	if err := SaveProjectActiveModel(path, "local/new"); err != nil {
		t.Fatalf("SaveProjectActiveModel() error = %v", err)
	}

	cfg, err := loadRawConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg["activeModel"] != "local/new" {
		t.Fatalf("activeModel = %q, want local/new", cfg["activeModel"])
	}
	if _, ok := cfg["provider"]; !ok {
		t.Fatal("provider field was not preserved")
	}
	if _, ok := cfg["custom"]; !ok {
		t.Fatal("custom field was not preserved")
	}
}

func TestSaveProjectActiveModel_InvalidRefFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".caretz", "config.json")

	if err := SaveProjectActiveModel(path, "raw-model"); err == nil {
		t.Fatal("SaveProjectActiveModel() error = nil, want invalid ref error")
	}
}

func TestLoad_InvalidJSONFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{`)

	if _, err := Load(path, ""); err == nil {
		t.Fatal("Load() error = nil, want parse error")
	}
}

func TestResolve_UnknownActiveModelFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{
		"activeModel": "local/missing",
		"provider": {
			"local": {
				"models": {}
			}
		}
	}`)

	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("Resolve() error = nil, want unknown model error")
	}
}

func TestResolve_InvalidActiveModelFormatFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{"activeModel": "qwen"}`)

	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("Resolve() error = nil, want activeModel format error")
	}
}

func TestLoad_InvalidLimitFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{
		"provider": {
			"local": {
				"models": {
					"qwen": {
						"limit": {
							"context": 0
						}
					}
				}
			}
		}
	}`)

	if _, err := Load(path, ""); err == nil {
		t.Fatal("Load() error = nil, want invalid limit error")
	}
}

func TestLoad_InvalidReasoningValuesFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{
		"provider": {
			"local": {
				"models": {
					"qwen": {
						"reasoning": "low",
						"reasoningValues": ["low", ""]
					}
				}
			}
		}
	}`)

	if _, err := Load(path, ""); err == nil {
		t.Fatal("Load() error = nil, want invalid reasoningValues error")
	}
}

func TestLoad_InvalidModelProtocolFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")
	writeFile(t, path, `{
		"provider": {
			"hosted": {
				"models": {
					"model": {"protocol": "messages"}
				}
			}
		}
	}`)

	if _, err := Load(path, ""); err == nil {
		t.Fatal("Load() error = nil, want invalid protocol error")
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

func loadRawConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func ptr(value string) *string {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func TestOpsProfileOpensPackageManagersAndEscalates(t *testing.T) {
	home := t.TempDir()
	prefix := filepath.Join(t.TempDir(), "homebrew")
	for _, dir := range []string{prefix, filepath.Join(home, ".cache"), filepath.Join(home, ".local"), filepath.Join(home, ".ssh")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("HOMEBREW_PREFIX", prefix)

	ops, err := LoadProfile("", "", "ops")
	if err != nil {
		t.Fatal(err)
	}
	sandbox := ops.Operational().Sandbox
	if sandbox.AskSandboxOff == nil || !*sandbox.AskSandboxOff || sandbox.SessionExec == nil || *sandbox.SessionExec {
		t.Fatalf("ops must escalate and run one-shot commands that see host processes: %+v", sandbox)
	}
	if sandbox.AllowDocker != nil {
		t.Fatalf("ops grants Docker: %+v", sandbox)
	}
	if sandbox.UnrestrictedNetwork == nil || !*sandbox.UnrestrictedNetwork {
		t.Fatalf("ops must see the host network: %+v", sandbox)
	}
	want := []string{prefix, filepath.Join(home, ".local"), filepath.Join(home, ".cache")}
	if !reflect.DeepEqual(sandbox.WritableRoots, want) {
		t.Fatalf("writable roots = %v, want only existing %v", sandbox.WritableRoots, want)
	}
	if !slices.Contains(sandbox.ReadRoots, filepath.Join(home, ".ssh")) {
		t.Fatalf("ops read roots miss ~/.ssh: %v", sandbox.ReadRoots)
	}
	if slices.Contains(builtinProfile("coding").Sandbox.WritableRoots, prefix) || builtinProfile("coding").Sandbox.AskSandboxOff != nil {
		t.Fatal("ops grants leaked into coding")
	}
}

func TestOpsProfileCanBeCustomized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"profile":"ops","profiles":{"ops":{"sandbox":{"askSandboxOff":false,"writableRoots":[]}}}}`)
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	sandbox := cfg.Operational().Sandbox
	if sandbox.AskSandboxOff == nil || *sandbox.AskSandboxOff || len(sandbox.WritableRoots) != 0 || sandbox.SessionExec == nil {
		t.Fatalf("override did not merge over built-in ops: %+v", sandbox)
	}
}

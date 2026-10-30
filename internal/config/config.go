package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/tools"
)

const ProjectConfigPath = ".caretz/config.json"

type Config struct {
	ModelRules  []ModelRule         `json:"modelRules,omitempty"`
	Warnings    []string            `json:"-"`
	ActiveModel string              `json:"activeModel"`
	Profile     string              `json:"profile"`
	Profiles    map[string]Profile  `json:"profiles"`
	Provider    map[string]Provider `json:"provider"`
	Runtime     Profile             `json:"-"`
	credentials Credentials
}

// Profile contains operational settings. Profiles never select the session's
// model or its reasoning effort; those remain root/provider-model concerns.
// Goal roles and schedules are the exception: they pick the model of the
// child runs they start.
type Profile struct {
	SystemPrompt   *string        `json:"systemPrompt"`
	Tools          []string       `json:"tools"`
	DisabledTools  []string       `json:"disabledTools"`
	ToolConfig     tools.Config   `json:"toolConfig"`
	Skills         []string       `json:"skills"`
	Agents         []string       `json:"agents"`
	Sandbox        Sandbox        `json:"sandbox"`
	Execution      Execution      `json:"execution"`
	Tasks          Tasks          `json:"tasks"`
	Goal           Goal           `json:"goal"`
	Schedules      *Schedules     `json:"schedules"`
	Output         Output         `json:"output"`
	Plan           *bool          `json:"plan"`
	Incognito      *bool          `json:"incognito"`
	Request        RequestPolicy  `json:"request"`
	ModelDiscovery ModelDiscovery `json:"modelDiscovery"`
}

type RequestPolicy struct {
	Timeout     Duration `json:"timeout"`
	MaxAttempts *int     `json:"maxAttempts"`
}

type ModelDiscovery struct {
	Enabled *bool `json:"enabled"`
}

// Duration accepts the same strings as CLI durations (for example "30s" or
// "5m"). A JSON number is interpreted as nanoseconds for compatibility with
// time.Duration's standard encoding.
type Duration struct {
	time.Duration
	Set bool
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	d.Set = true
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		value, err := time.ParseDuration(text)
		if err != nil {
			return err
		}
		d.Duration = value
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("duration must be a duration string: %w", err)
	}
	d.Duration = time.Duration(value)
	return nil
}

type Execution struct {
	Docker Docker `json:"docker"`
}
type Docker struct {
	Enabled     *bool    `json:"enabled"`
	Image       *string  `json:"image"`
	IdleTimeout Duration `json:"idleTimeout"`
	Publish     []string `json:"publish"`
}

type Output struct {
	Mode  string `json:"mode"`
	Theme string `json:"theme"`
}

type Tasks struct {
	Enabled *bool `json:"enabled"`
}

// Goal bounds every /goal run. maxTokens counts completion and uncached
// prompt tokens across all goal roles; maxDuration is running time.
type Goal struct {
	MaxTokens   *int64   `json:"maxTokens"`
	MaxDuration Duration `json:"maxDuration"`
	// Planner, Worker and Verifier pick the model each /goal role runs on;
	// an unset role uses the session's current model.
	Planner  *GoalRole `json:"planner"`
	Worker   *GoalRole `json:"worker"`
	Verifier *GoalRole `json:"verifier"`
}

type GoalRole struct {
	Model     string `json:"model"`
	Reasoning string `json:"reasoning"`
}

// Schedules picks the model, reasoning and context limits of scheduled runs.
// An unset model uses the session's current model; unset limits keep the
// model's own. Values are read when a run starts, so existing schedules
// follow config changes.
type Schedules struct {
	Model     string `json:"model"`
	Reasoning string `json:"reasoning"`
	Limit     Limit  `json:"limit"`
}

type Sandbox struct {
	SessionExec         *bool    `json:"sessionExec"`
	AllowLoopback       *bool    `json:"allowLoopback"`
	ReadRoots           []string `json:"readRoots"`
	WritableRoots       []string `json:"writableRoots"`
	AllowDocker         *bool    `json:"allowDocker"`
	UnrestrictedNetwork *bool    `json:"unrestrictedNetwork"`
	AskSandboxOff       *bool    `json:"askSandboxOff"`
	AllowSystemBus      *bool    `json:"allowSystemBus"`
	AllowGPU            *bool    `json:"allowGPU"`
	NetworkAllowlist    []string `json:"networkAllowlist"`
	Env                 []string `json:"env"`
	EnvLinux            []string `json:"envLinux"`
	EnvDarwin           []string `json:"envDarwin"`
	EnvFiles            []string `json:"envFile"`
	EnvFilesLinux       []string `json:"envFileLinux"`
	EnvFilesDarwin      []string `json:"envFileDarwin"`
}

type Provider struct {
	Local *bool `json:"local,omitempty"`
	// Auth selects how caretz authenticates to the provider. Empty means an
	// API key from credentials.json; AuthChatGPT signs in with a ChatGPT
	// account and spends its plan usage.
	Auth    string           `json:"auth,omitempty"`
	Name    string           `json:"name"`
	Options ProviderOptions  `json:"options"`
	Models  map[string]Model `json:"models"`
}

type ProviderOptions struct {
	BaseURL *string `json:"baseURL"`
	APIKey  *string `json:"apiKey"`
}

type Model struct {
	Name            string   `json:"name"`
	Model           string   `json:"model"`
	Protocol        string   `json:"protocol"`
	Reasoning       string   `json:"reasoning"`
	ReasoningValues []string `json:"reasoningValues"`
	Limit           Limit    `json:"limit"`
}

type Limit struct {
	Context          *int `json:"context"`
	ReservedTokens   *int `json:"reservedTokens"`
	KeepRecentTokens *int `json:"keepRecentTokens"`
	SummaryTokens    *int `json:"summaryTokens"`
}

type Resolved struct {
	APIKey           string
	BaseURL          string
	Model            string
	Protocol         string
	ReasoningEffort  string
	ContextWindow    int
	ReservedTokens   int
	KeepRecentTokens int
	SummaryTokens    int
}

type ModelChoice struct {
	Ref             string
	ProviderKey     string
	ModelKey        string
	ProviderName    string
	ModelName       string
	Model           string
	Reasoning       string
	ReasoningValues []string
}

func HomeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "caretz", "config.json"), nil
}

func HomeSkillsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "caretz", "skills"), nil
}

func HomeAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "caretz", "agents"), nil
}

func ProjectPath(cwd string) string {
	return filepath.Join(cwd, ProjectConfigPath)
}

func SaveProjectActiveModel(projectPath, activeModel string) error {
	if _, _, ok := splitModelRef(activeModel); !ok {
		return fmt.Errorf("activeModel must use provider/model format, got %q", activeModel)
	}

	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read config %s: %w", projectPath, err)
		}
	} else if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config %s: %w", projectPath, err)
		}
	}

	activeModelJSON, err := json.Marshal(activeModel)
	if err != nil {
		return fmt.Errorf("marshal activeModel: %w", err)
	}
	raw["activeModel"] = activeModelJSON

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config %s: %w", projectPath, err)
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(projectPath), 0755); err != nil {
		return fmt.Errorf("create config directory %s: %w", filepath.Dir(projectPath), err)
	}
	if err := os.WriteFile(projectPath, out, 0644); err != nil {
		return fmt.Errorf("write config %s: %w", projectPath, err)
	}
	return nil
}

func Load(homePath, projectPath string) (Config, error) {
	return LoadProfile(homePath, projectPath, "")
}

// LoadProfile resolves model configuration and operational profiles in their
// distinct precedence orders. selected overrides root profile selection when
// non-empty (used by CARETZ_PROFILE and an explicit --profile).
func LoadProfile(homePath, projectPath, selected string) (Config, error) {
	return LoadProfileWithTrust(homePath, projectPath, selected, false)
}

func LoadProfileWithTrust(homePath, projectPath, selected string, trustedProject bool) (Config, error) {
	var home, project Config
	if homePath != "" {
		cfg, ok, err := loadFile(homePath)
		if err != nil {
			return Config{}, err
		}
		if ok {
			home = cfg
		}
		if err := validateNoAPIKeys(home, homePath); err != nil {
			return Config{}, err
		}
	}
	if projectPath != "" {
		cfg, ok, err := loadFile(projectPath, !trustedProject)
		if err != nil {
			return Config{}, err
		}
		if ok {
			project = cfg
		}
		if err := validateNoAPIKeys(project, projectPath); err != nil {
			return Config{}, err
		}
	}
	var credentials Credentials
	if homePath != "" {
		var err error
		if credentials, err = LoadCredentials(CredentialsPath(homePath)); err != nil {
			return Config{}, err
		}
	}
	if err := validateModelRules(home.ModelRules, homePath); err != nil {
		return Config{}, err
	}
	rules := home.ModelRules
	warnings := []string{}
	if trustedProject {
		if err := validateModelRules(project.ModelRules, projectPath); err != nil {
			return Config{}, err
		}
		if project.ModelRules != nil {
			rules = project.ModelRules
		}
	} else {
		warnings = append(warnings, project.Warnings...)
	}
	merged := merge(home, project)
	merged.ModelRules, merged.Warnings = rules, warnings
	merged.credentials = credentials
	if strings.TrimSpace(selected) == "" {
		selected = merged.Profile
	}
	if strings.TrimSpace(selected) == "" {
		selected = "general"
	}
	merged.Profile = selected
	merged.Runtime = builtinGeneralProfile()
	if selected != "general" {
		merged.Runtime = mergeProfile(merged.Runtime, builtinProfile(selected))
	}
	for _, layer := range []Config{home, project} {
		merged.Runtime = mergeProfile(merged.Runtime, layer.Profiles["general"])
		if selected != "general" {
			merged.Runtime = mergeProfile(merged.Runtime, layer.Profiles[selected])
		}
	}
	if err := validate(merged); err != nil {
		return Config{}, err
	}
	return merged, nil
}

func builtinGeneralProfile() Profile {
	enabled := true
	return Profile{Tools: []string{"*"}, Skills: []string{"*"}, Agents: []string{"*"}, Tasks: Tasks{Enabled: &enabled}}
}

// BuiltinProfiles are the profiles Caretz defines without any configuration.
var BuiltinProfiles = []string{"general", "coding", "compact", "ops"}

func builtinProfile(name string) Profile {
	switch name {
	case "coding":
		enabled := true
		readRoots, environment := hostCredentialGrants()
		return Profile{Sandbox: Sandbox{
			SessionExec:      &enabled,
			AllowLoopback:    &enabled,
			ReadRoots:        readRoots,
			NetworkAllowlist: codingNetworkAllowlist(),
			Env:              append(codingEnvironment(), environment...),
			EnvDarwin:        codingDarwinEnvironment(),
		}}
	case "ops":
		enabled := true
		readRoots, environment := hostCredentialGrants()
		// Ops work inspects the host's interfaces, ports, processes, and daemon
		// sockets. Private namespaces would silently hide them, so ops shares
		// the host network instead of filtering egress, and runs one-shot
		// commands: session execution has its own PID namespace and no /run.
		// The system bus lets systemctl and loginctl query the host; changes
		// through it still need polkit authorization. GPU device nodes let
		// nvidia-smi and similar tools report the real hardware.
		disabled := false
		return Profile{Sandbox: Sandbox{
			SessionExec:         &disabled,
			AllowLoopback:       &enabled,
			AskSandboxOff:       &enabled,
			AllowSystemBus:      &enabled,
			AllowGPU:            &enabled,
			UnrestrictedNetwork: &enabled,
			ReadRoots:           append(readRoots, opsReadRoots()...),
			WritableRoots:       opsWritableRoots(),
			Env:                 append(codingEnvironment(), environment...),
			EnvDarwin:           codingDarwinEnvironment(),
		}}
	case "compact":
		disabled := false
		return Profile{
			Tools:  []string{"Bash"},
			Skills: []string{},
			Agents: []string{},
			Tasks:  Tasks{Enabled: &disabled},
		}
	default:
		return Profile{}
	}
}

// hostCredentialGrants lets sandboxed commands reuse the host's gh login,
// SSH keys, and SSH agent when they exist.
func hostCredentialGrants() (readRoots, environment []string) {
	if ghConfigDir := githubCLIConfigDir(); ghConfigDir != "" {
		readRoots = []string{ghConfigDir}
		environment = append(environment, "GH_CONFIG_DIR="+ghConfigDir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		sshDir := filepath.Join(home, ".ssh")
		if info, err := os.Stat(sshDir); err == nil && info.IsDir() {
			readRoots = append(readRoots, sshDir)
		}
	}
	if socket := os.Getenv("SSH_AUTH_SOCK"); filepath.IsAbs(socket) {
		if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			readRoots = append(readRoots, socket)
			environment = append(environment, "SSH_AUTH_SOCK="+socket)
		}
	}
	return readRoots, environment
}

// opsWritableRoots grants the package-manager prefixes and user-level install
// and cache directories that machine maintenance routinely writes. Anything
// else the ops profile needs escalates to the user per command.
func opsWritableRoots() []string {
	var candidates []string
	if prefix := strings.TrimSpace(os.Getenv("HOMEBREW_PREFIX")); filepath.IsAbs(prefix) {
		candidates = append(candidates, prefix)
	} else {
		candidates = append(candidates, "/opt/homebrew", "/usr/local/Homebrew", "/home/linuxbrew/.linuxbrew")
		// Intel Homebrew installs into /usr/local itself.
		if existingDir("/usr/local/Homebrew") {
			candidates = append(candidates, "/usr/local")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, rel := range []string{"Library/Caches/Homebrew", "Library/Logs/Homebrew", ".local", ".cache", ".cargo", "go", ".npm"} {
			candidates = append(candidates, filepath.Join(home, rel))
		}
	}
	return existingDirs(candidates)
}

func opsReadRoots() []string {
	candidates := []string{"/Library/Logs"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "Library", "Logs"))
	}
	return existingDirs(candidates)
}

func existingDirs(paths []string) []string {
	var dirs []string
	for _, path := range paths {
		if existingDir(path) && !slices.Contains(dirs, path) {
			dirs = append(dirs, path)
		}
	}
	return dirs
}

func existingDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func githubCLIConfigDir() string {
	var candidate string
	if configured := strings.TrimSpace(os.Getenv("GH_CONFIG_DIR")); configured != "" {
		candidate = configured
	} else if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
		candidate = filepath.Join(configHome, "gh")
	} else if home, err := os.UserHomeDir(); err == nil {
		candidate = filepath.Join(home, ".config", "gh")
	}
	if candidate == "" {
		return ""
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return ""
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func codingEnvironment() []string {
	return []string{
		"GH_TOKEN", "GITHUB_TOKEN",
		"NPM_TOKEN", "NODE_AUTH_TOKEN",
		"PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL",
		"GOPROXY", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOSUMDB",
		"CARGO_REGISTRIES_CRATES_IO_TOKEN", "GEM_HOST_API_KEY", "COMPOSER_AUTH",
	}
}

func codingDarwinEnvironment() []string {
	return []string{
		"HOMEBREW_GITHUB_API_TOKEN", "HOMEBREW_NO_AUTO_UPDATE", "HOMEBREW_NO_ANALYTICS",
		"DEVELOPER_DIR", "SDKROOT", "MACOSX_DEPLOYMENT_TARGET",
	}
}

func codingNetworkAllowlist() []string {
	return []string{
		"github.com", "*.github.com", "githubusercontent.com", "*.githubusercontent.com", "githubassets.com", "*.githubassets.com",
		"gitlab.com", "*.gitlab.com", "bitbucket.org", "*.bitbucket.org",
		"npmjs.org", "*.npmjs.org", "npmjs.com", "*.npmjs.com",
		"pypi.org", "*.pypi.org", "pythonhosted.org", "*.pythonhosted.org",
		"go.dev", "*.go.dev", "golang.org", "*.golang.org", "goproxy.io", "*.goproxy.io", "sum.golang.org",
		"crates.io", "*.crates.io", "rust-lang.org", "*.rust-lang.org",
		"rubygems.org", "*.rubygems.org", "packagist.org", "*.packagist.org",
		"maven.org", "*.maven.org", "apache.org", "*.apache.org", "sonatype.com", "*.sonatype.com", "gradle.org", "*.gradle.org",
		"nuget.org", "*.nuget.org",
		"docker.io", "*.docker.io", "ghcr.io", "quay.io", "*.quay.io",
		"googleapis.com", "*.googleapis.com", "cloudflare.docker.com", "*.cloudflare.docker.com",
	}
}

// Operational returns the operational profile resolved by Load or LoadProfile.
func (c Config) Operational() Profile {
	return c.Runtime
}

func (c Config) Resolve() (Resolved, error) {
	if c.ActiveModel == "" {
		return Resolved{}, nil
	}
	providerKey, modelKey, err := parseActiveModel(c.ActiveModel)
	if err != nil {
		return Resolved{}, err
	}
	return c.resolveProviderModel(providerKey, modelKey, c.ActiveModel)
}

func (c Config) ResolveModelRef(modelRef string) (Resolved, bool, error) {
	providerKey, modelKey, ok := splitModelRef(modelRef)
	if !ok {
		return Resolved{}, false, nil
	}
	if _, ok := c.Provider[providerKey]; !ok {
		return Resolved{}, false, nil
	}
	if _, _, found, err := findProviderModel(c.Provider[providerKey], modelKey); err != nil {
		return Resolved{}, false, err
	} else if !found {
		return Resolved{}, false, nil
	}
	resolved, err := c.resolveProviderModel(providerKey, modelKey, modelRef)
	if err != nil {
		return Resolved{}, false, err
	}
	return resolved, true, nil
}

// ResolveExplicitModelRef resolves a provider/model reference selected explicitly
// for the current process. Unlike ResolveModelRef, the model does not need to be
// present in the configured or discovered catalog: a known provider is enough.
func (c Config) ResolveExplicitModelRef(modelRef string) (Resolved, bool, error) {
	providerKey, modelKey, ok := splitModelRef(modelRef)
	if !ok {
		return Resolved{}, false, nil
	}
	provider, ok := c.Provider[providerKey]
	if !ok {
		return Resolved{}, false, nil
	}
	if _, _, found, err := findProviderModel(provider, modelKey); err != nil {
		return Resolved{}, false, err
	} else if found {
		resolved, err := c.resolveProviderModel(providerKey, modelKey, modelRef)
		return resolved, err == nil, err
	}

	resolved := Resolved{Model: modelKey, APIKey: c.ProviderAPIKey(providerKey)}
	if provider.Options.BaseURL != nil {
		resolved.BaseURL = *provider.Options.BaseURL
	}
	if provider.Auth == AuthChatGPT {
		resolved.BaseURL, resolved.Protocol = ChatGPTBaseURL, "responses"
	}
	return resolved, true, nil
}

// WithDiscoveredModels returns a copy of c whose provider model maps include
// IDs discovered from OpenAI-compatible /models endpoints. Configured entries
// remain authoritative and suppress duplicate discovered entries that resolve
// to the same API model ID.
func (c Config) WithDiscoveredModels(discovered map[string][]string) Config {
	out := c
	out.Provider = make(map[string]Provider, len(c.Provider))
	for providerKey, provider := range c.Provider {
		cloned := provider
		cloned.Models = make(map[string]Model, len(provider.Models))
		for modelKey, model := range provider.Models {
			cloned.Models[modelKey] = model
		}
		for _, modelID := range discovered[providerKey] {
			modelID = strings.TrimSpace(modelID)
			_, keyExists := cloned.Models[modelID]
			if modelID == "" || keyExists || providerHasEffectiveModel(cloned, modelID) {
				continue
			}
			cloned.Models[modelID] = Model{Model: modelID}
		}
		out.Provider[providerKey] = cloned
	}
	if out.ActiveModel == "" {
		choices := out.ModelChoices()
		if len(choices) > 0 {
			out.ActiveModel = choices[0].Ref
		}
	}
	return out
}

func (c Config) ModelChoices() []ModelChoice {
	var choices []ModelChoice
	for providerKey, provider := range c.Provider {
		for modelKey, model := range provider.Models {
			resolvedModel := model.Model
			if resolvedModel == "" {
				resolvedModel = modelKey
			}
			choices = append(choices, ModelChoice{
				Ref:             providerKey + "/" + modelKey,
				ProviderKey:     providerKey,
				ModelKey:        modelKey,
				ProviderName:    provider.Name,
				ModelName:       model.Name,
				Model:           resolvedModel,
				Reasoning:       strings.TrimSpace(model.Reasoning),
				ReasoningValues: trimStringSlice(model.ReasoningValues),
			})
		}
	}
	sort.Slice(choices, func(i, j int) bool {
		return choices[i].Ref < choices[j].Ref
	})
	return choices
}

func (c Config) resolveProviderModel(providerKey, modelKey, displayRef string) (Resolved, error) {
	provider, ok := c.Provider[providerKey]
	if !ok {
		return Resolved{}, fmt.Errorf("active model %q references unknown provider %q", displayRef, providerKey)
	}
	_, model, found, err := findProviderModel(provider, modelKey)
	if err != nil {
		return Resolved{}, fmt.Errorf("active model %q: %w", displayRef, err)
	}
	if !found {
		return Resolved{}, fmt.Errorf("active model %q references unknown model %q for provider %q", displayRef, modelKey, providerKey)
	}

	resolvedModel := model.Model
	if resolvedModel == "" {
		resolvedModel = modelKey
	}
	out := Resolved{
		Model:           resolvedModel,
		APIKey:          c.ProviderAPIKey(providerKey),
		Protocol:        strings.TrimSpace(model.Protocol),
		ReasoningEffort: strings.TrimSpace(model.Reasoning),
	}
	if provider.Options.BaseURL != nil {
		out.BaseURL = *provider.Options.BaseURL
	}
	if provider.Auth == AuthChatGPT {
		// Plan usage is served only by the Responses API on OpenAI's host.
		out.BaseURL, out.Protocol = ChatGPTBaseURL, "responses"
	}
	if model.Limit.Context != nil {
		out.ContextWindow = *model.Limit.Context
	}
	if model.Limit.ReservedTokens != nil {
		out.ReservedTokens = *model.Limit.ReservedTokens
	}
	if model.Limit.KeepRecentTokens != nil {
		out.KeepRecentTokens = *model.Limit.KeepRecentTokens
	}
	if model.Limit.SummaryTokens != nil {
		out.SummaryTokens = *model.Limit.SummaryTokens
	}
	return out, nil
}

func findProviderModel(provider Provider, modelKey string) (string, Model, bool, error) {
	if model, ok := provider.Models[modelKey]; ok {
		return modelKey, model, true, nil
	}
	var matchedKey string
	var matched Model
	for key, model := range provider.Models {
		effective := model.Model
		if effective == "" {
			effective = key
		}
		if effective != modelKey {
			continue
		}
		if matchedKey != "" {
			return "", Model{}, false, fmt.Errorf("model ID %q is configured by multiple aliases", modelKey)
		}
		matchedKey, matched = key, model
	}
	return matchedKey, matched, matchedKey != "", nil
}

func providerHasEffectiveModel(provider Provider, modelID string) bool {
	for key, model := range provider.Models {
		effective := model.Model
		if effective == "" {
			effective = key
		}
		if effective == modelID {
			return true
		}
	}
	return false
}

// loadFile reads one config layer. An untrusted layer is a workspace
// .caretz/config.json: its modelRules are ignored and it may not define
// providers, so a cloned repository can't point a stored key, the user's
// code, or a managed server binary at something it picks.
func loadFile(path string, untrusted ...bool) (Config, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	if _, ok := raw["localImage"]; ok {
		return Config{}, false, fmt.Errorf("parse config %s: config localImage moved to profiles.general.toolConfig.localImage", path)
	}
	for _, field := range []string{"sandbox", "execution", "tools", "tasks", "output", "systemPrompt", "disabledTools", "toolConfig", "skills", "agents", "plan", "incognito", "request", "modelDiscovery"} {
		if _, ok := raw[field]; ok {
			return Config{}, false, fmt.Errorf("parse config %s: config %s moved to profiles.general.%s", path, field, field)
		}
	}
	if profilesRaw, ok := raw["profiles"]; ok {
		var profiles map[string]map[string]json.RawMessage
		if err := json.Unmarshal(profilesRaw, &profiles); err != nil {
			return Config{}, false, fmt.Errorf("parse config %s profiles: %w", path, err)
		}
		for name, profile := range profiles {
			if _, ok := profile["extends"]; ok {
				return Config{}, false, fmt.Errorf("parse config %s: profiles.%s.extends is not supported; profiles inherit only general", path, name)
			}
			for _, forbidden := range []string{"activeModel", "provider", "model", "reasoning", "reasoningValues"} {
				if _, ok := profile[forbidden]; ok {
					return Config{}, false, fmt.Errorf("parse config %s: profiles.%s.%s is not allowed; model and reasoning selection belong at the root provider model", path, name, forbidden)
				}
			}
		}
	}
	ignoredRules := false
	if len(untrusted) > 0 && untrusted[0] {
		if _, present := raw["provider"]; present {
			return Config{}, false, fmt.Errorf("config %s: provider is not allowed in workspace config; define providers in ~/.config/caretz/config.json or a --config-root config", path)
		}
		if _, present := raw["modelRules"]; present {
			ignoredRules = true
			delete(raw, "modelRules")
			data, _ = json.Marshal(raw)
		}
	}
	if value, present := raw["modelRules"]; present && strings.TrimSpace(string(value)) == "null" {
		return Config{}, false, fmt.Errorf("modelRules in %s must be a list", path)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	if len(untrusted) > 0 && untrusted[0] {
		for name, profile := range cfg.Profiles {
			if _, present := profile.ToolConfig["localImage"]["endpoint"]; present {
				return Config{}, false, fmt.Errorf("config %s: profiles.%s.toolConfig.localImage.endpoint is not allowed in workspace config; set it in ~/.config/caretz/config.json or a --config-root config", path, name)
			}
		}
	}
	if ignoredRules {
		cfg.Warnings = append(cfg.Warnings, "modelRules in workspace config "+path+" ignored; put rules in personal config or --config-root")
	}
	return cfg, true, nil
}

func merge(base, override Config) Config {
	if override.ActiveModel != "" {
		base.ActiveModel = override.ActiveModel
	}
	if override.Profile != "" {
		base.Profile = override.Profile
	}
	if base.Profiles == nil {
		base.Profiles = map[string]Profile{}
	}
	for name, profile := range override.Profiles {
		base.Profiles[name] = mergeProfile(base.Profiles[name], profile)
	}
	if base.Provider == nil {
		base.Provider = map[string]Provider{}
	}
	for key, overrideProvider := range override.Provider {
		base.Provider[key] = mergeProvider(base.Provider[key], overrideProvider)
	}
	return base
}

func mergeProfile(base, override Profile) Profile {
	if override.SystemPrompt != nil {
		base.SystemPrompt = override.SystemPrompt
	}
	if override.Tools != nil {
		base.Tools = append([]string{}, override.Tools...)
	}
	if override.DisabledTools != nil {
		base.DisabledTools = append([]string{}, override.DisabledTools...)
	}
	if override.Skills != nil {
		base.Skills = append([]string{}, override.Skills...)
	}
	if override.Agents != nil {
		base.Agents = append([]string{}, override.Agents...)
	}
	base.ToolConfig = tools.MergeConfig(base.ToolConfig, override.ToolConfig)
	base.Sandbox = mergeSandbox(base.Sandbox, override.Sandbox)
	base.Execution = mergeExecution(base.Execution, override.Execution)
	base.Tasks = mergeTasks(base.Tasks, override.Tasks)
	if override.Goal.MaxTokens != nil {
		base.Goal.MaxTokens = override.Goal.MaxTokens
	}
	if override.Goal.MaxDuration.Set {
		base.Goal.MaxDuration = override.Goal.MaxDuration
	}
	if override.Goal.Planner != nil {
		base.Goal.Planner = override.Goal.Planner
	}
	if override.Goal.Worker != nil {
		base.Goal.Worker = override.Goal.Worker
	}
	if override.Goal.Verifier != nil {
		base.Goal.Verifier = override.Goal.Verifier
	}
	if override.Schedules != nil {
		base.Schedules = override.Schedules
	}
	if strings.TrimSpace(override.Output.Mode) != "" {
		base.Output.Mode = override.Output.Mode
	}
	if strings.TrimSpace(override.Output.Theme) != "" {
		base.Output.Theme = override.Output.Theme
	}
	if override.Plan != nil {
		base.Plan = override.Plan
	}
	if override.Incognito != nil {
		base.Incognito = override.Incognito
	}
	if override.Request.Timeout.Set {
		base.Request.Timeout = override.Request.Timeout
	}
	if override.Request.MaxAttempts != nil {
		base.Request.MaxAttempts = override.Request.MaxAttempts
	}
	if override.ModelDiscovery.Enabled != nil {
		base.ModelDiscovery.Enabled = override.ModelDiscovery.Enabled
	}
	return base
}

func mergeExecution(base, override Execution) Execution {
	if override.Docker.Enabled != nil {
		base.Docker.Enabled = override.Docker.Enabled
	}
	if override.Docker.Image != nil {
		base.Docker.Image = override.Docker.Image
	}
	if override.Docker.IdleTimeout.Set {
		base.Docker.IdleTimeout = override.Docker.IdleTimeout
	}
	if override.Docker.Publish != nil {
		base.Docker.Publish = append([]string{}, override.Docker.Publish...)
	}
	return base
}

func mergeTasks(base, override Tasks) Tasks {
	if override.Enabled != nil {
		base.Enabled = override.Enabled
	}
	return base
}

func mergeSandbox(base, override Sandbox) Sandbox {
	if override.SessionExec != nil {
		base.SessionExec = override.SessionExec
	}
	if override.ReadRoots != nil {
		base.ReadRoots = append([]string{}, override.ReadRoots...)
	}
	if override.WritableRoots != nil {
		base.WritableRoots = append([]string{}, override.WritableRoots...)
	}
	if override.NetworkAllowlist != nil {
		base.NetworkAllowlist = append([]string{}, override.NetworkAllowlist...)
	}
	if override.Env != nil {
		base.Env = append([]string{}, override.Env...)
	}
	if override.EnvLinux != nil {
		base.EnvLinux = append([]string{}, override.EnvLinux...)
	}
	if override.EnvDarwin != nil {
		base.EnvDarwin = append([]string{}, override.EnvDarwin...)
	}
	if override.EnvFiles != nil {
		base.EnvFiles = append([]string{}, override.EnvFiles...)
	}
	if override.EnvFilesLinux != nil {
		base.EnvFilesLinux = append([]string{}, override.EnvFilesLinux...)
	}
	if override.EnvFilesDarwin != nil {
		base.EnvFilesDarwin = append([]string{}, override.EnvFilesDarwin...)
	}
	if override.AllowDocker != nil {
		base.AllowDocker = override.AllowDocker
	}
	if override.AllowLoopback != nil {
		base.AllowLoopback = override.AllowLoopback
	}
	if override.UnrestrictedNetwork != nil {
		base.UnrestrictedNetwork = override.UnrestrictedNetwork
	}
	if override.AskSandboxOff != nil {
		base.AskSandboxOff = override.AskSandboxOff
	}
	if override.AllowSystemBus != nil {
		base.AllowSystemBus = override.AllowSystemBus
	}
	if override.AllowGPU != nil {
		base.AllowGPU = override.AllowGPU
	}
	return base
}

func mergeProvider(base, override Provider) Provider {
	if override.Name != "" {
		base.Name = override.Name
	}
	if override.Options.BaseURL != nil {
		base.Options.BaseURL = override.Options.BaseURL
		base.Local = nil
	}
	if override.Local != nil {
		base.Local = override.Local
	}
	if override.Auth != "" {
		base.Auth = override.Auth
	}
	if override.Options.APIKey != nil {
		base.Options.APIKey = override.Options.APIKey
	}
	if base.Models == nil {
		base.Models = map[string]Model{}
	}
	for key, overrideModel := range override.Models {
		base.Models[key] = mergeModel(base.Models[key], overrideModel)
	}
	return base
}

func mergeModel(base, override Model) Model {
	if override.Name != "" {
		base.Name = override.Name
	}
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Protocol != "" {
		base.Protocol = override.Protocol
	}
	if override.Reasoning != "" {
		base.Reasoning = override.Reasoning
	}
	if override.ReasoningValues != nil {
		base.ReasoningValues = append([]string(nil), override.ReasoningValues...)
	}
	if override.Limit.Context != nil {
		base.Limit.Context = override.Limit.Context
	}
	if override.Limit.ReservedTokens != nil {
		base.Limit.ReservedTokens = override.Limit.ReservedTokens
	}
	if override.Limit.KeepRecentTokens != nil {
		base.Limit.KeepRecentTokens = override.Limit.KeepRecentTokens
	}
	if override.Limit.SummaryTokens != nil {
		base.Limit.SummaryTokens = override.Limit.SummaryTokens
	}
	return base
}

func validate(cfg Config) error {
	if _, err := output.ParseMode(cfg.Runtime.Output.Mode); err != nil {
		return fmt.Errorf("config output.mode: %w", err)
	}
	if _, err := output.ParseTheme(cfg.Runtime.Output.Theme); err != nil {
		return fmt.Errorf("config output.theme: %w", err)
	}
	if strings.TrimSpace(cfg.Profile) == "" {
		return fmt.Errorf("config profile must not be empty")
	}
	if !slices.Contains(BuiltinProfiles, cfg.Profile) {
		if _, ok := cfg.Profiles[cfg.Profile]; !ok {
			return fmt.Errorf("config profile %q is not defined", cfg.Profile)
		}
	}
	if cfg.Runtime.Request.Timeout.Set && cfg.Runtime.Request.Timeout.Duration < 0 {
		return fmt.Errorf("config profiles.%s.request.timeout must be >= 0", cfg.Profile)
	}
	if cfg.Runtime.Request.MaxAttempts != nil && *cfg.Runtime.Request.MaxAttempts < 1 {
		return fmt.Errorf("config profiles.%s.request.maxAttempts must be >= 1", cfg.Profile)
	}
	for name, profile := range cfg.Profiles {
		if _, err := output.ParseMode(profile.Output.Mode); err != nil {
			return fmt.Errorf("config profiles.%s.output.mode: %w", name, err)
		}
		if _, err := output.ParseTheme(profile.Output.Theme); err != nil {
			return fmt.Errorf("config profiles.%s.output.theme: %w", name, err)
		}
		if profile.Request.Timeout.Set && profile.Request.Timeout.Duration < 0 {
			return fmt.Errorf("config profiles.%s.request.timeout must be >= 0", name)
		}
		if profile.Request.MaxAttempts != nil && *profile.Request.MaxAttempts < 1 {
			return fmt.Errorf("config profiles.%s.request.maxAttempts must be >= 1", name)
		}
		if err := validateSchedules(profile.Schedules, name); err != nil {
			return err
		}
	}
	for providerKey, provider := range cfg.Provider {
		if provider.Local != nil && *provider.Local && (provider.Options.BaseURL == nil || normalizeSetupBaseURL(*provider.Options.BaseURL) == "") {
			return fmt.Errorf("config provider %q local: true requires baseURL", providerKey)
		}
		if err := validateProviderAuth(providerKey, provider); err != nil {
			return err
		}
		for modelKey, model := range provider.Models {
			if protocol := strings.TrimSpace(model.Protocol); protocol != "" && protocol != "chat-completions" && protocol != "responses" {
				return fmt.Errorf("config provider %q model %q protocol must be one of chat-completions or responses", providerKey, modelKey)
			}
			if err := validatePositive(model.Limit.Context, "context", providerKey, modelKey); err != nil {
				return err
			}
			if err := validatePositive(model.Limit.ReservedTokens, "reservedTokens", providerKey, modelKey); err != nil {
				return err
			}
			if err := validatePositive(model.Limit.KeepRecentTokens, "keepRecentTokens", providerKey, modelKey); err != nil {
				return err
			}
			if err := validatePositive(model.Limit.SummaryTokens, "summaryTokens", providerKey, modelKey); err != nil {
				return err
			}
			for _, value := range model.ReasoningValues {
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("config provider %q model %q reasoningValues must not contain empty values", providerKey, modelKey)
				}
			}
		}
	}
	return nil
}

func trimStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}

func validateSchedules(schedules *Schedules, profile string) error {
	if schedules == nil {
		return nil
	}
	if strings.TrimSpace(schedules.Reasoning) != "" && strings.TrimSpace(schedules.Model) == "" {
		return fmt.Errorf("config profiles.%s.schedules.reasoning requires schedules.model", profile)
	}
	for _, limit := range []struct {
		field string
		value *int
	}{
		{"context", schedules.Limit.Context},
		{"reservedTokens", schedules.Limit.ReservedTokens},
		{"keepRecentTokens", schedules.Limit.KeepRecentTokens},
		{"summaryTokens", schedules.Limit.SummaryTokens},
	} {
		if limit.value != nil && *limit.value <= 0 {
			return fmt.Errorf("config profiles.%s.schedules.limit.%s must be positive", profile, limit.field)
		}
	}
	return nil
}

func validatePositive(value *int, field, providerKey, modelKey string) error {
	if value != nil && *value <= 0 {
		return fmt.Errorf("config provider %q model %q limit.%s must be positive", providerKey, modelKey, field)
	}
	return nil
}

func parseActiveModel(activeModel string) (string, string, error) {
	providerKey, modelKey, ok := splitModelRef(activeModel)
	if !ok {
		return "", "", fmt.Errorf("activeModel must use provider/model format, got %q", activeModel)
	}
	return providerKey, modelKey, nil
}

func splitModelRef(modelRef string) (string, string, bool) {
	parts := strings.SplitN(modelRef, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

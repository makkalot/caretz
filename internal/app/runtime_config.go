package app

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	caretz "github.com/makkalot/caretz"
	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

type stringListFlag []string

type runtimeFlagValues struct {
	APIKey           string
	BaseURL          string
	Model            string
	ReasoningEffort  string
	ContextWindow    int
	ReservedTokens   int
	KeepRecentTokens int
	SummaryTokens    int
	LLMTimeout       time.Duration
	LLMTimeoutSet    bool
	LLMMaxAttempts   int
	Output           string
	Theme            string
}

// modelSummary is the -v line naming the model a session talks to; source
// says where the choice came from ("--model", "resumed session", ...).
func modelSummary(settings runtimeSettings, source string) string {
	ref := cmp.Or(settings.ActiveModelRef, settings.Model)
	var b strings.Builder
	b.WriteString("model " + ref)
	if source != "" {
		b.WriteString(" (" + source + ")")
	}
	if settings.Model != "" && settings.Model != ref && !strings.HasSuffix(ref, "/"+settings.Model) {
		b.WriteString(" id=" + settings.Model)
	}
	b.WriteString(" endpoint=" + cmp.Or(settings.BaseURL, "default"))
	if settings.Protocol != "" {
		b.WriteString(" protocol=" + settings.Protocol)
	}
	if settings.ReasoningEffort != "" {
		b.WriteString(" reasoning=" + settings.ReasoningEffort)
	}
	if settings.ContextWindow > 0 {
		fmt.Fprintf(&b, " ctx=%d", settings.ContextWindow)
	}
	return b.String()
}

// modelSource names where the session model came from, for modelSummary.
// A model rule may still have replaced it; that prints its own notice.
func modelSource(explicitSource string, restoredSession bool, settings runtimeSettings) string {
	switch {
	case explicitSource != "":
		return explicitSource
	case restoredSession:
		return "resumed session"
	case settings.ActiveModelRef != "":
		return "config activeModel"
	default:
		return "default"
	}
}

type runtimeSettings struct {
	APIKey           string
	BaseURL          string
	Model            string
	Protocol         string
	ActiveModelRef   string
	ReasoningEffort  string
	ContextWindow    int
	ReservedTokens   int
	KeepRecentTokens int
	SummaryTokens    int
	LLMTimeout       time.Duration
	LLMMaxAttempts   int
	OutputMode       output.Mode
	Theme            string
	// ExplicitAPIKey reports that APIKey came from --api-key, which a model
	// rule mapping onto a provider keeps.
	ExplicitAPIKey bool
}

// cachedProviderModels adds the models that caretz setup or caretz models
// --refresh last fetched from each provider. Startup never contacts providers,
// so it stays fast offline and sends no key anywhere but the chat request.
func cachedProviderModels(cfg config.Config, cachePath string, flags baseFlagValues, stderr io.Writer) config.Config {
	if flags.DisableModelDiscovery {
		return cfg.WithDiscoveredModels(nil)
	}
	cache, err := config.LoadModelCache(cachePath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Warning: %v; run caretz models --refresh to rebuild it\n", err)
	}
	return cfg.WithDiscoveredModels(cache.DiscoveredFor(cfg))
}

// providerProbe is the result of asking one provider for its /models list.
type providerProbe struct {
	Key      string
	Provider config.Provider
	Models   []string
	Err      error
}

// probeProviders lists every provider's models concurrently and returns the
// results sorted by provider key. Each provider gets only its own stored key,
// never --api-key or OPENAI_API_KEY, which are meant for one chat endpoint.
func probeProviders(ctx context.Context, cfg config.Config, timeout time.Duration) []providerProbe {
	probes := make([]providerProbe, 0, len(cfg.Provider))
	for key, provider := range cfg.Provider {
		probes = append(probes, providerProbe{Key: key, Provider: provider})
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].Key < probes[j].Key })
	var wg sync.WaitGroup
	for i := range probes {
		probe := &probes[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			baseURL := ""
			if probe.Provider.Options.BaseURL != nil {
				baseURL = strings.TrimSpace(*probe.Provider.Options.BaseURL)
			}
			if baseURL == "" {
				probe.Err = fmt.Errorf("baseURL is not configured")
				return
			}
			apiKey := cmp.Or(cfg.ProviderAPIKey(probe.Key), config.NoAPIKey)
			probe.Models, probe.Err = llm.ListOpenAIModels(ctx, apiKey, baseURL, llm.OpenAIProviderOptions{
				RequestTimeout: timeout,
				MaxAttempts:    1,
			})
		}()
	}
	wg.Wait()
	return probes
}

// probeUnreachable reports whether a failed probe never got an HTTP answer.
// A 401 or 404 means the server is up, just misconfigured or keyless.
func probeUnreachable(probe providerProbe) bool {
	if probe.Err == nil {
		return false
	}
	_, answered := llm.APIErrorStatus(probe.Err)
	return !answered
}

// providerProbeReason condenses a failed probe into a few words.
func providerProbeReason(probe providerProbe) string {
	// Probes send only the stored key, never OPENAI_API_KEY, so a provider
	// that relies on the env key lands here too.
	if status, ok := llm.APIErrorStatus(probe.Err); ok && status == 401 {
		return fmt.Sprintf("key rejected (HTTP 401); run caretz auth set %s", probe.Key)
	}
	// A 403 often means a valid key that lacks permission, not a wrong key.
	if status, ok := llm.APIErrorStatus(probe.Err); ok && status == 403 {
		return fmt.Sprintf("access denied (HTTP 403); check key permissions or run caretz auth set %s", probe.Key)
	}
	msg := probe.Err.Error()
	switch {
	case errors.Is(probe.Err, context.DeadlineExceeded) || strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "i/o timeout"):
		return "timed out"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "no such host"):
		return "host not found"
	case strings.Contains(msg, "baseURL is not configured"):
		return "no baseURL"
	}
	// Network errors end in the useful part, e.g. "connect: no route to host".
	var opErr *net.OpError
	if errors.As(probe.Err, &opErr) && opErr.Err != nil {
		msg = opErr.Err.Error()
		if i := strings.LastIndex(msg, ": "); i >= 0 {
			msg = msg[i+2:]
		}
	}
	const maxReason = 60
	if len(msg) > maxReason {
		msg = msg[:maxReason] + "…"
	}
	return msg
}

type subagentModelResolver struct {
	// policyFor returns the current policy for a workspace, including rules
	// added during the session; nil leaves models unrestricted by rules.
	policyFor      func(workspace string) (config.ModelPolicy, error)
	fileConfig     config.Config
	llmTimeout     time.Duration
	llmMaxAttempts int
	logger         *log.Logger
	allowedModels  map[string]bool
}

type publicSubagentModelResolver struct {
	inner subagentModelResolver
}

func (r publicSubagentModelResolver) ResolveSubagentModel(ctx context.Context, request caretz.SubagentModelRequest) (caretz.SubagentModelResolution, error) {
	resolution, err := r.inner.ResolveSubagentModel(ctx, agent.SubagentModelRequest{
		Name:            request.Name,
		WorkspaceDir:    request.WorkspaceDir,
		Ref:             request.Ref,
		ReasoningEffort: request.ReasoningEffort,
		ValidateOnly:    request.ValidateOnly,
	})
	if err != nil {
		return caretz.SubagentModelResolution{}, err
	}
	return caretz.SubagentModelResolution{
		Ref:              resolution.Ref,
		Model:            resolution.Model,
		Provider:         resolution.Provider,
		ContextWindow:    resolution.ContextWindow,
		ReservedTokens:   resolution.ReservedTokens,
		KeepRecentTokens: resolution.KeepRecentTokens,
		SummaryTokens:    resolution.SummaryTokens,
		BaseURL:          resolution.BaseURL,
		APIKey:           resolution.APIKey,
	}, nil
}

func (r subagentModelResolver) ResolveSubagentModel(ctx context.Context, request agent.SubagentModelRequest) (agent.SubagentModelResolution, error) {
	_ = ctx
	ref := strings.TrimSpace(request.Ref)
	// A subagent or goal role model was named by the user, a definition or
	// the model; a blocked one is refused, never replaced.
	policy := config.ModelPolicy{Workspace: request.WorkspaceDir}
	if r.policyFor != nil {
		var err error
		if policy, err = r.policyFor(request.WorkspaceDir); err != nil {
			return agent.SubagentModelResolution{}, err
		}
	}
	if err := requireModel(r.fileConfig, policy, ref, r.allowedModels); err != nil {
		if request.Name != "" {
			err = fmt.Errorf("%s: %w", request.Name, err)
		}
		return agent.SubagentModelResolution{}, fmt.Errorf("%w; %s", err, config.AllowModelHint(ref))
	}

	resolved, ok, err := r.fileConfig.ResolveModelRef(ref)
	if err != nil {
		return agent.SubagentModelResolution{}, fmt.Errorf("failed to resolve subagent model %q: %w", ref, err)
	}
	if !ok {
		return agent.SubagentModelResolution{}, fmt.Errorf("unknown configured subagent model %q", ref)
	}
	settings, err := runtimeSettingsForModelSwitch(resolved)
	if err != nil {
		return agent.SubagentModelResolution{}, err
	}
	if reasoningEffort := strings.TrimSpace(request.ReasoningEffort); reasoningEffort != "" {
		settings.ReasoningEffort = reasoningEffort
	}
	var logf func(string, ...interface{})
	if r.logger != nil {
		logf = r.logger.Info
		if !request.ValidateOnly {
			r.logger.Info("subagent %s %s", cmp.Or(request.Name, "-"), modelSummary(settings, ""))
		}
	}
	provider := llm.NewOpenAIProviderWithOptions(settings.APIKey, settings.BaseURL, settings.Model, llm.OpenAIProviderOptions{
		RequestTimeout:        r.llmTimeout,
		DisableRequestTimeout: r.llmTimeout == 0,
		MaxAttempts:           r.llmMaxAttempts,
		ReasoningEffort:       settings.ReasoningEffort,
		Protocol:              settings.Protocol,
		Logf:                  logf,
	})
	return agent.SubagentModelResolution{
		Ref:              ref,
		Model:            settings.Model,
		Provider:         provider,
		ContextWindow:    settings.ContextWindow,
		ReservedTokens:   settings.ReservedTokens,
		KeepRecentTokens: settings.KeepRecentTokens,
		SummaryTokens:    settings.SummaryTokens,
		BaseURL:          settings.BaseURL,
		APIKey:           settings.APIKey,
	}, nil
}

type sandboxFlagState struct {
	SessionExecSet         bool
	SessionExecValue       bool
	AllowDockerSet         bool
	UnrestrictedNetworkSet bool
	AllowLoopbackSet       bool
	AllowLoopbackValue     bool
	AskSandboxOffSet       bool
	AskSandboxOffValue     bool
	ReadableRootsSet       bool
	WritableRootsSet       bool
	NetworkAllowlistSet    bool
	EnvironmentSet         bool
	Environment            []string
	EnvironmentFilesSet    bool
	EnvironmentFiles       []string
}

type sandboxRuntimeSettings struct {
	SessionExec         bool
	AllowDocker         bool
	UnrestrictedNetwork bool
	AllowLoopback       bool
	AskSandboxOff       bool
	AllowSystemBus      bool
	AllowGPU            bool
	Disabled            bool
	NetworkAllowlist    []string
	ReadableRoots       []string
	WritableRoots       []string
	HiddenPaths         []string
	ReadOnlyPaths       []string
	Environment         []string
}

const disableSandboxEnv = "CARETZ_DISABLE_SANDBOX"

type configRootFlagState struct {
	ConfigRootSet bool
}

type taskFlagMode string

const (
	taskFlagUnspecified taskFlagMode = ""
	taskFlagEnable      taskFlagMode = "enable"
	taskFlagDisable     taskFlagMode = "disable"
)

type taskToggleFlag struct {
	mode        *taskFlagMode
	modeWhenSet taskFlagMode
}

type baseFlagValues struct {
	resumeModel            string
	Workspace              string         `flag:"workspace" help:"Workspace directory for tools, sandbox, config discovery, and AGENTS.md" group:"Workspace and configuration"`
	ConfigRoot             string         `flag:"config-root" help:"Directory for project config, sessions, and agents. Defaults to .caretz; overrides CARETZ_CONFIG_ROOT." group:"Workspace and configuration"`
	Profile                string         `flag:"profile" help:"Advanced runtime profile (defaults to general)" group:"Workspace and configuration"`
	APIKey                 string         `flag:"api-key" help:"API key (overrides config and OPENAI_API_KEY env var)" group:"Model and context"`
	BaseURL                string         `flag:"base-url" help:"API base URL (overrides config and OPENAI_API_BASE_URL env var)" group:"Model and context"`
	Model                  string         `flag:"model" help:"Model name (overrides config and OPENAI_API_MODEL env var)" group:"Model and context"`
	ReasoningEffort        string         `flag:"reasoning" help:"Reasoning effort sent as reasoning_effort for OpenAI-compatible providers" group:"Model and context"`
	DisableModelDiscovery  bool           `flag:"no-model-discovery" help:"Ignore models cached by caretz setup and caretz models --refresh; use configured models or explicit model settings" group:"Model and context"`
	ContextWindow          int            `flag:"context-window" help:"LLM context window size in tokens (overrides config and CARETZ_CONTEXT_WINDOW env var)" group:"Model and context"`
	ReservedTokens         int            `flag:"reserved-tokens" help:"Tokens reserved for next prompt+response. Defaults to max(8192, contextWindow*0.08). Overrides config and CARETZ_RESERVED_TOKENS env var." group:"Model and context"`
	KeepRecentTokens       int            `flag:"keep-recent-tokens" help:"Tokens of recent messages to keep verbatim after compaction. Defaults to max(8192, contextWindow*0.15). Overrides config and CARETZ_KEEP_RECENT_TOKENS env var." group:"Model and context"`
	SummaryTokens          int            `flag:"summary-tokens" help:"Target tokens for compaction summaries. Defaults to clamp(keepRecentTokens/2, 2000, 6000). Overrides config and CARETZ_SUMMARY_TOKENS env var." group:"Model and context"`
	LLMTimeout             time.Duration  `flag:"llm-timeout" default:"5m" help:"Timeout per LLM API request; use 0 to disable" group:"Model and context"`
	LLMMaxAttempts         int            `flag:"llm-max-attempts" help:"Maximum LLM API request attempts. Defaults to 3." group:"Model and context"`
	SystemPrompt           string         `flag:"system-prompt" help:"System prompt to use instead of the default prompt built from skills and AGENTS.md" group:"Agent behavior"`
	SystemPromptFile       string         `flag:"system-prompt-file" help:"Read system prompt from file instead of building the default prompt" group:"Agent behavior"`
	EnabledTools           stringListFlag `flag:"tools" help:"Tool allowlist. Repeat or use comma-separated names." group:"Agent behavior"`
	DisabledTools          stringListFlag `flag:"disable-tool" help:"Disable this tool. Can be specified multiple times or as comma-separated names." group:"Agent behavior"`
	Tasks                  taskToggleFlag `flag:"tasks" help:"Enable internal task tracking tools" group:"Agent behavior"`
	NoTasks                taskToggleFlag `flag:"no-tasks" help:"Disable internal task tracking tools" group:"Agent behavior"`
	UnrestrictedNetwork    bool           `flag:"unrestricted-network" help:"Allow unrestricted network access in sandbox" group:"Execution and sandbox"`
	AllowLoopback          bool           `flag:"allow-loopback" help:"Allow sandboxed localhost TCP listeners and connections" group:"Execution and sandbox"`
	AllowDocker            bool           `flag:"allow-docker" help:"Allow sandboxed Bash commands to use the host Docker daemon" group:"Execution and sandbox"`
	AskSandboxOff          bool           `flag:"ask-sandbox-off" help:"Ask before rerunning a sandbox-blocked Bash command outside the sandbox" group:"Execution and sandbox"`
	PermissionTimeout      time.Duration  `flag:"permission-timeout" default:"0" help:"Deny an unanswered permission request after this long; 0 waits until answered" group:"Execution and sandbox"`
	SessionExec            bool           `flag:"session-exec" help:"Use persistent native execution state for Bash commands (default true; false runs each command on its own)" group:"Execution and sandbox"`
	PlanMode               bool           `flag:"plan" help:"Run in plan mode: disable file mutation tools and make Bash sandbox read-only" group:"Execution and sandbox"`
	ReadableRoots          stringListFlag `flag:"read-root" help:"Additional directory that tools and the sandbox may read from. Can be specified multiple times." group:"Execution and sandbox"`
	WritableRoots          stringListFlag `flag:"writable-root" help:"Additional directory that tools and the sandbox may write to. Can be specified multiple times." group:"Execution and sandbox"`
	NetworkAllowlist       stringListFlag `flag:"network-allow" help:"Allow sandboxed HTTP/HTTPS traffic to HOST[:PORT] or SOCKS TCP traffic to tcp://HOST:PORT. Can be specified multiple times." group:"Execution and sandbox"`
	SandboxEnvironment     stringListFlag `flag:"sandbox-env" help:"Pass NAME from the host or set NAME=value for sandboxed commands. Can be specified multiple times." group:"Execution and sandbox"`
	SandboxEnvironmentFile stringListFlag `flag:"sandbox-env-file" help:"Read NAME or NAME=value entries from a file for sandboxed commands. Can be specified multiple times." group:"Execution and sandbox"`
	Incognito              bool
	Output                 string
	Theme                  string
	Verbose                bool `flag:"v" help:"Enable informational logging" group:"Output and diagnostics"`
	Debug                  bool `flag:"vv" help:"Enable debug logging" group:"Output and diagnostics"`
	LLMTimeoutSet          bool
	TaskMode               taskFlagMode
	SandboxFlags           sandboxFlagState
	ConfigRootFlags        configRootFlagState
	Changed                map[string]bool
}

func prepareBaseFlags(values *baseFlagValues) {
	values.Tasks = taskToggleFlag{mode: &values.TaskMode, modeWhenSet: taskFlagEnable}
	values.NoTasks = taskToggleFlag{mode: &values.TaskMode, modeWhenSet: taskFlagDisable}
}

func recordBaseFlagVisits(parsed clicmd.ParsedOptions, values *baseFlagValues) {
	values.Changed = map[string]bool{}
	for _, name := range []string{"profile", "api-key", "base-url", "model", "reasoning", "no-model-discovery", "context-window", "reserved-tokens", "keep-recent-tokens", "summary-tokens", "llm-timeout", "llm-max-attempts", "permission-timeout", "system-prompt", "system-prompt-file", "tools", "disable-tool", "tasks", "no-tasks", "allow-docker", "allow-loopback", "ask-sandbox-off", "session-exec", "unrestricted-network", "plan", "read-root", "writable-root", "network-allow", "sandbox-env", "sandbox-env-file", "config-root", "incognito", "output", "theme"} {
		if parsed.Changed(name) {
			values.Changed[name] = true
		}
	}
	for _, name := range []string{"llm-timeout", "allow-docker", "allow-loopback", "ask-sandbox-off", "session-exec", "unrestricted-network", "read-root", "writable-root", "network-allow", "sandbox-env", "sandbox-env-file", "config-root"} {
		if !parsed.Changed(name) {
			continue
		}
		switch name {
		case "llm-timeout":
			values.LLMTimeoutSet = true
		case "allow-docker":
			values.SandboxFlags.AllowDockerSet = true
		case "allow-loopback":
			values.SandboxFlags.AllowLoopbackSet = true
			values.SandboxFlags.AllowLoopbackValue = values.AllowLoopback
		case "ask-sandbox-off":
			values.SandboxFlags.AskSandboxOffSet = true
			values.SandboxFlags.AskSandboxOffValue = values.AskSandboxOff
		case "session-exec":
			values.SandboxFlags.SessionExecSet = true
			values.SandboxFlags.SessionExecValue = values.SessionExec
		case "unrestricted-network":
			values.SandboxFlags.UnrestrictedNetworkSet = true
		case "read-root":
			values.SandboxFlags.ReadableRootsSet = true
		case "writable-root":
			values.SandboxFlags.WritableRootsSet = true
		case "network-allow":
			values.SandboxFlags.NetworkAllowlistSet = true
		case "sandbox-env":
			values.SandboxFlags.EnvironmentSet = true
			values.SandboxFlags.Environment = append([]string(nil), values.SandboxEnvironment...)
		case "sandbox-env-file":
			values.SandboxFlags.EnvironmentFilesSet = true
			values.SandboxFlags.EnvironmentFiles = append([]string(nil), values.SandboxEnvironmentFile...)
		case "config-root":
			values.ConfigRootFlags.ConfigRootSet = true
		}
	}
}

func (f taskToggleFlag) String() string {
	if f.mode == nil {
		return ""
	}
	return string(*f.mode)
}

func (f taskToggleFlag) IsBoolFlag() bool {
	return true
}

func (f taskToggleFlag) Set(value string) error {
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	mode := f.modeWhenSet
	if !enabled {
		if mode == taskFlagEnable {
			mode = taskFlagDisable
		} else {
			mode = taskFlagEnable
		}
	}
	if f.mode == nil {
		return nil
	}
	if *f.mode != taskFlagUnspecified && *f.mode != mode {
		return fmt.Errorf("--tasks and --no-tasks cannot be combined")
	}
	*f.mode = mode
	return nil
}

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("value must not be empty")
	}
	*f = append(*f, value)
	return nil
}

func splitFlagList(values []string) []string {
	var result []string
	seen := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name := strings.TrimSpace(part)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

func toolNameMap(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	result := make(map[string]bool, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			result[name] = true
		}
	}
	return result
}

func resolveRootDir(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("resolving symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stating path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return resolved, nil
}

func resolveConfigRoot(cwd, flagConfigRoot string, flags configRootFlagState, getenv func(string) string) (string, error) {
	root := ""
	if flags.ConfigRootSet {
		root = flagConfigRoot
	} else if envRoot := getenv("CARETZ_CONFIG_ROOT"); strings.TrimSpace(envRoot) != "" {
		root = envRoot
	} else {
		root = filepath.Join(cwd, ".caretz")
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("config root must not be empty")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(cwd, root)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving config root: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("config root is not a directory: %s", absRoot)
		}
		resolved, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return "", fmt.Errorf("resolving config root symlinks: %w", err)
		}
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("stating config root: %w", err)
	}
	return absRoot, nil
}

func resolveRootDirs(paths []string, flagName string) ([]string, error) {
	var roots []string
	for _, root := range paths {
		resolved, err := resolveRootDir(root)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", flagName, root, err)
		}
		roots = append(roots, resolved)
	}
	return roots, nil
}

// Read grants may include an exact Unix socket, such as an SSH agent. Never
// broaden a socket grant to its containing directory.
func resolveReadableRoots(paths []string) ([]string, error) {
	var roots []string
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			resolved, err = filepath.Abs(resolved)
		}
		if err == nil {
			var info os.FileInfo
			info, err = os.Stat(resolved)
			if err == nil && !info.IsDir() && info.Mode()&os.ModeSocket == 0 {
				err = fmt.Errorf("expected a directory or Unix socket")
			}
		}
		if err != nil {
			return nil, fmt.Errorf("invalid --read-root %q: %w", path, err)
		}
		roots = append(roots, resolved)
	}
	return roots, nil
}

func existingDirs(paths []string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
			absPath = resolved
		}
		info, err := os.Stat(absPath)
		if err != nil || !info.IsDir() || seen[absPath] {
			continue
		}
		seen[absPath] = true
		dirs = append(dirs, absPath)
	}
	return dirs
}

func runtimeReadableResourceRoots(skillDirs, agentDirs []string) []string {
	paths := append([]string{}, skillDirs...)
	paths = append(paths, agentDirs...)
	return existingDirs(paths)
}

func skillDirsForRuntime(homeSkillsDir, cwd, configRoot string, configRootSupplied bool) []string {
	dirs := []string{homeSkillsDir}
	if configRootSupplied {
		dirs = append(dirs, filepath.Join(configRoot, "skills"))
	}
	dirs = append(dirs,
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(cwd, ".caretz", "skills"),
	)
	return dirs
}

func agentDirsForRuntime(homeAgentsDir, cwd, configRoot string, configRootSupplied bool) []string {
	dirs := []string{homeAgentsDir}
	if configRootSupplied {
		dirs = append(dirs, filepath.Join(configRoot, "agents"))
	}
	dirs = append(dirs, filepath.Join(cwd, ".caretz", "agents"))
	return dirs
}

// ensureAgentsDirs creates missing agent directories before the sandbox
// starts. The Linux sandbox can only make an existing path read-only, so a
// missing directory would let a session create it and plant an agent that
// later sessions load.
func ensureAgentsDirs(dirs []string, logger *log.Logger) {
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil && logger != nil {
			logger.Warn("could not create agents directory %s: %v", dir, err)
		}
	}
}

func selectSessionAdapter(prompt, adapter string, adapterSet bool, sessionID string) (string, error) {
	defaultAdapter := "tui"
	if prompt != "" {
		defaultAdapter = "cli"
	}
	if !adapterSet {
		return defaultAdapter, nil
	}

	adapter = strings.TrimSpace(adapter)
	switch adapter {
	case "cli", "tui", "slack", "telegram":
	default:
		return "", fmt.Errorf("invalid adapter %q: must be one of cli, tui, slack, telegram", adapter)
	}
	if prompt != "" && (adapter == "slack" || adapter == "telegram") {
		return "", fmt.Errorf("--adapter %s is only supported in interactive TUI mode", adapter)
	}
	if adapter != defaultAdapter && strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("-s is required when -adapter %s is used", adapter)
	}
	return adapter, nil
}

func SystemPromptWithWorkspace(systemPrompt, workspaceDir string) string {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return systemPrompt
	}
	line := "Working directory: " + workspaceDir
	marker := "Working directory:"
	index := strings.LastIndex(systemPrompt, marker)
	if index < 0 {
		return systemPrompt + "\n\n" + line
	}
	lineEnd := strings.IndexByte(systemPrompt[index:], '\n')
	if lineEnd < 0 {
		return systemPrompt[:index] + line
	}
	return systemPrompt[:index] + line + systemPrompt[index+lineEnd:]
}

func systemPromptWithWorkspace(systemPrompt, workspaceDir string) string {
	return SystemPromptWithWorkspace(systemPrompt, workspaceDir)
}

func resolveSandboxSettings(fileConfig config.Config, flags sandboxFlagState, flagReadableRoots, flagWritableRoots, flagNetworkAllowlist []string, flagAllowDocker, flagUnrestrictedNetwork bool) (sandboxRuntimeSettings, error) {
	// Session execution is the default: on Linux its supervisor is PID 1 in a
	// private PID namespace, so nothing a command starts outlives Caretz.
	// Profiles that must see the host's processes, such as ops, opt out.
	settings := sandboxRuntimeSettings{SessionExec: true}
	profile := fileConfig.Operational()
	if profile.Sandbox.SessionExec != nil {
		settings.SessionExec = *profile.Sandbox.SessionExec
	}
	if profile.Sandbox.AllowDocker != nil {
		settings.AllowDocker = *profile.Sandbox.AllowDocker
	}
	if profile.Sandbox.UnrestrictedNetwork != nil {
		settings.UnrestrictedNetwork = *profile.Sandbox.UnrestrictedNetwork
	}
	if profile.Sandbox.AllowLoopback != nil {
		settings.AllowLoopback = *profile.Sandbox.AllowLoopback
	}
	if profile.Sandbox.AskSandboxOff != nil {
		settings.AskSandboxOff = *profile.Sandbox.AskSandboxOff
	}
	if profile.Sandbox.AllowSystemBus != nil {
		settings.AllowSystemBus = *profile.Sandbox.AllowSystemBus
	}
	if profile.Sandbox.AllowGPU != nil {
		settings.AllowGPU = *profile.Sandbox.AllowGPU
	}

	readRootInputs := profile.Sandbox.ReadRoots
	if flags.ReadableRootsSet {
		readRootInputs = appendUniqueStrings(readRootInputs, flagReadableRoots...)
	}
	readableRoots, err := resolveReadableRoots(readRootInputs)
	if err != nil {
		return sandboxRuntimeSettings{}, err
	}
	settings.ReadableRoots = readableRoots

	writableRootInputs := profile.Sandbox.WritableRoots
	if flags.WritableRootsSet {
		writableRootInputs = appendUniqueStrings(writableRootInputs, flagWritableRoots...)
	}
	writableRoots, err := resolveRootDirs(writableRootInputs, "--writable-root")
	if err != nil {
		return sandboxRuntimeSettings{}, err
	}
	settings.WritableRoots = writableRoots

	networkAllowlist := profile.Sandbox.NetworkAllowlist
	if flags.NetworkAllowlistSet {
		networkAllowlist = appendUniqueStrings(networkAllowlist, flagNetworkAllowlist...)
	}
	settings.NetworkAllowlist = append([]string(nil), networkAllowlist...)
	for field, values := range map[string][]string{
		"env": profile.Sandbox.Env, "envLinux": profile.Sandbox.EnvLinux, "envDarwin": profile.Sandbox.EnvDarwin,
	} {
		if err := sandbox.ValidateEnvironment(values); err != nil {
			return sandboxRuntimeSettings{}, fmt.Errorf("sandbox %s: %w", field, err)
		}
	}
	environment := sandboxEnvironmentForOS(profile.Sandbox, runtime.GOOS)
	envFiles := sandboxEnvironmentFilesForOS(profile.Sandbox, runtime.GOOS)
	if flags.EnvironmentFilesSet {
		envFiles = append(envFiles, flags.EnvironmentFiles...)
	}
	for _, path := range envFiles {
		entries, err := readSandboxEnvironmentFile(path)
		if err != nil {
			return sandboxRuntimeSettings{}, err
		}
		environment = append(environment, entries...)
	}
	if flags.EnvironmentSet {
		environment = append(environment, flags.Environment...)
	}
	environment = codingGitHubEnvironment(fileConfig.Profile, environment, os.LookupEnv, hostGitHubAuthToken)
	if err := sandbox.ValidateEnvironment(environment); err != nil {
		return sandboxRuntimeSettings{}, err
	}
	settings.Environment = environment

	if flags.AllowDockerSet {
		settings.AllowDocker = flagAllowDocker
	}
	if flags.SessionExecSet {
		settings.SessionExec = flags.SessionExecValue
	}
	if flags.UnrestrictedNetworkSet {
		settings.UnrestrictedNetwork = flagUnrestrictedNetwork
	}
	if flags.AllowLoopbackSet {
		settings.AllowLoopback = flags.AllowLoopbackValue
	}
	if flags.AskSandboxOffSet {
		settings.AskSandboxOff = flags.AskSandboxOffValue
	}
	settings.Disabled = envTruthy(os.Getenv(disableSandboxEnv))

	return settings, nil
}

func sandboxEnvironmentFilesForOS(sandboxConfig config.Sandbox, goos string) []string {
	files := append([]string(nil), sandboxConfig.EnvFiles...)
	switch goos {
	case "linux":
		files = append(files, sandboxConfig.EnvFilesLinux...)
	case "darwin":
		files = append(files, sandboxConfig.EnvFilesDarwin...)
	}
	return files
}

func readSandboxEnvironmentFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--sandbox-env-file %s: %w", path, err)
	}
	defer func() {
		_ = file.Close()
	}()
	var entries []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("--sandbox-env-file %s: %w", path, err)
	}
	if err := sandbox.ValidateEnvironment(entries); err != nil {
		return nil, fmt.Errorf("--sandbox-env-file %s: %w", path, err)
	}
	return entries, nil
}

func sandboxEnvironmentForOS(sandboxConfig config.Sandbox, goos string) []string {
	environment := append([]string(nil), sandboxConfig.Env...)
	switch goos {
	case "linux":
		environment = append(environment, sandboxConfig.EnvLinux...)
	case "darwin":
		environment = append(environment, sandboxConfig.EnvDarwin...)
	}
	return environment
}

func codingGitHubEnvironment(profile string, environment []string, lookupEnv func(string) (string, bool), authToken func() (string, error)) []string {
	if (profile != "coding" && profile != "ops") || githubTokenConfigured(environment, lookupEnv) {
		return environment
	}
	token, err := authToken()
	if err != nil || strings.TrimSpace(token) == "" {
		return environment
	}
	return append(environment, "GH_TOKEN="+strings.TrimSpace(token))
}

func githubTokenConfigured(environment []string, lookupEnv func(string) (string, bool)) bool {
	for i := len(environment) - 1; i >= 0; i-- {
		name, _, explicit := strings.Cut(environment[i], "=")
		if name != "GH_TOKEN" && name != "GITHUB_TOKEN" {
			continue
		}
		if explicit {
			return true
		}
		if value, ok := lookupEnv(name); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func hostGitHubAuthToken() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func appendUniqueStrings(base []string, additions ...string) []string {
	result := append([]string(nil), base...)
	seen := make(map[string]bool, len(result)+len(additions))
	for _, value := range result {
		seen[value] = true
	}
	for _, value := range additions {
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}

// codingWorktreeGitRoot returns the shared Git metadata directory for a linked
// worktree. A normal checkout keeps .git inside the workspace and needs no
// additional grant. Linked worktrees keep refs, objects, and their own index in
// the parent repository, so the coding profile makes that directory writable,
// as .git already is in a normal checkout.
func codingWorktreeGitRoot(profileName, workspaceDir string) string {
	if profileName != "coding" {
		return ""
	}
	marker := filepath.Join(workspaceDir, ".git")
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return ""
	}
	gitDirText := strings.TrimSpace(string(data))
	if !strings.HasPrefix(gitDirText, "gitdir:") {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(gitDirText, "gitdir:"))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workspaceDir, gitDir)
	}
	gitDir, err = filepath.EvalSymlinks(filepath.Clean(gitDir))
	if err != nil {
		return ""
	}
	commonText, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return ""
	}
	commonDir := strings.TrimSpace(string(commonText))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	commonDir, err = filepath.EvalSymlinks(filepath.Clean(commonDir))
	if err != nil {
		return ""
	}
	if info, err := os.Stat(commonDir); err != nil || !info.IsDir() {
		return ""
	}
	return commonDir
}

// worktreeGitReadOnlyPaths lists the shared Git metadata that would let a
// sandboxed command run code on the host the next time Git runs outside the
// sandbox. These stay read-only inside a writable common directory.
func worktreeGitReadOnlyPaths(commonDir string) []string {
	return []string{
		filepath.Join(commonDir, "hooks"),
		filepath.Join(commonDir, "config"),
		filepath.Join(commonDir, "info"),
	}
}

func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func resolveDisableTasks(fileConfig config.Config, mode taskFlagMode) bool {
	disableTasks := false
	if fileConfig.Operational().Tasks.Enabled != nil {
		disableTasks = !*fileConfig.Operational().Tasks.Enabled
	}
	switch mode {
	case taskFlagEnable:
		disableTasks = false
	case taskFlagDisable:
		disableTasks = true
	}
	return disableTasks
}

func resolveRuntimeSettings(fileConfig config.Config, flags runtimeFlagValues, getenv func(string) string) (runtimeSettings, error) {
	resolvedConfig, err := fileConfig.Resolve()
	envModel := getenv("OPENAI_API_MODEL")
	hasExplicitModel := flags.Model != "" || envModel != ""
	if err != nil && !hasExplicitModel {
		return runtimeSettings{}, fmt.Errorf("failed to resolve config: %w", err)
	}

	settings := runtimeSettings{
		APIKey:           resolvedConfig.APIKey,
		BaseURL:          resolvedConfig.BaseURL,
		Model:            resolvedConfig.Model,
		Protocol:         resolvedConfig.Protocol,
		ActiveModelRef:   fileConfig.ActiveModel,
		ReasoningEffort:  resolvedConfig.ReasoningEffort,
		ContextWindow:    resolvedConfig.ContextWindow,
		ReservedTokens:   resolvedConfig.ReservedTokens,
		KeepRecentTokens: resolvedConfig.KeepRecentTokens,
		SummaryTokens:    resolvedConfig.SummaryTokens,
		LLMTimeout:       llm.DefaultRequestTimeout,
		LLMMaxAttempts:   llm.DefaultMaxAttempts,
	}
	profile := fileConfig.Operational()
	outputValue := profile.Output.Mode
	if envOutput := strings.TrimSpace(getenv("CARETZ_OUTPUT")); envOutput != "" {
		outputValue = envOutput
	}
	if strings.TrimSpace(flags.Output) != "" {
		outputValue = flags.Output
	}
	settings.OutputMode, err = output.ParseMode(outputValue)
	if err != nil {
		return runtimeSettings{}, err
	}
	themeValue := profile.Output.Theme
	if envTheme := strings.TrimSpace(getenv("CARETZ_THEME")); envTheme != "" {
		themeValue = envTheme
	}
	if strings.TrimSpace(flags.Theme) != "" {
		themeValue = flags.Theme
	}
	settings.Theme, err = output.ParseTheme(themeValue)
	if err != nil {
		return runtimeSettings{}, err
	}
	if profile.Request.Timeout.Set {
		settings.LLMTimeout = profile.Request.Timeout.Duration
	}
	if profile.Request.MaxAttempts != nil {
		settings.LLMMaxAttempts = *profile.Request.MaxAttempts
	}

	// A configured provider owns its key: it gets only the one stored for it,
	// so OPENAI_API_KEY never reaches another host. The env key serves ad hoc
	// endpoints, set by a base URL override or used without any provider.
	usesProvider := resolvedConfig.BaseURL != ""
	providerAPIKey := resolvedConfig.APIKey
	if envBaseURL := getenv("OPENAI_API_BASE_URL"); envBaseURL != "" {
		settings.BaseURL = envBaseURL
		usesProvider = false
	}
	if envModel != "" {
		settings.Model = envModel
		settings.Protocol = ""
		settings.ActiveModelRef = ""
	}
	if envReasoningEffort := getenv("OPENAI_REASONING"); envReasoningEffort != "" {
		settings.ReasoningEffort = strings.TrimSpace(envReasoningEffort)
	}
	if envCW := getenv("CARETZ_CONTEXT_WINDOW"); envCW != "" {
		if n, err := strconv.Atoi(envCW); err == nil && n > 0 {
			settings.ContextWindow = n
		}
	}
	if envRT := getenv("CARETZ_RESERVED_TOKENS"); envRT != "" {
		if n, err := strconv.Atoi(envRT); err == nil && n > 0 {
			settings.ReservedTokens = n
		}
	}
	if envKRT := getenv("CARETZ_KEEP_RECENT_TOKENS"); envKRT != "" {
		if n, err := strconv.Atoi(envKRT); err == nil && n > 0 {
			settings.KeepRecentTokens = n
		}
	}
	if envST := getenv("CARETZ_SUMMARY_TOKENS"); envST != "" {
		if n, err := strconv.Atoi(envST); err == nil && n > 0 {
			settings.SummaryTokens = n
		}
	}

	if flags.Model != "" {
		if strings.Contains(flags.Model, "/") {
			modelConfig, ok, err := fileConfig.ResolveExplicitModelRef(flags.Model)
			if err != nil {
				return runtimeSettings{}, fmt.Errorf("failed to resolve --model %q: %w", flags.Model, err)
			}
			if ok {
				overlayResolvedModelConfig(&settings, modelConfig)
				settings.ActiveModelRef = flags.Model
				usesProvider = modelConfig.BaseURL != ""
				providerAPIKey = modelConfig.APIKey
			} else {
				settings.Model = flags.Model
				settings.Protocol = ""
				settings.ActiveModelRef = ""
			}
		} else {
			settings.Model = flags.Model
			settings.Protocol = ""
			settings.ActiveModelRef = ""
		}
	}
	if flags.BaseURL != "" {
		settings.BaseURL = flags.BaseURL
		usesProvider = false
	}
	switch {
	case flags.APIKey != "":
		settings.APIKey = flags.APIKey
		settings.ExplicitAPIKey = true
	case usesProvider:
		settings.APIKey = providerAPIKey
	default:
		settings.APIKey = getenv("OPENAI_API_KEY")
	}
	if flags.ReasoningEffort != "" {
		settings.ReasoningEffort = strings.TrimSpace(flags.ReasoningEffort)
	}
	if flags.ContextWindow > 0 {
		settings.ContextWindow = flags.ContextWindow
	}
	if flags.ReservedTokens > 0 {
		settings.ReservedTokens = flags.ReservedTokens
	}
	if flags.KeepRecentTokens > 0 {
		settings.KeepRecentTokens = flags.KeepRecentTokens
	}
	if flags.SummaryTokens > 0 {
		settings.SummaryTokens = flags.SummaryTokens
	}
	if flags.LLMTimeoutSet {
		if flags.LLMTimeout < 0 {
			return runtimeSettings{}, fmt.Errorf("--llm-timeout must be >= 0")
		}
		settings.LLMTimeout = flags.LLMTimeout
	}
	if flags.LLMMaxAttempts > 0 {
		settings.LLMMaxAttempts = flags.LLMMaxAttempts
	} else if flags.LLMMaxAttempts < 0 {
		return runtimeSettings{}, fmt.Errorf("--llm-max-attempts must be >= 1")
	}

	if settings.APIKey == "" {
		settings.APIKey = "noset"
	}
	if settings.Model == "" {
		return runtimeSettings{}, fmt.Errorf("no model selected: run caretz setup, or set --model, OPENAI_API_MODEL, or activeModel in config")
	}
	if settings.BaseURL == "" {
		return runtimeSettings{}, fmt.Errorf("OPENAI_API_BASE_URL not set (run caretz setup, or configure it with config, an env var, or --base-url)")
	}
	if settings.ContextWindow <= 0 {
		settings.ContextWindow = 200000
	}
	if settings.ReservedTokens <= 0 {
		settings.ReservedTokens = int(math.Max(8192, float64(settings.ContextWindow)*0.08))
	}
	if settings.KeepRecentTokens <= 0 {
		settings.KeepRecentTokens = int(math.Max(8192, float64(settings.ContextWindow)*0.15))
	}
	if settings.SummaryTokens <= 0 {
		settings.SummaryTokens = defaultSummaryTokens(settings.KeepRecentTokens)
	}
	if settings.LLMMaxAttempts <= 0 {
		settings.LLMMaxAttempts = llm.DefaultMaxAttempts
	}

	return settings, nil
}

// overlayResolvedModelConfig points settings at a provider's model. The
// provider owns the key, so one it lacks never falls back to the key before;
// only a typed --api-key wins. The endpoint is replaced even when the provider
// has none, so its key never goes to the previous provider's host.
func overlayResolvedModelConfig(settings *runtimeSettings, resolved config.Resolved) {
	if !settings.ExplicitAPIKey {
		settings.APIKey = cmp.Or(resolved.APIKey, config.NoAPIKey)
	}
	settings.BaseURL = resolved.BaseURL
	if resolved.Model != "" {
		settings.Model = resolved.Model
	}
	settings.Protocol = resolved.Protocol
	if resolved.ReasoningEffort != "" {
		settings.ReasoningEffort = resolved.ReasoningEffort
	}
	if resolved.ContextWindow > 0 {
		settings.ContextWindow = resolved.ContextWindow
	}
	if resolved.ReservedTokens > 0 {
		settings.ReservedTokens = resolved.ReservedTokens
	}
	if resolved.KeepRecentTokens > 0 {
		settings.KeepRecentTokens = resolved.KeepRecentTokens
	}
	if resolved.SummaryTokens > 0 {
		settings.SummaryTokens = resolved.SummaryTokens
	}
}

func runtimeSettingsForModelSwitch(resolved config.Resolved) (runtimeSettings, error) {
	settings := runtimeSettings{
		APIKey:           resolved.APIKey,
		BaseURL:          resolved.BaseURL,
		Model:            resolved.Model,
		Protocol:         resolved.Protocol,
		ReasoningEffort:  resolved.ReasoningEffort,
		ContextWindow:    resolved.ContextWindow,
		ReservedTokens:   resolved.ReservedTokens,
		KeepRecentTokens: resolved.KeepRecentTokens,
		SummaryTokens:    resolved.SummaryTokens,
	}
	if settings.APIKey == "" {
		settings.APIKey = "noset"
	}
	if settings.BaseURL == "" {
		return runtimeSettings{}, fmt.Errorf("model config baseURL is required")
	}
	if settings.Model == "" {
		return runtimeSettings{}, fmt.Errorf("model config model is required")
	}
	if settings.ContextWindow <= 0 {
		settings.ContextWindow = 200000
	}
	if settings.ReservedTokens <= 0 {
		settings.ReservedTokens = int(math.Max(8192, float64(settings.ContextWindow)*0.08))
	}
	if settings.KeepRecentTokens <= 0 {
		settings.KeepRecentTokens = int(math.Max(8192, float64(settings.ContextWindow)*0.15))
	}
	if settings.SummaryTokens <= 0 {
		settings.SummaryTokens = defaultSummaryTokens(settings.KeepRecentTokens)
	}
	return settings, nil
}

func defaultSummaryTokens(keepRecentTokens int) int {
	target := keepRecentTokens / 2
	if target < 2000 {
		return 2000
	}
	if target > 6000 {
		return 6000
	}
	return target
}

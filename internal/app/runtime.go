package app

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	caretz "github.com/makkalot/caretz"
	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/schedules"
	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/skills"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
	libruntime "github.com/makkalot/caretz/lib/runtime"
)

type Runtime struct {
	Ctx                context.Context
	CWD                string
	ConfigRoot         string
	ProjectConfigPath  string
	SessionsDir        string
	HomeConfigPath     string
	HomeSkillsDir      string
	HomeAgentsDir      string
	ConfigRootSupplied bool
	SkillDirs          []string
	AgentsDirs         []string
	FileConfig         config.Config
	Settings           runtimeSettings
	Logger             *log.Logger
	Provider           llm.Provider
	ToolRegistry       *tools.Registry
	SandboxSettings    sandboxRuntimeSettings
	SystemPromptBase   string
	SystemPrompt       string
	Incognito          bool
	PlanMode           bool
	DisableTasks       bool
	StartupResources   startupResources
	Profile            config.Profile
	Client             *caretz.Client
	PermissionTimeout  time.Duration

	// Model rule enforcement for this workspace.
	NoticeWriter io.Writer
	ModelPolicy  config.ModelPolicy
	ModelNotices []string
	// ModelRuleAnswered is set when the rule came from the cloud prompt just
	// answered, whose one-line summary in ModelNotices replaces the rule notice.
	ModelRuleAnswered      bool
	EndpointOverride       string
	EndpointOverrideSource string
	RequestedSettings      *runtimeSettings
	// ExplicitModelSource is "--model" or "OPENAI_API_MODEL" when the user
	// named the model; new sessions then refuse it rather than fall back.
	ExplicitModelSource string
}

type Options struct {
	Tools              []tools.Tool
	SystemPromptSuffix string
	InteractiveRunner  libruntime.InteractiveRunner
	ExplorerRunner     libruntime.ExplorerRunner
	// SlashCommands are added to the built-in commands of the agent command.
	SlashCommands []libruntime.CustomSlashCommand
	// SessionIntegrations are installed on the agent command's session, and
	// carried over to the sessions it starts.
	SessionIntegrations []sessiontools.Factory
	// PermissionPrompts replace the built-in TUI prompt for the permission
	// capabilities they are keyed by.
	PermissionPrompts map[string]slashcmd.PermissionPrompt
	// MessageRenderers draw user and tool messages the built-in TUI would
	// otherwise render itself.
	MessageRenderers []output.MessageRenderer
	BuildInfo        BuildInfo
}

type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

func BuildRuntime(ctx context.Context, args []string, stderr io.Writer) (*Runtime, int) {
	return BuildRuntimeWithOptions(ctx, args, stderr, Options{})
}

func BuildRuntimeWithOptions(ctx context.Context, args []string, stderr io.Writer, opts Options) (*Runtime, int) {
	if ctx == nil {
		ctx = context.Background()
	}
	if stderr == nil {
		stderr = io.Discard
	}
	logger := log.New()

	var cliFlags runtimeCommandOptions
	prepareBaseFlags(&cliFlags.baseFlagValues)
	command := &clicmd.Command{Use: "caretz", Short: "Build a Caretz runtime", Options: &cliFlags, Args: clicmd.NoArgs}
	result, err := command.Parse(args)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		command.Usage(stderr)
		return nil, 2
	}
	if result.Help {
		command.Help(io.Discard)
		return nil, 0
	}
	baseFlags := cliFlags.baseFlagValues
	baseFlags.Output = cliFlags.Output
	baseFlags.Theme = cliFlags.Theme
	baseFlags.Incognito = cliFlags.Incognito
	recordBaseFlagVisits(result.Options, &baseFlags)
	if baseFlags.Debug {
		logger.SetVerbosity(2)
	} else if baseFlags.Verbose {
		logger.SetVerbosity(1)
	}
	rt, code := buildRuntimeFromFlags(ctx, baseFlags, logger, stderr, opts)
	if rt != nil {
		rt.printModelNotices(stderr)
	}
	return rt, code
}

type runtimeCommandOptions struct {
	baseFlagValues
	Incognito bool   `flag:"incognito" help:"Keep the agent session in memory instead of writing session files" group:"Session state"`
	Output    string `flag:"output" help:"Output mode: compact or detailed (overrides config and CARETZ_OUTPUT)" group:"Output and diagnostics"`
	Theme     string `flag:"theme" help:"Color theme: ansi, catppuccin-mocha, dracula, github-dark, or tokyo-night (overrides config and CARETZ_THEME)" group:"Output and diagnostics"`
}

type AdapterSessionOptions struct {
	WorkspaceDir string
	FileSender   tools.FileSender
}

func (rt *Runtime) NewAdapterSession(adapterType, channelID string, opts AdapterSessionOptions) (*caretz.Session, error) {
	if rt == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	if rt.Client == nil {
		return nil, fmt.Errorf("runtime client is nil")
	}
	workspace := opts.WorkspaceDir
	if workspace == "" {
		workspace = rt.CWD
	}
	policy, err := rt.FileConfig.PolicyForWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	requested := rt.Settings
	if rt.RequestedSettings != nil {
		requested = *rt.RequestedSettings
	}
	settings, notice, err := enforceSessionModel(rt.FileConfig, policy, requested, nil, rt.ExplicitModelSource)
	if err != nil {
		return nil, err
	}
	if err := checkModelEndpoint(rt.FileConfig, policy, settings, rt.EndpointOverride, rt.EndpointOverrideSource); err != nil {
		return nil, err
	}
	session, err := rt.Client.NewSession(channelID, caretz.SessionOptions{
		Adapter:           adapterType,
		WorkspaceDir:      workspace,
		FileSender:        opts.FileSender,
		PermissionTimeout: rt.PermissionTimeout,
		Model: &caretz.ModelOptions{
			APIKey: settings.APIKey, BaseURL: settings.BaseURL, Model: settings.Model,
			Protocol: settings.Protocol, ReasoningEffort: settings.ReasoningEffort,
			ContextWindow: settings.ContextWindow, ReservedTokens: settings.ReservedTokens,
			KeepRecentTokens: settings.KeepRecentTokens, SummaryTokens: settings.SummaryTokens,
			Timeout: settings.LLMTimeout, MaxAttempts: settings.LLMMaxAttempts,
		},
	})
	if err != nil {
		return nil, err
	}
	emit := func(message string) error {
		if err := recordModelNotice(session.AgentSession(), message); err != nil {
			return err
		}
		writer := rt.NoticeWriter
		if writer == nil {
			writer = io.Discard
		}
		_, err := fmt.Fprintln(writer, message)
		return err
	}
	if rule := modelRuleNotice(policy); rule != "" && rt.NoticeWriter != nil {
		_, _ = fmt.Fprintln(rt.NoticeWriter, rule)
	}
	if notice := hostedModelRuleNotice(rt.FileConfig, policy, settings.ActiveModelRef, settings.BaseURL); notice != "" && rt.NoticeWriter != nil {
		_, _ = fmt.Fprintln(rt.NoticeWriter, notice)
	}
	if needsCloudConsent(rt.FileConfig, policy, settings.ActiveModelRef, settings.BaseURL) && rt.NoticeWriter != nil {
		_, _ = fmt.Fprintln(rt.NoticeWriter, cloudWorkspaceWarning(workspace, settings.ActiveModelRef, settings.BaseURL))
	}
	if notice != "" {
		if err := emit(notice); err != nil {
			_ = session.Close()
			return nil, err
		}
	}
	session.AgentSession().SetSubagentModelResolver(subagentModelResolver{
		fileConfig: rt.FileConfig, policyFor: rt.FileConfig.PolicyForWorkspace, llmTimeout: settings.LLMTimeout,
		llmMaxAttempts: settings.LLMMaxAttempts, logger: rt.Logger,
	})
	// Adapters get the scheduler here so scheduled runs use the profile's
	// schedules block; an integration installed later under the same name is
	// ignored.
	if err := session.AgentSession().InstallIntegration(schedules.NewIntegrationWithDefaults(scheduleDefaults(rt.FileConfig.Operational().Schedules))); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("failed to restore schedules: %w", err)
	}
	return session, nil
}

func buildRuntimeFromFlags(ctx context.Context, baseFlags baseFlagValues, logger *log.Logger, stderr io.Writer, opts Options) (*Runtime, int) {
	cwd, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("failed to get working directory: %v", err))
	}
	if strings.TrimSpace(baseFlags.Workspace) != "" {
		workspace, err := resolveRootDir(baseFlags.Workspace)
		if err != nil {
			output.Errorf(stderr, "invalid --workspace %q: %v", baseFlags.Workspace, err)
			return nil, 1
		}
		cwd = workspace
	}
	if baseFlags.PermissionTimeout < 0 {
		output.Errorf(stderr, "--permission-timeout must be >= 0")
		return nil, 2
	}
	if strings.TrimSpace(baseFlags.SystemPrompt) != "" && strings.TrimSpace(baseFlags.SystemPromptFile) != "" {
		output.Errorf(stderr, "--system-prompt and --system-prompt-file cannot be combined")
		return nil, 2
	}
	configRoot, err := resolveConfigRoot(cwd, baseFlags.ConfigRoot, baseFlags.ConfigRootFlags, os.Getenv)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return nil, 1
	}
	configRootSupplied := baseFlags.ConfigRootFlags.ConfigRootSet ||
		strings.TrimSpace(os.Getenv("CARETZ_CONFIG_ROOT")) != ""
	projectConfigPath := filepath.Join(configRoot, "config.json")

	homeConfigPath, err := config.HomeConfigPath()
	if err != nil {
		panic(fmt.Sprintf("failed to find home config path: %v", err))
	}
	homeSkillsDir, err := config.HomeSkillsDir()
	if err != nil {
		panic(fmt.Sprintf("failed to find home skills directory: %v", err))
	}
	homeAgentsDir, err := config.HomeAgentsDir()
	if err != nil {
		panic(fmt.Sprintf("failed to find home agents directory: %v", err))
	}
	skillDirs := skillDirsForRuntime(homeSkillsDir, cwd, configRoot, configRootSupplied)
	agentsDirs := agentDirsForRuntime(homeAgentsDir, cwd, configRoot, configRootSupplied)
	ensureAgentsDirs(agentsDirs, logger)

	selectedProfile := strings.TrimSpace(os.Getenv("CARETZ_PROFILE"))
	if baseFlags.Changed["profile"] {
		selectedProfile = baseFlags.Profile
	}
	fileConfig, err := config.LoadProfileWithTrust(homeConfigPath, projectConfigPath, selectedProfile, configRootSupplied)
	if err != nil {
		output.Errorf(stderr, "failed to load config: %v", err)
		return nil, 1
	}
	for _, warning := range fileConfig.Warnings {
		output.Warnf(stderr, "%s", warning)
	}
	policy, err := fileConfig.PolicyForWorkspace(cwd)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return nil, 1
	}
	modelNotices := []string{}
	endpointOverride, endpointSource := os.Getenv("OPENAI_API_BASE_URL"), "OPENAI_API_BASE_URL"
	if baseFlags.BaseURL != "" {
		endpointOverride, endpointSource = baseFlags.BaseURL, "--base-url"
	}
	profile := fileConfig.Operational()
	if profile.ModelDiscovery.Enabled != nil && !*profile.ModelDiscovery.Enabled && !baseFlags.Changed["no-model-discovery"] {
		baseFlags.DisableModelDiscovery = true
	}
	// A rule that allows nothing still admits local models; with none
	// configured, stop before model discovery contacts a hosted provider.
	if policy.Rule != nil && len(policy.Rule.Allow) == 0 && len(fileConfig.LocalSelectors()) == 0 {
		_, _, err := policy.Select(fileConfig, fileConfig.ActiveModel, nil)
		output.Errorf(stderr, "%v", err)
		return nil, 1
	}
	fileConfig = cachedProviderModels(fileConfig, config.ModelCachePath(homeConfigPath), baseFlags, stderr)
	profile = fileConfig.Operational()
	slashcmd.ConfigureGoal(profile.Goal)
	if profile.Plan != nil && !baseFlags.Changed["plan"] {
		baseFlags.PlanMode = *profile.Plan
	}
	if profile.Incognito != nil && !baseFlags.Changed["incognito"] {
		baseFlags.Incognito = *profile.Incognito
	}
	runtimeFlags := runtimeFlagValues{
		APIKey: baseFlags.APIKey, BaseURL: baseFlags.BaseURL, Model: baseFlags.Model,
		ReasoningEffort: baseFlags.ReasoningEffort, ContextWindow: baseFlags.ContextWindow,
		ReservedTokens: baseFlags.ReservedTokens, KeepRecentTokens: baseFlags.KeepRecentTokens,
		SummaryTokens: baseFlags.SummaryTokens, LLMTimeout: baseFlags.LLMTimeout,
		LLMTimeoutSet: baseFlags.LLMTimeoutSet, LLMMaxAttempts: baseFlags.LLMMaxAttempts,
		Output: baseFlags.Output, Theme: baseFlags.Theme,
	}
	var requestedSettings *runtimeSettings
	if original, err := resolveRuntimeSettings(fileConfig, runtimeFlags, os.Getenv); err == nil {
		requestedSettings = &original
	}
	// explicitModelSource names where a model the user chose came from; such a
	// model must be allowed, while activeModel from config may fall back.
	explicitModelSource := ""
	if env := os.Getenv("OPENAI_API_MODEL"); env != "" {
		explicitModelSource = "OPENAI_API_MODEL"
	}
	if baseFlags.Model != "" {
		explicitModelSource = "--model"
	}
	selectionConfig := fileConfig
	if policy.Rule != nil {
		requested, source := fileConfig.ActiveModel, ""
		if baseFlags.resumeModel != "" {
			requested, source = baseFlags.resumeModel, "resumed session"
		}
		if env := os.Getenv("OPENAI_API_MODEL"); env != "" {
			requested, source = env, explicitModelSource
		}
		if baseFlags.Model != "" {
			requested, source = baseFlags.Model, explicitModelSource
		}
		selected := requested
		if source != "" {
			if err := policy.Require(fileConfig, requested); err != nil {
				output.Errorf(stderr, "%s: %v; start with an allowed model (--model), then %s", source, err, config.AllowModelHint(requested))
				return nil, 1
			}
		} else {
			var notice string
			selected, notice, err = policy.Select(fileConfig, requested, nil)
			if err != nil {
				output.Errorf(stderr, "%v", err)
				return nil, 1
			}
			if notice != "" {
				modelNotices = append(modelNotices, notice)
			}
		}
		// Resolve through the normal explicit-model path, with the selected model's defaults.
		runtimeFlags.Model = selected
		selectionConfig.ActiveModel = selected
	}
	runtimeConfig, err := resolveRuntimeSettings(selectionConfig, runtimeFlags, os.Getenv)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return nil, 1
	}
	// The theme is process-wide: every Style, including ones already
	// created, renders with it from here on.
	if err := output.SetTheme(runtimeConfig.Theme); err != nil {
		output.Errorf(stderr, "%v", err)
		return nil, 1
	}

	if err := checkModelEndpoint(fileConfig, policy, runtimeConfig, endpointOverride, endpointSource); err != nil {
		output.Errorf(stderr, "%v", err)
		return nil, 1
	}
	sandboxSettings, err := resolveSandboxSettings(fileConfig, baseFlags.SandboxFlags, baseFlags.ReadableRoots, baseFlags.WritableRoots, baseFlags.NetworkAllowlist, baseFlags.AllowDocker, baseFlags.UnrestrictedNetwork)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return nil, 1
	}
	if gitRoot := codingWorktreeGitRoot(fileConfig.Profile, cwd); gitRoot != "" {
		sandboxSettings.ReadableRoots = appendUniqueStrings(sandboxSettings.ReadableRoots, gitRoot)
		sandboxSettings.WritableRoots = appendUniqueStrings(sandboxSettings.WritableRoots, gitRoot)
		sandboxSettings.ReadOnlyPaths = appendUniqueStrings(sandboxSettings.ReadOnlyPaths, worktreeGitReadOnlyPaths(gitRoot)...)
	}
	sandboxSettings.ReadableRoots = append(sandboxSettings.ReadableRoots, runtimeReadableResourceRoots(skillDirs, agentsDirs)...)
	sandboxSettings.ReadOnlyPaths = appendUniqueStrings(sandboxSettings.ReadOnlyPaths, agentsDirs...)
	sandboxSettings.HiddenPaths = []string{homeConfigPath, projectConfigPath, config.CredentialsPath(homeConfigPath)}
	disableTasks := resolveDisableTasks(fileConfig, baseFlags.TaskMode)

	discoveredSkills, err := skills.DiscoverSkills(skillDirs, logger)
	if err != nil {
		output.Warnf(stderr, "skill discovery failed: %v", err)
	} else {
		for _, s := range discoveredSkills {
			logger.Debug("  Skill: %s - %s (%s)", s.Name, s.Description, s.Location)
		}
	}
	discoveredSkills = filterSkills(discoveredSkills, profile.Skills)

	sandboxConfig := sandbox.Config{
		UnrestrictedNetwork: sandboxSettings.UnrestrictedNetwork,
		AllowLoopback:       sandboxSettings.AllowLoopback,
		NetworkAllowlist:    sandboxSettings.NetworkAllowlist,
		ReadableRoots:       sandboxSettings.ReadableRoots,
		WritableRoots:       sandboxSettings.WritableRoots,
		AllowDocker:         sandboxSettings.AllowDocker,
		AllowSystemBus:      sandboxSettings.AllowSystemBus,
		AllowGPU:            sandboxSettings.AllowGPU,
		HiddenPaths:         sandboxSettings.HiddenPaths,
		ReadOnlyPaths:       sandboxSettings.ReadOnlyPaths,
		SessionPaths:        []string{filepath.Join(configRoot, "sessions")},
		Environment:         sandboxSettings.Environment,
	}
	if rgPath, err := exec.LookPath("rg"); err == nil {
		if resolved, err := filepath.EvalSymlinks(rgPath); err == nil {
			sandboxConfig.ExtraBinaries = append(sandboxConfig.ExtraBinaries, resolved)
			logger.Debug("Found rg at: %s (resolved: %s)", rgPath, resolved)
		}
	}

	customSystemPrompt := skills.GeneralSystemPrompt
	switch fileConfig.Profile {
	case "coding":
		customSystemPrompt = skills.BaseSystemPrompt
	case "compact":
		customSystemPrompt = skills.CompactSystemPrompt
	case "ops":
		customSystemPrompt = skills.OpsSystemPrompt
	}
	if profile.SystemPrompt != nil {
		customSystemPrompt = *profile.SystemPrompt
	}
	if baseFlags.Changed["system-prompt"] {
		customSystemPrompt = strings.TrimSpace(baseFlags.SystemPrompt)
	}
	if strings.TrimSpace(baseFlags.SystemPromptFile) != "" {
		systemPromptFile := baseFlags.SystemPromptFile
		if !filepath.IsAbs(systemPromptFile) {
			systemPromptFile = filepath.Join(cwd, systemPromptFile)
		}
		data, err := os.ReadFile(systemPromptFile)
		if err != nil {
			output.Errorf(stderr, "read system prompt file %s: %v", baseFlags.SystemPromptFile, err)
			return nil, 1
		}
		customSystemPrompt = string(data)
	}
	if suffix := strings.TrimSpace(opts.SystemPromptSuffix); suffix != "" {
		if strings.TrimSpace(customSystemPrompt) == "" {
			customSystemPrompt = skills.BaseSystemPrompt
		}
		customSystemPrompt = strings.TrimSpace(customSystemPrompt) + "\n\n" + suffix
	}

	sandboxRunner := sandbox.NewRunner(sandboxConfig)
	runSandboxedOptions := sandboxRunner.RunContextOptions
	if sandboxSettings.Disabled {
		runSandboxedOptions = sandbox.RunUnsandboxed
	}
	tavilyAPIKey := cmp.Or(os.Getenv("TAVILY_API_KEY"), fileConfig.SearchAPIKey("tavily"))
	braveSearchAPIKey := cmp.Or(os.Getenv("BRAVE_SEARCH_API_KEY"), fileConfig.SearchAPIKey("brave"))
	registry := &tools.Registry{
		Logger:              logger,
		RunSandboxed:        sandboxRunner.Run,
		RunSandboxedContext: sandboxRunner.RunContext,
		RunSandboxedOptions: runSandboxedOptions,
		TavilyAPIKey:        tavilyAPIKey,
		BraveSearchAPIKey:   braveSearchAPIKey,
		ToolConfig:          profile.ToolConfig,
		SandboxDisabled:     sandboxSettings.Disabled,
		SessionExec:         sandboxSettings.SessionExec,
		AskSandboxOff:       sandboxSettings.AskSandboxOff,
		RunUnsandboxed:      sandbox.RunApproved,
		SandboxEnvironment:  sandboxRunner.Environment,
		NewSandboxSession:   func() *sandbox.Session { return sandboxRunner.NewSession(sandboxSettings.Disabled) },
		DisableTasks:        disableTasks,
		ReadableRoots:       sandboxSettings.ReadableRoots,
		WritableRoots:       sandboxSettings.WritableRoots,
		HiddenPaths:         sandboxSettings.HiddenPaths,
		EnabledTools:        profileToolMap(profile.Tools, baseFlags.EnabledTools, baseFlags.Changed["tools"]),
		DisabledTools:       toolNameMap(resolvedList(profile.DisabledTools, baseFlags.DisabledTools, baseFlags.Changed["disable-tool"])),
	}
	registry.WorkspaceDir = cwd
	registry.SetPlanMode(baseFlags.PlanMode)
	registry = registry.WithSandboxConfig(sandboxConfig)
	if err := registerCustomTools(registry, opts.Tools); err != nil {
		output.Errorf(stderr, "%v", err)
		return nil, 2
	}
	if err := registry.ValidateConfig(); err != nil {
		output.Errorf(stderr, "invalid tool config: %v", err)
		return nil, 2
	}
	if err := validateLocalImageModel(fileConfig, profile.ToolConfig["localImage"]); err != nil {
		output.Errorf(stderr, "invalid tool config: %v", err)
		return nil, 2
	}

	agentInstructions, err := skills.LoadAgentInstructions(cwd)
	if err != nil {
		output.Warnf(stderr, "AGENTS.md could not be loaded: %v", err)
	}
	startupResources := startupResources{}
	if strings.TrimSpace(agentInstructions) != "" {
		startupResources.Context = append(startupResources.Context, "AGENTS.md")
	}
	for _, skill := range discoveredSkills {
		startupResources.Skills = append(startupResources.Skills, skill.Name)
	}
	configuredAgents, err := subagents.DiscoverFromDirs(agentsDirs)
	if err != nil {
		output.Warnf(stderr, "subagent discovery failed: %v", err)
	} else {
		configuredAgents = filterAgents(configuredAgents, profile.Agents)
		for _, configuredAgent := range configuredAgents {
			startupResources.Agents = append(startupResources.Agents, configuredAgent.Name)
		}
	}

	systemPrompt := skills.BuildSystemPromptWithOptions(skills.SystemPromptOptions{
		BasePrompt:        customSystemPrompt,
		Skills:            discoveredSkills,
		CWD:               cwd,
		AgentInstructions: agentInstructions,
		Tools:             registry.ToolDefinitions(),
	})
	logger.Info("%s", startupSummary(startupSummaryInput{
		Profile:            fileConfig.Profile,
		SandboxDisabled:    sandboxSettings.Disabled,
		PlanMode:           baseFlags.PlanMode,
		CustomSystemPrompt: profile.SystemPrompt != nil || baseFlags.Changed["system-prompt"] || baseFlags.Changed["system-prompt-file"],
		Resources:          startupResources,
		Brave:              registry.BraveSearchAPIKey != "",
		Tavily:             registry.TavilyAPIKey != "",
	}))

	provider := llm.NewOpenAIProviderWithOptions(runtimeConfig.APIKey, runtimeConfig.BaseURL, runtimeConfig.Model, llm.OpenAIProviderOptions{
		RequestTimeout:        runtimeConfig.LLMTimeout,
		DisableRequestTimeout: runtimeConfig.LLMTimeout == 0,
		MaxAttempts:           runtimeConfig.LLMMaxAttempts,
		ReasoningEffort:       runtimeConfig.ReasoningEffort,
		Protocol:              runtimeConfig.Protocol,
		Logf:                  logger.Info,
	})

	stateDir := configRoot
	if baseFlags.Incognito {
		stateDir = ""
	}
	client, err := caretz.NewClient(ctx, caretz.ClientOptions{
		Model: caretz.ModelOptions{
			APIKey:           runtimeConfig.APIKey,
			BaseURL:          runtimeConfig.BaseURL,
			Model:            runtimeConfig.Model,
			ReasoningEffort:  runtimeConfig.ReasoningEffort,
			Protocol:         runtimeConfig.Protocol,
			ContextWindow:    runtimeConfig.ContextWindow,
			ReservedTokens:   runtimeConfig.ReservedTokens,
			KeepRecentTokens: runtimeConfig.KeepRecentTokens,
			SummaryTokens:    runtimeConfig.SummaryTokens,
			Timeout:          runtimeConfig.LLMTimeout,
			MaxAttempts:      runtimeConfig.LLMMaxAttempts,
		},
		WorkspaceDir:         cwd,
		PreparedSystemPrompt: systemPrompt,
		Tools:                opts.Tools,
		Sandbox: caretz.SandboxOptions{
			Disabled:            sandboxSettings.Disabled,
			SessionExec:         sandboxSettings.SessionExec,
			ReadOnly:            baseFlags.PlanMode,
			ReadableRoots:       sandboxSettings.ReadableRoots,
			WritableRoots:       sandboxSettings.WritableRoots,
			NetworkAllowlist:    sandboxSettings.NetworkAllowlist,
			UnrestrictedNetwork: sandboxSettings.UnrestrictedNetwork,
			AllowLoopback:       sandboxSettings.AllowLoopback,
			AllowDocker:         sandboxSettings.AllowDocker,
			AllowSystemBus:      sandboxSettings.AllowSystemBus,
			AllowGPU:            sandboxSettings.AllowGPU,
			AskSandboxOff:       sandboxSettings.AskSandboxOff,
			HiddenPaths:         sandboxSettings.HiddenPaths,
			ReadOnlyPaths:       sandboxSettings.ReadOnlyPaths,
			Env:                 sandboxSettings.Environment,
		},
		State:             caretz.StateOptions{Dir: stateDir},
		Logger:            logger,
		TavilyAPIKey:      tavilyAPIKey,
		BraveSearchAPIKey: braveSearchAPIKey,
		ToolsConfig:       caretz.ToolOptions(profile.ToolConfig),
		EnabledTools:      profileToolMap(profile.Tools, baseFlags.EnabledTools, baseFlags.Changed["tools"]),
		DisabledTools:     toolNameMap(resolvedList(profile.DisabledTools, baseFlags.DisabledTools, baseFlags.Changed["disable-tool"])),
		AgentsDirs:        agentsDirs,
		AllowedAgents:     resourceNameMap(profile.Agents),
		DisableTasks:      disableTasks,
		SubagentModelResolver: publicSubagentModelResolver{inner: subagentModelResolver{
			fileConfig:     fileConfig,
			policyFor:      fileConfig.PolicyForWorkspace,
			llmTimeout:     runtimeConfig.LLMTimeout,
			llmMaxAttempts: runtimeConfig.LLMMaxAttempts,
			logger:         logger,
		}},
	})
	if err != nil {
		output.Errorf(stderr, "failed to create caretz client: %v", err)
		return nil, 1
	}

	return &Runtime{
		Ctx:                ctx,
		CWD:                cwd,
		ConfigRoot:         configRoot,
		ProjectConfigPath:  projectConfigPath,
		SessionsDir:        filepath.Join(configRoot, "sessions"),
		HomeConfigPath:     homeConfigPath,
		HomeSkillsDir:      homeSkillsDir,
		HomeAgentsDir:      homeAgentsDir,
		ConfigRootSupplied: configRootSupplied,
		SkillDirs:          skillDirs,
		AgentsDirs:         agentsDirs,
		FileConfig:         fileConfig,
		Settings:           runtimeConfig,
		Logger:             logger,
		Provider:           provider,
		ToolRegistry:       registry,
		SandboxSettings:    sandboxSettings,
		SystemPromptBase:   customSystemPrompt,
		SystemPrompt:       systemPrompt,
		Incognito:          baseFlags.Incognito,
		PlanMode:           baseFlags.PlanMode,
		DisableTasks:       disableTasks,
		StartupResources:   startupResources,
		Profile:            profile,
		Client:             client,
		PermissionTimeout:  baseFlags.PermissionTimeout,

		NoticeWriter:           stderr,
		RequestedSettings:      requestedSettings,
		ModelPolicy:            policy,
		ModelNotices:           modelNotices,
		EndpointOverride:       endpointOverride,
		EndpointOverrideSource: endpointSource,
		ExplicitModelSource:    explicitModelSource,
	}, 0
}

type startupSummaryInput struct {
	Profile            string
	SandboxDisabled    bool
	PlanMode           bool
	CustomSystemPrompt bool
	Resources          startupResources
	Brave              bool
	Tavily             bool
}

// startupSummary is the single -v line describing what a run was set up with.
func startupSummary(in startupSummaryInput) string {
	parts := []string{"startup profile=" + cmp.Or(in.Profile, "default")}
	switch {
	case in.SandboxDisabled:
		parts = append(parts, "sandbox=off("+disableSandboxEnv+")")
	case in.PlanMode:
		parts = append(parts, "sandbox=on(read-only)")
	default:
		parts = append(parts, "sandbox=on")
	}
	if len(in.Resources.Context) > 0 {
		parts = append(parts, "context="+strings.Join(in.Resources.Context, ","))
	}
	if in.CustomSystemPrompt {
		parts = append(parts, "system-prompt=custom")
	}
	if len(in.Resources.Skills) > 0 {
		parts = append(parts, "skills="+strings.Join(in.Resources.Skills, ","))
	}
	if len(in.Resources.Agents) > 0 {
		parts = append(parts, "agents="+strings.Join(in.Resources.Agents, ","))
	}
	var web []string
	if in.Brave {
		web = append(web, "brave")
	}
	if in.Tavily {
		web = append(web, "tavily")
	}
	if len(web) > 0 {
		parts = append(parts, "web="+strings.Join(web, ","))
	}
	return strings.Join(parts, " ")
}

func skillNames(discovered []skills.Skill) string {
	names := make([]string, 0, len(discovered))
	for _, skill := range discovered {
		names = append(names, skill.Name)
	}
	return strings.Join(names, ",")
}

func registerCustomTools(registry *tools.Registry, customTools []tools.Tool) error {
	for _, tool := range customTools {
		if err := registry.RegisterTool(tool); err != nil {
			return err
		}
	}
	return nil
}

func resolvedList(configured []string, flags stringListFlag, explicitlySet bool) []string {
	if explicitlySet {
		return splitFlagList(flags)
	}
	if len(configured) == 1 && configured[0] == "*" {
		return nil
	}
	return append([]string(nil), configured...)
}

// validateLocalImageModel checks that a localImage model used without an
// endpoint names a configured provider model. Model rules are applied per
// workspace when the tool runs.
func validateLocalImageModel(cfg config.Config, raw map[string]any) error {
	endpoint, _ := raw["endpoint"].(string)
	ref, _ := raw["model"].(string)
	if ref = strings.TrimSpace(ref); strings.TrimSpace(endpoint) != "" || ref == "" {
		return nil
	}
	if _, ok, err := cfg.ResolveModelRef(ref); err != nil {
		return fmt.Errorf("tools.localImage.model %q: %w", ref, err)
	} else if !ok {
		return fmt.Errorf("tools.localImage.model %q is not a configured provider/model; use a provider/model reference, or set endpoint to call a vision server directly", ref)
	}
	return nil
}

func profileToolMap(configured []string, flags stringListFlag, explicitlySet bool) map[string]bool {
	if !explicitlySet && len(configured) == 1 && configured[0] == "*" {
		return nil
	}
	list := resolvedList(configured, flags, explicitlySet)
	if configured == nil && !explicitlySet {
		return nil
	}
	if len(list) == 1 && list[0] == "*" {
		return nil
	}
	result := map[string]bool{}
	for _, name := range list {
		result[name] = true
	}
	return result
}

func resourceNameMap(configured []string) map[string]bool {
	if configured == nil {
		return nil
	}
	if len(configured) == 1 && configured[0] == "*" {
		return nil
	}
	result := map[string]bool{}
	for _, name := range configured {
		result[name] = true
	}
	return result
}

func filterSkills(items []skills.Skill, allowed []string) []skills.Skill {
	if allowed == nil {
		return items
	}
	if len(allowed) == 1 && allowed[0] == "*" {
		return items
	}
	wanted := toolNameMap(allowed)
	out := make([]skills.Skill, 0, len(items))
	for _, item := range items {
		if wanted[item.Name] {
			out = append(out, item)
		}
	}
	return out
}

func filterAgents(items []subagents.Definition, allowed []string) []subagents.Definition {
	if allowed == nil {
		return items
	}
	if len(allowed) == 1 && allowed[0] == "*" {
		return items
	}
	wanted := toolNameMap(allowed)
	out := make([]subagents.Definition, 0, len(items))
	for _, item := range items {
		if wanted[item.Name] {
			out = append(out, item)
		}
	}
	return out
}

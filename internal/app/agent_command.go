package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	caretz "github.com/makkalot/caretz"
	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/chatgptauth"
	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/llm"
	libruntime "github.com/makkalot/caretz/lib/runtime"
)

func runAgentCommand(ctx context.Context, parsed clicmd.ParsedOptions, values agentCommandOptions, stdout, stderr io.Writer, opts Options) int {
	logger := log.New()
	prompt := values.Prompt
	promptFile := values.PromptFile
	flagSessionID := values.SessionID
	flagAdapter := values.Adapter
	baseFlags := values.baseFlagValues
	baseFlags.Incognito = values.Incognito
	baseFlags.Output = values.Output
	baseFlags.Theme = values.Theme
	if values.Version {
		_, _ = fmt.Fprintln(stdout, formatBuildInfo(opts.BuildInfo))
		return 0
	}
	if prompt != "" && promptFile != "" {
		output.Errorf(stderr, "-p and --prompt-file cannot be combined")
		return 2
	}
	if promptFile != "" {
		data, err := os.ReadFile(promptFile)
		if err != nil {
			output.Errorf(stderr, "read prompt file %s: %v", promptFile, err)
			return 1
		}
		prompt = string(data)
	}
	adapterSet := parsed.Changed("adapter")
	recordBaseFlagVisits(parsed, &baseFlags)
	if baseFlags.Debug {
		logger.SetVerbosity(2)
	} else if baseFlags.Verbose {
		logger.SetVerbosity(1)
	}
	if baseFlags.Incognito && strings.TrimSpace(flagSessionID) != "" {
		output.Errorf(stderr, "--incognito and -s cannot be combined")
		return 2
	}
	selectedAdapter, err := selectSessionAdapter(prompt, flagAdapter, adapterSet, flagSessionID)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 2
	}
	if err := restoreSessionProfile(&baseFlags, selectedAdapter, flagSessionID); err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}

	rt, code := buildRuntimeFromFlags(ctx, baseFlags, logger, stderr, opts)
	if code != 0 {
		return code
	}
	if code, handled := runBangPrompt(ctx, prompt, rt.ToolRegistry, stdout, stderr); handled {
		return code
	}
	baseFlags.Incognito = rt.Incognito
	baseFlags.PlanMode = rt.PlanMode
	if baseFlags.Incognito && strings.TrimSpace(flagSessionID) != "" {
		output.Errorf(stderr, "incognito mode and -s cannot be combined")
		return 2
	}
	cwd := rt.CWD
	sessionsDir := rt.SessionsDir
	agentsDirs := rt.AgentsDirs
	fileConfig := rt.FileConfig
	runtimeConfig := rt.Settings
	projectConfigPath := rt.ProjectConfigPath

	sessionID := flagSessionID
	if sessionID == "" {
		sessionID = uuid.NewV4().String()[:8]
	}

	adapterType := selectedAdapter
	sessionRuntimeFile := sessionRuntimePath(sessionsDir, adapterType, sessionID)
	restoredSession := false
	if !baseFlags.Incognito && strings.TrimSpace(baseFlags.Model) == "" && strings.TrimSpace(os.Getenv("OPENAI_API_MODEL")) == "" {
		state, found, err := loadSessionRuntime(sessionRuntimeFile)
		if err != nil {
			output.Errorf(stderr, "%v", err)
			return 1
		}
		if found {
			restored, err := restoreRestrictedSession(fileConfig, rt.ModelPolicy, runtimeConfig, state, nil)
			if err != nil {
				output.Errorf(stderr, "%v", err)
				return 1
			}
			applyExplicitSessionRuntimeOverrides(&restored, runtimeConfig, baseFlags, os.Getenv)
			runtimeConfig = restored
			rt.Settings = restored
			restoredSession = true
		}
	}

	requestedRef := runtimeConfig.ActiveModelRef
	if requestedRef == "" {
		requestedRef = runtimeConfig.Model
	}
	ruleFile := rt.HomeConfigPath
	if rt.ConfigRootSupplied {
		var err error
		if ruleFile, err = config.ModelRulesFile(rt.HomeConfigPath, projectConfigPath); err != nil {
			output.Errorf(stderr, "%v", err)
			return 1
		}
	}
	// consentSummary replaces the rule and switch notices when the user just
	// answered the cloud prompt, so the outcome is shown once, briefly.
	var consentSummary string
	if needsCloudConsent(fileConfig, rt.ModelPolicy, requestedRef, runtimeConfig.BaseURL) {
		if isTerminalStream(stdin) && isTerminalWriter(stderr) {
			var err error
			consentSummary, err = confirmCloudWorkspace(terminalCloudChooser(stdin, stderr), fileConfig, &rt.ModelPolicy, requestedRef, runtimeConfig.BaseURL, ruleFile)
			if err != nil {
				output.Errorf(stderr, "%v", err)
				return 1
			}
		} else {
			_, _ = fmt.Fprintln(stderr, cloudWorkspaceWarning(cwd, requestedRef, runtimeConfig.BaseURL))
		}
	}

	var notice string
	var policyErr error
	runtimeConfig, notice, policyErr = enforceModelSettings(fileConfig, rt.ModelPolicy, runtimeConfig, nil)
	if policyErr == nil {
		policyErr = checkModelEndpoint(fileConfig, rt.ModelPolicy, runtimeConfig, rt.EndpointOverride, rt.EndpointOverrideSource)
	}
	if policyErr != nil {
		output.Errorf(stderr, "%v", policyErr)
		return 1
	}
	if consentSummary != "" {
		notice = consentSummary
		rt.ModelRuleAnswered = true
	}
	if notice != "" {
		rt.ModelNotices = append(rt.ModelNotices, notice)
	}
	rt.Settings = runtimeConfig
	if notice := hostedModelRuleNotice(fileConfig, rt.ModelPolicy, runtimeConfig.ActiveModelRef, runtimeConfig.BaseURL); notice != "" {
		rt.ModelNotices = append(rt.ModelNotices, notice)
	}
	// The built-in TUI shows the rule in its banner and replays recorded
	// switches, so only prompt mode and external runners print them here.
	// Quiet one-shot runs leave them out; they are still recorded in the session.
	builtinTUI := prompt == "" && opts.InteractiveRunner == nil
	quietPrompt := prompt != "" && logger.Verbosity() == 0
	if !builtinTUI && !quietPrompt {
		rt.printModelNotices(stderr)
	}
	logger.Info("%s", modelSummary(runtimeConfig, modelSource(rt.ExplicitModelSource, restoredSession, runtimeConfig)))
	logger.Info("session %s adapter=%s workspace=%s", sessionID, adapterType, cwd)
	if prompt == "" && adapterSet && adapterType != "tui" {
		logger.Debug("Debug-attaching TUI to %s session %s; current directory is the tool workspace", adapterType, sessionID)
	}

	if !baseFlags.Incognito {
		if err := requireSessionRepairCheck(sessionsDir, sessionID); err != nil {
			var repairRequired *sessionRepairRequired
			if errors.As(err, &repairRequired) {
				_, _ = fmt.Fprintf(stderr, "Cannot safely reopen session %s yet.\n\n", sessionID)
				if repairRequired.Reason == "incomplete_tail_detected" {
					_, _ = fmt.Fprintln(stderr, "caretz found that the saved conversation ends inside an unfinished assistant/tool turn.")
				} else {
					_, _ = fmt.Fprintln(stderr, "caretz found an assistant/tool turn that did not finish before a later message was recorded.")
				}
				_, _ = fmt.Fprintln(stderr, "Continuing without repair would force caretz to hide part of the saved conversation to make the model history valid.")
				_, _ = fmt.Fprintln(stderr)
				if len(repairRequired.PendingToolCallIDs) > 0 {
					_, _ = fmt.Fprintf(stderr, "Pending tool call(s): %s\n", strings.Join(repairRequired.PendingToolCallIDs, ", "))
				} else {
					_, _ = fmt.Fprintln(stderr, "All recorded tool calls have results, but the assistant never wrote a final response.")
				}
				_, _ = fmt.Fprintln(stderr)
				_, _ = fmt.Fprintf(stderr, "Inspect details:  caretz session inspect %s\n", sessionID)
				_, _ = fmt.Fprintf(stderr, "Repair safely:   caretz session repair %s\n", sessionID)
				_, _ = fmt.Fprintln(stderr)
				_, _ = fmt.Fprintln(stderr, "Repair backs up context.jsonpl, injects synthetic recovery messages at the broken turn, and rewrites the context log.")
				return 1
			}
			if !isSessionNotFound(err) {
				output.Errorf(stderr, "failed to inspect session repair state: %v", err)
				return 1
			}
		}
	}

	publicSession, err := rt.Client.NewSession(sessionID, caretz.SessionOptions{
		Adapter:           adapterType,
		PermissionTimeout: rt.PermissionTimeout,
		Model: &caretz.ModelOptions{
			APIKey:           runtimeConfig.APIKey,
			BaseURL:          runtimeConfig.BaseURL,
			Model:            runtimeConfig.Model,
			Protocol:         runtimeConfig.Protocol,
			ReasoningEffort:  runtimeConfig.ReasoningEffort,
			ContextWindow:    runtimeConfig.ContextWindow,
			ReservedTokens:   runtimeConfig.ReservedTokens,
			KeepRecentTokens: runtimeConfig.KeepRecentTokens,
			SummaryTokens:    runtimeConfig.SummaryTokens,
			Timeout:          runtimeConfig.LLMTimeout,
			MaxAttempts:      runtimeConfig.LLMMaxAttempts,
		},
	})
	if err != nil {
		output.Errorf(stderr, "failed to create agent session: %v", err)
		return 1
	}
	session := publicSession.AgentSession()
	for _, notice := range rt.ModelNotices {
		if err := recordModelNotice(session, notice); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
	}
	// The workspace's policy is rt.ModelPolicy, which also holds rules added
	// during this run; other directories use the configured rules.
	policyFor := func(workspace string) (config.ModelPolicy, error) {
		if workspace == cwd {
			return rt.ModelPolicy, nil
		}
		return fileConfig.PolicyForWorkspace(workspace)
	}
	session.SetSubagentModelResolver(subagentModelResolver{fileConfig: fileConfig, policyFor: policyFor, llmTimeout: runtimeConfig.LLMTimeout, llmMaxAttempts: runtimeConfig.LLMMaxAttempts, logger: logger})
	if !baseFlags.Incognito {
		if err := saveSessionRuntime(sessionRuntimeFile, sessionRuntimeStateFromSettings(runtimeConfig, fileConfig.Profile)); err != nil {
			output.Errorf(stderr, "%v", err)
			return 1
		}
	}

	// List every model: switching to one the rule blocks asks to override it.
	currentModelChoices := func() []config.ModelChoice {
		return fileConfig.ModelChoices()
	}
	modelChoices := currentModelChoices()
	currentModelRef := runtimeConfig.ActiveModelRef
	modelSwitcher := func(ctx context.Context, session *agent.AgentSession, ref, reasoningEffort string) (slashcmd.ModelSwitchResult, error) {
		warn := func(message string) { _, _ = fmt.Fprintln(stderr, message) }
		targetURL := modelSwitchBaseURL(fileConfig, ref)
		if err := confirmModelOverride(ctx, session, fileConfig, &rt.ModelPolicy, ruleFile, ref, targetURL, warn); err != nil {
			return slashcmd.ModelSwitchResult{}, err
		}
		if err := confirmCloudModelSwitch(ctx, session, fileConfig, &rt.ModelPolicy, ruleFile, cwd, ref, targetURL, warn); err != nil {
			return slashcmd.ModelSwitchResult{}, err
		}
		resolved, ok, err := fileConfig.ResolveModelRef(ref)
		if err != nil {
			return slashcmd.ModelSwitchResult{}, fmt.Errorf("failed to resolve model %q: %w", ref, err)
		}
		if !ok {
			return slashcmd.ModelSwitchResult{}, fmt.Errorf("unknown configured model %q", ref)
		}
		settings, err := runtimeSettingsForModelSwitch(resolved)
		if err != nil {
			return slashcmd.ModelSwitchResult{}, err
		}
		if strings.TrimSpace(reasoningEffort) != "" {
			settings.ReasoningEffort = strings.TrimSpace(reasoningEffort)
		}
		provider := chatgptauth.NewProvider(settings.APIKey, settings.BaseURL, settings.Model, llm.OpenAIProviderOptions{
			RequestTimeout:        runtimeConfig.LLMTimeout,
			DisableRequestTimeout: runtimeConfig.LLMTimeout == 0,
			MaxAttempts:           runtimeConfig.LLMMaxAttempts,
			ReasoningEffort:       settings.ReasoningEffort,
			Protocol:              settings.Protocol,
			Logf:                  logger.Info,
		})
		if err := session.SwitchModel(provider, settings.ContextWindow, settings.ReservedTokens, settings.KeepRecentTokens, settings.SummaryTokens); err != nil {
			return slashcmd.ModelSwitchResult{}, err
		}
		if err := recordModelNotice(session, fmt.Sprintf("model switched from %s to %s", currentModelRef, ref)); err != nil {
			return slashcmd.ModelSwitchResult{}, err
		}
		currentModelRef = ref
		if !baseFlags.Incognito {
			state := sessionRuntimeState{ModelRef: ref, Model: settings.Model, ReasoningEffort: settings.ReasoningEffort, Profile: fileConfig.Profile}
			path := sessionRuntimePath(session.BaseDir(), session.AdapterType(), session.ChannelID())
			if err := saveSessionRuntime(path, state); err != nil {
				return slashcmd.ModelSwitchResult{}, err
			}
		}
		if err := config.SaveProjectActiveModel(projectConfigPath, ref); err != nil {
			return slashcmd.ModelSwitchResult{}, fmt.Errorf("failed to save active model %q: %w", ref, err)
		}
		return slashcmd.ModelSwitchResult{Ref: ref, Model: settings.Model, ReasoningEffort: settings.ReasoningEffort}, nil
	}
	if !rt.ModelRuleAnswered {
		rt.StartupResources.ModelRule = modelRuleSummary(rt.ModelPolicy)
	}
	if rt.ModelPolicy.Rule != nil {
		rt.StartupResources.ModelRulePath = rt.ModelPolicy.Rule.Path
	}
	modelsCommand := slashcmd.ModelsCommand{
		ChoicesFunc: currentModelChoices,
		RulesReport: func() string {
			// Re-read config so rules saved during the session are listed.
			current, err := config.LoadProfileWithTrust(rt.HomeConfigPath, projectConfigPath, fileConfig.Profile, rt.ConfigRootSupplied)
			if err != nil {
				return fmt.Sprintf("error: reading model rules: %v", err)
			}
			return modelRulesReport(current.ModelRules, rt.ModelPolicy.Rule, cwd)
		},
		AllowModel: func(_ context.Context, session *agent.AgentSession, ref string) (string, error) {
			rule, message, err := allowModelCommand(fileConfig, &rt.ModelPolicy, ruleFile, ref)
			if err != nil || rule == nil {
				return message, err
			}
			if fileConfig, err = fileConfig.WithModelRule(*rule); err != nil {
				return "", err
			}
			return message, recordModelNotice(session, message)
		},
		CurrentRef: func(session *agent.AgentSession) string {
			if currentModelRef != "" {
				return currentModelRef
			}
			return session.Model()
		},
		Switcher: modelSwitcher,
	}
	logger.Debug("Preparing command registry")
	slashCommandRegistry, err := newSlashCommandRegistry(cwd, agentsDirs, rt.Profile.Agents, rt.FileConfig.Profile, modelsCommand)
	if err != nil {
		panic(fmt.Sprintf("failed to create command registry: %v", err))
	}
	if err := registerCustomSlashCommands(slashCommandRegistry, opts.SlashCommands); err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	for _, factory := range opts.SessionIntegrations {
		if err := session.InstallIntegration(factory); err != nil {
			output.Errorf(stderr, "failed to install session integration: %v", err)
			return 1
		}
	}

	if prompt == "" {
		var interactiveErr error
		if opts.InteractiveRunner != nil {
			interactiveErr = opts.InteractiveRunner(libruntime.InteractiveParams{
				ModelRule:            rt.ModelPolicy.Description(),
				ModelNotices:         append([]string(nil), rt.ModelNotices...),
				Session:              session,
				Logger:               logger,
				Model:                runtimeConfig.Model,
				ModelRef:             runtimeConfig.ActiveModelRef,
				Profile:              rt.FileConfig.Profile,
				ReasoningEffort:      runtimeConfig.ReasoningEffort,
				Root:                 cwd,
				AgentsDirs:           agentsDirs,
				SlashCommandRegistry: publicSlashCommandRegistry{inner: slashCommandRegistry},
				ModelChoices:         publicModelChoices(modelChoices),
			})
		} else {
			interactiveErr = startTUI(session, logger, runtimeConfig.Model, runtimeConfig.ActiveModelRef, rt.FileConfig.Profile, cwd, agentsDirs, slashCommandRegistry, modelChoices, rt.PlanMode, runtimeConfig.OutputMode, !rt.Incognito, rt.ConfigRoot, rt.ConfigRootSupplied, rt.StartupResources, scheduleDefaults(rt.FileConfig.Operational().Schedules), tuiHooks{permissionPrompts: opts.PermissionPrompts, messageRenderers: opts.MessageRenderers})
		}
		if interactiveErr != nil {
			output.Errorf(stderr, "%v", interactiveErr)
			return 1
		}
		return 0
	}

	// One-shot mode prints only the answer, like other harnesses' -p; -v adds
	// the interactive activity rendering on stderr alongside the logs.
	if logger.Verbosity() > 0 {
		cliOutput := output.NewCLIOutput(runtimeConfig.OutputMode)
		cliOutput.SetMessageRenderers(opts.MessageRenderers)
		if err := cliOutput.RegisterHandlers(session); err != nil {
			panic(fmt.Sprintf("failed to register cli output handlers: %v", err))
		}
	} else if err := output.NewPromptOutput(stdout).RegisterHandlers(session); err != nil {
		panic(fmt.Sprintf("failed to register prompt output handlers: %v", err))
	}

	if result, handled, err := slashCommandRegistry.Execute(context.Background(), session, prompt); handled {
		if err != nil {
			output.Errorf(stderr, "%v", err)
			return 1
		}
		if result.Message != "" && !result.MessagePublished {
			_, _ = fmt.Fprintln(stdout, result.Message)
		}
		if result.ExitCode != 0 {
			return result.ExitCode
		}
		return 0
	}

	_, err = session.Call(context.Background(), prompt)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	return 0
}

func restoreSessionProfile(flags *baseFlagValues, adapterType, sessionID string) error {
	if flags == nil || strings.TrimSpace(sessionID) == "" || flags.Incognito {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory for session profile: %w", err)
	}
	if strings.TrimSpace(flags.Workspace) != "" {
		cwd, err = resolveRootDir(flags.Workspace)
		if err != nil {
			return fmt.Errorf("invalid --workspace %q: %w", flags.Workspace, err)
		}
	}
	configRoot, err := resolveConfigRoot(cwd, flags.ConfigRoot, flags.ConfigRootFlags, os.Getenv)
	if err != nil {
		return err
	}
	state, found, err := loadSessionRuntime(sessionRuntimePath(filepath.Join(configRoot, "sessions"), adapterType, sessionID))
	if err != nil {
		return err
	}
	if found {
		flags.resumeModel = state.ModelRef
		if flags.resumeModel == "" {
			flags.resumeModel = state.Model
		}
	}
	if found && state.Profile != "" && !flags.Changed["profile"] && strings.TrimSpace(os.Getenv("CARETZ_PROFILE")) == "" {
		flags.Profile = state.Profile
		flags.Changed["profile"] = true
	}
	return nil
}

func runBangPrompt(ctx context.Context, prompt string, registry *tools.Registry, stdout, stderr io.Writer) (int, bool) {
	if !strings.HasPrefix(prompt, "!") {
		return 0, false
	}
	command := strings.TrimSpace(strings.TrimPrefix(prompt, "!"))
	if command == "" {
		output.Errorf(stderr, "! requires a command")
		return 2, true
	}
	if registry == nil || !registry.IsToolEnabled("Bash") {
		output.Errorf(stderr, "Bash tool is disabled")
		return 1, true
	}
	result, err := registry.RunInteractiveCommand(ctx, command)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 1, true
	}
	_, _ = io.WriteString(stdout, result.Stdout)
	_, _ = io.WriteString(stderr, result.Stderr)
	// With --ask-sandbox-off, a refused command can rerun on the host once
	// the user approves, as the model's Bash commands can. Without anyone to
	// ask, the sandboxed result stands.
	if request := registry.InteractiveSandboxOff(result); request != nil {
		allowed, err := tools.RequestPermission(ctx, llm.ToolCall{Name: "Bash"}, request)
		if err != nil && !errors.Is(err, tools.ErrPermissionApprovalUnavailable) {
			output.Errorf(stderr, "%v", err)
			return 1, true
		}
		if allowed {
			if result, err = registry.RunApprovedInteractiveCommand(ctx, command); err != nil {
				output.Errorf(stderr, "%v", err)
				return 1, true
			}
			if !result.Displayed {
				_, _ = io.WriteString(stdout, result.Stdout)
				_, _ = io.WriteString(stderr, result.Stderr)
			}
		}
	}
	if result.TimedOut {
		output.Errorf(stderr, "command timed out")
		return 124, true
	}
	return result.ExitCode, true
}

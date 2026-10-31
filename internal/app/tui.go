package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
	"unicode"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/herdr"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/schedules"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/progress"
	"github.com/makkalot/caretz/lib/types"
	"golang.org/x/term"
)

// Package-level streams default to the real OS streams but are variables so
// tests can substitute fakes. The interactive loop reads stdin and writes its
// prompts/banner/activity to stderr; assistant responses are written to stdout
// by the output handlers.
var (
	stdin  io.Reader = os.Stdin
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

var errExternalWake = errors.New("external wake")

type startupResources struct {
	ModelRule     string
	ModelRulePath string
	Context       []string
	Skills        []string
	Agents        []string
}

// scheduleDefaults turns the profile's schedules block into the model
// settings of scheduled runs.
func scheduleDefaults(cfg *config.Schedules) schedules.Defaults {
	if cfg == nil {
		return schedules.Defaults{}
	}
	defaults := schedules.Defaults{Model: strings.TrimSpace(cfg.Model), Reasoning: strings.TrimSpace(cfg.Reasoning)}
	limit := func(value *int) int {
		if value == nil {
			return 0
		}
		return *value
	}
	limits := eventing.ContextLimits{
		Context:          limit(cfg.Limit.Context),
		ReservedTokens:   limit(cfg.Limit.ReservedTokens),
		KeepRecentTokens: limit(cfg.Limit.KeepRecentTokens),
		SummaryTokens:    limit(cfg.Limit.SummaryTokens),
	}
	if limits != (eventing.ContextLimits{}) {
		defaults.Limits = &limits
	}
	return defaults
}

// tuiHooks are the embedder's additions to the built-in TUI.
type tuiHooks struct {
	permissionPrompts map[string]slashcmd.PermissionPrompt
	messageRenderers  []output.MessageRenderer
}

func (h tuiHooks) newCLIOutput(mode output.Mode) *output.CLIOutput {
	cliOutput := output.NewCLIOutput(mode)
	cliOutput.SetMessageRenderers(h.messageRenderers)
	return cliOutput
}

func startTUI(session *agent.AgentSession, logger *log.Logger, model, modelRef, profile, root string, agentsDirs []string, slashCommandRegistry *slashcmd.Registry, modelChoices []config.ModelChoice, planMode bool, outputMode output.Mode, resumable bool, configRoot string, configRootSupplied bool, resources startupResources, scheduled schedules.Defaults, hooks tuiHooks) error {
	logger.Debug("Starting dependency-light interactive UI")
	cliOutput := hooks.newCLIOutput(outputMode)
	cliOutput.EnableReasoningStreaming()
	if err := cliOutput.RegisterHandlers(session); err != nil {
		return fmt.Errorf("failed to register interactive output handlers: %w", err)
	}
	if err := session.InstallIntegration(schedules.NewIntegrationWithDefaults(scheduled)); err != nil {
		return fmt.Errorf("failed to restore schedules: %w", err)
	}

	printBanner(profile)
	printStartupResources(resources)
	if err := replaySession(cliOutput, session); err != nil {
		return fmt.Errorf("failed to replay session: %w", err)
	}

	reporter := herdr.NewFromEnvironment(herdr.Options{
		Adapter: session.AdapterType(), ConfigRoot: configRoot,
		ConfigRootSupplied: configRootSupplied, Resumable: resumable,
	})
	state := &tuiState{session: session, cliOutput: cliOutput, registry: slashCommandRegistry, outputMode: outputMode, resumable: resumable, lifecycle: reporter, hooks: hooks}
	state.reportLifecycle(herdr.StateIdle, "")
	defer state.close()
	if isTerminalStream(stdin) {
		state.asyncWake = make(chan struct{}, 1)
		if err := state.watchAsyncRuns(); err != nil {
			return err
		}
	}
	if state.asyncWake != nil {
		return runRawTUI(state, root, agentsDirs)
	}
	return runScannerTUI(state)
}

// tuiState holds the mutable interactive state shared by the raw and scanner
// input loops so command handling and session switching behave identically in
// both.
type tuiState struct {
	session     *agent.AgentSession
	cliOutput   *output.CLIOutput
	registry    *slashcmd.Registry
	runCommand  commandRunner
	outputMode  output.Mode
	resumable   bool
	clearScreen func()
	asyncWake   chan struct{}
	asyncEvents eventing.Subscription
	lifecycle   lifecycleReporter
	hooks       tuiHooks
}

type lifecycleReporter interface {
	Report(state, message, sessionID string)
	Close()
}

type commandRunner func(func(context.Context) (slashcmd.Result, bool, error)) (slashcmd.Result, bool, error)

func (s *tuiState) watchAsyncRuns() error {
	subscription, err := s.session.SubscribeAuditEvents(eventing.AsyncRunRequested)
	if err != nil {
		return err
	}
	s.asyncEvents = subscription
	go func() {
		for range subscription.Events() {
			select {
			case s.asyncWake <- struct{}{}:
			default:
			}
		}
	}()
	return nil
}

func (s *tuiState) close() {
	if s.asyncEvents != nil {
		s.asyncEvents.Close()
	}
	_ = s.session.Close()
	if s.lifecycle != nil {
		s.lifecycle.Close()
	}
}

func (s *tuiState) reportLifecycle(state, message string) {
	if s.lifecycle != nil {
		s.lifecycle.Report(state, message, s.session.ChannelID())
	}
}

// processLine handles a single input line. Blank lines are ignored. It reports
// whether the loop should exit and any fatal error. Regular (non-command)
// prompts are run through the agent with Ctrl+C cancellation support.
func (s *tuiState) processLine(text string) (exit bool, err error) {
	return s.processLineWith(text, runPrompt)
}

type promptRunner func(*agent.AgentSession, string) error

func (s *tuiState) processLineWith(text string, runner promptRunner) (exit bool, err error) {
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
	s.reportLifecycle(herdr.StateWorking, "")
	defer func() { s.reportLifecycle(herdr.StateIdle, "") }()
	bangPrompt := strings.HasPrefix(text, "!")
	commandEchoed := bangPrompt || s.registry.Handles(text)
	if commandEchoed {
		s.cliOutput.RenderUserInput(text)
	}
	executeCommand := func(ctx context.Context) (slashcmd.Result, bool, error) {
		if bangPrompt {
			ctx = tools.WithPermissionRequester(ctx, s.session.RequestPermission)
			_, handled := runBangPrompt(ctx, text, s.session.ToolRegistry(), stdout, stderr)
			return slashcmd.Result{}, handled, nil
		}
		commandCtx := slashcmd.WithTerminal(slashcmd.WithProgress(ctx, s.cliOutput.RenderCommandProgress), stderr)
		return s.registry.Execute(commandCtx, s.session, text)
	}
	var result slashcmd.Result
	var handled bool
	var execErr error
	if s.runCommand != nil {
		result, handled, execErr = s.runCommand(executeCommand)
	} else {
		result, handled, execErr = executeCommand(context.Background())
	}
	if handled {
		if execErr != nil {
			if agent.IsCleanStop(execErr) {
				return false, nil
			}
			output.Errorf(stderr, "%v", execErr)
			return false, nil
		}
		if result.NewSession != nil {
			oldSession := s.session
			oldEvents := s.asyncEvents
			s.session = result.NewSession
			s.cliOutput = s.hooks.newCLIOutput(s.outputMode)
			if regErr := s.cliOutput.RegisterHandlers(s.session); regErr != nil {
				return false, fmt.Errorf("failed to register switched session handlers: %w", regErr)
			}
			if replayErr := replaySession(s.cliOutput, s.session); replayErr != nil {
				return false, fmt.Errorf("failed to replay switched session: %w", replayErr)
			}
			if oldEvents != nil {
				oldEvents.Close()
			}
			_ = oldSession.Close()
			if s.asyncWake != nil {
				if watchErr := s.watchAsyncRuns(); watchErr != nil {
					return false, watchErr
				}
			}
			if s.clearScreen != nil {
				s.clearScreen()
				// The first echo was on the screen that /clear replaced.
				if commandEchoed {
					s.cliOutput.RenderUserInput(text)
				}
			}
			if result.Message != "" && !result.MessagePublished {
				_, _ = fmt.Fprint(stderr, s.cliOutput.FormatCommandResult(result.Message))
			}
			return false, nil
		}
		if result.Message != "" && !result.MessagePublished {
			_, _ = fmt.Fprint(stderr, s.cliOutput.FormatCommandResult(result.Message))
		}
		if result.Exit {
			return true, nil
		}
		return false, nil
	}

	callErr := runner(s.session, text)
	if callErr != nil {
		if agent.IsCleanStop(callErr) {
			return false, nil
		}
		output.Errorf(stderr, "%v", callErr)
	}
	return false, nil
}

func replaySession(cliOutput *output.CLIOutput, session *agent.AgentSession) error {
	cliOutput.ReplayMessages(session.GetContextMessages())
	goalEvents, err := session.GoalEvents()
	if err != nil {
		return err
	}
	cliOutput.ReplayGoals(goalEvents)
	runs, err := session.SubagentReplays()
	if err != nil {
		return err
	}
	cliOutput.ReplaySubagents(runs)
	notices, err := modelPolicyNotices(session)
	if err != nil {
		return err
	}
	// Earlier switches are history; only the latest explains the model now.
	if len(notices) > 0 {
		output.Notef(stderr, "%s", notices[len(notices)-1])
	}
	return nil
}

// runScannerTUI is the fallback loop for non-TTY stdin (piped input or input
// redirection). It keeps the simple line-oriented behavior.
func runScannerTUI(state *tuiState) error {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		exit, err := state.processLine(strings.TrimSpace(scanner.Text()))
		if err != nil {
			return err
		}
		if exit {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

// runRawTUI is the interactive loop for a real TTY. It uses the raw-terminal
// line editor for @-mention completion and exits on Ctrl+C or Ctrl+D while
// idle.
func runRawTUI(state *tuiState, root string, agentsDirs []string) error {
	style := output.NewStyle(stderr)
	editor := newInputEditor(stdin, stderr, style.Prompt("> "), style, root, agentsDirs, state.registry)
	editor.completion.filterAgents(state.session.AllowedAgents())
	editor.planMode = func() bool { return state.session.PlanMode() }
	editor.togglePlanMode = func() { state.session.SetPlanMode(!state.session.PlanMode()) }
	// Read through state so the footer follows /model, /clear and session switches.
	editor.status = func() editorStatus {
		return editorStatus{
			Model:         state.session.Model(),
			Reasoning:     state.session.ReasoningEffort(),
			ContextTokens: state.session.EstimatedContextTokens(),
			ContextWindow: state.session.ContextWindow(),
			Usage:         state.cliOutput.LatestUsage(),
		}
	}
	state.clearScreen = editor.runtime.ClearScreen
	state.runCommand = func(call func(context.Context) (slashcmd.Result, bool, error)) (slashcmd.Result, bool, error) {
		return runRawCommand(editor.terminal, state.session, call, state.lifecycle, state.hooks.permissionPrompts)
	}
	defer func() {
		state.clearScreen = nil
		state.runCommand = nil
	}()
	firstPrompt := true
	for {
		if !firstPrompt {
			_, _ = fmt.Fprintln(stderr)
		}
		firstPrompt = false

		line, err := editor.readLine(state.asyncWake)
		if err != nil {
			if errors.Is(err, errExternalWake) {
				goto redrawn
			redrawn:
				continue
			}
			if errors.Is(err, ErrInterrupted) {
				if state.resumable {
					_, _ = fmt.Fprintln(stderr, style.Muted("Resume this session with: ")+style.Accent("caretz -s "+state.session.ChannelID()))
				}
				return nil
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		exit, err := state.processLineWith(strings.TrimSpace(line), func(session *agent.AgentSession, text string) error {
			return runRawPrompt(editor.terminal, session, text, state.lifecycle, state.hooks.permissionPrompts)
		})
		if err != nil {
			return err
		}
		if exit {
			return nil
		}
	}
}

// runRawPrompt runs the agent while the raw TUI loop handles immediate cancellation
// requests, permission prompts, and immediate Ctrl-C cancellation. The loop is
// entirely in-process and does not require a socket listener.
func runRawPrompt(terminal *ansi.Terminal, session *agent.AgentSession, text string, lifecycle lifecycleReporter, prompts map[string]slashcmd.PermissionPrompt) error {
	subscription, err := session.SubscribeAuditEvents(eventing.PermissionRequested)
	if err != nil {
		return err
	}
	defer subscription.Close()
	return runInteractiveRawEventLoop(terminal, subscription.Events(), func(ctx context.Context, stop <-chan struct{}) error {
		ctx = progress.With(ctx, func(message string) { _, _ = fmt.Fprintln(terminal.Writer(), message) })
		_, err := session.CallUntilStopWithPermissions(ctx, text, stop)
		return err
	}, session.ResolvePermission, permissionLifecycle(lifecycle, session), prompts)
}

func runRawCommand(terminal *ansi.Terminal, session *agent.AgentSession, call func(context.Context) (slashcmd.Result, bool, error), lifecycle lifecycleReporter, prompts map[string]slashcmd.PermissionPrompt) (result slashcmd.Result, handled bool, resultErr error) {
	subscription, err := session.SubscribeAuditEvents(eventing.PermissionRequested)
	if err != nil {
		return result, false, err
	}
	defer subscription.Close()
	listenErr := runInteractiveRawEventLoop(terminal, subscription.Events(), func(ctx context.Context, _ <-chan struct{}) error {
		result, handled, resultErr = call(agent.WithPermissionPrompts(ctx))
		return resultErr
	}, session.ResolvePermission, permissionLifecycle(lifecycle, session), prompts)
	if listenErr != nil {
		resultErr = listenErr
	}
	return result, handled, resultErr
}

// runInteractiveRawEventLoop runs call while processing terminal control events
// and optional permission requests. A nil permissionEvents channel disables
// permission handling while retaining ESC and Ctrl-C behavior.
type permissionStateReporter struct {
	blocked func(eventing.PermissionRequest)
	working func()
	pending func(string) bool
}

func permissionLifecycle(lifecycle lifecycleReporter, session *agent.AgentSession) permissionStateReporter {
	return permissionStateReporter{
		pending: func(id string) bool {
			for _, request := range session.PendingPermissions() {
				if request.RequestID == id {
					return true
				}
			}
			return false
		},
		blocked: func(request eventing.PermissionRequest) {
			if lifecycle == nil {
				return
			}
			if request.Capability == eventing.PermissionCapabilityQuestion {
				lifecycle.Report(herdr.StateBlocked, "Question: "+request.Reason, session.ChannelID())
				return
			}
			lifecycle.Report(herdr.StateBlocked, "Permission required by "+request.ToolName, session.ChannelID())
		},
		working: func() {
			if lifecycle != nil {
				lifecycle.Report(herdr.StateWorking, "", session.ChannelID())
			}
		},
	}
}

func runInteractiveRawEventLoop(terminal *ansi.Terminal, permissionEvents <-chan *types.Event, call func(context.Context, <-chan struct{}) error, resolve func(eventing.PermissionResolution) error, lifecycle permissionStateReporter, prompts map[string]slashcmd.PermissionPrompt) (returnErr error) {
	restore, err := terminal.BeginInput()
	if err != nil {
		return err
	}
	defer func() { restore() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Approved unsandboxed commands run on this terminal. The loop runs them
	// itself so it stops reading keys while the command owns the terminal.
	foreground := make(chan foregroundRun)
	ctx = tools.WithForegroundRunner(ctx, func(runCtx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error) {
		run := foregroundRun{ctx: runCtx, cwd: cwd, command: command, opts: opts, done: make(chan foregroundResult, 1)}
		select {
		case foreground <- run:
		case <-runCtx.Done():
			return "", "", runCtx.Err()
		}
		got := <-run.done
		return got.stdout, got.stderr, got.err
	})
	// Full-screen command views, such as /review, borrow the terminal the
	// same way.
	screens := make(chan screenRun)
	ctx = slashcmd.WithScreenRunner(ctx, func(view func(*ansi.Terminal) error) error {
		run := screenRun{view: view, done: make(chan error, 1)}
		select {
		case screens <- run:
		case <-ctx.Done():
			return ctx.Err()
		}
		return <-run.done
	})
	stop := make(chan struct{})
	stopping := false
	result := make(chan error, 1)
	go func() { result <- call(ctx, stop) }()
	finished := false
	defer func() {
		cancel()
		if !finished {
			returnErr = errors.Join(returnErr, <-result)
		}
	}()

	for {
		select {
		case callErr := <-result:
			finished = true
			return callErr
		case run := <-foreground:
			restore()
			run.done <- runOnTerminal(terminal, run)
			if restore, err = terminal.BeginInput(); err != nil {
				restore = func() {}
				cancel()
				finished = true
				return <-result
			}
		case run := <-screens:
			restore()
			run.done <- run.view(terminal)
			if restore, err = terminal.BeginInput(); err != nil {
				restore = func() {}
				cancel()
				finished = true
				return <-result
			}
		case event, ok := <-permissionEvents:
			if !ok {
				permissionEvents = nil
				continue
			}
			if ctx.Err() != nil {
				continue
			}
			request, decodeErr := eventing.DecodePermissionRequest(event)
			if decodeErr != nil {
				return decodeErr
			}
			if lifecycle.pending != nil && !lifecycle.pending(request.RequestID) {
				continue
			}
			if lifecycle.blocked != nil {
				lifecycle.blocked(request)
			}
			resolution, promptErr := promptPermission(terminal, request, prompts)
			if errors.Is(promptErr, context.Canceled) {
				cancel()
				continue
			}
			if promptErr != nil {
				return promptErr
			}
			if resolve != nil {
				if lifecycle.pending != nil && !lifecycle.pending(request.RequestID) {
					continue
				}
				if resolveErr := resolve(resolution); resolveErr != nil {
					return resolveErr
				}
			}
			if lifecycle.working != nil {
				lifecycle.working()
			}
		default:
		}

		event, ready, readErr := terminal.ReadEventWithin(50 * time.Millisecond)
		if readErr != nil {
			cancel()
			finished = true
			return <-result
		}
		if !ready {
			continue
		}
		switch event.Kind {
		case ansi.EventEscape, ansi.EventCtrlC:
			cancel()
			if !stopping {
				stopping = true
				style := output.NewStyle(terminal.Writer())
				_, _ = fmt.Fprintln(terminal.Writer(), style.Warning("stopping"+output.GlyphEllipsis))
			}
		}
	}
}

// promptPermission asks the user to answer request with the embedder's prompt
// for its capability, or the built-in one.
func promptPermission(terminal *ansi.Terminal, request eventing.PermissionRequest, prompts map[string]slashcmd.PermissionPrompt) (eventing.PermissionResolution, error) {
	if prompt := prompts[request.Capability]; prompt != nil {
		resolution, err := prompt(terminal, request)
		resolution.RequestID = request.RequestID
		return resolution, err
	}
	if request.Capability == cloudModelCapability || request.Capability == modelOverrideCapability {
		return cloudConsentResolution(terminal, request)
	}
	if request.Capability == eventing.PermissionCapabilityQuestion {
		return questionResolution(terminal, request)
	}
	decision, err := readPermissionDecision(terminal, request)
	return eventing.PermissionResolution{RequestID: request.RequestID, Decision: decision, Resolver: "tui"}, err
}

type foregroundRun struct {
	ctx     context.Context
	cwd     string
	command string
	opts    sandbox.RunOptions
	done    chan foregroundResult
}

type screenRun struct {
	view func(*ansi.Terminal) error
	done chan error
}

type foregroundResult struct {
	stdout, stderr string
	err            error
}

// runOnTerminal hands the terminal to one approved unsandboxed command, so
// sudo and other programs can prompt the user directly.
func runOnTerminal(terminal *ansi.Terminal, run foregroundRun) foregroundResult {
	out := terminal.Writer()
	style := output.NewStyle(out)
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		stdout, stderr, runErr := sandbox.RunApproved(run.ctx, run.cwd, run.command, run.opts)
		return foregroundResult{stdout, stderr, runErr}
	}
	defer func() { _ = tty.Close() }()
	_, _ = fmt.Fprintf(out, "\n%s %s\n%s\n",
		style.Bold(style.Error("Running outside the sandbox on your terminal")),
		style.Muted("(Ctrl-C stops it)"),
		style.Accent("$ "+run.command),
	)
	stdout, stderr, runErr := sandbox.RunApprovedForeground(run.ctx, run.cwd, run.command, run.opts, tty, out)
	status := "done"
	if runErr != nil {
		status = runErr.Error()
	}
	rule := output.GlyphRule + output.GlyphRule
	_, _ = fmt.Fprintf(out, "%s\n", style.Muted(rule+" back in caretz ("+status+") "+rule))
	return foregroundResult{stdout, stderr, runErr}
}

func readPermissionDecision(terminal *ansi.Terminal, request eventing.PermissionRequest) (string, error) {
	out := terminal.Writer()
	style := output.NewStyle(out)
	sandboxOff := request.Capability == tools.PermissionSandboxOff
	if sandboxOff {
		// The reason is parsed from output the model's command produced, so
		// it is labeled as such rather than shown as Caretz's own finding.
		scope := "Approval covers only this exact command, once or for the session; other commands stay sandboxed."
		_, _ = fmt.Fprintf(out, "\n%s\n  %s %s\n  %s\n",
			style.Bold(style.Error("Rerun OUTSIDE the sandbox with full host access?")),
			style.Muted("Detected in the command's output:"),
			style.Warning(request.Reason),
			style.Muted(scope),
		)
	} else {
		_, _ = fmt.Fprintf(out, "\n%s %s\n  %s: %s\n",
			style.Bold(style.Warning("Permission required by")),
			style.Bold(style.Accent(request.ToolName)),
			style.Muted(request.Capability),
			style.Accent(request.Target),
		)
	}
	if request.Command != "" {
		_, _ = fmt.Fprintf(out, "  %s: %s\n", style.Muted("command"), request.Command)
	}
	if request.MayHaveRun {
		_, _ = fmt.Fprintln(out, "  "+style.Warning(output.GlyphWarning+" the command already ran and may have produced partial side effects."))
	}
	if request.Preview != "" {
		// The preview is what approval releases, so it is shown in full; it
		// is stripped so its content cannot redraw the prompt.
		preview := strings.ReplaceAll(ansi.Strip(request.Preview), "\n", "\n    ")
		_, _ = fmt.Fprintf(out, "  %s\n    %s\n", style.Muted("content:"), preview)
	}
	choices := []buttonChoice{{label: "Allow once", key: 'y'}}
	decisions := []string{eventing.PermissionDecisionAllow}
	if !request.NoSessionGrant {
		choices = append(choices, buttonChoice{label: "Allow for session", key: 's'})
		decisions = append(decisions, eventing.PermissionDecisionAllowSession)
	}
	choices = append(choices, buttonChoice{label: "Deny", key: 'n', danger: true})
	decisions = append(decisions, eventing.PermissionDecisionDeny)
	choice, err := readButtonChoice(terminal, choices, len(choices)-1, request.ExpiresAt, "Permission timed out; denied.")
	if err != nil {
		return "", err
	}
	if choice == buttonCancelled {
		return "", context.Canceled
	}
	return decisions[choice], nil
}

// buttonChoice is one option in a horizontal button prompt.
type buttonChoice struct {
	label  string
	key    rune // shortcut, matched case-insensitively
	danger bool // rendered red when selected
}

// buttonCancelled is returned by readButtonChoice for Escape and Ctrl-C.
const buttonCancelled = -1

// readButtonChoice lets the user pick a choice with the arrow keys, Tab,
// Enter or a shortcut. It returns buttonCancelled for Escape or Ctrl-C, and
// the selected default when expiresAt passes.
func readButtonChoice(terminal *ansi.Terminal, choices []buttonChoice, selected int, expiresAt time.Time, timeoutMessage string) (int, error) {
	out := terminal.Writer()
	style := output.NewStyle(out)
	fallback := selected
	render := func() {
		// The row is redrawn in place with \r, so it must stay short enough
		// not to wrap on narrow terminals.
		_, _ = fmt.Fprint(out, "\r  ")
		for i, choice := range choices {
			if i == selected {
				button := "[>" + choice.label + "<]"
				if choice.danger {
					button = style.Bold(style.Error(button))
				} else {
					button = style.Bold(style.Prompt(button))
				}
				_, _ = fmt.Fprintf(out, "%s ", button)
			} else {
				_, _ = fmt.Fprintf(out, "%s ", style.Muted("[ "+choice.label+" ]"))
			}
		}
		_, _ = fmt.Fprint(out, "\x1b[K")
	}
	render()
	for {
		wait := 50 * time.Millisecond
		if !expiresAt.IsZero() {
			remaining := time.Until(expiresAt)
			if remaining <= 0 {
				_, _ = fmt.Fprintln(out, "\n  "+style.Warning(timeoutMessage))
				return fallback, nil
			}
			if remaining < wait {
				wait = remaining
			}
		}
		event, ready, err := terminal.ReadEventWithin(wait)
		if err != nil {
			return 0, err
		}
		if !ready {
			continue
		}
		switch event.Kind {
		case ansi.EventLeft:
			selected = (selected + len(choices) - 1) % len(choices)
			render()
		case ansi.EventRight, ansi.EventTab:
			selected = (selected + 1) % len(choices)
			render()
		case ansi.EventEnter:
			_, _ = fmt.Fprintln(out)
			return selected, nil
		case ansi.EventEscape, ansi.EventCtrlC:
			_, _ = fmt.Fprintln(out)
			return buttonCancelled, nil
		case ansi.EventRune:
			for i, choice := range choices {
				if choice.key != 0 && unicode.ToLower(event.Rune) == choice.key {
					_, _ = fmt.Fprintln(out)
					return i, nil
				}
			}
		}
	}
}

// runPrompt executes a prompt with a cancellable context. When stdin is a real
// terminal, a SIGINT handler cancels the context on Ctrl+C so the active
// request can be interrupted.
//
// The two Ctrl+C cases are separated by terminal mode. While the editor is
// idle it runs in raw mode, where Ctrl+C is delivered as a byte (0x03) that the
// editor turns into ErrInterrupted to exit the loop. While a request is active
// the terminal is in cooked mode, where Ctrl+C raises SIGINT; the handler here
// cancels the request so the next prompt continues normally.
func runPrompt(session *agent.AgentSession, text string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if isTerminalStream(stdin) {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		done := make(chan struct{})
		go func() {
			select {
			case <-sigCh:
				cancel()
			case <-done:
			}
		}()
		defer func() {
			signal.Stop(sigCh)
			close(done)
		}()
	}

	_, err := session.Call(ctx, text)
	return err
}

// isTerminalStream reports whether r is an *os.File backed by a terminal.
func isTerminalStream(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// printBanner prints one startup line. Model, mode and context live in the
// prompt footer, and an active model rule gets its own startup section.
func printBanner(profile string) {
	style := output.NewStyle(stderr)
	banner := style.Bold("caretz")
	if strings.TrimSpace(profile) != "" {
		banner += style.Muted(" · profile=") + style.Accent(profile)
	}
	_, _ = fmt.Fprintln(stderr, banner+style.Muted(" · /help for commands, /exit to quit"))
}

func printStartupResources(resources startupResources) {
	style := output.NewStyle(stderr)
	var modelRule []string
	if resources.ModelRule != "" {
		modelRule = []string{resources.ModelRule}
	}
	sections := []struct {
		title string
		items []string
	}{
		{title: "Model rule", items: modelRule},
		{title: "Context", items: resources.Context},
		{title: "Skills", items: resources.Skills},
		{title: "Agents", items: resources.Agents},
	}
	for _, section := range sections {
		if len(section.items) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(stderr, "\n%s\n", style.Heading(section.title))
		for _, item := range section.items {
			_, _ = fmt.Fprintf(stderr, "  %s\n", style.Muted(item))
		}
	}
}

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/turnscope"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/types"
)

const defaultAsyncRunTimeout = 15 * time.Minute

// asyncTimeout is the cause of an async run's deadline. It names the limit
// that fired, so a timed-out run reports which one it was rather than a bare
// deadline error.
type asyncTimeout struct {
	limit    string
	duration time.Duration
}

func (e asyncTimeout) Error() string {
	return fmt.Sprintf("%s timeout of %s exceeded", e.limit, formatTimeout(e.duration))
}

func (e asyncTimeout) Unwrap() error { return context.DeadlineExceeded }

// formatTimeout drops the zero units time.Duration prints, so 15m0s reads 15m.
func formatTimeout(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// timeoutError returns the limit that ended ctx in place of the deadline
// error err carries from it.
func timeoutError(ctx context.Context, err error) error {
	var limit asyncTimeout
	if err == nil || errors.As(err, &limit) || ctx.Err() == nil {
		return err
	}
	if errors.As(context.Cause(ctx), &limit) {
		return limit
	}
	return err
}

// asyncBudget splits an async run's timeout between its phases.
type asyncBudget struct {
	run, model time.Duration
	deadline   time.Time
}

func (b asyncBudget) left() time.Duration { return time.Until(b.deadline) }

// modelContext bounds one model phase by the model timeout and by left, the
// run budget still unspent. The phase clock starts now, so a caller that
// measures left before waiting for the execution lock is not charged for
// the wait.
func (b asyncBudget) modelContext(parent context.Context, left time.Duration) (context.Context, context.CancelFunc) {
	if b.model > 0 && b.model < left {
		return context.WithTimeoutCause(parent, b.model, asyncTimeout{"model", b.model})
	}
	return context.WithTimeoutCause(parent, left, asyncTimeout{"run", b.run})
}

var asyncRunCapacity = make(chan struct{}, 4)

type ownedAsyncRun struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	started    bool
	done       chan struct{}
	finishOnce sync.Once
}

type ownedAsyncKey struct{}

func (r *ownedAsyncRun) finish() {
	if r != nil {
		r.finishOnce.Do(func() { close(r.done) })
	}
}
func finishOwnedWork(ctx context.Context) {
	run, _ := ctx.Value(ownedAsyncKey{}).(*ownedAsyncRun)
	run.finish()
}

// AsyncTurnRunner runs a background turn on behalf of the session. An
// interactive frontend installs one so background turns (schedules, peer
// messages) get the same ESC, Ctrl-C, and permission prompts as typed turns.
// It must call turn at most once and return turn's error; an error returned
// without calling turn means the turn never ran.
type AsyncTurnRunner func(ctx context.Context, turn func(ctx context.Context, stop <-chan struct{}) error) error

// SetAsyncTurnRunner installs runner for later background turns; nil restores
// headless execution, where permission requests are denied immediately.
func (a *AgentSession) SetAsyncTurnRunner(runner AsyncTurnRunner) {
	if runner == nil {
		a.asyncTurnRunner.Store(nil)
		return
	}
	a.asyncTurnRunner.Store(&runner)
}

func (a *AgentSession) PublishAsyncEvent(ctx context.Context, event *types.Event) error {
	if a == nil || a.eventSessionStore == nil {
		return fmt.Errorf("agent session audit store is required")
	}
	if event == nil {
		return fmt.Errorf("async event is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.lifecycleCtx != nil {
		select {
		case <-a.lifecycleCtx.Done():
			return a.lifecycleCtx.Err()
		default:
		}
	}
	if event.EventType == eventing.AsyncRunRequested {
		if err := a.ensureAsyncExecutor(); err != nil {
			return err
		}
		if scope := turnscope.From(ctx); scope != nil {
			request, err := eventing.DecodeAsyncRunRequest(event)
			if err != nil {
				return err
			}
			if request.Mode != eventing.AsyncRunSubagent && request.Mode != eventing.AsyncRunCommand {
				return a.AppendAuditEvent(event)
			}
			runCtx, cancel := context.WithCancel(a.lifecycleCtx)
			workScope := &turnscope.Scope{}
			run := &ownedAsyncRun{ctx: turnscope.With(runCtx, workScope), cancel: cancel, done: make(chan struct{})}
			a.asyncOwnedMu.Lock()
			if a.asyncOwned == nil {
				a.asyncOwned = make(map[string]*ownedAsyncRun)
			}
			a.asyncOwned[request.RequestID] = run
			a.asyncOwnedMu.Unlock()
			scope.Register(func() (string, error) {
				run.mu.Lock()
				cancel()
				started := run.started
				run.mu.Unlock()
				if started {
					<-run.done
				}
				notes, cleanupErr := workScope.Stop()
				return strings.Join(append([]string{"Cancelled async run " + request.RequestID + "."}, notes...), "\n"), cleanupErr
			})
		}
	}
	err := a.AppendAuditEvent(event)
	if err != nil && event.EventType == eventing.AsyncRunRequested {
		request, decodeErr := eventing.DecodeAsyncRunRequest(event)
		if decodeErr == nil {
			a.asyncOwnedMu.Lock()
			run := a.asyncOwned[request.RequestID]
			delete(a.asyncOwned, request.RequestID)
			a.asyncOwnedMu.Unlock()
			if run != nil {
				run.cancel()
				run.finish()
			}
		}
	}
	return err
}

func (a *AgentSession) SubscribeAuditEvents(eventTypes ...string) (eventing.Subscription, error) {
	if a == nil || a.eventSessionStore == nil {
		return nil, fmt.Errorf("agent session audit store is required")
	}
	return a.eventSessionStore.SubscribeEvents(eventTypes...)
}

func (a *AgentSession) SubscribeContextEvents(eventTypes ...string) (eventing.Subscription, error) {
	if a == nil || a.contextSessionStore == nil {
		return nil, fmt.Errorf("agent session context store is required")
	}
	return a.contextSessionStore.SubscribeEvents(eventTypes...)
}

func (a *AgentSession) ensureAsyncExecutor() error {
	a.asyncStartOnce.Do(func() {
		a.asyncStartErr = a.startAsyncExecutor()
	})
	return a.asyncStartErr
}

func (a *AgentSession) startAsyncExecutor() error {
	subscription, err := a.SubscribeAuditEvents(eventing.AsyncRunRequested)
	if err != nil {
		return err
	}
	a.asyncRequests = subscription
	a.asyncWG.Add(1)
	go func() {
		defer a.asyncWG.Done()
		for {
			select {
			case <-a.lifecycleCtx.Done():
				return
			case event, ok := <-subscription.Events():
				if !ok {
					return
				}
				a.asyncWG.Add(1)
				go func() {
					defer a.asyncWG.Done()
					a.executeAsyncEvent(event)
				}()
			}
		}
	}()
	return nil
}

func (a *AgentSession) executeAsyncEvent(event *types.Event) {
	request, err := eventing.DecodeAsyncRunRequest(event)
	if err != nil {
		return
	}
	baseCtx := a.lifecycleCtx
	a.asyncOwnedMu.Lock()
	owned := a.asyncOwned[request.RequestID]
	delete(a.asyncOwned, request.RequestID)
	a.asyncOwnedMu.Unlock()
	if owned != nil {
		owned.mu.Lock()
		owned.started = true
		baseCtx = owned.ctx
		owned.mu.Unlock()
		baseCtx = context.WithValue(baseCtx, ownedAsyncKey{}, owned)
		defer owned.finish()
		defer owned.cancel()
	}
	result := eventing.AsyncRunResult{RequestID: request.RequestID, Source: request.Source, SourceID: request.SourceID, Status: "succeeded"}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Status = "panicked"
			result.Error = fmt.Sprint(recovered)
		}
		if result.Status != "succeeded" {
			label := request.SourceID
			if label == "" {
				label = request.RequestID
			}
			notice := fmt.Sprintf("Background task %s %s before producing a result.", label, asyncStatusText(result.Status))
			if result.Status == "refused" {
				notice += " " + result.Error + "."
			}
			_ = a.AppendAuditEvent(eventing.NewAgentLogMessageInReplyToWithMetadata(a.channelId, "", "", notice, a.adapterType, a.channelId))
		}
		_ = a.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunCompleted(a.channelId, result))
	}()

	select {
	case asyncRunCapacity <- struct{}{}:
	case <-baseCtx.Done():
		result.Status = "cancelled"
		result.Error = baseCtx.Err().Error()
		return
	}
	defer func() { <-asyncRunCapacity }()

	timeout := request.Timeout
	if timeout <= 0 {
		timeout = defaultAsyncRunTimeout
	}
	budget := asyncBudget{run: timeout, model: request.ModelTimeout, deadline: time.Now().Add(timeout)}
	ctx, cancel := context.WithDeadlineCause(baseCtx, budget.deadline, asyncTimeout{"run", timeout})
	defer cancel()
	ctx = sessiontools.WithAsyncRequest(ctx, request)
	if err := a.PublishAsyncEvent(ctx, eventing.NewAsyncRunStarted(a.channelId, request)); err != nil {
		err = timeoutError(ctx, err)
		result.Status = asyncStatus(err, ctx)
		result.Error = err.Error()
		return
	}

	switch request.Mode {
	case "", eventing.AsyncRunForeground:
		err = a.runAsyncTurn(ctx, func(ctx context.Context, stop <-chan struct{}) error {
			response, err := a.callUntilStopUnlocked(ctx, request.Prompt, stop)
			if err == nil {
				result.Response = responseText(response)
			}
			return err
		})
	case eventing.AsyncRunSubagent:
		err = a.runAsyncSubagent(ctx, request, budget)
	case eventing.AsyncRunCommand:
		// The command runs without the execution lock so it does not block
		// the user's turns; only the turn that reads its output takes it.
		err = a.runAsyncCommand(ctx, request, budget)
	default:
		err = fmt.Errorf("unsupported async run mode %q", request.Mode)
	}
	if err != nil {
		err = timeoutError(ctx, err)
		result.Status = asyncStatus(err, ctx)
		result.Error = err.Error()
	}
}

func (a *AgentSession) withExecution(run func() error) error {
	return a.withExecutionContext(context.Background(), run)
}

func (a *AgentSession) withExecutionContext(ctx context.Context, run func() error) error {
	if err := a.executionMu.LockContext(ctx); err != nil {
		return err
	}
	defer a.executionMu.Unlock()
	return run()
}

// runAsyncTurn runs one background turn of this session under the execution
// lock. With a frontend's AsyncTurnRunner installed, the frontend runs it, so
// the turn gets the same ESC, Ctrl-C, and permission prompts as a typed one.
func (a *AgentSession) runAsyncTurn(ctx context.Context, turn func(ctx context.Context, stop <-chan struct{}) error) error {
	runner := a.asyncTurnRunner.Load()
	if runner == nil {
		return a.withExecutionContext(ctx, func() error { return turn(ctx, nil) })
	}
	return (*runner)(ctx, func(frontendCtx context.Context, stop <-chan struct{}) error {
		// Either the run's own deadline or the frontend (Ctrl-C) ends it.
		turnCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(frontendCtx, cancel)()
		return a.withExecutionContext(turnCtx, func() error {
			// Someone is at the terminal to answer, so ask instead of denying.
			return turn(tools.WithPermissionRequester(turnCtx, a.requestPermission), stop)
		})
	})
}

// ErrCommandNeedsSandbox refuses a background command in a session whose
// sandbox is disabled: unattended commands never run on the host.
var ErrCommandNeedsSandbox = errors.New("background commands need the sandbox, which is disabled for this session")

// runAsyncCommand runs request.Prompt as a one-shot sandboxed command, then
// gives the session one turn to read the result. The command never leaves the
// sandbox: the one-shot runner never escalates or asks for approval, and a
// session whose sandbox is disabled refuses the command instead.
func (a *AgentSession) runAsyncCommand(ctx context.Context, request eventing.AsyncRunRequest, budget asyncBudget) error {
	command := strings.TrimSpace(request.Prompt)
	if command == "" {
		return fmt.Errorf("async command must not be empty")
	}
	registry := a.ToolRegistry()
	if registry == nil || registry.SandboxDisabled {
		return ErrCommandNeedsSandbox
	}
	result, err := registry.RunVerificationCommandWithTimeout(ctx, command, request.CommandTimeoutSeconds)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	left := budget.left()
	finishOwnedWork(ctx)
	err = a.runAsyncTurn(a.lifecycleCtx, func(ctx context.Context, stop <-chan struct{}) error {
		turnCtx, cancel := budget.modelContext(ctx, left)
		defer cancel()
		_, err := a.callUntilStopUnlocked(turnCtx, asyncCommandReport(request, result), stop)
		return timeoutError(turnCtx, err)
	})
	if err == nil && result.TimedOut {
		// The model has read the partial output; the run still counts as
		// timed out so the source can retry it and the next run is told.
		return commandTimeout(request)
	}
	return err
}

// commandTimeout is the limit a command of request runs under.
func commandTimeout(request eventing.AsyncRunRequest) asyncTimeout {
	seconds := request.CommandTimeoutSeconds
	if seconds <= 0 {
		seconds = sandbox.DefaultTimeoutSeconds
	}
	return asyncTimeout{"command", time.Duration(seconds) * time.Second}
}

func asyncCommandReport(request eventing.AsyncRunRequest, result tools.VerificationResult) string {
	var b strings.Builder
	b.WriteString("Scheduled command finished.\n\n")
	if request.SourceID != "" {
		fmt.Fprintf(&b, "Schedule: %s\n", request.SourceID)
	}
	if request.PreviousRun != "" {
		fmt.Fprintf(&b, "Previous run did not succeed:\n%s\n\n", request.PreviousRun)
	}
	fmt.Fprintf(&b, "Command:\n%s\n\nExit code: %d", result.Command, result.ExitCode)
	if result.TimedOut {
		fmt.Fprintf(&b, " (%s)", commandTimeout(request))
	}
	b.WriteString("\n")
	if len(result.Denials) > 0 {
		denials := make([]string, len(result.Denials))
		for i, denial := range result.Denials {
			denials[i] = denial.String()
		}
		fmt.Fprintf(&b, "Blocked by the sandbox: %s\n", strings.Join(denials, ", "))
	}
	output := func(name, text string) {
		if strings.TrimSpace(text) == "" {
			text = "(empty)"
		}
		fmt.Fprintf(&b, "\n%s:\n%s\n", name, strings.TrimRight(text, "\n"))
	}
	output("Stdout", result.Stdout)
	output("Stderr", result.Stderr)
	b.WriteString("\nReact if the output needs action; otherwise report the result to the user concisely.")
	return b.String()
}

func asyncStatusText(status string) string {
	switch status {
	case "timed_out":
		return "timed out"
	case "cancelled":
		return "was cancelled"
	case "refused":
		return "was refused"
	case "panicked":
		return "panicked"
	default:
		return "failed"
	}
}

func asyncStatus(err error, ctx context.Context) string {
	if errors.Is(err, ErrCommandNeedsSandbox) {
		return "refused"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed_out"
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, ErrGracefulStop) {
		return "cancelled"
	}
	return "failed"
}

func (a *AgentSession) runAsyncSubagent(ctx context.Context, request eventing.AsyncRunRequest, budget asyncBudget) error {
	task := strings.TrimSpace(request.Prompt)
	if task == "" {
		return fmt.Errorf("async subagent task must not be empty")
	}
	var definition subagents.Definition
	var child *AgentSession
	// Snapshot the parent's runtime while serialized with model changes, /clear,
	// and integration installation. Only child construction needs this lock;
	// the child's tools and model calls run independently of the conversation.
	err := a.withExecutionContext(ctx, func() error {
		a.integrationMu.Lock()
		defer a.integrationMu.Unlock()
		definition = subagents.Definition{Name: "background", Description: "Runs an isolated background task", Mode: "subagent", SystemPrompt: a.systemPrompt}
		registry := SubagentToolRegistry(a, definition).WithLivePermissionsFrom(a.ToolRegistry())
		var err error
		child, err = a.NewSubagentChildSession(ctx, definition, SubagentChildOptions{Prefix: "background", ModelRefOverride: strings.TrimSpace(request.Model), ReasoningEffort: strings.TrimSpace(request.ReasoningEffort), Limits: request.ContextLimits, ToolRegistry: registry, IsolatedExecution: true})
		return err
	})
	if err != nil {
		return err
	}
	defer func() { _ = child.Close() }()
	if err := a.AppendAuditEvent(eventing.NewSubagentInvocationStarted(a.ChannelID(), definition.Name, child.ChannelID(), a.ChannelID(), task, child.ContextWindow())); err != nil {
		return err
	}
	if err := RegisterSubagentProgressHandlers(a, child, definition.Name); err != nil {
		return err
	}
	childTask := task
	if request.PreviousRun != "" {
		childTask = fmt.Sprintf("%s\n\nThe previous run of this task did not succeed. Change course if the same problem would recur:\n%s", task, request.PreviousRun)
	}
	callCtx, cancelCall := budget.modelContext(ctx, budget.left())
	response, err := child.Call(callCtx, BuildSubagentPrompt(definition, childTask, a.ChannelID()))
	err = timeoutError(callCtx, err)
	cancelCall()
	if err != nil {
		_ = a.AppendAuditEvent(eventing.NewSubagentInvocationFailed(a.ChannelID(), definition.Name, child.ChannelID(), a.ChannelID(), err.Error(), child.EstimatedContextTokens(), child.ContextWindow()))
		return err
	}
	result := responseText(response)
	if err := a.AppendAuditEvent(eventing.NewSubagentInvocationCompleted(a.ChannelID(), definition.Name, child.ChannelID(), a.ChannelID(), result, child.EstimatedContextTokens(), child.ContextWindow())); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only the report turn takes the execution lock; its model phase starts
	// once the lock is held, so waiting for a user turn is not charged.
	left := budget.left()
	finishOwnedWork(ctx)
	return a.runAsyncTurn(a.lifecycleCtx, func(ctx context.Context, stop <-chan struct{}) error {
		turnCtx, cancel := budget.modelContext(ctx, left)
		defer cancel()
		_, err := a.callUntilStopUnlocked(turnCtx, fmt.Sprintf("Background task completed.\n\nChild session ID: %s\n\nTask:\n%s\n\nResult:\n%s\n\nReport the result to the user concisely.", child.ChannelID(), task, result), stop)
		return timeoutError(turnCtx, err)
	})
}

func (a *AgentSession) InstallIntegration(factory sessiontools.Factory) error {
	if factory == nil {
		return fmt.Errorf("session integration factory is nil")
	}
	integration, err := factory(a)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(integration.Name())
	if name == "" {
		return fmt.Errorf("session integration name must not be empty")
	}
	a.integrationMu.Lock()
	if _, exists := a.integrations[name]; exists {
		a.integrationMu.Unlock()
		_ = integration.Close()
		return nil
	}
	previousRegistry := a.toolRegistry
	registry := previousRegistry
	for _, tool := range integration.Tools() {
		registry, err = registry.WithSessionTool(tool)
		if err != nil {
			a.integrationMu.Unlock()
			_ = integration.Close()
			return err
		}
	}
	if err := a.SwitchRuntime(registry, a.systemPrompt); err != nil {
		a.integrationMu.Unlock()
		_ = integration.Close()
		return err
	}
	if err := integration.Start(a.lifecycleCtx); err != nil {
		_ = a.SwitchRuntime(previousRegistry, a.systemPrompt)
		a.integrationMu.Unlock()
		_ = integration.Close()
		return err
	}
	a.integrations[name] = integration
	a.integrationFactories = append(a.integrationFactories, factory)
	a.integrationMu.Unlock()
	return nil
}

func (a *AgentSession) Integration(name string) (sessiontools.Integration, bool) {
	a.integrationMu.Lock()
	defer a.integrationMu.Unlock()
	integration, ok := a.integrations[name]
	return integration, ok
}

func (a *AgentSession) Close() error {
	var closeErr error
	a.closeOnce.Do(func() {
		a.integrationMu.Lock()
		integrations := make([]sessiontools.Integration, 0, len(a.integrations))
		for _, integration := range a.integrations {
			integrations = append(integrations, integration)
		}
		a.integrationMu.Unlock()
		for _, integration := range integrations {
			if err := integration.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		a.lifecycleCancel()
		if a.asyncRequests != nil {
			a.asyncRequests.Close()
		}
		a.asyncWG.Wait()
		if a.ownsSessionExecution && a.toolRegistry != nil {
			if err := a.toolRegistry.CloseSessionExecution(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

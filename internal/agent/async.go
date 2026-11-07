package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/turnscope"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/types"
)

const (
	defaultAsyncRunTimeout = 15 * time.Minute
	// asyncSubagentSource marks runs started through CallSubagent.
	asyncSubagentSource = "subagent"
	// subagentReportTimeout bounds the turn that reports finished runs.
	subagentReportTimeout = 15 * time.Minute
	// subagentReportWindow lets runs finishing together share one report turn.
	subagentReportWindow = 500 * time.Millisecond
)

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
	prepared := a.takePreparedAsyncChild(request.RequestID)
	if prepared != nil {
		baseCtx = prepared.bindCancel(baseCtx)
		defer a.finishBackgroundRun(prepared)
	}
	handedOff := false
	defer func() {
		if prepared != nil && !handedOff {
			a.abandonAsyncChild(prepared, baseCtx, errors.New(result.Error))
		}
	}()

	select {
	case asyncRunCapacity <- struct{}{}:
	case <-baseCtx.Done():
		result.Status = "cancelled"
		result.Error = baseCtx.Err().Error()
		return
	}
	releaseSlot := sync.OnceFunc(func() { <-asyncRunCapacity })
	defer releaseSlot()
	if prepared != nil {
		prepared.releaseSlot = releaseSlot
	}

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
		err = a.withExecutionContext(ctx, func() error {
			_, err := a.callUntilStopUnlocked(ctx, request.Prompt, nil)
			return err
		})
	case eventing.AsyncRunSubagent:
		handedOff = true
		err = a.runAsyncSubagent(ctx, request, budget, prepared)
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
	err = a.withExecutionContext(a.lifecycleCtx, func() error {
		turnCtx, cancel := budget.modelContext(a.lifecycleCtx, left)
		defer cancel()
		_, err := a.callUntilStopUnlocked(turnCtx, asyncCommandReport(request, result), nil)
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
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	return "failed"
}

// asyncChild is a built child that has not run yet.
type asyncChild struct {
	definition subagents.Definition
	child      *AgentSession
	resumed    *SubagentRun // the earlier run this one continues, if any

	// done closes once the run's final event is in the log and any report is
	// queued, which is all WaitSubagents needs.
	done       chan struct{}
	doneOnce   sync.Once
	finishOnce sync.Once
	// releaseSlot gives up the run's concurrency slot once its child has ended.
	// Keeping it through the report would deadlock: the report needs a turn
	// that may be waiting for a run that needs the slot.
	releaseSlot func()

	mu          sync.Mutex
	cancel      context.CancelCauseFunc
	cancelCause error
}

func (r *asyncChild) markDone() { r.doneOnce.Do(func() { close(r.done) }) }

// requestCancel stops the run. A run still waiting for the executor stops as
// soon as it binds its context.
func (r *asyncChild) requestCancel(cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel(cause)
	} else {
		r.cancelCause = cause
	}
}

func (r *asyncChild) bindCancel(parent context.Context) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithCancelCause(parent)
	r.cancel = cancel
	if r.cancelCause != nil {
		cancel(r.cancelCause)
	}
	return ctx
}

// finishBackgroundRun forgets a run that has ended, releasing its context.
func (a *AgentSession) finishBackgroundRun(run *asyncChild) {
	run.finishOnce.Do(func() {
		a.asyncOwnedMu.Lock()
		delete(a.backgroundRuns, run.child.ChannelID())
		a.asyncOwnedMu.Unlock()
		run.markDone()
		run.requestCancel(context.Canceled)
		a.backgroundWG.Done()
	})
}

// subagentReport tells the session's model how a background run ended.
type subagentReport struct {
	childID   string
	text      string
	delivered chan struct{}
}

// reportBackgroundRun queues text for the next report turn, frees the run's
// slot, and waits for the report to be delivered.
func (a *AgentSession) reportBackgroundRun(ctx context.Context, run *asyncChild, text string) {
	// Stopping the launching turn waits for this run's work, and the report
	// needs the lock that turn still holds.
	finishOwnedWork(ctx)
	report := &subagentReport{childID: run.child.ChannelID(), text: text, delivered: make(chan struct{})}
	a.reportMu.Lock()
	a.pendingReports = append(a.pendingReports, report)
	if !a.reportDraining {
		a.reportDraining = true
		a.asyncWG.Add(1)
		go a.drainSubagentReports()
	}
	a.reportMu.Unlock()
	run.markDone()
	if run.releaseSlot != nil {
		run.releaseSlot()
	}
	select {
	case <-report.delivered:
	case <-a.lifecycleCtx.Done():
	}
}

// drainSubagentReports runs report turns until none are queued. Runs that end
// together, or during a turn, share one report.
func (a *AgentSession) drainSubagentReports() {
	defer a.asyncWG.Done()
	timer := time.NewTimer(subagentReportWindow)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-a.lifecycleCtx.Done():
	}
	for {
		err := a.withExecutionContext(a.lifecycleCtx, a.runSubagentReportTurn)
		a.reportMu.Lock()
		if err != nil || len(a.pendingReports) == 0 {
			a.reportDraining = false
			a.reportMu.Unlock()
			return
		}
		a.reportMu.Unlock()
	}
}

// runSubagentReportTurn must hold the execution lock. It takes the queue under
// that lock, so a WaitSubagents call in a turn cannot lose a report to it.
func (a *AgentSession) runSubagentReportTurn() error {
	a.reportMu.Lock()
	reports := a.pendingReports
	a.pendingReports = nil
	a.reportMu.Unlock()
	if len(reports) == 0 {
		return nil
	}
	defer func() {
		for _, report := range reports {
			close(report.delivered)
		}
	}()
	texts := make([]string, len(reports))
	for i, report := range reports {
		texts[i] = report.text
	}
	closing := "Report the result to the user concisely."
	if len(reports) > 1 {
		closing = "Report the results to the user concisely."
	}
	turnCtx, cancel := context.WithTimeout(a.lifecycleCtx, subagentReportTimeout)
	defer cancel()
	if _, err := a.callUntilStopUnlocked(turnCtx, strings.Join(texts, "\n\n---\n\n")+"\n\n"+closing, nil); err != nil {
		a.logger.Warn("Failed to report background subagents: %v", err)
	}
	return nil
}

// claimSubagentReports removes the queued reports of runs whose results the
// model has already read, so they start no turn.
func (a *AgentSession) claimSubagentReports(childIDs map[string]bool) {
	a.reportMu.Lock()
	defer a.reportMu.Unlock()
	a.pendingReports = slices.DeleteFunc(a.pendingReports, func(report *subagentReport) bool {
		if !childIDs[report.childID] {
			return false
		}
		close(report.delivered)
		return true
	})
}

// BackgroundSubagentsRunning counts background runs not yet reported.
func (a *AgentSession) BackgroundSubagentsRunning() int {
	a.asyncOwnedMu.Lock()
	defer a.asyncOwnedMu.Unlock()
	return len(a.backgroundRuns)
}

// WaitForBackgroundSubagents blocks until every background run has ended and
// been reported, including runs a report turn starts. A one-shot process calls
// it before exiting, which would otherwise kill them.
func (a *AgentSession) WaitForBackgroundSubagents(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		a.backgroundWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// newAsyncChild builds the child for request from a snapshot of the parent's
// runtime. The caller must hold the execution lock, which serializes the
// snapshot with model changes, /clear, and integration installation. Only
// construction needs the lock; the child's tools and model calls run
// independently of the conversation.
func (a *AgentSession) newAsyncChild(ctx context.Context, request eventing.AsyncRunRequest) (*asyncChild, error) {
	a.integrationMu.Lock()
	defer a.integrationMu.Unlock()
	definition := subagents.Definition{Name: "background", Description: "Runs an isolated background task", Mode: "subagent", SystemPrompt: a.systemPrompt}
	prefix := "background"
	if name := strings.TrimSpace(request.Agent); name != "" {
		var err error
		if definition, err = a.loadEnabledSubagent(name); err != nil {
			return nil, err
		}
		if !definition.ReadOnly() {
			return nil, fmt.Errorf("subagent %q cannot run in the background because it can change files; set its permission to deny both edit and bash, or run it without background", definition.Name)
		}
		prefix = "subagent-" + definition.Name
	}
	registry := SubagentToolRegistry(a, definition).WithLivePermissionsFrom(a.ToolRegistry())
	child, err := a.NewSubagentChildSession(ctx, definition, SubagentChildOptions{Prefix: prefix, ModelRefOverride: strings.TrimSpace(request.Model), ReasoningEffort: strings.TrimSpace(request.ReasoningEffort), Limits: request.ContextLimits, ToolRegistry: registry, IsolatedExecution: true, ResumeChildID: request.ResumeChild})
	if err != nil {
		return nil, err
	}
	return &asyncChild{definition: definition, child: child, done: make(chan struct{})}, nil
}

func (a *AgentSession) appendAsyncChildStarted(run *asyncChild, request eventing.AsyncRunRequest) error {
	model := firstNonEmpty(request.Model, run.definition.Model)
	reasoning := firstNonEmpty(request.ReasoningEffort, run.definition.Reasoning)
	return a.AppendAuditEvent(eventing.NewSubagentInvocationStartedWithConfig(a.ChannelID(), run.definition.Name, run.child.ChannelID(), a.ChannelID(), strings.TrimSpace(request.Prompt), model, reasoning, run.child.ContextWindow()))
}

func (a *AgentSession) appendAsyncChildStopped(run *asyncChild, ctx context.Context, err error) {
	_ = a.AppendAuditEvent(eventing.NewSubagentInvocationStopped(a.ChannelID(), run.definition.Name, run.child.ChannelID(), a.ChannelID(), err.Error(), a.subagentStopReason(ctx, err), run.child.EstimatedContextTokens(), run.child.ContextWindow()))
}

// abandonAsyncChild ends a prepared child that will never run. Its Started
// event is already in the log, so it needs a final event too.
func (a *AgentSession) abandonAsyncChild(run *asyncChild, ctx context.Context, err error) {
	a.appendAsyncChildStopped(run, ctx, err)
	_ = run.child.Close()
	a.finishBackgroundRun(run)
}

// reportAsyncSubagentFailure gives the session a turn to learn that a named
// run failed, since otherwise the model still expects its result. A cancelled
// or closed run gets no turn: whoever stopped it already knows.
func (a *AgentSession) reportAsyncSubagentFailure(ctx context.Context, request eventing.AsyncRunRequest, run *asyncChild, err error) {
	reason := a.subagentStopReason(ctx, err)
	if request.Agent == "" || (reason != eventing.SubagentStopError && reason != eventing.SubagentStopTimeout) {
		return
	}
	a.reportBackgroundRun(ctx, run, fmt.Sprintf("Background subagent %q ended without a result: %s.\n\nChild session ID: %s\n\nTask:\n%s\n\nError:\n%s", run.definition.Name, stoppedRunStatus(reason), run.child.ChannelID(), strings.TrimSpace(request.Prompt), err))
}

func (a *AgentSession) takePreparedAsyncChild(requestID string) *asyncChild {
	a.asyncOwnedMu.Lock()
	defer a.asyncOwnedMu.Unlock()
	run := a.asyncPrepared[requestID]
	delete(a.asyncPrepared, requestID)
	return run
}

// abandonPreparedAsyncChildren ends children whose request never reached the
// executor because the session closed first.
func (a *AgentSession) abandonPreparedAsyncChildren() {
	a.asyncOwnedMu.Lock()
	prepared := a.asyncPrepared
	a.asyncPrepared = nil
	a.asyncOwnedMu.Unlock()
	for _, run := range prepared {
		a.abandonAsyncChild(run, a.lifecycleCtx, errors.New("session closed before the run started"))
	}
}

// startBackgroundSubagent starts a named agent without waiting for it. The
// caller must hold the execution lock, as a tool call does through its turn.
// The child is built here instead of in the executor, which would need that
// same lock and so could not start until the turn ended.
func (a *AgentSession) startBackgroundSubagent(ctx context.Context, opts SubagentRunOptions) (SubagentRunResult, error) {
	opts, resumed, err := a.resolveSubagentResume(opts)
	if err != nil {
		return SubagentRunResult{}, err
	}
	name := strings.TrimSpace(opts.Name)
	task := strings.TrimSpace(opts.Task)
	if name == "" {
		return SubagentRunResult{}, fmt.Errorf("subagent name must not be empty")
	}
	if task == "" {
		return SubagentRunResult{}, fmt.Errorf("subagent task must not be empty")
	}
	request := eventing.AsyncRunRequest{
		RequestID:       uuid.NewV4().String(),
		Source:          asyncSubagentSource,
		SourceID:        name,
		Prompt:          task,
		Mode:            eventing.AsyncRunSubagent,
		Agent:           name,
		ResumeChild:     opts.ResumeChildID,
		Model:           strings.TrimSpace(opts.ModelRefOverride),
		ReasoningEffort: strings.TrimSpace(opts.ReasoningEffort),
	}
	run, err := a.newAsyncChild(ctx, request)
	if err != nil {
		return SubagentRunResult{}, err
	}
	run.resumed = resumed
	if err := a.appendAsyncChildStarted(run, request); err != nil {
		_ = run.child.Close()
		return SubagentRunResult{}, err
	}
	a.asyncOwnedMu.Lock()
	if a.asyncPrepared == nil {
		a.asyncPrepared = make(map[string]*asyncChild)
	}
	a.asyncPrepared[request.RequestID] = run
	if a.backgroundRuns == nil {
		a.backgroundRuns = make(map[string]*asyncChild)
	}
	a.backgroundRuns[run.child.ChannelID()] = run
	a.backgroundWG.Add(1)
	a.asyncOwnedMu.Unlock()
	if err := a.PublishAsyncEvent(ctx, eventing.NewAsyncRunRequested(a.channelId, request)); err != nil {
		if prepared := a.takePreparedAsyncChild(request.RequestID); prepared != nil {
			a.abandonAsyncChild(prepared, ctx, err)
		}
		return SubagentRunResult{}, err
	}
	return SubagentRunResult{Name: run.definition.Name, Task: task, ChildSessionID: run.child.ChannelID()}, nil
}

// runAsyncSubagent runs the child and reports its result to the session.
// prepared is the child built when the run was requested; without one it is
// built here, which waits for any turn in progress.
func (a *AgentSession) runAsyncSubagent(ctx context.Context, request eventing.AsyncRunRequest, budget asyncBudget, prepared *asyncChild) error {
	task := strings.TrimSpace(request.Prompt)
	if task == "" {
		return fmt.Errorf("async subagent task must not be empty")
	}
	run := prepared
	if run == nil {
		err := a.withExecutionContext(ctx, func() error {
			var err error
			run, err = a.newAsyncChild(ctx, request)
			return err
		})
		if err != nil {
			return err
		}
		if err := a.appendAsyncChildStarted(run, request); err != nil {
			_ = run.child.Close()
			return err
		}
	}
	definition, child := run.definition, run.child
	defer func() { _ = child.Close() }()
	if err := RegisterSubagentProgressHandlers(a, child, definition.Name); err != nil {
		a.appendAsyncChildStopped(run, ctx, err)
		return err
	}
	childTask := task
	if request.PreviousRun != "" {
		childTask = fmt.Sprintf("%s\n\nThe previous run of this task did not succeed. Change course if the same problem would recur:\n%s", task, request.PreviousRun)
	}
	callCtx, cancelCall := budget.modelContext(ctx, budget.left())
	response, err := child.Call(callCtx, buildSubagentRunPrompt(definition, childTask, a.ChannelID(), run.resumed))
	err = timeoutError(callCtx, err)
	cancelCall()
	if err != nil {
		a.appendAsyncChildStopped(run, ctx, err)
		a.reportAsyncSubagentFailure(ctx, request, run, err)
		return err
	}
	result := responseText(response)
	if err := a.AppendAuditEvent(eventing.NewSubagentInvocationCompleted(a.ChannelID(), definition.Name, child.ChannelID(), a.ChannelID(), result, child.EstimatedContextTokens(), child.ContextWindow())); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Agent != "" {
		a.reportBackgroundRun(ctx, run, fmt.Sprintf("Background subagent %q completed.\n\nChild session ID: %s\n\nTask:\n%s\n\nResult:\n%s", definition.Name, child.ChannelID(), task, result))
		return nil
	}
	// Only the report turn takes the execution lock; its model phase starts
	// once the lock is held, so waiting for a user turn is not charged.
	left := budget.left()
	finishOwnedWork(ctx)
	return a.withExecutionContext(a.lifecycleCtx, func() error {
		turnCtx, cancel := budget.modelContext(a.lifecycleCtx, left)
		defer cancel()
		_, err := a.callUntilStopUnlocked(turnCtx, fmt.Sprintf("Background task completed.\n\nChild session ID: %s\n\nTask:\n%s\n\nResult:\n%s\n\nReport the result to the user concisely.", child.ChannelID(), task, result), nil)
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
		a.abandonPreparedAsyncChildren()
		if a.ownsSessionExecution && a.toolRegistry != nil {
			if err := a.toolRegistry.CloseSessionExecution(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if a.onClose != nil {
			a.onClose()
		}
	})
	return closeErr
}

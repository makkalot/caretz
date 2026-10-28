package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
)

// Worker statuses recorded with each attempt.
const (
	WorkerDone     = "LOOP_TASK_DONE"
	WorkerContinue = "LOOP_TASK_CONTINUE"
	WorkerBlocked  = "LOOP_TASK_BLOCKED"
)

// Handoff is a worker's report. Its prose is never evidence.
type Handoff struct {
	Result      string   `json:"result"`
	Notes       string   `json:"notes"`
	Discoveries []string `json:"discoveries"`
	// Invalid is set when the worker returned no valid handoff; its check
	// still decides whether the work is done.
	Invalid bool         `json:"-"`
	Stats   *tasks.Stats `json:"-"`
}

type PlanRequest struct {
	Reason string
	Mode   EvidenceMode
}

// Roles are the model calls of a goal. Each gets the state it may read. The
// planner explores the workspace itself. Verification is two calls on the
// verifier model: the reviewer reads the change with read-only tools, then
// the judge decides from the checks and the review.
type Roles interface {
	Plan(ctx context.Context, state State, request PlanRequest) (Proposal, error)
	Work(ctx context.Context, state State, task Task) (Handoff, error)
	Review(ctx context.Context, state State, checks string) (Review, error)
	Judge(ctx context.Context, state State, checks string, review Review) (Verdict, error)
}

// Exec runs deterministic checks.
type Exec interface {
	Workspace
	RunCheck(ctx context.Context, check Check) (CheckOutcome, error)
	ValidateCheck(ctx context.Context, check Check) error
}

// Store persists goal events. Sync refreshes the views derived from state
// after every event.
type Store interface {
	Load() ([]eventing.GoalEvent, error)
	Append(event eventing.GoalEvent) error
	Sync(state State) error
}

// Inputs move a goal. Each command parses into one.
type Input interface{ input() }

type Start struct {
	ID      string
	Goal    string
	Created CreatedData
}

// Approve, Revise and Retry may change role models; nil keeps them.
type Approve struct{ Models *Models }
type Revise struct {
	Edit   Edit
	Models *Models
}
type Answer struct{ Text string }
type Resume struct{}

// Retry replans a terminal goal; Reason is optional user feedback for it.
type Retry struct {
	Models *Models
	Reason string
}
type Stop struct{}

func (Start) input()   {}
func (Approve) input() {}
func (Revise) input()  {}
func (Answer) input()  {}
func (Resume) input()  {}
func (Retry) input()   {}
func (Stop) input()    {}

var ErrNoGoal = errors.New("no goal in this session")

// recordError marks a failure to record an event. The run cannot continue
// and the error is returned instead of ending the goal.
type recordError struct{ err error }

func (e recordError) Error() string { return e.err.Error() }
func (e recordError) Unwrap() error { return e.err }

type Engine struct {
	Store  Store
	Roles  Roles
	Exec   Exec
	Limits Limits
	Now    func() time.Time
	Notify func(ctx context.Context, message string)

	state State
}

// State is the goal as of the last Load or Step.
func (e *Engine) State() State { return e.state }

// Load rebuilds the goal from its events.
func (e *Engine) Load() (State, bool, error) {
	events, err := e.Store.Load()
	if err != nil {
		return State{}, false, err
	}
	state, found, err := Replay(events)
	if err != nil {
		return state, found, err
	}
	e.state = state
	return state, found, nil
}

// Step applies one input and drives the goal until it needs the user again.
func (e *Engine) Step(ctx context.Context, in Input) (State, error) {
	state, found, err := e.Load()
	if start, ok := in.(Start); ok {
		if err != nil && !errors.Is(err, ErrLegacy) {
			return state, err
		}
		if err == nil && found && state.Stage.Active() {
			return state, fmt.Errorf("a goal is already active; use /goal:status or /goal:stop")
		}
		return e.start(ctx, start)
	}
	if err != nil {
		return state, err
	}
	switch in := in.(type) {
	case Approve:
		if !found || state.Stage != AwaitingApproval {
			return state, fmt.Errorf("goal is not awaiting_approval; use /goal:status")
		}
		if state.Question != "" {
			return state, fmt.Errorf("answer the pending goal question before approval")
		}
		if err := e.emit(EventApproved, "Goal plan approved", "", ApprovedData(in)); err != nil {
			return e.state, err
		}
		return e.run(ctx)
	case Revise:
		if !found {
			return state, ErrNoGoal
		}
		switch in.Edit.Target {
		case EditPlan:
			if state.Stage != AwaitingApproval {
				return state, fmt.Errorf("goal is not awaiting_approval; use /goal:status")
			}
			e.notify(ctx, "goal plan revision started")
			return e.planForApproval(ctx, []pending{{EventEditRequested, "Plan edit requested", "", EditData(in)}}, "user requested changes: "+in.Edit.Text)
		case EditEvidence:
			if !Allowed(EventEditRequested, state.Stage) {
				return state, fmt.Errorf("cancel the active turn before revising goal evidence")
			}
			e.notify(ctx, "goal evidence revision started")
			return e.planForApproval(ctx, []pending{{EventEditRequested, "Evidence edit requested", "", EditData(in)}}, "evidence change requested: "+in.Edit.Text)
		}
		return state, fmt.Errorf("unknown edit target %q", in.Edit.Target)
	case Answer:
		if !found || (state.Stage != AwaitingInput && state.Stage != AwaitingApproval) || state.Question == "" {
			return state, fmt.Errorf("goal is not awaiting an answer; use /goal:status")
		}
		data := AnsweredData{Answer: in.Text}
		if len(state.TamperedEvidence) > 0 {
			// The user reviewed the changed evidence; accept it as the new baseline.
			data.Accepted = Freeze(e.Exec, state, true)
		}
		answered := pending{EventAnswered, "User answered", "", data}
		next, err := e.preview(answered)
		if err != nil {
			return state, err
		}
		if next.Stage == Planning {
			state, err := e.planForApproval(ctx, []pending{answered}, "plan after user answer")
			if err != nil || state.Stage != AwaitingApproval || !state.AutoApprove || state.Question != "" || state.PendingEdit != nil {
				return state, err
			}
			if err := e.emit(EventApproved, "Goal plan approved", "", nil); err != nil {
				return e.state, err
			}
			return e.run(ctx)
		}
		if err := e.emit(answered.Type, answered.Summary, "", answered.Data); err != nil {
			return e.state, err
		}
		return e.run(ctx)
	case Resume:
		if !found || !state.Stage.Running() {
			return state, resumeError(state, found)
		}
		return e.run(ctx)
	case Retry:
		if !found {
			return state, ErrNoGoal
		}
		if !Allowed(EventRetryRequested, state.Stage) {
			return state, fmt.Errorf("cancel the active turn before retrying the goal")
		}
		reason := "user requested retry"
		if exit := strings.TrimSpace(state.ExitReason); exit != "" {
			reason += " after: " + exit
		} else if question := strings.TrimSpace(state.Question); question != "" {
			reason += " while paused on: " + Preview(question, 300)
		}
		feedback := strings.TrimSpace(in.Reason)
		if feedback != "" {
			reason += "; user feedback: " + feedback
		}
		e.notify(ctx, "goal retry planning started")
		return e.planForApproval(ctx, []pending{{EventRetryRequested, "Retry requested", "", RetryData{Reason: reason, Feedback: feedback, Models: in.Models}}}, reason)
	case Stop:
		if !found {
			return state, ErrNoGoal
		}
		if state.Stage.Running() {
			return state, fmt.Errorf("cancel the active turn to stop this goal run")
		}
		if state.Stage.Terminal() {
			return state, nil
		}
		return e.end(EventStopped, "stopped by user")
	}
	return state, fmt.Errorf("unknown goal input %T", in)
}

func resumeError(state State, found bool) error {
	if found {
		switch state.Stage {
		case Stalled, Stopped, OverBudget:
			return fmt.Errorf("goal is %s; use /goal:retry", state.Stage)
		case AwaitingApproval:
			return fmt.Errorf("goal is awaiting approval; use /goal:approve")
		case AwaitingInput:
			return fmt.Errorf("goal is awaiting input; use /goal:answer")
		}
	}
	return fmt.Errorf("goal is not running; use /goal:status")
}

func (e *Engine) start(ctx context.Context, in Start) (State, error) {
	e.state = State{}
	if err := e.emitEvent(eventing.GoalEvent{Type: EventCreated, GoalID: in.ID, Goal: in.Goal, Summary: "Goal accepted"}, in.Created); err != nil {
		return e.state, err
	}
	e.notify(ctx, "goal planning started")
	state, err := e.planForApproval(ctx, nil, "initial plan")
	if err != nil {
		var recordErr recordError
		if !errors.As(err, &recordErr) {
			_, _ = e.end(EventInterrupted, err.Error())
		}
		return e.state, err
	}
	if state.Stage != AwaitingApproval || !state.AutoApprove || state.Question != "" {
		return state, nil
	}
	if err := e.emit(EventApproved, "Goal plan approved", "", nil); err != nil {
		return e.state, err
	}
	return e.run(ctx)
}

// pending is an event not yet recorded.
type pending struct {
	Type    string
	Summary string
	TaskID  string
	Data    any
}

// preview applies events to a copy of the state without recording them.
func (e *Engine) preview(events ...pending) (State, error) {
	scratch := e.state.Clone()
	for _, p := range events {
		event, err := e.build(scratch, p.Type, p.Summary, p.TaskID, p.Data)
		if err != nil {
			return scratch, err
		}
		if err := scratch.Apply(event); err != nil {
			return scratch, err
		}
	}
	return scratch, nil
}

// planForApproval plans after the given request events and records them
// with the plan only when planning succeeds, so a failed planner leaves the
// goal as it was.
func (e *Engine) planForApproval(ctx context.Context, requests []pending, reason string) (State, error) {
	scratch, err := e.preview(requests...)
	if err != nil {
		return e.state, err
	}
	proposal, err := e.Roles.Plan(ctx, scratch, PlanRequest{Reason: reason, Mode: scratch.PlanMode})
	if err != nil {
		return e.state, err
	}
	data, err := PlanFromProposal(scratch, proposal, scratch.PlanMode)
	if err != nil {
		return e.state, err
	}
	for _, request := range requests {
		if err := e.emit(request.Type, request.Summary, request.TaskID, request.Data); err != nil {
			return e.state, err
		}
	}
	// A question with no plan to attach it to, or with --yes, waits for an
	// answer instead of approval.
	if data.Plan == nil && (e.state.AutoApprove || len(e.state.ActiveTaskIDs()) == 0) {
		return e.state, e.pause(data.Question, nil, nil)
	}
	summary := "Plan question"
	if data.Plan != nil {
		summary = fmt.Sprintf("Plan v%d created with %d tasks", e.state.PlanVersion+1, len(data.Plan.Tasks))
	}
	return e.state, e.emit(EventPlanCreated, summary, "", data)
}

// run drives the loop: work (worker, then its deterministic check) ->
// verify (evidence, regressions, review, judge) -> replan on any gap.
func (e *Engine) run(ctx context.Context) (State, error) {
	if err := e.emit(EventRunStarted, "Goal execution started", "", RunStartedData{Frozen: Freeze(e.Exec, e.state, false)}); err != nil {
		return e.state, err
	}
	if len(e.state.Tasks) == 0 {
		return e.end(EventStalled, "no tasks were planned")
	}
	for {
		if err := ctx.Err(); err != nil {
			return e.end(EventInterrupted, err.Error())
		}
		var err error
		switch e.state.Stage {
		case Working:
			err = e.work(ctx)
		case Verifying:
			err = e.verify(ctx)
		case Replanning:
			err = e.replan(ctx)
		default:
			return e.state, nil
		}
		if err != nil {
			var recordErr recordError
			if errors.As(err, &recordErr) {
				return e.state, err
			}
			if ctx.Err() != nil {
				return e.end(EventInterrupted, ctx.Err().Error())
			}
			return e.end(EventStalled, err.Error())
		}
	}
}

// overBudget explains an exhausted budget, or returns "".
func (e *Engine) overBudget() string {
	if e.Limits.MaxTokens > 0 && e.state.TokensUsed >= e.Limits.MaxTokens {
		return fmt.Sprintf("token budget exhausted: %d of %d tokens used", e.state.TokensUsed, e.Limits.MaxTokens)
	}
	if e.Limits.MaxActiveTime > 0 && e.state.Active(e.now()) >= e.Limits.MaxActiveTime {
		return fmt.Sprintf("time budget exhausted: ran %s of %s", e.state.Active(e.now()).Round(time.Minute), e.Limits.MaxActiveTime)
	}
	return ""
}

func (e *Engine) work(ctx context.Context) error {
	index := e.state.NextTask()
	if index < 0 {
		if e.state.AllTasksDone() {
			e.notify(ctx, fmt.Sprintf("goal pass %d tasks done; final verification starting", e.state.Pass))
			return e.emit(EventVerificationStarted, "All tasks done; verifying the goal", "", nil)
		}
		return e.escalate(e.state.StuckFailures())
	}
	task := e.state.Tasks[index]
	if task.Status == TaskAttempted {
		return e.checkTask(ctx, index)
	}
	if reason := e.overBudget(); reason != "" {
		_, err := e.end(EventBudgetExceeded, reason)
		return err
	}
	checkValid := false
	if strings.HasPrefix(task.CheckStatus, "pending-") && task.CheckStatus != "pending-self" {
		if err := e.Exec.ValidateCheck(ctx, task.Check); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			writer := strings.TrimPrefix(task.CheckStatus, "pending-")
			reason := fmt.Sprintf("check written by %s cannot run: %v", writer, err)
			return e.failTask(index, TaskCheckedData{TaskID: task.ID, Status: TaskFailed, Outcome: CheckOutcome{Status: CheckInvalid, Reason: reason}}, CheckInvalid, reason)
		}
		checkValid = true
	}
	e.notify(ctx, fmt.Sprintf("goal pass %d task %s worker starting: %s", e.state.Pass, e.state.ShortTaskID(task.ID), Preview(task.Title, 80)))
	if err := e.emit(EventTaskStarted, fmt.Sprintf("Attempt %d started", task.Attempts+1), task.ID, TaskStartedData{TaskID: task.ID, CheckValid: checkValid}); err != nil {
		return err
	}
	handoff, err := e.Roles.Work(ctx, e.state, e.state.Tasks[index])
	if err != nil {
		return err
	}
	data := TaskWorkedData{TaskID: task.ID, Result: handoff.Result, Notes: handoff.Notes, Discoveries: handoff.Discoveries, Stats: handoff.Stats, Status: TaskAttempted, WorkerStatus: WorkerBlocked}
	switch {
	case handoff.Invalid:
		data.Result, data.Notes, data.WorkerStatus = "ok", "worker returned no valid handoff", WorkerContinue
	case handoff.Result == "ok":
		data.WorkerStatus = WorkerDone
	}
	switch data.Result {
	case "blocked":
		data.Status = TaskPending
	case "scope_changed":
		data.Status = TaskFailed
	}
	if err := e.emit(EventTaskWorked, Compact(data.Notes, 500), task.ID, data); err != nil {
		return err
	}
	e.notify(ctx, "goal worker finished: "+Preview(task.Title, 80))
	task = e.state.Tasks[index]
	if data.Result == "blocked" && task.Kind != KindCheck {
		if touched := e.state.ProtectedFilesIn(task.LastOutcome + "\n" + task.NextAction); len(touched) > 0 {
			// No one can grant a work task write access to evidence; asking
			// the user only reruns the same task. The plan must change.
			return e.escalate([]Failure{{Source: "task " + task.ID, Status: "scope_changed", Reason: "work task " + e.state.ShortTaskID(task.ID) + " must change protected evidence files (" + strings.Join(touched, ", ") + "), which work tasks can never write and no one can unlock: move that change into a check task, or revise the evidence. Worker said: " + DefaultString(task.LastOutcome, "blocked")}})
		}
	}
	switch data.Result {
	case "blocked":
		return e.pause(fmt.Sprintf("Task %s (%s) is blocked: %s\nAnswer with the decision or grant the access; the task then reruns.", e.state.ShortTaskID(task.ID), task.Title, DefaultString(task.LastOutcome, "no details")), nil, nil)
	case "scope_changed":
		return e.escalate([]Failure{{Source: "task " + task.ID, Status: "scope_changed", Reason: DefaultString(task.LastOutcome, "worker reported the task is wrong as written")}})
	}
	return nil
}

// failTask records a task that cannot pass as written and escalates.
func (e *Engine) failTask(index int, data TaskCheckedData, status, reason string) error {
	if err := e.emit(EventTaskChecked, data.Outcome.Reason, data.TaskID, data); err != nil {
		return err
	}
	return e.escalate([]Failure{TaskFailure(e.state.Tasks[index], status, reason)})
}

// checkTask runs a task's deterministic check after its worker.
func (e *Engine) checkTask(ctx context.Context, index int) error {
	task := e.state.Tasks[index]
	data := TaskCheckedData{TaskID: task.ID}
	if strings.HasPrefix(task.CheckStatus, "pending-") {
		if err := e.Exec.ValidateCheck(ctx, task.Check); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			reason := "check cannot run after the task: " + err.Error()
			data.Status, data.Outcome = TaskFailed, CheckOutcome{Status: CheckInvalid, Reason: reason}
			return e.failTask(index, data, CheckInvalid, reason)
		}
		data.CheckValid = true
	}
	outcome, err := e.Exec.RunCheck(ctx, task.Check)
	if err != nil {
		return err
	}
	data.Outcome, data.Ran = outcome, true
	switch outcome.Status {
	case CheckPassed:
		data.Status = TaskDone
		done := e.state.Clone()
		done.Tasks[index].Status = TaskDone
		// Check tasks may write evidence; freeze what now exists.
		data.Frozen = Freeze(e.Exec, done, task.Kind == KindCheck)
		if err := e.emit(EventTaskChecked, outcome.Reason, task.ID, data); err != nil {
			return err
		}
		e.notify(ctx, "goal task check passed: "+Preview(task.Title, 80))
		return nil
	case CheckFailed:
		// Retry while each attempt fails differently; the same failure twice
		// means the task as written makes no progress.
		if OutcomeKey(outcome) != task.LastFailureKey {
			data.Status = TaskPending
			if err := e.emit(EventTaskChecked, outcome.Reason, task.ID, data); err != nil {
				return err
			}
			e.notify(ctx, "goal task check failed; retrying with the check output: "+Preview(outcome.Reason, 120))
			return nil
		}
		data.Status = TaskFailed
		return e.failTask(index, data, CheckFailed, fmt.Sprintf("failed the same way after %d attempts: %s", task.Attempts, outcome.Reason))
	case CheckInvalid:
		data.Status = TaskFailed
		return e.failTask(index, data, CheckInvalid, outcome.Reason)
	case CheckEnvBlocked:
		// The work is kept; after the user unblocks it, only the check reruns.
		data.Status = TaskAttempted
		if err := e.emit(EventTaskChecked, outcome.Reason, task.ID, data); err != nil {
			return err
		}
		return e.pause(fmt.Sprintf("The check `%s` for task %s was blocked by the environment: %s\nGrant the access (for example in the sandbox settings) and answer to rerun the check, or revise the plan with /goal:evidence --edit.", task.Check.Label(), e.state.ShortTaskID(task.ID), outcome.Reason), nil, nil)
	}
	return fmt.Errorf("checker returned unknown status %q", outcome.Status)
}

type evidenceReport struct {
	checks     CheckTally
	failures   []Failure
	envBlocked []Failure
	details    string
}

// runEvidence runs every approved evidence check, then each distinct check
// of a done task as a regression.
func (e *Engine) runEvidence(ctx context.Context) (evidenceReport, error) {
	type namedCheck struct {
		source string
		check  Check
	}
	var checks []namedCheck
	seen := map[string]bool{}
	for _, item := range e.state.Evidence.Items {
		checks = append(checks, namedCheck{item.ID, item.Check})
		seen[item.Check.Key()] = true
	}
	evidenceTotal := len(checks)
	for _, task := range e.state.Tasks {
		if task.Status != TaskDone || task.Check.Empty() || seen[task.Check.Key()] {
			continue
		}
		seen[task.Check.Key()] = true
		checks = append(checks, namedCheck{"regression " + task.ID, task.Check})
	}
	report := evidenceReport{checks: CheckTally{RegressionTotal: len(checks) - evidenceTotal}}
	var details []string
	for i, item := range checks {
		outcome, err := e.Exec.RunCheck(ctx, item.check)
		if err != nil {
			return report, err
		}
		status := outcome.Status
		if outcome.Suspect {
			status += ", but the output reports a failure: judge whether it is real"
		}
		details = append(details, fmt.Sprintf("%s | %s | %s (exit %d)\n%s", item.source, item.check.Label(), status, outcome.ExitCode, Compact(outcome.Tail, 1200)))
		if outcome.Status == CheckPassed {
			if i < evidenceTotal {
				report.checks.EvidencePassed++
			} else {
				report.checks.RegressionPassed++
			}
			continue
		}
		failure := Failure{Source: item.source, Check: item.check.Label(), Key: item.check.Key(), Status: outcome.Status, Reason: outcome.Reason, Tail: outcome.Tail}
		if outcome.Status == CheckEnvBlocked {
			report.envBlocked = append(report.envBlocked, failure)
		} else {
			report.failures = append(report.failures, failure)
		}
	}
	report.details = strings.Join(details, "\n\n")
	return report, nil
}

func (e *Engine) verify(ctx context.Context) error {
	if tampered := Tampered(e.Exec, e.state); len(tampered) > 0 {
		return e.pause(TamperedQuestion(tampered), tampered, nil)
	}
	e.notify(ctx, "goal overall verification started")
	report, err := e.runEvidence(ctx)
	if err != nil {
		return err
	}
	data := VerificationData{Checks: report.checks, Failures: report.failures, EnvBlocked: report.envBlocked, PrevEvidenceFailures: e.state.PrevEvidenceFailures}
	if len(report.envBlocked) > 0 {
		if err := e.emit(EventVerificationFinished, "Verification blocked by the environment", "", data); err != nil {
			return err
		}
		return e.pause("Verification was blocked by the environment, which work cannot fix:\n- "+strings.Join(FailureLines(report.envBlocked), "\n- ")+"\nGrant the access (for example in the sandbox settings) and answer to rerun verification, or revise the evidence with /goal:evidence --edit.", nil, nil)
	}
	if len(report.failures) > 0 {
		stuck := e.stuckEvidence(report.failures, &data)
		if err := e.emit(EventVerificationFinished, "Verification failed: "+SummarizeFailures(report.failures), "", data); err != nil {
			return err
		}
		if len(stuck) > 0 {
			return e.pause(EvidenceQuestion(stuck), nil, report.failures)
		}
		e.notify(ctx, "goal overall check found a gap: "+Preview(SummarizeFailures(report.failures), 160))
		return e.escalate(report.failures)
	}
	if err := e.emit(EventVerificationFinished, fmt.Sprintf("Passed %d goal checks and %d/%d regression checks", report.checks.EvidencePassed, report.checks.RegressionPassed, report.checks.RegressionTotal), "", data); err != nil {
		return err
	}
	e.notify(ctx, "goal review started")
	review, err := e.Roles.Review(ctx, e.state, report.details)
	if err != nil {
		return err
	}
	if err := e.emit(EventReviewed, fmt.Sprintf("Review: %d findings (%d blocking)", len(review.Findings), review.BlockingFindings()), "", ReviewedData{Review: review, Tokens: review.Tokens}); err != nil {
		return err
	}
	e.notify(ctx, ReviewProgress(review))
	verdict, err := e.Roles.Judge(ctx, e.state, report.details, review)
	if err != nil {
		return err
	}
	if err := e.emit(EventJudged, Compact(verdict.Reason, 300), "", JudgedData{Verdict: verdict, Tokens: verdict.Tokens}); err != nil {
		return err
	}
	e.notify(ctx, VerdictProgress(e.state.Summary, review, verdict))
	if verdict.Done {
		_, err := e.end(EventCompleted, verdict.Reason)
		return err
	}
	return e.escalate(verdict.Failures(review))
}

// stuckEvidence returns the evidence failures that work cannot fix: checks
// that cannot run as written, or, when only evidence fails, the same
// evidence failing again after a replan. Replans cannot change approved
// evidence, so only the user can unblock these.
func (e *Engine) stuckEvidence(failures []Failure, data *VerificationData) []Failure {
	ids := e.state.EvidenceFailureIDs(failures)
	repeated := ids != nil && slices.Equal(ids, e.state.PrevEvidenceFailures)
	data.PrevEvidenceFailures = ids
	if repeated {
		return failures
	}
	evidence := make(map[string]bool, len(e.state.Evidence.Items))
	for _, item := range e.state.Evidence.Items {
		evidence[item.ID] = true
	}
	var invalid []Failure
	for _, failure := range failures {
		if evidence[failure.Source] && failure.Status == CheckInvalid {
			invalid = append(invalid, failure)
		}
	}
	return invalid
}

func (e *Engine) replan(ctx context.Context) error {
	if reason := e.overBudget(); reason != "" {
		_, err := e.end(EventBudgetExceeded, reason)
		return err
	}
	if e.state.NoProgressPasses >= 2 {
		_, err := e.end(EventStalled, "no progress: the same failures repeated across two passes: "+SummarizeFailures(e.state.Failures))
		return err
	}
	e.notify(ctx, "goal revising remaining plan")
	proposal, err := e.Roles.Plan(ctx, e.state, PlanRequest{Reason: fmt.Sprintf("replan after pass %d", e.state.Pass), Mode: EvidenceLocked})
	if err != nil {
		return err
	}
	data, err := PlanFromProposal(e.state, proposal, EvidenceLocked)
	if err != nil {
		return err
	}
	if data.Plan == nil {
		return e.pause(data.Question, nil, nil)
	}
	return e.emit(EventReplanned, fmt.Sprintf("Revised plan v%d", e.state.PlanVersion+1), "", data)
}

func (e *Engine) escalate(failures []Failure) error {
	return e.emit(EventEscalated, SummarizeFailures(failures), "", EscalatedData{Failures: failures})
}

func (e *Engine) pause(question string, tampered []string, failures []Failure) error {
	return e.emit(EventPaused, question, "", PausedData{Question: question, Tampered: tampered, Failures: failures})
}

// end records a terminal event.
func (e *Engine) end(eventType, reason string) (State, error) {
	err := e.emit(eventType, reason, "", EndedData{Reason: reason})
	return e.state, err
}

func (e *Engine) emit(eventType, summary, taskID string, data any) error {
	event, err := e.build(e.state, eventType, summary, taskID, data)
	if err != nil {
		return recordError{err}
	}
	return e.record(event)
}

func (e *Engine) emitEvent(event eventing.GoalEvent, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return recordError{err}
	}
	event.Revision, event.At, event.Data = e.state.Revision+1, e.now(), payload
	return e.record(event)
}

func (e *Engine) build(state State, eventType, summary, taskID string, data any) (eventing.GoalEvent, error) {
	event := eventing.GoalEvent{Type: eventType, GoalID: state.ID, Revision: state.Revision + 1, At: e.now(), Summary: Compact(summary, 300), TaskID: taskID}
	if data != nil {
		payload, err := json.Marshal(data)
		if err != nil {
			return event, err
		}
		event.Data = payload
	}
	return event, nil
}

// record applies an event to a copy of the state, persists it, and only
// then adopts the new state.
func (e *Engine) record(event eventing.GoalEvent) error {
	next := e.state.Clone()
	if err := next.Apply(event); err != nil {
		return recordError{err}
	}
	event.Stage = eventLabels[event.Type]
	event.Status, event.NextAction = next.Stage.String(), next.NextCommand()
	if event.TaskID != "" {
		if index := next.TaskIndex(event.TaskID); index >= 0 {
			event.TaskTitle, event.TaskStatus = Compact(next.Tasks[index].Title, 120), string(next.Tasks[index].Status)
		}
	}
	if err := e.Store.Append(event); err != nil {
		return recordError{err}
	}
	e.state = next
	if err := e.Store.Sync(next); err != nil {
		return recordError{err}
	}
	return nil
}

// eventLabels name each event's step in timelines.
var eventLabels = map[string]string{
	EventCreated: "created", EventPlanCreated: "planning",
	EventApproved: "approval", EventRunStarted: "running", EventTaskStarted: "task", EventTaskWorked: "task",
	EventTaskChecked: "task-check", EventVerificationStarted: "verify", EventVerificationFinished: "verification",
	EventReviewed: "review", EventJudged: "judge", EventEscalated: "escalation", EventReplanned: "replanning",
	EventPaused: "paused", EventAnswered: "answer", EventEditRequested: "edit", EventRetryRequested: "retry",
	EventInterrupted: "stopped", EventCompleted: "done", EventStopped: "stopped", EventStalled: "stalled",
	EventBudgetExceeded: "budget",
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) notify(ctx context.Context, message string) {
	if e.Notify != nil {
		e.Notify(ctx, message)
	}
}

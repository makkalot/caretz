package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
)

// ErrLegacy marks a goal recorded by an older caretz, whose events do not
// carry the data needed to rebuild it.
var ErrLegacy = errors.New("this goal was recorded by an older caretz and cannot be continued; start a new /goal")

// transitions lists the stages each event may be applied in. An event in any
// other stage is a bug in the engine, never a user error.
var transitions = map[string][]Stage{
	EventCreated:              {StageNone},
	EventPlanCreated:          {Planning},
	EventApproved:             {AwaitingApproval},
	EventRunStarted:           {Working, Verifying, Replanning},
	EventTaskStarted:          {Working},
	EventTaskWorked:           {Working},
	EventTaskChecked:          {Working},
	EventVerificationStarted:  {Working},
	EventVerificationFinished: {Verifying},
	EventReviewed:             {Verifying},
	EventJudged:               {Verifying},
	EventEscalated:            {Working, Verifying},
	EventReplanned:            {Replanning},
	EventPaused:               {Planning, Working, Verifying, Replanning},
	EventAnswered:             {AwaitingInput, AwaitingApproval},
	EventEditRequested:        {AwaitingApproval, AwaitingInput, Done, Stalled, Stopped, OverBudget},
	EventRetryRequested:       {AwaitingApproval, AwaitingInput, Done, Stalled, Stopped, OverBudget},
	EventInterrupted:          {Planning, Working, Verifying, Replanning},
	EventCompleted:            {Verifying},
	EventStalled:              {Working, Verifying, Replanning},
	EventStopped:              {Planning, AwaitingApproval, AwaitingInput, Working, Verifying, Replanning},
	EventBudgetExceeded:       {Working, Replanning},
}

// Allowed reports whether an event of eventType may be applied in stage.
func Allowed(eventType string, stage Stage) bool {
	for _, allowed := range transitions[eventType] {
		if allowed == stage {
			return true
		}
	}
	return false
}

// Replay folds the events of the latest goal in a session log. found is
// false when the session has no goal.
func Replay(events []eventing.GoalEvent) (state State, found bool, err error) {
	start := -1
	for i := range events {
		if events[i].Type == EventCreated {
			start = i
		}
	}
	if start < 0 {
		return State{}, false, nil
	}
	if len(events[start].Data) == 0 {
		return State{}, true, ErrLegacy
	}
	for _, event := range events[start:] {
		if err := state.Apply(event); err != nil {
			return state, true, fmt.Errorf("replay goal %s revision %d: %w", event.GoalID, event.Revision, err)
		}
	}
	return state, true, nil
}

// Clone returns a deep copy, so a failed append cannot leave a half-applied
// state behind.
func (s State) Clone() State {
	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("clone goal state: %v", err))
	}
	var clone State
	if err := json.Unmarshal(data, &clone); err != nil {
		panic(fmt.Sprintf("clone goal state: %v", err))
	}
	return clone
}

func decode[T any](event eventing.GoalEvent) (T, error) {
	var data T
	if len(event.Data) == 0 {
		return data, nil
	}
	err := json.Unmarshal(event.Data, &data)
	return data, err
}

// Apply folds one event into the state.
func (s *State) Apply(event eventing.GoalEvent) error {
	if _, known := transitions[event.Type]; !known {
		return fmt.Errorf("unknown goal event %q", event.Type)
	}
	if !Allowed(event.Type, s.Stage) {
		return fmt.Errorf("goal event %s is not allowed in stage %s", event.Type, s.Stage)
	}
	if event.Type != EventCreated && event.GoalID != s.ID {
		return fmt.Errorf("goal event for %s applied to goal %s", event.GoalID, s.ID)
	}
	if event.Revision != s.Revision+1 {
		return fmt.Errorf("goal event revision %d does not follow %d", event.Revision, s.Revision)
	}
	if err := s.apply(event); err != nil {
		return fmt.Errorf("%s: %w", event.Type, err)
	}
	s.Revision = event.Revision
	return nil
}

func (s *State) apply(event eventing.GoalEvent) error {
	at := event.At
	switch event.Type {
	case EventCreated:
		data, err := decode[CreatedData](event)
		if err != nil {
			return err
		}
		*s = State{
			ID: event.GoalID, Goal: event.Goal, SessionID: data.SessionID, Adapter: data.Adapter,
			Stage: Planning, PlanMode: EvidenceProposal, AutoApprove: data.AutoApprove, Models: data.Models,
			BaseRevision: data.BaseRevision, StartDirty: data.StartDirty, References: data.References,
			Pass: 1, Evidence: Evidence{Version: 1},
		}
	case EventPlanCreated:
		data, err := decode[PlanData](event)
		if err != nil {
			return err
		}
		if err := s.applyPlan(data); err != nil {
			return err
		}
		s.TokensUsed += data.Tokens
		s.Stage = AwaitingApproval
	case EventApproved:
		data, err := decode[ApprovedData](event)
		if err != nil {
			return err
		}
		if s.Question != "" {
			return fmt.Errorf("the plan has an unanswered question")
		}
		if data.Models != nil {
			s.Models = s.Models.Override(*data.Models)
		}
		s.PendingEdit, s.Approved, s.Stage = nil, true, Working
	case EventRunStarted:
		data, err := decode[RunStartedData](event)
		if err != nil {
			return err
		}
		for i := range s.Tasks {
			if s.Tasks[i].Status == TaskRunning {
				// The previous process died mid-worker; rerun that attempt.
				s.Tasks[i].Status = TaskPending
			}
		}
		s.mergeFrozen(data.Frozen)
		// A saved start time belongs to an interrupted process; time between
		// processes is not charged to the active-time budget.
		s.RunningSince, s.Question = &at, ""
	case EventTaskStarted:
		data, err := decode[TaskStartedData](event)
		if err != nil {
			return err
		}
		task, err := s.task(data.TaskID)
		if err != nil {
			return err
		}
		if data.CheckValid {
			task.CheckStatus = "valid"
		}
		task.Status, task.Attempts = TaskRunning, task.Attempts+1
		s.WorkerAttempts++
	case EventTaskWorked:
		data, err := decode[TaskWorkedData](event)
		if err != nil {
			return err
		}
		task, err := s.task(data.TaskID)
		if err != nil {
			return err
		}
		task.LastOutcome = Compact(data.Notes, 500)
		task.WorkerStatus, task.Stats, task.LastPass, task.Status = data.WorkerStatus, data.Stats, s.Pass, data.Status
		s.TokensUsed += BillableTokens(data.Stats)
		for _, discovery := range data.Discoveries {
			if value := Compact(discovery, 300); value != "" {
				s.note(EntryDiscovery, task.ID, task.ID+": "+value)
			}
		}
		s.logf(task.ID, "%s executor (%s): %s", task.ID, data.Result, Preview(task.LastOutcome, 160))
	case EventTaskChecked:
		data, err := decode[TaskCheckedData](event)
		if err != nil {
			return err
		}
		task, err := s.task(data.TaskID)
		if err != nil {
			return err
		}
		if data.CheckValid {
			task.CheckStatus = "valid"
		}
		task.LastCheck = data.Outcome.Reason
		if data.Ran {
			task.LastCheckOutput = data.Outcome.Tail
			task.CheckHistory = append(task.CheckHistory, TaskCheck{Attempt: task.Attempts, Passed: data.Outcome.Status == CheckPassed, Reason: data.Outcome.Status + ": " + data.Outcome.Reason})
		}
		if data.Outcome.Status == CheckFailed {
			task.LastFailureKey = OutcomeKey(data.Outcome)
		}
		task.Status = data.Status
		s.mergeFrozen(data.Frozen)
		switch {
		case data.Outcome.Status == CheckPassed:
			s.logf(task.ID, "%s ok (att %d)", task.ID, task.Attempts)
		case data.Outcome.Status == CheckFailed && task.Status == TaskPending:
			s.logf(task.ID, "%s fail (att %d): %s", task.ID, task.Attempts, data.Outcome.Reason)
		case data.Outcome.Status == CheckFailed:
			s.logf(task.ID, "%s failed the same way twice: %s", task.ID, data.Outcome.Reason)
		default:
			s.logf(task.ID, "%s %s: %s", task.ID, data.Outcome.Status, data.Outcome.Reason)
		}
	case EventVerificationStarted:
		s.Stage = Verifying
	case EventVerificationFinished:
		data, err := decode[VerificationData](event)
		if err != nil {
			return err
		}
		s.Checks, s.PrevEvidenceFailures = data.Checks, data.PrevEvidenceFailures
		if len(data.Failures) > 0 {
			s.logf("", "verify: %s", Preview(SummarizeFailures(data.Failures), 200))
		}
	case EventReviewed:
		data, err := decode[ReviewedData](event)
		if err != nil {
			return err
		}
		review := data.Review
		s.Review, s.Verdict = &review, nil
		s.TokensUsed += data.Tokens
		s.logf("", "review: %d findings (%d blocking) — %s", len(review.Findings), review.BlockingFindings(), Preview(review.Summary, 160))
	case EventJudged:
		data, err := decode[JudgedData](event)
		if err != nil {
			return err
		}
		verdict := data.Verdict
		s.Verdict = &verdict
		s.TokensUsed += data.Tokens
		s.Summary = Compact(DefaultString(verdict.Summary, FirstSentence(verdict.Reason)), 300)
		result := "gap"
		if verdict.Done {
			result = "done"
		}
		s.logf("", "judge: %s — %s", result, Preview(verdict.Reason, 200))
	case EventEscalated:
		data, err := decode[EscalatedData](event)
		if err != nil {
			return err
		}
		s.Failures = data.Failures
		s.note(EntryFeedback, "", Compact(fmt.Sprintf("pass %d: %s", s.Pass, SummarizeFailures(data.Failures)), 500))
		if key := FailureKey(data.Failures); key == s.NoProgressKey {
			s.NoProgressPasses++
		} else {
			s.NoProgressKey, s.NoProgressPasses = key, 1
		}
		s.Stage = Replanning
	case EventReplanned:
		data, err := decode[PlanData](event)
		if err != nil {
			return err
		}
		if data.Plan == nil {
			return fmt.Errorf("a replan needs a plan")
		}
		if err := s.applyPlan(data); err != nil {
			return err
		}
		s.TokensUsed += data.Tokens
		s.Replans++
		s.Pass++
		s.Answer = ""
		s.Stage = Working
	case EventPaused:
		data, err := decode[PausedData](event)
		if err != nil {
			return err
		}
		s.Question, s.TamperedEvidence = data.Question, data.Tampered
		if data.Failures != nil {
			s.Failures = data.Failures
		}
		s.PausedFrom, s.Stage = s.Stage, AwaitingInput
		s.stopClock(at)
		s.logf("", "paused: %s", Preview(data.Question, 160))
	case EventAnswered:
		data, err := decode[AnsweredData](event)
		if err != nil {
			return err
		}
		if s.Question == "" {
			return fmt.Errorf("no question is pending")
		}
		s.note(EntryFeedback, "", Compact("User answered: "+Preview(s.Question, 200)+" → "+data.Answer, 500))
		if len(s.TamperedEvidence) > 0 {
			s.mergeFrozen(data.Accepted)
			s.logf("", "user accepted evidence change: %s", joinComma(s.TamperedEvidence))
		}
		s.Answer, s.Question, s.TamperedEvidence = data.Answer, "", nil
		if s.Stage == AwaitingApproval || len(s.Tasks) == 0 || s.PendingEdit != nil || s.PausedFrom == Planning || !s.PausedFrom.Running() {
			s.Stage = Planning
		} else {
			s.Stage = s.PausedFrom
		}
	case EventEditRequested:
		data, err := decode[EditData](event)
		if err != nil {
			return err
		}
		edit := data.Edit
		if data.Models != nil {
			s.Models = s.Models.Override(*data.Models)
		}
		switch edit.Target {
		case EditPlan:
			if s.Stage != AwaitingApproval {
				return fmt.Errorf("a plan edit needs a plan awaiting approval")
			}
			s.PlanMode = EvidenceProposal
		case EditEvidence:
			s.note(EntryFeedback, "", Compact("evidence change requested: "+edit.Text, 500))
			s.ExitReason, s.TamperedEvidence, s.PrevEvidenceFailures = "", nil, nil
			s.NoProgressKey, s.NoProgressPasses = "", 0
			s.PlanMode = EvidenceRevision
		default:
			return fmt.Errorf("unknown edit target %q", edit.Target)
		}
		s.PendingEdit, s.Question, s.Stage = &edit, "", Planning
	case EventRetryRequested:
		data, err := decode[RetryData](event)
		if err != nil {
			return err
		}
		if data.Models != nil {
			s.Models = s.Models.Override(*data.Models)
		}
		s.ExitReason, s.Question, s.Answer, s.PendingEdit = "", "", "", nil
		s.Replans, s.WorkerAttempts, s.TokensUsed, s.ActiveTime, s.RunningSince = 0, 0, 0, 0, nil
		s.Checks, s.NoProgressKey, s.NoProgressPasses = CheckTally{}, "", 0
		s.PrevEvidenceFailures, s.TamperedEvidence = nil, nil
		s.AutoApprove = false
		s.Pass++
		s.PlanMode = EvidenceLocked
		if data.Feedback != "" {
			s.note(EntryFeedback, "", Compact("retry requested: "+data.Feedback, 500))
			s.PlanMode = EvidenceRetry
		}
		s.Stage = Planning
	case EventCompleted, EventStalled, EventStopped, EventInterrupted, EventBudgetExceeded:
		data, err := decode[EndedData](event)
		if err != nil {
			return err
		}
		s.stopClock(at)
		s.ExitReason = Compact(data.Reason, 500)
		switch event.Type {
		case EventCompleted:
			s.Stage, s.Failures = Done, nil
		case EventStalled:
			s.Stage = Stalled
		case EventBudgetExceeded:
			s.Stage = OverBudget
		default:
			s.Stage, s.Question = Stopped, ""
		}
	}
	return nil
}

// applyPlan replaces the unfinished tasks: active tasks missing from the
// plan are superseded; done and superseded tasks stay as history.
func (s *State) applyPlan(data PlanData) error {
	for _, answer := range data.Defaults {
		s.note(EntryDiscovery, "", "default answer: "+answer)
	}
	s.Question = data.Question
	if data.Plan == nil {
		return nil
	}
	proposed := data.Plan.Tasks
	active := make(map[string]bool, len(proposed))
	for _, task := range proposed {
		if active[task.ID] {
			return fmt.Errorf("plan repeats task %s", task.ID)
		}
		if index := s.TaskIndex(task.ID); index >= 0 && s.Tasks[index].Status.Closed() {
			return fmt.Errorf("plan changes closed task %s", task.ID)
		}
		active[task.ID] = true
	}
	history := make([]Task, 0, len(s.Tasks)+len(proposed))
	for _, task := range s.Tasks {
		if active[task.ID] {
			continue
		}
		if !task.Status.Closed() {
			task.Status = TaskSuperseded
		}
		history = append(history, task)
	}
	s.Tasks = append(history, proposed...)
	s.PlanVersion++
	s.PlanSummary = Compact(data.Plan.Summary, 800)
	if data.Plan.Evidence != nil {
		s.Evidence = *data.Plan.Evidence
	}
	return nil
}

func (s *State) task(id string) (*Task, error) {
	index := s.TaskIndex(id)
	if index < 0 {
		return nil, fmt.Errorf("unknown task %s", id)
	}
	return &s.Tasks[index], nil
}

// mergeFrozen records evidence file hashes by evidence ID.
func (s *State) mergeFrozen(frozen map[string]map[string]string) {
	for i := range s.Evidence.Items {
		item := &s.Evidence.Items[i]
		for rel, hash := range frozen[item.ID] {
			if item.Files == nil {
				item.Files = map[string]string{}
			}
			item.Files[rel] = hash
		}
	}
}

func (s *State) stopClock(at time.Time) {
	if s.RunningSince != nil {
		if elapsed := at.Sub(*s.RunningSince); elapsed > 0 {
			s.ActiveTime += elapsed
		}
		s.RunningSince = nil
	}
}

func (s *State) note(kind EntryKind, taskID, text string) {
	s.Journal = append(s.Journal, Entry{Kind: kind, Pass: s.Pass, TaskID: taskID, Text: text})
}

func (s *State) logf(taskID, format string, args ...any) {
	s.note(EntryLog, taskID, fmt.Sprintf("c%d ", s.Pass)+fmt.Sprintf(format, args...))
}

func joinComma(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ", "
		}
		out += value
	}
	return out
}

package goal

import (
	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
)

// Event payloads. Each records a decision the engine already made, so Apply
// is deterministic and never reads the workspace or calls a model.

type CreatedData struct {
	SessionID    string            `json:"session_id"`
	Adapter      string            `json:"adapter"`
	AutoApprove  bool              `json:"auto_approve,omitempty"`
	Models       Models            `json:"models,omitempty"`
	BaseRevision string            `json:"base_revision,omitempty"`
	StartDirty   map[string]string `json:"start_dirty,omitempty"`
	References   []Reference       `json:"references,omitempty"`
}

// PlanData is a planning result. Plan is nil when the planner only asked a
// question about the current plan.
type PlanData struct {
	Plan *PlanBody `json:"plan,omitempty"`
	// Question asks the user; with a plan awaiting approval it is attached
	// to the proposal, otherwise the goal waits for an answer.
	Question string `json:"question,omitempty"`
	// Defaults are planner questions answered by their defaults (--yes).
	Defaults []string `json:"defaults,omitempty"`
	// Tokens is what the planner spent.
	Tokens int64 `json:"tokens,omitempty"`
}

type PlanBody struct {
	// Tasks is the complete ordered list of unfinished tasks, with harness
	// IDs. Active tasks missing from it are superseded.
	Tasks    []Task    `json:"tasks"`
	Evidence *Evidence `json:"evidence,omitempty"`
	Summary  string    `json:"summary,omitempty"`
}

// RunStartedData freezes evidence files that exist now (see Freeze).
type RunStartedData struct {
	Frozen map[string]map[string]string `json:"frozen,omitempty"`
}

type TaskStartedData struct {
	TaskID     string `json:"task_id"`
	CheckValid bool   `json:"check_valid,omitempty"`
}

type TaskWorkedData struct {
	TaskID       string       `json:"task_id"`
	Result       string       `json:"result"`
	Notes        string       `json:"notes,omitempty"`
	Discoveries  []string     `json:"discoveries,omitempty"`
	WorkerStatus string       `json:"worker_status,omitempty"`
	Status       TaskStatus   `json:"status"`
	Stats        *tasks.Stats `json:"stats,omitempty"`
}

type TaskCheckedData struct {
	TaskID  string       `json:"task_id"`
	Status  TaskStatus   `json:"status"`
	Outcome CheckOutcome `json:"outcome"`
	// Ran is false when the check could not run at all.
	Ran        bool                         `json:"ran,omitempty"`
	CheckValid bool                         `json:"check_valid,omitempty"`
	Frozen     map[string]map[string]string `json:"frozen,omitempty"`
}

type VerificationData struct {
	Checks               CheckTally `json:"checks"`
	Failures             []Failure  `json:"failures,omitempty"`
	EnvBlocked           []Failure  `json:"env_blocked,omitempty"`
	PrevEvidenceFailures []string   `json:"prev_evidence_failures,omitempty"`
}

type ReviewedData struct {
	Review Review `json:"review"`
	Tokens int64  `json:"tokens,omitempty"`
}

type JudgedData struct {
	Verdict Verdict `json:"verdict"`
	Tokens  int64   `json:"tokens,omitempty"`
}

type EscalatedData struct {
	Failures []Failure `json:"failures"`
}

type PausedData struct {
	Question string    `json:"question"`
	Tampered []string  `json:"tampered,omitempty"`
	Failures []Failure `json:"failures,omitempty"`
}

type AnsweredData struct {
	Answer string `json:"answer"`
	// Accepted refreezes evidence the user accepted after it changed.
	Accepted map[string]map[string]string `json:"accepted,omitempty"`
}

type EditData struct {
	Edit   Edit    `json:"edit"`
	Models *Models `json:"models,omitempty"`
}

type ApprovedData struct {
	Models *Models `json:"models,omitempty"`
}

type RetryData struct {
	Reason string `json:"reason,omitempty"`
	// Feedback is what the user asked the retry to do differently.
	Feedback string  `json:"feedback,omitempty"`
	Models   *Models `json:"models,omitempty"`
}

type EndedData struct {
	Reason string `json:"reason"`
}

// Event types. They live in lib/eventing so the session log recognizes them.
const (
	EventCreated              = eventing.GoalCreated
	EventPlanCreated          = eventing.GoalPlanCreated
	EventApproved             = eventing.GoalApproved
	EventRunStarted           = eventing.GoalRunStarted
	EventTaskStarted          = eventing.GoalTaskStarted
	EventTaskWorked           = eventing.GoalTaskCompleted
	EventTaskChecked          = eventing.GoalTaskChecked
	EventVerificationStarted  = eventing.GoalVerificationStarted
	EventVerificationFinished = eventing.GoalVerificationCompleted
	EventReviewed             = eventing.GoalReviewed
	EventJudged               = eventing.GoalJudged
	EventEscalated            = eventing.GoalEscalated
	EventReplanned            = eventing.GoalReplanCreated
	EventPaused               = eventing.GoalPaused
	EventAnswered             = eventing.GoalAnswered
	EventEditRequested        = eventing.GoalEditRequested
	EventRetryRequested       = eventing.GoalRetryRequested
	EventInterrupted          = eventing.GoalInterrupted
	EventCompleted            = eventing.GoalCompleted
	EventStopped              = eventing.GoalStopped
	EventStalled              = eventing.GoalStalled
	EventBudgetExceeded       = eventing.GoalBudgetExceeded
)

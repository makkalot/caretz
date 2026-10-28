// Package goal implements /goal: a harness that plans a goal into tasks,
// runs workers, grades each task with a deterministic check, verifies the
// whole goal with approved evidence, and replans on any gap.
//
// Three model roles do the work: a planner (which also explores the
// workspace), workers, and a verifier that reviews the result and decides.
// The goal's event log is its only source of truth. State is a fold of the
// events (see Apply); state.md, task logs and the session task panel are
// views derived from it.
package goal

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/tasks"
)

// Stage is where a goal is in its life cycle. Every event moves a goal from
// one stage to another; Apply rejects any move the transition table forbids.
type Stage int

const (
	StageNone Stage = iota
	Planning
	AwaitingApproval
	AwaitingInput
	Working
	Verifying
	Replanning
	Done
	Stalled
	Stopped
	OverBudget
)

var stageNames = [...]string{"none", "planning", "awaiting_approval", "awaiting_input", "working", "verifying", "replanning", "done", "stalled", "stopped", "over_budget"}

func (s Stage) String() string {
	if int(s) < len(stageNames) {
		return stageNames[s]
	}
	return fmt.Sprintf("stage(%d)", int(s))
}

func (s Stage) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

func (s *Stage) UnmarshalText(text []byte) error {
	for i, name := range stageNames {
		if name == string(text) {
			*s = Stage(i)
			return nil
		}
	}
	return fmt.Errorf("unknown goal stage %q", text)
}

// Running reports a stage in which the harness drives workers and checks.
func (s Stage) Running() bool { return s == Working || s == Verifying || s == Replanning }

// Terminal reports a stage that only /goal:retry or a new goal leaves.
func (s Stage) Terminal() bool {
	return s == Done || s == Stalled || s == Stopped || s == OverBudget
}

// Active reports a goal that blocks starting another one.
func (s Stage) Active() bool { return s != StageNone && !s.Terminal() }

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskRunning    TaskStatus = "running"
	TaskAttempted  TaskStatus = "attempted"
	TaskDone       TaskStatus = "done"
	TaskFailed     TaskStatus = "failed"
	TaskSuperseded TaskStatus = "superseded"
)

// Closed reports a task no plan may change again.
func (s TaskStatus) Closed() bool { return s == TaskDone || s == TaskSuperseded }

// Task kinds. A check task writes verification only; a work task changes the
// product and is graded by a check it did not write.
const (
	KindWork  = "work"
	KindCheck = "check"
)

type Task struct {
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Kind            string       `json:"kind,omitempty"`
	NextAction      string       `json:"next_action"`
	DependsOn       []string     `json:"depends_on,omitempty"`
	Check           Check        `json:"check,omitempty"`
	CheckStatus     string       `json:"check_status,omitempty"`
	Validation      []string     `json:"validation,omitempty"`
	CheckHistory    []TaskCheck  `json:"check_history,omitempty"`
	Status          TaskStatus   `json:"status"`
	Attempts        int          `json:"attempts"`
	LastOutcome     string       `json:"last_outcome,omitempty"`
	WorkerStatus    string       `json:"worker_status,omitempty"`
	LastPass        int          `json:"last_pass,omitempty"`
	LastCheck       string       `json:"last_check,omitempty"`
	LastCheckOutput string       `json:"last_check_output,omitempty"`
	LastFailureKey  string       `json:"last_failure_key,omitempty"`
	Stats           *tasks.Stats `json:"stats,omitempty"`
}

type TaskCheck struct {
	Attempt int    `json:"attempt"`
	Passed  bool   `json:"passed"`
	Reason  string `json:"reason"`
}

type Evidence struct {
	Version  int            `json:"version,omitempty"`
	Items    []EvidenceItem `json:"items,omitempty"`
	Criteria []string       `json:"criteria,omitempty"`
}

type EvidenceItem struct {
	ID     string `json:"id"`
	Check  Check  `json:"check"`
	Covers string `json:"covers"`
	Gaps   string `json:"gaps,omitempty"`
	Status string `json:"status"`
	// Files maps each workspace file the check depends on (its script, or
	// the test files a test runner runs) to its sha256 when the evidence was
	// frozen. Work tasks cannot write these files; a change is tampering.
	Files map[string]string `json:"files,omitempty"`
}

// Failure is one item of the structured report that drives a replan.
type Failure struct {
	Source string `json:"source"`
	Check  string `json:"check,omitempty"`
	Key    string `json:"key,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	Tail   string `json:"output_tail,omitempty"`
}

// Reference is material the goal links to, such as the issue it implements.
// The harness fetches it once so every role works from the same specification.
type Reference struct {
	URL     string `json:"url"`
	Source  string `json:"source,omitempty"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

type CriterionResult struct {
	Criterion string   `json:"criterion"`
	Status    string   `json:"status"`
	Evidence  []string `json:"evidence"`
}

type Finding struct {
	Severity  string `json:"severity"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Criterion string `json:"criterion,omitempty"`
	Problem   string `json:"problem"`
	Fix       string `json:"fix,omitempty"`
}

func (f Finding) Location() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

func (f Finding) Blocking() bool {
	return f.Severity == "blocker" || f.Severity == "major"
}

// Review is the reviewer's report on the change: whether each criterion is
// implemented, with the code it cites, and the defects it found.
type Review struct {
	Criteria []CriterionResult `json:"criteria"`
	Findings []Finding         `json:"findings"`
	Summary  string            `json:"summary,omitempty"`
	// Tokens is what the reviewer spent; it is charged to the budget.
	Tokens int64 `json:"-"`
}

func (r Review) BlockingFindings() int {
	count := 0
	for _, finding := range r.Findings {
		if finding.Blocking() {
			count++
		}
	}
	return count
}

// Validate applies the review rules that need no workspace: one result per
// criterion in order, known statuses and severities, a file and a problem
// for every finding.
func (r Review) Validate(criteria []string) error {
	if len(r.Criteria) != len(criteria) {
		return fmt.Errorf("criteria needs exactly %d entries, one per overall criterion in order; got %d", len(criteria), len(r.Criteria))
	}
	for i, item := range r.Criteria {
		switch item.Status {
		case "met", "unmet", "unclear":
		default:
			return fmt.Errorf("criteria[%d].status must be met, unmet or unclear", i)
		}
	}
	for i, finding := range r.Findings {
		switch finding.Severity {
		case "blocker", "major", "minor":
		default:
			return fmt.Errorf("findings[%d].severity must be blocker, major or minor", i)
		}
		if strings.TrimSpace(finding.Problem) == "" || strings.TrimSpace(finding.File) == "" {
			return fmt.Errorf("findings[%d] needs a file and a problem", i)
		}
	}
	return nil
}

// FindingDecision is the judge's disposition of review finding F<Index>.
type FindingDecision struct {
	Index    int    `json:"index"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// Verdict is the judge's final decision, made from the check results and
// the review. The judge accepts or dismisses every blocking finding.
type Verdict struct {
	Done      bool              `json:"done"`
	Reason    string            `json:"reason"`
	Summary   string            `json:"summary,omitempty"`
	Gaps      []string          `json:"gaps,omitempty"`
	Decisions []FindingDecision `json:"findings,omitempty"`
	// Tokens is what the judge spent; it is charged to the budget.
	Tokens int64 `json:"-"`
}

// Decision returns the judge's decision on finding F<index>.
func (v Verdict) Decision(index int) (FindingDecision, bool) {
	for _, decision := range v.Decisions {
		if decision.Index == index {
			return decision, true
		}
	}
	return FindingDecision{}, false
}

// Validate makes the judge address every blocking finding of review, and
// forbids declaring the goal done while accepting one.
func (v Verdict) Validate(review Review) error {
	if strings.TrimSpace(v.Reason) == "" {
		return fmt.Errorf("reason is required")
	}
	seen := map[int]bool{}
	for _, decision := range v.Decisions {
		if decision.Index < 1 || decision.Index > len(review.Findings) {
			return fmt.Errorf("findings index %d does not name a review finding (F1-F%d)", decision.Index, len(review.Findings))
		}
		if decision.Decision != "accept" && decision.Decision != "dismiss" {
			return fmt.Errorf("finding F%d decision must be accept or dismiss", decision.Index)
		}
		if decision.Decision == "dismiss" && strings.TrimSpace(decision.Reason) == "" {
			return fmt.Errorf("dismissing finding F%d needs a reason", decision.Index)
		}
		seen[decision.Index] = true
		if v.Done && decision.Decision == "accept" && review.Findings[decision.Index-1].Blocking() {
			return fmt.Errorf("done=true conflicts with accepted %s finding F%d; either dismiss it with a reason or set done=false", review.Findings[decision.Index-1].Severity, decision.Index)
		}
	}
	for i, finding := range review.Findings {
		if finding.Blocking() && !seen[i+1] {
			return fmt.Errorf("address %s finding F%d with accept or dismiss", finding.Severity, i+1)
		}
	}
	return nil
}

// Failures turns a negative verdict into a replan report: the judge's gaps
// and every review finding the judge accepted.
func (v Verdict) Failures(review Review) []Failure {
	var failures []Failure
	for _, gap := range v.Gaps {
		if gap = Compact(gap, 300); gap != "" {
			failures = append(failures, Failure{Source: "judge", Status: "gap", Reason: gap})
		}
	}
	for i, finding := range review.Findings {
		decision, ok := v.Decision(i + 1)
		if !ok || decision.Decision != "accept" {
			continue
		}
		reason := finding.Problem
		if finding.Fix != "" {
			reason += " — fix: " + finding.Fix
		}
		failures = append(failures, Failure{Source: "review", Check: finding.Location(), Key: "review:" + finding.File, Status: finding.Severity, Reason: Compact(reason, 400)})
	}
	if len(failures) == 0 {
		failures = append(failures, Failure{Source: "judge", Status: "gap", Reason: Compact(v.Reason, 300)})
	}
	return failures
}

// EditTarget names what a user edit revises.
type EditTarget string

const (
	EditPlan     EditTarget = "plan"
	EditEvidence EditTarget = "evidence"
)

type Edit struct {
	Target EditTarget `json:"target"`
	Text   string     `json:"text"`
}

// EvidenceMode is how a planning step may treat the approved evidence.
type EvidenceMode int

const (
	// EvidenceLocked keeps approved evidence; replans cannot weaken it.
	EvidenceLocked EvidenceMode = iota
	// EvidenceProposal lets a plan that is not yet approved set evidence.
	EvidenceProposal
	// EvidenceRevision is a user-requested evidence change after approval.
	EvidenceRevision
	// EvidenceRetry is a retry with user feedback: the plan may revise the
	// approved evidence when the feedback asks for it, and otherwise keeps it.
	EvidenceRetry
)

// EntryKind classifies a journal entry.
type EntryKind string

const (
	EntryLog       EntryKind = "log"
	EntryFeedback  EntryKind = "feedback"
	EntryDiscovery EntryKind = "discovery"
)

// Entry is one line of the goal journal. Prompts and views render the slice
// they need from it instead of keeping a separate list per kind.
type Entry struct {
	Kind   EntryKind `json:"kind"`
	Pass   int       `json:"pass,omitempty"`
	TaskID string    `json:"task_id,omitempty"`
	Text   string    `json:"text"`
}

// CheckTally counts the checks the last verification passed.
type CheckTally struct {
	EvidencePassed   int `json:"evidence_passed,omitempty"`
	RegressionPassed int `json:"regression_passed,omitempty"`
	RegressionTotal  int `json:"regression_total,omitempty"`
}

// Limits is the one budget of a goal run: model tokens and running time.
// Zero means unlimited.
type Limits struct {
	MaxTokens     int64
	MaxActiveTime time.Duration
}

var DefaultLimits = Limits{MaxTokens: 10_000_000, MaxActiveTime: 120 * time.Minute}

// BillableTokens counts what a model call costs against the budget:
// completion and uncached prompt tokens.
func BillableTokens(stats *tasks.Stats) int64 {
	if stats == nil {
		return 0
	}
	if billable := stats.TotalTokens - stats.CachedPromptTokens; billable > 0 {
		return billable
	}
	return 0
}

// State is the fold of a goal's events. Nothing writes it except Apply.
type State struct {
	ID           string            `json:"id"`
	Goal         string            `json:"goal"`
	SessionID    string            `json:"session_id"`
	Adapter      string            `json:"adapter"`
	Revision     int               `json:"revision"`
	Stage        Stage             `json:"stage"`
	PausedFrom   Stage             `json:"paused_from,omitempty"`
	PlanMode     EvidenceMode      `json:"plan_mode,omitempty"`
	Approved     bool              `json:"approved,omitempty"`
	AutoApprove  bool              `json:"auto_approve,omitempty"`
	Models       Models            `json:"models,omitempty"`
	BaseRevision string            `json:"base_revision,omitempty"`
	StartDirty   map[string]string `json:"start_dirty,omitempty"`
	References   []Reference       `json:"references,omitempty"`
	Pass         int               `json:"pass"`
	PlanVersion  int               `json:"plan_version"`
	PlanSummary  string            `json:"plan_summary,omitempty"`
	Evidence     Evidence          `json:"evidence"`
	Tasks        []Task            `json:"tasks"`
	// Failures is the latest failure report.
	Failures []Failure `json:"failures,omitempty"`
	Review   *Review   `json:"review,omitempty"`
	Verdict  *Verdict  `json:"verdict,omitempty"`
	// Question is pending until the user answers it.
	Question string `json:"question,omitempty"`
	// Answer is the latest user answer; the next replan consumes it.
	Answer           string   `json:"answer,omitempty"`
	PendingEdit      *Edit    `json:"pending_edit,omitempty"`
	TamperedEvidence []string `json:"tampered_evidence,omitempty"`
	ExitReason       string   `json:"exit_reason,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	WorkerAttempts   int      `json:"worker_attempts"`
	Replans          int      `json:"replans"`
	TokensUsed       int64    `json:"tokens_used"`
	NoProgressKey    string   `json:"no_progress_key,omitempty"`
	NoProgressPasses int      `json:"no_progress_passes,omitempty"`
	// PrevEvidenceFailures holds the evidence IDs that alone failed the last
	// verification, to detect evidence that keeps failing across replans.
	PrevEvidenceFailures []string      `json:"prev_evidence_failures,omitempty"`
	Checks               CheckTally    `json:"checks"`
	ActiveTime           time.Duration `json:"active_time"`
	RunningSince         *time.Time    `json:"running_since,omitempty"`
	Journal              []Entry       `json:"journal,omitempty"`
}

// ShortID is the goal ID without its "goal-" prefix.
func (s State) ShortID() string { return strings.TrimPrefix(s.ID, "goal-") }

// ShortTaskID drops the goal prefix from a task ID: goal-ab12cd34-T3 -> T3.
func (s State) ShortTaskID(id string) string { return strings.TrimPrefix(id, s.ID+"-") }

// TaskID resolves a full or short task ID to the full ID of a known task.
func (s State) TaskID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for _, candidate := range []string{name, s.ID + "-" + name} {
		if s.TaskIndex(candidate) >= 0 {
			return candidate
		}
	}
	return ""
}

// Active returns the time spent running, including the current run.
func (s State) Active(now time.Time) time.Duration {
	duration := s.ActiveTime
	if s.RunningSince != nil {
		duration += now.Sub(*s.RunningSince)
	}
	return duration
}

// Entries returns the last limit journal entries of kind (all if limit <= 0).
func (s State) Entries(kind EntryKind, limit int) []string {
	var values []string
	for _, entry := range s.Journal {
		if entry.Kind == kind {
			values = append(values, entry.Text)
		}
	}
	return Tail(values, limit)
}

// FailingChecks lists the check keys of the latest failure report.
func (s State) FailingChecks() []string {
	var keys []string
	for _, failure := range s.Failures {
		if failure.Key != "" {
			keys = append(keys, failure.Key)
		}
	}
	return keys
}

func (s State) TaskIndex(id string) int {
	for i := range s.Tasks {
		if s.Tasks[i].ID == id {
			return i
		}
	}
	return -1
}

// ActiveTaskIDs lists tasks that are neither done nor superseded.
func (s State) ActiveTaskIDs() []string {
	var ids []string
	for _, task := range s.Tasks {
		if !task.Status.Closed() {
			ids = append(ids, task.ID)
		}
	}
	return ids
}

func (s State) AllTasksDone() bool {
	for _, task := range s.Tasks {
		if !task.Status.Closed() {
			return false
		}
	}
	return true
}

// NextTask returns the first active task whose dependencies are done and
// that still needs a worker (pending) or a check (attempted).
func (s State) NextTask() int {
	for index, task := range s.Tasks {
		if task.Status != TaskPending && task.Status != TaskAttempted {
			continue
		}
		ready := true
		for _, dependency := range task.DependsOn {
			if dep := s.TaskIndex(dependency); dep < 0 || s.Tasks[dep].Status != TaskDone {
				ready = false
				break
			}
		}
		if ready {
			return index
		}
	}
	return -1
}

// StuckFailures explains why active tasks cannot proceed: they failed, or
// they wait on a task that failed.
func (s State) StuckFailures() []Failure {
	var failures []Failure
	for _, task := range s.Tasks {
		switch task.Status {
		case TaskDone, TaskSuperseded:
			continue
		case TaskFailed:
			failures = append(failures, TaskFailure(task, CheckFailed, DefaultString(task.LastCheck, "task failed")))
		default:
			var waiting []string
			for _, dependency := range task.DependsOn {
				if index := s.TaskIndex(dependency); index < 0 || s.Tasks[index].Status != TaskDone {
					waiting = append(waiting, dependency)
				}
			}
			failures = append(failures, Failure{Source: "task " + task.ID, Status: "waiting", Reason: "waiting on unfinished tasks: " + strings.Join(waiting, ", ")})
		}
	}
	return failures
}

// LastFeedback is the most recent failure summary.
func (s State) LastFeedback() string {
	feedback := s.Entries(EntryFeedback, 1)
	if len(feedback) == 0 {
		return "overall goal check failed"
	}
	return feedback[0]
}

// EvidenceFailureIDs returns the sorted IDs of the approved evidence checks
// behind failures, or nil when any failure has another source.
func (s State) EvidenceFailureIDs(failures []Failure) []string {
	if len(failures) == 0 {
		return nil
	}
	evidence := make(map[string]bool, len(s.Evidence.Items))
	for _, item := range s.Evidence.Items {
		evidence[item.ID] = true
	}
	ids := make([]string, 0, len(failures))
	for _, failure := range failures {
		if !evidence[failure.Source] {
			return nil
		}
		ids = append(ids, failure.Source)
	}
	sortStrings(ids)
	return ids
}

// NextCommand is the command that moves the goal on from its stage.
func (s State) NextCommand() string {
	switch s.Stage {
	case AwaitingApproval:
		if s.Question != "" {
			return "/goal:answer <answer>"
		}
		return "/goal:approve"
	case AwaitingInput:
		return "/goal:answer <answer>"
	case Working, Verifying, Replanning:
		return "/goal:resume"
	case Stalled, OverBudget:
		if s.EvidenceFailureIDs(s.Failures) != nil {
			return "/goal:evidence --edit"
		}
		return "/goal:retry"
	}
	return ""
}

func TaskFailure(task Task, status, reason string) Failure {
	failure := Failure{Source: "task " + task.ID, Status: status, Reason: Compact(reason, 300), Tail: task.LastCheckOutput}
	if !task.Check.Empty() {
		failure.Check, failure.Key = task.Check.Label(), task.Check.Key()
	}
	return failure
}

var digits = regexp.MustCompile(`[0-9]+`)

// OutcomeKey fingerprints a failed check result. Numbers are masked so test
// durations and addresses do not make the same failure look new.
func OutcomeKey(outcome CheckOutcome) string {
	return outcome.Status + "|" + digits.ReplaceAllString(outcome.Reason, "#")
}

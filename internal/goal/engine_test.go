package goal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
)

type memoryStore struct {
	events []eventing.GoalEvent
	synced int
}

func (s *memoryStore) Load() ([]eventing.GoalEvent, error) { return s.events, nil }
func (s *memoryStore) Append(event eventing.GoalEvent) error {
	s.events = append(s.events, event)
	return nil
}
func (s *memoryStore) Sync(State) error { s.synced++; return nil }

func (s *memoryStore) types() []string {
	var types []string
	for _, event := range s.events {
		types = append(types, event.Type)
	}
	return types
}

// fakeExec passes every check unless outcomes names it.
type fakeExec struct {
	outcomes map[string][]CheckOutcome
	ran      []string
}

func (e *fakeExec) EvidenceFiles(Check) []string { return nil }
func (e *fakeExec) FileHash(string) string       { return "hash" }
func (e *fakeExec) ValidateCheck(context.Context, Check) error {
	return nil
}
func (e *fakeExec) RunCheck(_ context.Context, check Check) (CheckOutcome, error) {
	e.ran = append(e.ran, check.Key())
	if queue := e.outcomes[check.Key()]; len(queue) > 0 {
		e.outcomes[check.Key()] = queue[1:]
		return queue[0], nil
	}
	return CheckOutcome{Status: CheckPassed, Reason: "exit 0"}, nil
}

type fakeRoles struct {
	plans    []Proposal
	planErr  error
	verdicts []Verdict
	requests []PlanRequest
	workers  int
}

func (r *fakeRoles) Plan(_ context.Context, _ State, request PlanRequest) (Proposal, error) {
	r.requests = append(r.requests, request)
	if r.planErr != nil {
		return Proposal{}, r.planErr
	}
	plan := r.plans[0]
	if len(r.plans) > 1 {
		r.plans = r.plans[1:]
	}
	return plan, nil
}

func (r *fakeRoles) Work(context.Context, State, Task) (Handoff, error) {
	r.workers++
	return Handoff{Result: "ok", Notes: "done", Stats: &tasks.Stats{TotalTokens: 1000, CachedPromptTokens: 400}}, nil
}

func (r *fakeRoles) Review(context.Context, State, string) (Review, error) {
	return Review{Summary: "fine", Criteria: []CriterionResult{{Criterion: "works", Status: "met", Evidence: []string{"main.go:1"}}}, Tokens: 30}, nil
}

func (r *fakeRoles) Judge(context.Context, State, string, Review) (Verdict, error) {
	verdict := r.verdicts[0]
	if len(r.verdicts) > 1 {
		r.verdicts = r.verdicts[1:]
	}
	return verdict, nil
}

func addOp(ref, check string) PlanOp {
	return PlanOp{Op: OpAdd, Ref: ref, Title: "task " + check, NextAction: "do", Validation: []string{"ok"}, Check: &Check{Shell: check}}
}

func onePlan(check string) Proposal {
	return Proposal{
		PlanSummary:  "do it",
		GoalCriteria: []string{"works"},
		Evidence:     []EvidenceItem{{ID: "E1", Check: Check{Argv: []string{"verify"}}, Status: "valid"}},
		Ops:          []PlanOp{addOp("a", check)},
	}
}

var done = Verdict{Done: true, Reason: "verified"}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newTestEngine(roles *fakeRoles, exec *fakeExec) (*Engine, *memoryStore, *clock) {
	store := &memoryStore{}
	c := &clock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	if exec.outcomes == nil {
		exec.outcomes = map[string][]CheckOutcome{}
	}
	return &Engine{Store: store, Roles: roles, Exec: exec, Limits: DefaultLimits, Now: c.Now}, store, c
}

func start(auto bool) Start {
	return Start{ID: "goal-t", Goal: "ship it", Created: CreatedData{SessionID: "s", Adapter: "test", AutoApprove: auto}}
}

func TestEngineRunsGoalToDoneAndReplaysTheSameState(t *testing.T) {
	roles := &fakeRoles{plans: []Proposal{onePlan("check-1"), onePlan("check-2")}, verdicts: []Verdict{{Done: false, Reason: "gap", Gaps: []string{"missing totals"}, Tokens: 50}, done}}
	engine, store, _ := newTestEngine(roles, &fakeExec{})
	state, err := engine.Step(context.Background(), start(true))
	if err != nil {
		t.Fatal(err)
	}
	if state.Stage != Done || state.Replans != 1 || state.Pass != 2 || roles.workers != 2 || state.TokensUsed != 2*600+2*30+50 {
		t.Fatalf("state = %+v", state)
	}
	if roles.requests[0].Mode != EvidenceProposal || roles.requests[1].Mode != EvidenceLocked {
		t.Fatalf("plan modes = %+v", roles.requests)
	}
	want := []string{EventCreated, EventPlanCreated, EventApproved, EventRunStarted,
		EventTaskStarted, EventTaskWorked, EventTaskChecked, EventVerificationStarted, EventVerificationFinished, EventReviewed, EventJudged, EventEscalated, EventReplanned,
		EventTaskStarted, EventTaskWorked, EventTaskChecked, EventVerificationStarted, EventVerificationFinished, EventReviewed, EventJudged, EventCompleted}
	if got := store.types(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events =\n%v\nwant\n%v", got, want)
	}
	replayed, found, err := Replay(store.events)
	if err != nil || !found || !reflect.DeepEqual(replayed, state) {
		t.Fatalf("replay = %+v, %v, %v\nwant %+v", replayed, found, err, state)
	}
	if store.synced != len(store.events) {
		t.Fatalf("views synced %d times for %d events", store.synced, len(store.events))
	}
}

func TestEngineFailedPlannerLeavesGoalUnchanged(t *testing.T) {
	roles := &fakeRoles{plans: []Proposal{onePlan("check-1")}}
	engine, store, _ := newTestEngine(roles, &fakeExec{})
	if _, err := engine.Step(context.Background(), start(false)); err != nil {
		t.Fatal(err)
	}
	before := len(store.events)
	roles.planErr = errors.New("planner down")
	state, err := engine.Step(context.Background(), Revise{Edit: Edit{Target: EditPlan, Text: "split it"}})
	if err == nil || len(store.events) != before || state.Stage != AwaitingApproval || state.PendingEdit != nil {
		t.Fatalf("failed edit recorded events: err %v, events %d->%d, state %+v", err, before, len(store.events), state)
	}
}

func TestEngineRetriesTaskCheckThenEscalates(t *testing.T) {
	failed := CheckOutcome{Status: CheckFailed, Reason: "exit 1: mismatch after 0.12s", Tail: "mismatch"}
	slower := CheckOutcome{Status: CheckFailed, Reason: "exit 1: mismatch after 0.31s", Tail: "mismatch"}
	other := CheckOutcome{Status: CheckFailed, Reason: "exit 1: other error", Tail: "other"}
	exec := &fakeExec{outcomes: map[string][]CheckOutcome{"check-1": {other, failed, slower, other, failed, slower}}}
	roles := &fakeRoles{plans: []Proposal{onePlan("check-1"), {PlanSummary: "retry", Ops: []PlanOp{{Op: OpRevise, ID: "T1", NextAction: "try again"}}}}, verdicts: []Verdict{done}}
	engine, _, _ := newTestEngine(roles, exec)
	state, err := engine.Step(context.Background(), start(true))
	if err != nil {
		t.Fatal(err)
	}
	// A task retries while its failure changes; the same failure twice (timings
	// masked) fails it, and the same failure report twice stalls the goal.
	if state.Stage != Stalled || !strings.Contains(state.ExitReason, "no progress") || roles.workers != 6 {
		t.Fatalf("state = %s %q, workers %d", state.Stage, state.ExitReason, roles.workers)
	}
}

func TestEngineChargesOnlyRunningTime(t *testing.T) {
	roles := &fakeRoles{plans: []Proposal{onePlan("check-1")}}
	exec := &fakeExec{outcomes: map[string][]CheckOutcome{"check-1": {{Status: CheckEnvBlocked, Reason: "network"}}}}
	engine, _, c := newTestEngine(roles, exec)
	if _, err := engine.Step(context.Background(), start(false)); err != nil {
		t.Fatal(err)
	}
	// Approval waits an hour; only the run counts.
	c.now = c.now.Add(time.Hour)
	state, err := engine.Step(context.Background(), Approve{})
	if err != nil || state.Stage != AwaitingInput || state.ActiveTime != 0 {
		t.Fatalf("paused = %+v, %v", state, err)
	}
}

func TestApplyRejectsIllegalTransitions(t *testing.T) {
	data, _ := json.Marshal(CreatedData{SessionID: "s"})
	created := eventing.GoalEvent{Type: EventCreated, GoalID: "goal-t", Revision: 1, Data: data}
	tests := []struct {
		name   string
		events []eventing.GoalEvent
		want   string
	}{
		{"approve before a plan", []eventing.GoalEvent{created, {Type: EventApproved, GoalID: "goal-t", Revision: 2}}, "not allowed in stage planning"},
		{"task before a run", []eventing.GoalEvent{created, {Type: EventTaskStarted, GoalID: "goal-t", Revision: 2}}, "not allowed"},
		{"revision gap", []eventing.GoalEvent{created, {Type: EventPlanCreated, GoalID: "goal-t", Revision: 3}}, "does not follow"},
		{"other goal", []eventing.GoalEvent{created, {Type: EventPlanCreated, GoalID: "goal-x", Revision: 2}}, "applied to goal"},
		{"unknown event", []eventing.GoalEvent{created, {Type: "Goal.Nope", GoalID: "goal-t", Revision: 2}}, "unknown goal event"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := Replay(test.events); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Replay() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReplayUsesLatestGoalAndFlagsLegacy(t *testing.T) {
	data, _ := json.Marshal(CreatedData{SessionID: "s"})
	events := []eventing.GoalEvent{
		{Type: EventCreated, GoalID: "goal-old", Revision: 1},
		{Type: EventCreated, GoalID: "goal-new", Goal: "new", Revision: 1, Data: data},
	}
	state, found, err := Replay(events)
	if err != nil || !found || state.ID != "goal-new" || state.Stage != Planning {
		t.Fatalf("Replay() = %+v, %v, %v", state, found, err)
	}
	if _, found, err := Replay(events[:1]); !found || !errors.Is(err, ErrLegacy) {
		t.Fatalf("legacy Replay() = %v, %v", found, err)
	}
}

func TestEngineStopsWhenTokenBudgetIsSpent(t *testing.T) {
	roles := &fakeRoles{plans: []Proposal{onePlan("check-1"), onePlan("check-2")}, verdicts: []Verdict{{Done: false, Reason: "gap", Gaps: []string{"x"}}}}
	engine, _, _ := newTestEngine(roles, &fakeExec{})
	engine.Limits.MaxTokens = 500
	state, err := engine.Step(context.Background(), start(true))
	if err != nil || state.Stage != OverBudget || !strings.Contains(state.ExitReason, "token budget") || roles.workers != 1 {
		t.Fatalf("state = %s %q, workers %d, %v", state.Stage, state.ExitReason, roles.workers, err)
	}
}

func TestApplyPatchOwnsTaskIdentity(t *testing.T) {
	state := State{ID: "goal-p", Tasks: []Task{
		{ID: "goal-p-T1", Title: "done", Status: TaskDone, Check: Check{Shell: "a"}},
		{ID: "goal-p-T2", Title: "keep", Status: TaskPending, Check: Check{Shell: "b"}, Attempts: 1},
		{ID: "goal-p-T3", Title: "broken", Status: TaskFailed, Check: Check{Shell: "c"}, Attempts: 2},
	}}
	ops := []PlanOp{
		{Op: OpRevise, ID: "T3", NextAction: "new approach"},
		{Op: OpAdd, Ref: "tests", Kind: KindCheck, Title: "write test", Validation: []string{"ok"}, Check: &Check{Argv: []string{"test", "-e", "x_test.go"}}},
		{Op: OpAdd, Ref: "impl", Title: "implement", Validation: []string{"ok"}, DependsOn: []string{"T2"}, Check: &Check{Argv: []string{"go", "test"}, WrittenBy: "tests"}},
	}
	patch, err := ApplyPatch(state, ops)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, task := range patch.Tasks {
		ids = append(ids, state.ShortTaskID(task.ID)+":"+string(task.Status))
	}
	if want := []string{"T2:pending", "T3:pending", "T4:pending", "T5:pending"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("tasks = %v, want %v", ids, want)
	}
	impl := patch.Tasks[3]
	if impl.Check.WrittenBy != "goal-p-T4" || !reflect.DeepEqual(impl.DependsOn, []string{"goal-p-T2", "goal-p-T4"}) || impl.CheckStatus != "pending-goal-p-T4" {
		t.Fatalf("impl = %+v", impl)
	}
	if patch.Tasks[1].Attempts != 2 || patch.Tasks[1].NextAction != "new approach" {
		t.Fatalf("revised task lost history: %+v", patch.Tasks[1])
	}
	for name, ops := range map[string][]PlanOp{
		"done task":         {{Op: OpRevise, ID: "T1", Title: "again"}},
		"failed left as is": {{Op: OpRevise, ID: "T2", Title: "x"}},
		"drop no reason":    {{Op: OpDrop, ID: "T3"}},
		"unknown dep":       {{Op: OpDrop, ID: "T3", Reason: "x"}, {Op: OpAdd, Ref: "n", Title: "n", Check: &Check{Shell: "n"}, DependsOn: []string{"nope"}}},
		"unknown op":        {{Op: "rename", ID: "T2"}},
	} {
		if _, err := ApplyPatch(state, ops); err == nil {
			t.Errorf("%s: patch accepted", name)
		}
	}
	if _, err := ApplyPatch(state, []PlanOp{{Op: OpDrop, ID: "goal-p-T3", Reason: "obsolete"}}); err != nil {
		t.Fatalf("drop by full ID: %v", err)
	}
}

func TestVerdictMustAddressBlockingFindings(t *testing.T) {
	review := Review{Findings: []Finding{{Severity: "blocker", File: "main.go", Line: 2, Problem: "never registered", Fix: "register it"}, {Severity: "minor", File: "a.md", Problem: "typo"}}}
	for name, test := range map[string]struct {
		verdict Verdict
		want    string
	}{
		"unaddressed":      {Verdict{Done: true, Reason: "x"}, "address blocker finding F1"},
		"accepted is done": {Verdict{Done: true, Reason: "x", Decisions: []FindingDecision{{Index: 1, Decision: "accept"}}}, "conflicts with accepted"},
		"bare dismissal":   {Verdict{Done: true, Reason: "x", Decisions: []FindingDecision{{Index: 1, Decision: "dismiss"}}}, "needs a reason"},
		"unknown index":    {Verdict{Reason: "x", Decisions: []FindingDecision{{Index: 3, Decision: "accept"}}}, "does not name"},
		"no reason":        {Verdict{Decisions: []FindingDecision{{Index: 1, Decision: "accept"}}}, "reason is required"},
	} {
		if err := test.verdict.Validate(review); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
	dismissed := Verdict{Done: true, Reason: "intended", Decisions: []FindingDecision{{Index: 1, Decision: "dismiss", Reason: "registered elsewhere"}}}
	if err := dismissed.Validate(review); err != nil {
		t.Fatalf("reasoned dismissal rejected: %v", err)
	}
	accepted := Verdict{Reason: "gap", Gaps: []string{"no docs"}, Decisions: []FindingDecision{{Index: 1, Decision: "accept"}, {Index: 2, Decision: "dismiss", Reason: "cosmetic"}}}
	failures := accepted.Failures(review)
	if len(failures) != 2 || failures[0].Source != "judge" || failures[1].Source != "review" || failures[1].Check != "main.go:2" || !strings.Contains(failures[1].Reason, "fix: register it") {
		t.Fatalf("failures = %+v", failures)
	}
}

// mapWorkspace serves evidence files from a map; absent files are missing.
type mapWorkspace map[string]string

func (w mapWorkspace) EvidenceFiles(Check) []string {
	var files []string
	for rel := range w {
		files = append(files, rel)
	}
	return files
}

func (w mapWorkspace) FileHash(rel string) string {
	if hash, ok := w[rel]; ok {
		return hash
	}
	return MissingFile
}

func TestTamperedReportsKindAndAcceptingDeletionSticks(t *testing.T) {
	workspace := mapWorkspace{"a_test.go": "1", "b_test.go": "1"}
	state := State{Evidence: Evidence{Items: []EvidenceItem{{ID: "E1"}}}}
	state.mergeFrozen(Freeze(workspace, state, false))
	delete(workspace, "a_test.go")
	workspace["b_test.go"] = "2"
	tampered := Tampered(workspace, state)
	if want := []string{"E1 a_test.go (deleted)", "E1 b_test.go (modified)"}; !reflect.DeepEqual(tampered, want) {
		t.Fatalf("tampered = %v, want %v", tampered, want)
	}
	question := TamperedQuestion(tampered)
	for _, want := range []string{"git diff -- a_test.go b_test.go", "false positive", "coverage was not dropped"} {
		if !strings.Contains(question, want) {
			t.Fatalf("question missing %q:\n%s", want, question)
		}
	}
	state.mergeFrozen(Freeze(workspace, state, true))
	if got := Tampered(workspace, state); got != nil {
		t.Fatalf("accepted changes still tampered: %v", got)
	}
}

func TestRetryFeedbackIsKeptForLaterReplans(t *testing.T) {
	state := State{Stage: Done, Pass: 1}
	event, err := json.Marshal(RetryData{Reason: "user requested retry; user feedback: keep alias", Feedback: "keep alias"})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.apply(eventing.GoalEvent{Type: EventRetryRequested, Data: event}); err != nil {
		t.Fatal(err)
	}
	if got := state.Entries(EntryFeedback, 1); len(got) != 1 || !strings.Contains(got[0], "retry requested: keep alias") {
		t.Fatalf("feedback = %v", got)
	}
}

func TestRetryReplansAPausedGoalAndMayReviseEvidence(t *testing.T) {
	revised := onePlan("check-2")
	revised.Evidence = []EvidenceItem{{ID: "E1", Check: Check{Argv: []string{"verify-narrow"}}, Status: "valid"}}
	roles := &fakeRoles{plans: []Proposal{onePlan("check-1"), revised, onePlan("check-3")}}
	exec := &fakeExec{outcomes: map[string][]CheckOutcome{"check-1": {{Status: CheckEnvBlocked, Reason: "network"}}}}
	engine, _, _ := newTestEngine(roles, exec)
	if _, err := engine.Step(context.Background(), start(false)); err != nil {
		t.Fatal(err)
	}
	if state, err := engine.Step(context.Background(), Approve{}); err != nil || state.Stage != AwaitingInput {
		t.Fatalf("paused = %+v, %v", state, err)
	}
	state, err := engine.Step(context.Background(), Retry{Reason: "the criterion is wrong, drop it"})
	if err != nil || state.Stage != AwaitingApproval || state.Question != "" {
		t.Fatalf("retry from awaiting_input = %+v, %v", state, err)
	}
	request := roles.requests[1]
	if request.Mode != EvidenceRetry || !strings.Contains(request.Reason, "while paused on:") || !strings.Contains(request.Reason, "user feedback: the criterion is wrong") {
		t.Fatalf("retry plan request = %+v", request)
	}
	// Without feedback a retry keeps the approved evidence.
	if _, err := engine.Step(context.Background(), Retry{}); err != nil {
		t.Fatal(err)
	}
	if mode := roles.requests[2].Mode; mode != EvidenceLocked {
		t.Fatalf("plain retry mode = %v", mode)
	}
}

func TestPlanDiffShowsWhatChangedFromThePreviousVersion(t *testing.T) {
	before := State{
		ID:          "g",
		PlanVersion: 3,
		Evidence: Evidence{Version: 1, Criteria: []string{"command removed", "unrelated edits are restored"}, Items: []EvidenceItem{
			{ID: "E1", Check: Check{Argv: []string{"./scripts/check.sh"}}, Covers: "no refs"},
			{ID: "E2", Check: Check{Argv: []string{"go", "test", "./..."}}},
		}},
		Tasks: []Task{
			{ID: "g-T1", Title: "remove command", NextAction: "restore unrelated paths, then remove", Status: TaskPending, Check: Check{Shell: "a"}},
			{ID: "g-T2", Title: "remove docs", NextAction: "edit docs", Status: TaskPending, Check: Check{Shell: "b"}},
			{ID: "g-T3", Title: "verifier", Status: TaskDone, Check: Check{Shell: "c"}},
		},
	}
	after := before.Clone()
	after.PlanVersion, after.Evidence.Version = 4, 2
	after.Evidence.Criteria = []string{"command removed"}
	after.Evidence.Items = []EvidenceItem{{ID: "E1", Check: Check{Argv: []string{"./scripts/check.sh", "--narrow"}}, Covers: "no refs"}}
	after.Tasks[0].NextAction = "remove; leave other files alone"
	after.Tasks[1].Status = TaskSuperseded
	after.Tasks = append(after.Tasks, Task{ID: "g-T4", Title: "update docs", Kind: KindCheck, NextAction: "edit docs/cli.md", Status: TaskPending, Check: Check{Shell: "d"}})
	diff := PlanDiff(before, after)
	for _, want := range []string{
		"### Changes from plan v3 to v4 (evidence v1 to v2)",
		"- − unrelated edits are restored",
		"  - check: − `./scripts/check.sh`", "  - check: + `./scripts/check.sh --narrow`",
		"- − E2 `go test ./...`",
		"- ~ T1 remove command", "  - next action: − restore unrelated paths, then remove", "  - next action: + remove; leave other files alone",
		"- + T4 update docs (check)", "  - next action: edit docs/cli.md",
		"- − T2 remove docs",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "verifier") {
		t.Fatalf("diff shows a finished task:\n%s", diff)
	}
	if PlanDiff(after, after) != "" || PlanDiff(State{}, after) != "" {
		t.Fatal("diff without a new plan version")
	}
}

// blockedRoles reports every worker attempt as blocked with notes.
type blockedRoles struct {
	*fakeRoles
	notes string
}

func (r blockedRoles) Work(context.Context, State, Task) (Handoff, error) {
	r.workers++
	return Handoff{Result: "blocked", Notes: r.notes}, nil
}

// evidenceExec freezes x_test.go as the evidence of every check.
type evidenceExec struct{ *fakeExec }

func (evidenceExec) EvidenceFiles(Check) []string { return []string{"x_test.go"} }

func TestWorkTaskBlockedOnEvidenceReplansInsteadOfAskingForAccess(t *testing.T) {
	roles := blockedRoles{fakeRoles: &fakeRoles{plans: []Proposal{onePlan("check-1"), onePlan("check-2")}}, notes: "git restore x_test.go: Operation not permitted; grant write access"}
	engine, store, _ := newTestEngine(roles.fakeRoles, &fakeExec{})
	engine.Roles, engine.Exec = roles, evidenceExec{&fakeExec{outcomes: map[string][]CheckOutcome{}}}
	if _, err := engine.Step(context.Background(), start(false)); err != nil {
		t.Fatal(err)
	}
	state, err := engine.Step(context.Background(), Approve{})
	if err != nil {
		t.Fatal(err)
	}
	if types := store.types(); !Contains(types, EventEscalated) || Contains(types, EventPaused) {
		t.Fatalf("a blocked work task on evidence must replan, not pause: events = %v, question = %q", types, state.Question)
	}
	recorded, _ := json.Marshal(store.events)
	if len(roles.requests) < 2 || !strings.Contains(string(recorded), "move that change into a check task") || !strings.Contains(string(recorded), "x_test.go") {
		t.Fatalf("replan did not name the protected files: %d plans, events %s", len(roles.requests), recorded)
	}
}

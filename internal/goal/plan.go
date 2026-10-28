package goal

import (
	"context"
	"fmt"
	"strings"
)

// Proposal is the planner's answer: a patch to the current plan, plus the
// evidence and criteria when the planner may set them.
type Proposal struct {
	Ops          []PlanOp       `json:"ops"`
	Evidence     []EvidenceItem `json:"evidence,omitempty"`
	PlanSummary  string         `json:"plan_summary"`
	GoalCriteria []string       `json:"goal_criteria,omitempty"`
	NeedsHuman   bool           `json:"needs_human"`
	Question     string         `json:"question"`
	Questions    []Question     `json:"questions,omitempty"`
	// Tokens is what the planner spent; it is charged to the budget.
	Tokens int64 `json:"-"`
}

// Plan operations. The harness owns task identity: "add" introduces a task
// under a ref the rest of the patch may use, "drop" and "revise" name an
// unfinished task by ID. Done tasks cannot be named at all.
const (
	OpAdd    = "add"
	OpDrop   = "drop"
	OpRevise = "revise"
)

type PlanOp struct {
	Op         string   `json:"op"`
	Ref        string   `json:"ref,omitempty"`
	ID         string   `json:"id,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Title      string   `json:"title,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Validation []string `json:"validation,omitempty"`
	Check      *Check   `json:"check,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

type Question struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Default  *string  `json:"default"`
	Context  string   `json:"context,omitempty"`
}

// CheckValidator proves a check can run without running it.
type CheckValidator interface {
	Validate(ctx context.Context, check Check) error
}

// PatchResult is a plan after a patch: the complete ordered list of
// unfinished tasks, the IDs each ref received, and the tasks the patch added
// or revised.
type PatchResult struct {
	Tasks   []Task
	Refs    map[string]string
	Touched map[string]bool
	Added   map[string]bool
}

// ApplyPatch applies plan operations to the unfinished tasks. Tasks the
// patch does not name keep their state. A failed task must be revised or
// dropped: as written it cannot pass.
func ApplyPatch(state State, ops []PlanOp) (PatchResult, error) {
	result := PatchResult{Refs: map[string]string{}, Touched: map[string]bool{}, Added: map[string]bool{}}
	next := len(state.Tasks) + 1
	for i, op := range ops {
		if op.Op != OpAdd {
			continue
		}
		ref := strings.TrimSpace(op.Ref)
		if ref == "" {
			return result, fmt.Errorf("ops[%d]: add needs a ref", i)
		}
		if _, dup := result.Refs[ref]; dup {
			return result, fmt.Errorf("ops[%d]: ref %s is used twice", i, ref)
		}
		if state.TaskID(ref) != "" {
			return result, fmt.Errorf("ops[%d]: ref %s is already a task ID; pick another ref", i, ref)
		}
		result.Refs[ref] = fmt.Sprintf("%s-T%d", state.ID, next)
		next++
	}
	resolve := func(name string) string {
		name = strings.TrimSpace(name)
		if id, ok := result.Refs[name]; ok {
			return id
		}
		return state.TaskID(name)
	}
	var active []Task
	position := map[string]int{}
	for _, task := range state.Tasks {
		if !task.Status.Closed() {
			position[task.ID] = len(active)
			active = append(active, task)
		}
	}
	dropped := map[string]bool{}
	for i, op := range ops {
		switch op.Op {
		case OpAdd:
			if op.Check == nil {
				return result, fmt.Errorf("ops[%d] (%s): add needs a check", i, op.Ref)
			}
			id := result.Refs[strings.TrimSpace(op.Ref)]
			task := Task{ID: id, Kind: DefaultString(op.Kind, KindWork), Title: op.Title, NextAction: DefaultString(op.NextAction, op.Title), DependsOn: op.DependsOn, Validation: op.Validation, Check: *op.Check, Status: TaskPending}
			position[id] = len(active)
			active = append(active, task)
			result.Touched[id], result.Added[id] = true, true
		case OpDrop, OpRevise:
			id := resolve(op.ID)
			if id == "" {
				return result, fmt.Errorf("ops[%d]: %s names unknown task %q", i, op.Op, op.ID)
			}
			index, ok := position[id]
			if !ok || dropped[id] {
				return result, fmt.Errorf("ops[%d]: task %s is closed; done and superseded tasks cannot change", i, state.ShortTaskID(id))
			}
			if result.Touched[id] {
				return result, fmt.Errorf("ops[%d]: task %s is changed twice", i, state.ShortTaskID(id))
			}
			result.Touched[id] = true
			if op.Op == OpDrop {
				if strings.TrimSpace(op.Reason) == "" {
					return result, fmt.Errorf("ops[%d]: drop %s needs a reason", i, state.ShortTaskID(id))
				}
				dropped[id] = true
				continue
			}
			task := &active[index]
			if op.Kind != "" {
				task.Kind = op.Kind
			}
			if op.Title != "" {
				task.Title = op.Title
			}
			if op.NextAction != "" {
				task.NextAction = op.NextAction
			}
			if op.DependsOn != nil {
				task.DependsOn = op.DependsOn
			}
			if len(op.Validation) > 0 {
				task.Validation = op.Validation
			}
			if op.Check != nil {
				task.Check = *op.Check
			}
			task.Status, task.WorkerStatus, task.LastFailureKey = TaskPending, "", ""
		default:
			return result, fmt.Errorf("ops[%d]: op must be add, drop or revise, got %q", i, op.Op)
		}
	}
	old := TasksByID(state.Tasks)
	for _, task := range active {
		if dropped[task.ID] {
			continue
		}
		if result.Touched[task.ID] {
			deps := make([]string, 0, len(task.DependsOn))
			for _, dep := range task.DependsOn {
				id := resolve(dep)
				if id == "" {
					return result, fmt.Errorf("task %s depends on unknown task %q", state.ShortTaskID(task.ID), dep)
				}
				if !Contains(deps, id) {
					deps = append(deps, id)
				}
			}
			task.DependsOn = deps
			if writer := strings.TrimSpace(task.Check.WrittenBy); writer != "" && writer != "self" {
				id := resolve(writer)
				if id == "" {
					return result, fmt.Errorf("task %s check is written_by unknown task %q", state.ShortTaskID(task.ID), writer)
				}
				task.Check.WrittenBy = id
			}
			if writer := task.Check.WrittenBy; writer != "" && writer != "self" && writer != task.ID && !Contains(task.DependsOn, writer) {
				task.DependsOn = append(task.DependsOn, writer)
			}
			if len(task.Validation) == 0 && !task.Check.Empty() {
				task.Validation = []string{"Check passes: " + task.Check.Label()}
			}
			task.CheckStatus = "valid"
			if task.Check.WrittenBy != "" {
				task.CheckStatus = PendingCheckStatus(task)
			}
		} else if task.Status == TaskFailed {
			return result, fmt.Errorf("task %s failed and cannot pass as written; revise it with a new approach or drop it", state.ShortTaskID(task.ID))
		}
		result.Tasks = append(result.Tasks, task)
	}
	if err := ValidateDependencies(result.Tasks, old); err != nil {
		return result, err
	}
	return result, nil
}

// PlanFromProposal turns a validated proposal into the payload of a plan
// event: it resolves questions, applies the patch and rewrites evidence.
func PlanFromProposal(state State, proposal Proposal, mode EvidenceMode) (PlanData, error) {
	data := PlanData{Tokens: proposal.Tokens}
	if proposal.NeedsHuman && len(proposal.Questions) > 0 {
		var rendered, defaults []string
		allDefaults := true
		for _, question := range proposal.Questions {
			line := question.Question
			if len(question.Options) > 0 {
				line += " [" + strings.Join(question.Options, " | ") + "]"
			}
			if question.Default != nil {
				line += " [default: " + *question.Default + "]"
				defaults = append(defaults, question.Question+" = "+*question.Default)
			} else {
				allDefaults = false
			}
			rendered = append(rendered, line)
		}
		if state.AutoApprove && allDefaults && len(proposal.Ops) > 0 {
			data.Defaults = defaults
			proposal.NeedsHuman = false
		} else {
			proposal.Question = strings.Join(rendered, "\n")
		}
	}
	if proposal.NeedsHuman {
		data.Question = proposal.Question
		return data, nil
	}
	patch, err := ApplyPatch(state, proposal.Ops)
	if err != nil {
		return data, err
	}
	body := &PlanBody{Tasks: patch.Tasks, Summary: Compact(proposal.PlanSummary, 800)}
	if mode != EvidenceLocked && (len(proposal.Evidence) > 0 || len(proposal.GoalCriteria) > 0) {
		evidence := state.Clone().Evidence
		if len(proposal.Evidence) > 0 {
			items := append([]EvidenceItem(nil), proposal.Evidence...)
			for i := range items {
				if items[i].ID == "" {
					items[i].ID = fmt.Sprintf("E%d", i+1)
				}
				if writer := strings.TrimSpace(items[i].Check.WrittenBy); writer != "" {
					if id, ok := patch.Refs[writer]; ok {
						items[i].Check.WrittenBy = id
					} else if id := state.TaskID(writer); id != "" {
						items[i].Check.WrittenBy = id
					}
				}
				items[i].Files = nil
			}
			evidence.Items = items
			if mode == EvidenceRevision || mode == EvidenceRetry {
				evidence.Version++
			}
			if evidence.Version == 0 {
				evidence.Version = 1
			}
		}
		if len(proposal.GoalCriteria) > 0 {
			evidence.Criteria = proposal.GoalCriteria
		}
		body.Evidence = &evidence
	}
	data.Plan = body
	return data, nil
}

func PendingCheckStatus(task Task) string {
	writer := strings.TrimSpace(task.Check.WrittenBy)
	if writer == "" || writer == "self" || writer == task.ID {
		return "pending-self"
	}
	return "pending-" + writer
}

func TasksByID(list []Task) map[string]Task {
	byID := make(map[string]Task, len(list))
	for _, task := range list {
		byID[task.ID] = task
	}
	return byID
}

func ValidateDependencies(proposed []Task, old map[string]Task) error {
	active := make(map[string]Task, len(proposed))
	for _, task := range proposed {
		active[task.ID] = task
	}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("goal plan has a dependency cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range active[id].DependsOn {
			if _, exists := active[dependency]; exists {
				if err := visit(dependency); err != nil {
					return err
				}
			} else if prior, exists := old[dependency]; !exists || prior.Status != TaskDone {
				return fmt.Errorf("task %s depends on unavailable task %s", id, dependency)
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for _, task := range proposed {
		if err := visit(task.ID); err != nil {
			return err
		}
	}
	return nil
}

// reviewGap reports a failure report raised by the reviewer or judge
// rather than by a failing check.
func reviewGap(failures []Failure) bool {
	for _, failure := range failures {
		if failure.Source == "review" || failure.Source == "judge" {
			return true
		}
	}
	return false
}

// OnlyEvidenceRevision reports an evidence revision after every task is done.
// Such a plan may keep the verified work as is and change only evidence.
func OnlyEvidenceRevision(state State, mode EvidenceMode) bool {
	return mode == EvidenceRevision && len(state.Tasks) > 0 && len(state.ActiveTaskIDs()) == 0
}

// ValidatePlan applies every deterministic plan rule to the patched plan. It
// runs only cheap probes through validator, never the checks themselves.
func ValidatePlan(ctx context.Context, validator CheckValidator, state State, plan Proposal, mode EvidenceMode) error {
	if plan.NeedsHuman {
		if strings.TrimSpace(plan.Question) == "" && len(plan.Questions) == 0 {
			return fmt.Errorf("needs_human requires a question")
		}
		return nil
	}
	if strings.TrimSpace(plan.PlanSummary) == "" {
		return fmt.Errorf("plan_summary is required: explain how the tasks fit together")
	}
	if len(state.Evidence.Criteria) == 0 && len(plan.GoalCriteria) == 0 {
		return fmt.Errorf("goal_criteria needs at least one overall criterion")
	}
	if mode != EvidenceLocked {
		for _, criterion := range plan.GoalCriteria {
			if strings.TrimSpace(criterion) == "" || strings.ContainsAny(criterion, "\r\n") {
				return fmt.Errorf("goal_criteria entries must be single non-empty lines")
			}
		}
	}
	patch, err := ApplyPatch(state, plan.Ops)
	if err != nil {
		return err
	}
	if len(patch.Tasks) == 0 && !OnlyEvidenceRevision(state, mode) {
		return fmt.Errorf("the plan has no unfinished tasks; an empty plan cannot complete a goal, so add verification or implementation tasks even if the work looks done")
	}
	kinds := map[string]string{}
	for _, task := range state.Tasks {
		if task.Status == TaskDone {
			kinds[task.ID] = DefaultString(task.Kind, KindWork)
		}
	}
	for _, task := range patch.Tasks {
		kinds[task.ID] = DefaultString(task.Kind, KindWork)
	}
	failing := map[string]bool{}
	for _, key := range state.FailingChecks() {
		failing[key] = true
	}
	// Review and judge gaps fail no check: the fix may keep passing an
	// existing check, and the reviewer and judge look again.
	gap := reviewGap(state.Failures)
	doneByCheck := map[string]string{}
	for _, task := range state.Tasks {
		if task.Status == TaskDone && !task.Check.Empty() {
			doneByCheck[task.Check.Key()] = task.ID
		}
	}
	for _, task := range patch.Tasks {
		if !patch.Touched[task.ID] {
			continue
		}
		name := state.ShortTaskID(task.ID)
		if kind := DefaultString(task.Kind, KindWork); kind != KindWork && kind != KindCheck {
			return fmt.Errorf("task %s has kind %q; use work or check", name, task.Kind)
		}
		if strings.TrimSpace(task.Title) == "" || strings.ContainsAny(task.Title, "\r\n") || strings.ContainsAny(task.NextAction, "\r\n") {
			return fmt.Errorf("task %s needs a single-line title and next_action", name)
		}
		if len(task.Validation) == 0 {
			return fmt.Errorf("task %s needs at least one validation criterion", name)
		}
		for _, criterion := range task.Validation {
			if strings.TrimSpace(criterion) == "" || strings.ContainsAny(criterion, "\r\n") {
				return fmt.Errorf("task %s has an empty or multiline validation criterion", name)
			}
		}
		if _, err := task.Check.Command(); err != nil {
			return fmt.Errorf("task %s check: %w", name, err)
		}
		if err := validateWrittenBy(state, name, task, kinds); err != nil {
			return err
		}
		if task.Check.WrittenBy == "" {
			if err := validator.Validate(ctx, task.Check); err != nil {
				return fmt.Errorf("task %s check `%s` cannot run yet (%v); if a check task writes it, set written_by to that task", name, task.Check.Label(), err)
			}
		}
		if patch.Added[task.ID] {
			key := task.Check.Key()
			if doneID := doneByCheck[key]; doneID != "" && !failing[key] && !gap {
				return fmt.Errorf("task %s reuses the check `%s` of verified task %s, which still passes; do not redo verified work, and give a new task a check that fails until its gap is fixed", name, task.Check.Label(), state.ShortTaskID(doneID))
			}
		}
	}
	if mode == EvidenceLocked {
		return nil
	}
	if len(plan.Evidence) == 0 && len(state.Evidence.Items) == 0 {
		return fmt.Errorf("the plan needs at least one evidence check that proves the overall goal")
	}
	if mode == EvidenceRevision && len(plan.Evidence) == 0 {
		return fmt.Errorf("an evidence revision must return the complete evidence list")
	}
	for index, item := range plan.Evidence {
		name := DefaultString(item.ID, fmt.Sprintf("evidence[%d]", index))
		if _, err := item.Check.Command(); err != nil {
			return fmt.Errorf("evidence %s: %w", name, err)
		}
		writer := strings.TrimSpace(item.Check.WrittenBy)
		if writer == "self" {
			return fmt.Errorf("evidence %s: written_by must name the check task that writes it", name)
		}
		if writer != "" {
			id := patch.Refs[writer]
			if id == "" {
				id = state.TaskID(writer)
			}
			if kinds[id] != KindCheck {
				return fmt.Errorf("evidence %s is written by %s, which is not a check task; a separate check task must write evidence", name, writer)
			}
			continue
		}
		if item.Status == "missing" {
			return fmt.Errorf("evidence %s is missing; set written_by to the check task that writes it", name)
		}
		if err := validator.Validate(ctx, item.Check); err != nil {
			return fmt.Errorf("evidence %s `%s` cannot run (%v); if a check task writes it, set written_by", name, item.Check.Label(), err)
		}
	}
	return nil
}

func validateWrittenBy(state State, name string, task Task, kinds map[string]string) error {
	writer := strings.TrimSpace(task.Check.WrittenBy)
	kind := DefaultString(task.Kind, KindWork)
	switch {
	case writer == "":
		return nil
	case writer == "self" || writer == task.ID:
		if kind != KindCheck {
			return fmt.Errorf("work task %s cannot write its own check; add a check task that writes it and set written_by to that task", name)
		}
		return nil
	case kinds[writer] == "":
		return fmt.Errorf("task %s check is written_by unknown task %s", name, state.ShortTaskID(writer))
	case kinds[writer] != KindCheck:
		return fmt.Errorf("task %s check is written_by %s, which is not a check task; only check tasks write checks", name, state.ShortTaskID(writer))
	}
	return nil
}

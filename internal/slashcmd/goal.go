package slashcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/goal"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/types"
)

const (
	goalJudgeDiffLimit      = 12000
	goalEvidenceSourceLimit = 4000
)

// GoalCommand, GoalApproveCommand and GoalRetryCommand take per-role model
// flags; Choices and CurrentRef come from the configured models. Choices is
// a func so completion follows models added or switched during the session.
type GoalCommand struct {
	CurrentRef func(*agent.AgentSession) string
	Choices    func() []config.ModelChoice
}
type GoalApproveCommand struct {
	CurrentRef func(*agent.AgentSession) string
	Choices    func() []config.ModelChoice
}
type GoalAnswerCommand struct{}
type GoalEvidenceCommand struct{}
type GoalResumeCommand struct{}
type GoalRetryCommand struct {
	CurrentRef func(*agent.AgentSession) string
	Choices    func() []config.ModelChoice
}
type GoalStatusCommand struct{}
type GoalStopCommand struct{}

func (GoalCommand) Name() string        { return "goal" }
func (GoalCommand) Description() string { return "Work toward a goal with independent verification" }
func (GoalCommand) Usage() string {
	return "/goal [--yes] " + goalModelUsage + " CONDITION..."
}
func (GoalCommand) Aliases() []string { return nil }

func (GoalApproveCommand) Name() string { return "goal:approve" }
func (GoalApproveCommand) Description() string {
	return "Approve or revise the proposed goal plan"
}
func (GoalApproveCommand) Usage() string {
	return "/goal:approve " + goalModelUsage + ` [--edit "CHANGES"]`
}
func (GoalApproveCommand) Aliases() []string { return nil }

func (GoalAnswerCommand) Name() string        { return "goal:answer" }
func (GoalAnswerCommand) Description() string { return "Answer a paused goal question and resume" }
func (GoalAnswerCommand) Usage() string       { return "/goal:answer ANSWER..." }
func (GoalAnswerCommand) Aliases() []string   { return nil }

func (GoalEvidenceCommand) Name() string        { return "goal:evidence" }
func (GoalEvidenceCommand) Description() string { return "Revise the approved goal evidence" }
func (GoalEvidenceCommand) Usage() string       { return `/goal:evidence --edit "CHANGES"` }
func (GoalEvidenceCommand) Aliases() []string   { return nil }

func (GoalResumeCommand) Name() string        { return "goal:resume" }
func (GoalResumeCommand) Description() string { return "Resume an interrupted goal run" }
func (GoalResumeCommand) Usage() string       { return "/goal:resume" }
func (GoalResumeCommand) Aliases() []string   { return nil }

func (GoalRetryCommand) Name() string { return "goal:retry" }
func (GoalRetryCommand) Description() string {
	return "Replan the goal, optionally with feedback, and wait for approval"
}
func (GoalRetryCommand) Usage() string     { return "/goal:retry " + goalModelUsage + " [REASON...]" }
func (GoalRetryCommand) Aliases() []string { return nil }

func (GoalStatusCommand) Name() string        { return "goal:status" }
func (GoalStatusCommand) Description() string { return "Show goal status and the next command" }
func (GoalStatusCommand) Usage() string       { return "/goal:status" }
func (GoalStatusCommand) Aliases() []string   { return nil }

func (GoalStopCommand) Name() string        { return "goal:stop" }
func (GoalStopCommand) Description() string { return "Stop a paused goal run" }
func (GoalStopCommand) Usage() string       { return "/goal:stop" }
func (GoalStopCommand) Aliases() []string   { return nil }

// goalLimits bound every goal run. The app sets them from the profile.
var goalLimits = goal.DefaultLimits

// ConfigureGoal sets the budget and per-role models of goal runs from the
// profile; unset values keep the defaults.
func ConfigureGoal(cfg config.Goal) {
	limits := goal.DefaultLimits
	if cfg.MaxTokens != nil {
		limits.MaxTokens = *cfg.MaxTokens
	}
	if cfg.MaxDuration.Duration > 0 {
		limits.MaxActiveTime = cfg.MaxDuration.Duration
	}
	goalLimits = limits
	goalDefaultModels = goal.Models{Planner: goalRoleModel(cfg.Planner), Worker: goalRoleModel(cfg.Worker), Verifier: goalRoleModel(cfg.Verifier)}
}

func (c GoalCommand) Complete(args string, cursor int) (Completion, bool) {
	return goalOptionCompletion(args, cursor, goalStartOptionDefs, callChoices(c.Choices))
}

func (c GoalApproveCommand) Complete(args string, cursor int) (Completion, bool) {
	return goalOptionCompletion(args, cursor, goalApproveOptionDefs, callChoices(c.Choices))
}

func (c GoalRetryCommand) Complete(args string, cursor int) (Completion, bool) {
	return goalOptionCompletion(args, cursor, goalModelOptionDefs, callChoices(c.Choices))
}

func callChoices(choices func() []config.ModelChoice) []config.ModelChoice {
	if choices == nil {
		return nil
	}
	return choices()
}

func newGoalEngine(session *agent.AgentSession) *goal.Engine {
	return &goal.Engine{
		Store:  goalStore{session: session, limits: goalLimits},
		Roles:  goalRoles{session: session},
		Exec:   goalExec{session: session},
		Limits: goalLimits,
		Notify: EmitProgress,
	}
}

// stepGoal feeds one input to the goal engine and reports the stage it
// stopped in. Exit codes come from that stage.
func stepGoal(ctx context.Context, session *agent.AgentSession, in goal.Input) (Result, error) {
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}
	// The prior state only feeds the plan diff; Step reports load errors.
	before, _, _ := loadGoalState(session)
	state, err := newGoalEngine(session).Step(ctx, in)
	if err != nil {
		return Result{}, err
	}
	result := Result{Message: goalStatusMessage(state, goalStateFile(session)), ExitCode: goalExitCode(state.Stage)}
	if diff := goal.PlanDiff(before, state); diff != "" {
		result.Message = diff + "\n\n" + result.Message
	}
	if _, stop := in.(goal.Stop); stop {
		result.ExitCode = 0
	}
	if state.Stage == goal.Done || state.Stage == goal.Stalled || state.Stage == goal.Stopped {
		published, err := session.RecordCommandOutcome(state.ID, result.Message)
		if err != nil {
			return result, err
		}
		result.MessagePublished = published
	}
	return result, nil
}

func (c GoalCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	if session == nil || !session.TasksEnabled() {
		return Result{}, fmt.Errorf("/goal requires a session with internal tasks")
	}
	options, text, err := scanGoalOptions(args, goalStartOptionDefs)
	if err != nil {
		return Result{}, err
	}
	auto := options["yes"] == "true"
	if strings.HasSuffix(text, " --yes") {
		auto, text = true, strings.TrimSpace(strings.TrimSuffix(text, " --yes"))
	}
	if text == "" {
		return Result{}, fmt.Errorf("usage: %s", (GoalCommand{}).Usage())
	}
	flags, err := goalModelsFromOptions(options, session, c.CurrentRef)
	if err != nil {
		return Result{}, err
	}
	models := goalDefaultModels
	if flags != nil {
		models = models.Override(*flags)
	}
	// Fail before fetching references when another goal is active or a
	// model is unknown.
	if state, found, err := loadGoalState(session); err == nil && found && state.Stage.Active() {
		return Result{}, fmt.Errorf("a goal is already active; use /goal:status or /goal:stop")
	}
	if err := validateGoalModels(ctx, session, models); err != nil {
		return Result{}, err
	}
	start := goal.Start{
		ID:   "goal-" + uuid.NewV4().String()[:8],
		Goal: text,
		Created: goal.CreatedData{
			SessionID: session.ChannelID(), Adapter: session.AdapterType(), AutoApprove: auto, Models: models,
			BaseRevision: goalGitRevision(ctx, session), StartDirty: captureGoalStartDirty(ctx, session),
			References: fetchGoalReferences(ctx, session, text),
		},
	}
	return stepGoal(ctx, session, start)
}

func previewLine(value string, max int) string { return goal.Preview(value, max) }

func defaultString(value, fallback string) string { return goal.DefaultString(value, fallback) }

// goalModelFlags parses model flags and validates the models they choose.
func goalModelFlags(ctx context.Context, session *agent.AgentSession, options map[string]string, currentRef func(*agent.AgentSession) string) (*goal.Models, error) {
	if session == nil {
		return nil, fmt.Errorf("agent session is required")
	}
	models, err := goalModelsFromOptions(options, session, currentRef)
	if err != nil || models == nil {
		return models, err
	}
	return models, validateGoalModels(ctx, session, *models)
}

func (c GoalApproveCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	options, rest, err := scanGoalOptions(args, goalApproveOptionDefs)
	if err != nil {
		return Result{}, err
	}
	if rest != "" {
		return Result{}, fmt.Errorf("usage: %s", (GoalApproveCommand{}).Usage())
	}
	models, err := goalModelFlags(ctx, session, options, c.CurrentRef)
	if err != nil {
		return Result{}, err
	}
	edit, editing := options["edit"]
	if !editing {
		return stepGoal(ctx, session, goal.Approve{Models: models})
	}
	if edit == "" {
		return Result{}, fmt.Errorf("plan edit is empty")
	}
	return stepGoal(ctx, session, goal.Revise{Edit: goal.Edit{Target: goal.EditPlan, Text: edit}, Models: models})
}

func (GoalAnswerCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	answer := strings.TrimSpace(args)
	if session != nil && answer == "" {
		return Result{}, fmt.Errorf("usage: %s", (GoalAnswerCommand{}).Usage())
	}
	return stepGoal(ctx, session, goal.Answer{Text: answer})
}

func (GoalEvidenceCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	options, rest, err := scanGoalOptions(args, []optionDef{{Name: "edit", TakesValue: true}})
	if err != nil || rest != "" || options["edit"] == "" {
		return Result{}, fmt.Errorf("usage: %s", (GoalEvidenceCommand{}).Usage())
	}
	edit := options["edit"]
	return stepGoal(ctx, session, goal.Revise{Edit: goal.Edit{Target: goal.EditEvidence, Text: edit}})
}

func (GoalResumeCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	if err := rejectArguments("goal:resume", args); err != nil {
		return Result{}, err
	}
	return stepGoal(ctx, session, goal.Resume{})
}

func (c GoalRetryCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	options, rest, err := scanGoalOptions(args, goalModelOptionDefs)
	if err != nil {
		return Result{}, err
	}
	models, err := goalModelFlags(ctx, session, options, c.CurrentRef)
	if err != nil {
		return Result{}, err
	}
	return stepGoal(ctx, session, goal.Retry{Models: models, Reason: rest})
}

func (GoalStatusCommand) Run(_ context.Context, session *agent.AgentSession, args string) (Result, error) {
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}
	if err := rejectArguments("goal:status", args); err != nil {
		return Result{}, err
	}
	state, found, err := loadGoalState(session)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{Message: "No goal in this session. Next: /goal <condition>"}, nil
	}
	return Result{Message: goalStatusMessage(state, goalStateFile(session))}, nil
}

func (GoalStopCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	if err := rejectArguments("goal:stop", args); err != nil {
		return Result{}, err
	}
	return stepGoal(ctx, session, goal.Stop{})
}

// goalRegistry returns the session's tools in build mode. Starting a goal
// asks for changes, so plan mode, which only limits the session's own model,
// does not apply to the goal's roles or checks.
func goalRegistry(session *agent.AgentSession) *tools.Registry {
	return session.ToolRegistry().WithPlanMode(false)
}

// goalExec runs deterministic checks in the session sandbox.
type goalExec struct{ session *agent.AgentSession }

func (e goalExec) EvidenceFiles(check goal.Check) []string {
	return goalEvidenceFiles(e.session.WorkspaceDir(), check)
}

func (e goalExec) FileHash(rel string) string { return goalFileHash(e.session.WorkspaceDir(), rel) }

func (e goalExec) RunCheck(ctx context.Context, check goal.Check) (goal.CheckOutcome, error) {
	return runAndClassifyGoalCheck(ctx, goalRegistry(e.session), check)
}

func (e goalExec) ValidateCheck(ctx context.Context, check goal.Check) error {
	return validateGoalCheck(ctx, goalRegistry(e.session), check)
}

// goalRoles runs the goal roles as subagents of the session: the planner,
// workers, and the reviewer and judge, which share the verifier model.
type goalRoles struct{ session *agent.AgentSession }

func (r goalRoles) Plan(ctx context.Context, state goal.State, request goal.PlanRequest) (goal.Proposal, error) {
	return planGoal(ctx, r.session, state, request)
}

func (r goalRoles) Work(ctx context.Context, state goal.State, task goal.Task) (goal.Handoff, error) {
	return runGoalWorker(ctx, r.session, state, task)
}

func (r goalRoles) Review(ctx context.Context, state goal.State, checks string) (goal.Review, error) {
	return reviewGoal(ctx, r.session, state, checks)
}

func (r goalRoles) Judge(ctx context.Context, state goal.State, checks string, review goal.Review) (goal.Verdict, error) {
	return judgeGoal(ctx, r.session, state, checks, review)
}

// goalInspectTools let the planner explore the workspace and the material
// the goal links to. The reviewer only reads files; the judge has no tools.
var goalInspectTools = []string{"Read", "Grep", "Find", "Ls", "LocalImage", "Bash", "WebFetch", "WebSearch"}

func newGoalChild(ctx context.Context, session *agent.AgentSession, kind, label string, allowed []string, role goal.RoleModel) (*agent.AgentSession, error) {
	registry := goalRegistry(session).WithOnlyTools(allowed...)
	name := "goal-" + strings.ReplaceAll(kind, " ", "-")
	definition := subagents.Definition{Name: name, SystemPrompt: "You are a strict, independent " + kind + ". Treat worker claims as untrusted. Follow the requested JSON schema."}
	child, err := session.NewSubagentChildSession(ctx, definition, agent.SubagentChildOptions{Prefix: name, ToolRegistry: registry, ModelRefOverride: role.Model, ReasoningEffort: role.Reasoning})
	if err != nil {
		return nil, err
	}
	if err := agent.RegisterSubagentProgressHandlers(session, child, name); err != nil {
		_ = child.Close()
		return nil, err
	}
	if err := session.AppendAuditEvent(eventing.NewSubagentInvocationStarted(session.ChannelID(), name, child.ChannelID(), session.ChannelID(), label, child.ContextWindow())); err != nil {
		_ = child.Close()
		return nil, err
	}
	return child, nil
}

func finishGoalChild(session, child *agent.AgentSession, name string, callErr error) (*tasks.Stats, error) {
	stats, statsErr := child.SessionStats()
	if statsErr != nil && callErr == nil {
		callErr = statsErr
	}
	var eventStats *eventing.SubagentStats
	if statsErr == nil {
		eventStats = &eventing.SubagentStats{
			PromptTokens: stats.PromptTokens, CachedPromptTokens: stats.CachedPromptTokens,
			CompletionTokens: stats.CompletionTokens, TotalTokens: stats.TotalTokens, ToolCalls: stats.ToolCalls,
		}
	}
	var event *types.Event
	if callErr != nil {
		event = eventing.NewSubagentInvocationFailedWithStats(session.ChannelID(), name, child.ChannelID(), session.ChannelID(), callErr.Error(), child.EstimatedContextTokens(), child.ContextWindow(), eventStats)
	} else {
		event = eventing.NewSubagentInvocationCompletedWithStats(session.ChannelID(), name, child.ChannelID(), session.ChannelID(), "", child.EstimatedContextTokens(), child.ContextWindow(), eventStats)
	}
	if err := session.AppendAuditEvent(event); err != nil && callErr == nil {
		return stats, err
	}
	return stats, callErr
}

// callGoalJSON runs one structured role call and returns what it spent.
func callGoalJSON(ctx context.Context, session *agent.AgentSession, kind, label, prompt string, allowed []string, role goal.RoleModel, target any) (stats *tasks.Stats, resultErr error) {
	child, err := newGoalChild(ctx, session, kind, label, allowed, role)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, child.Close()) }()
	_, callErr := child.RunStructured(ctx, prompt, target)
	if callErr != nil {
		callErr = fmt.Errorf("%s: %w", kind, callErr)
	}
	return finishGoalChild(session, child, "goal-"+strings.ReplaceAll(kind, " ", "-"), callErr)
}

type goalVerdict struct {
	goal.Verdict

	validate func(goal.Verdict) error
}

func (v *goalVerdict) ValidateStructuredCandidate(candidate any) error {
	if v.validate == nil {
		return nil
	}
	verdict, ok := candidate.(*goalVerdict)
	if !ok {
		return fmt.Errorf("expected goal verdict")
	}
	return v.validate(verdict.Verdict)
}

// judgeGoal makes the final decision from the check results and the review
// report on the verifier model. It has no tools; it weighs evidence others
// gathered.
func judgeGoal(ctx context.Context, session *agent.AgentSession, state goal.State, checks string, review goal.Review) (goal.Verdict, error) {
	prompt := fmt.Sprintf(`You are an independent judge making the final decision on a goal. All deterministic checks passed, and an independent reviewer inspected the changes. Weigh the check results, the review report, the evidence sources and the changes below, then decide. You have no tools, and worker prose is not evidence. A zero exit is necessary but not sufficient: the goal is not done when output contradicts it, when a check is too weak to prove what it covers, when a requirement is unmet, or when the review leaves a criterion unclear and nothing else here proves it.
For every blocker or major review finding, accept it (it is real and must be fixed) or dismiss it with a reason (it is wrong or outside the goal). done=true is only possible when no blocker or major finding is accepted. You may also accept minor findings.
Return JSON only: {"done":bool,"reason":"specific evidence-based conclusion","summary":"short user-facing conclusion","gaps":["each concrete unmet requirement not already covered by an accepted finding"],"findings":[{"index":1,"decision":"accept|dismiss","reason":"..."}]}.
Goal: %s
Referenced material:
%s
Overall criteria:
%s
Approved evidence:
%s
Check results:
%s
Review report:
%s
Evidence sources:
%s
Changes since the goal started:
%s
Tasks:
%s`, state.Goal, formatGoalReferences(state.References, goalReferencePromptSize), strings.Join(state.Evidence.Criteria, "\n"), goal.FormatEvidence(state.Evidence), checks, goal.FormatReview(review), goalEvidenceSources(session.WorkspaceDir(), state), renderGoalChanges(ctx, session, state, goalJudgeDiffLimit), goalReviewTaskView(state))
	verdict := goalVerdict{validate: func(candidate goal.Verdict) error { return candidate.Validate(review) }}
	stats, err := callGoalJSON(ctx, session, "judge", "Decide the goal", prompt, nil, state.Models.Verifier, &verdict)
	if err != nil {
		return goal.Verdict{}, err
	}
	verdict.Tokens = goal.BillableTokens(stats)
	return verdict.Verdict, nil
}

// goalEvidenceSources shows the judge what each evidence check executes, so a
// weak or rewritten script cannot pass on its exit code alone.
func goalEvidenceSources(workspace string, state goal.State) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, item := range state.Evidence.Items {
		for _, rel := range goalEvidenceFiles(workspace, item.Check) {
			if seen[rel] {
				continue
			}
			seen[rel] = true
			data, err := os.ReadFile(filepath.Join(workspace, rel))
			if err != nil {
				continue
			}
			content := string(data)
			if len(content) > goalEvidenceSourceLimit {
				content = content[:goalEvidenceSourceLimit] + "\n... (truncated)"
			}
			fmt.Fprintf(&b, "--- %s (%s)\n%s\n", rel, item.ID, content)
		}
	}
	if b.Len() == 0 {
		return "(evidence runs project commands only)"
	}
	return b.String()
}

func goalGitRevision(ctx context.Context, session *agent.AgentSession) string {
	result, err := goalRegistry(session).RunVerificationCommand(ctx, "git rev-parse HEAD")
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

type goalWorkerHandoff struct {
	TaskID      string   `json:"task_id"`
	Result      string   `json:"result"`
	Notes       string   `json:"notes"`
	Discoveries []string `json:"discoveries"`
}

func (h *goalWorkerHandoff) ValidateStructured() error {
	switch h.Result {
	case "ok", "blocked", "scope_changed":
		return nil
	}
	return fmt.Errorf(`result must be "ok", "blocked" or "scope_changed"`)
}

// goalWorkerProtected lists the files a worker must not write: frozen
// evidence, and for a work task graded by a check a check task wrote, the
// files that check runs. Check tasks write verification, so they get none.
func goalWorkerProtected(workspace string, state goal.State, task goal.Task) []string {
	if task.Kind == goal.KindCheck {
		return nil
	}
	protected := state.ProtectedFiles()
	if task.Check.WrittenBy != "" {
		for _, rel := range goalEvidenceFiles(workspace, task.Check) {
			if !goal.Contains(protected, rel) {
				protected = append(protected, rel)
			}
		}
	}
	return protected
}

// runGoalWorker runs one attempt at a task in a child session with the
// session's tools, minus session tools and nested subagents. Protected files
// are read-only in its sandbox and file tools, so evidence cannot be
// weakened; the hash check at verification is only a backstop.
func runGoalWorker(ctx context.Context, session *agent.AgentSession, state goal.State, task goal.Task) (goal.Handoff, error) {
	protected := goalWorkerProtected(session.WorkspaceDir(), state, task)
	workerRegistry := goalRegistry(session).WithoutSessionTools().WithDisabledTools(map[string]bool{"CallSubagent": true}).WithProtectedPaths(protected...)
	// An empty system prompt keeps the session's own prompt for the worker.
	child, err := session.NewSubagentChildSession(ctx, subagents.Definition{Name: "goal-worker"}, agent.SubagentChildOptions{
		Prefix: "goal-worker", ToolRegistry: workerRegistry, IsolatedExecution: len(protected) > 0,
		ModelRefOverride: state.Models.Worker.Model, ReasoningEffort: state.Models.Worker.Reasoning,
	})
	if err != nil {
		return goal.Handoff{}, err
	}
	if err := agent.RegisterSubagentProgressHandlers(session, child, "goal-worker"); err != nil {
		_ = child.Close()
		return goal.Handoff{}, err
	}
	if err := session.AppendAuditEvent(eventing.NewSubagentInvocationStarted(session.ChannelID(), "goal-worker", child.ChannelID(), session.ChannelID(), task.Title, child.ContextWindow())); err != nil {
		_ = child.Close()
		return goal.Handoff{}, err
	}
	var handoff goalWorkerHandoff
	_, callErr := child.RunStructured(ctx, goalWorkerPrompt(state, task, protected), &handoff)
	invalid := callErr != nil && strings.Contains(callErr.Error(), "structured output invalid after")
	if invalid {
		// The worker may still have done the work; its check decides.
		callErr = nil
	}
	stats, finishErr := finishGoalChild(session, child, "goal-worker", callErr)
	_ = child.Close()
	if finishErr != nil {
		return goal.Handoff{}, finishErr
	}
	return goal.Handoff{Result: handoff.Result, Notes: handoff.Notes, Discoveries: handoff.Discoveries, Invalid: invalid, Stats: stats}, nil
}

func goalWorkerPrompt(state goal.State, task goal.Task, protected []string) string {
	role := "This is a work task: change the product so the harness check passes. Tests and evidence files are read-only for you; if one must change, report scope_changed so a check task can change it."
	if task.Kind == goal.KindCheck {
		role = "This is a check task: write only verification (tests or scripts) for the criteria below. Do not implement the feature. What you write should fail until the work it verifies is done, and must not depend on network access or tools the sandbox denies."
	}
	if len(protected) > 20 {
		protected = append(protected[:20:20], fmt.Sprintf("... and %d more test and evidence files", len(protected)-20))
	}
	previous := "none"
	if task.LastCheck != "" {
		previous = task.LastCheck
		if task.LastCheckOutput != "" {
			previous += "\nOutput tail:\n" + task.LastCheckOutput
		}
	}
	return fmt.Sprintf(`Work on this bounded task toward the immutable goal. Inspect the workspace before editing. Follow the existing sandbox and user approval rules. Do not write or ask to edit the harness state. Return JSON only: {"task_id":%q,"result":"ok|blocked|scope_changed","notes":"at most two lines","discoveries":["durable fact"]}. Do not claim that checks pass; the harness runs the check below after you finish.
Use result "blocked" only when you need a user decision or access the sandbox denies (network, permissions, tools); put the exact request in notes. Use "scope_changed" when the task is wrong as written. Never work around a sandbox denial by weakening tests or checks.
%s
Goal: %s
Referenced material:
%s
Task: %s
Next action: %s
Validation criteria: %s
Harness check: %s
Previous worker outcome: %s
Previous check result: %s
Read-only files: %s
Accumulated discoveries: %s
User answer: %s`, task.ID, role, state.Goal, formatGoalReferences(state.References, goalReferencePromptSize), task.Title, task.NextAction, strings.Join(task.Validation, "\n"), task.Check.Label(), defaultString(task.LastOutcome, "none"), previous, defaultString(strings.Join(protected, ", "), "none"), strings.Join(state.Entries(goal.EntryDiscovery, 20), "\n"), state.Answer)
}

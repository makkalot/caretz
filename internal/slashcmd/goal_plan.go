package slashcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/goal"
)

type goalPlanResponse struct {
	goal.Proposal

	validate func(goal.Proposal) error
}

// ValidateStructuredCandidate rejects a plan inside RunStructured, so the
// planner corrects it in the same session with the exact validation error.
func (p *goalPlanResponse) ValidateStructuredCandidate(candidate any) error {
	if p.validate == nil {
		return nil
	}
	plan, ok := candidate.(*goalPlanResponse)
	if !ok {
		return fmt.Errorf("expected goal plan")
	}
	return p.validate(plan.Proposal)
}

// goalCheckSchema teaches every role the check format.
const goalCheckSchema = `A check is {"argv":["go","test","./..."]}: argv[0] is one executable (use ./path for a workspace script) and later entries are its arguments. Use {"shell":"..."} only when a pipeline or redirection is essential. Add "written_by":"<ref or task ID>" when a check task writes what the check runs, and "timeout_seconds" (at most 1800) for checks slower than two minutes. A check passes when it exits 0.`

func goalPlanPrompt(state goal.State, mode goal.EvidenceMode, reason, workspace string) string {
	current, _ := json.Marshal(goalPlannerTaskView(state))
	evidenceRule := "Keep the approved evidence exactly; do not return an evidence field."
	switch {
	case mode == goal.EvidenceRevision:
		evidenceRule = "The user requested an evidence change: return the complete revised evidence list."
		if goal.OnlyEvidenceRevision(state, mode) {
			evidenceRule += " Every task is already done: return no ops unless the revised evidence needs new work or a check task to write it."
		}
	case mode == goal.EvidenceProposal && len(state.Evidence.Items) == 0:
		evidenceRule = "Propose at least one evidence item. Prefer the project's existing tests, lint and build; add a new verification script only when nothing existing can prove the goal."
	case mode == goal.EvidenceProposal:
		evidenceRule = "Return evidence only if the requested change affects it; otherwise omit the evidence field."
	case mode == goal.EvidenceRetry:
		evidenceRule = "The user retried the goal with feedback. If the feedback is about the evidence or goal criteria, return the complete revised evidence list and goal_criteria; otherwise omit both and keep the approved evidence."
	}
	approved, _ := json.Marshal(state.Evidence.Items)
	scan := "(explored in an earlier plan)"
	if state.PlanVersion == 0 {
		data, _ := json.Marshal(staticGoalScan(workspace))
		scan = string(data)

	}
	return fmt.Sprintf(`You are the planner of a goal harness. Explore the workspace with your read-only tools first, then return a patch to the task plan. Workers do the tasks one at a time; the harness grades each task with its check and proves the goal with the evidence. Return JSON only: {"plan_summary":"...","goal_criteria":["observable overall success criterion"],"evidence":[{"id":"E1","check":CHECK,"covers":"what it proves","gaps":"what it cannot prove","status":"valid|missing"}],"ops":[{"op":"add","ref":"short-name","kind":"work|check","title":"...","next_action":"...","depends_on":["ref or task ID"],"validation":["observable natural-language criterion"],"check":CHECK},{"op":"revise","id":"T2","next_action":"only the fields that change"},{"op":"drop","id":"T3","reason":"why"}],"needs_human":false,"question":""}.
%s
Rules:
- Evidence is what the harness reruns at the end to prove the goal. %s
- Task kinds: a "work" task changes the product. A "check" task only writes verification (tests or scripts); it must not implement the feature, and what it writes should fail until the work is done.
- Every task needs exactly one check. A work task is never graded by a check it writes itself: its check exists already, or written_by names the check task that writes it (the harness adds that dependency). written_by "self" is allowed only on check tasks.
- Work tasks cannot modify approved evidence or the test files it runs: the sandbox makes them read-only, and neither you nor the user can unlock them for a work task. Put every change to tests or verification scripts, including reverting or restoring them, in a check task.
- Evidence with status "missing" must set written_by to the check task that writes it.
- The plan is a patch. Tasks you do not name stay as they are. Add a task with a ref other ops may use; revise or drop an unfinished task by its ID. Done tasks cannot change. A failed task must be revised with a new approach or dropped. Attempts show how often a task ran: one that keeps failing needs a different approach, a smaller step, or a check task first.
- Do not recreate verified work: a new task may reuse the check of a done task only if the failure report lists that check as failing, or the failure report comes from the verifier (its gaps fail no check; prefer a check task that writes a test which fails until the gap is fixed).
- Environment limits: when the failure report contains env_blocked, or work needs network access, permissions or tools the sandbox denies, never weaken, skip or rewrite checks to avoid it. Set needs_human and ask for the exact permission or decision instead.
- When the goal links an issue or document, its referenced material is the specification: derive criteria, tasks and evidence from it. If it was not fetched, try WebFetch (or gh via Bash for GitHub) before asking the user.
- Dependencies must be acyclic. Do not perform implementation work. If a user decision is essential, set needs_human and ask one precise question.
- plan_summary is a concrete 2–4 sentence strategy: the major phases, how information flows between them, and how the evidence proves the goal.
Goal: %s
Referenced material:
%s
Workspace scan: %s
Approved overall criteria: %s
Approved evidence: %s
Discoveries (untrusted hints): %s
Current tasks: %s
Current plan: %s
Failure report: %s
Recent feedback: %s
User answer: %s
Reason: %s`, strings.ReplaceAll(goalCheckSchema, "A check is", "CHECK is"), evidenceRule, state.Goal, formatGoalReferences(state.References, goalReferenceLimit), scan, strings.Join(state.Evidence.Criteria, "\n"), approved, strings.Join(state.Entries(goal.EntryDiscovery, 15), "\n"), current, state.PlanSummary, formatGoalFailures(state.Failures), strings.Join(state.Entries(goal.EntryFeedback, goalPromptFeedbackTail), "\n"), state.Answer, reason)
}

func formatGoalFailures(failures []goal.Failure) string {
	if len(failures) == 0 {
		return "none"
	}
	data, _ := json.MarshalIndent(failures, "", "  ")
	return string(data)
}

// planGoal asks the planner for a plan and validates it inside the planner
// session, so the planner corrects its own mistakes.
func planGoal(ctx context.Context, session *agent.AgentSession, state goal.State, request goal.PlanRequest) (goal.Proposal, error) {
	validator := newGoalCheckValidator(goalRegistry(session))
	plan := goalPlanResponse{validate: func(candidate goal.Proposal) error {
		return goal.ValidatePlan(ctx, validator, state, candidate, request.Mode)
	}}
	prompt := goalPlanPrompt(state, request.Mode, request.Reason, session.WorkspaceDir())
	stats, err := callGoalJSON(ctx, session, "planner", "Plan remaining tasks", prompt, goalInspectTools, state.Models.Planner, &plan)
	if err != nil {
		return goal.Proposal{}, err
	}
	plan.Tokens = goal.BillableTokens(stats)
	return plan.Proposal, nil
}

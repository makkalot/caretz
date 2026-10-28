package slashcmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/goal"
)

const (
	goalReviewDiffBudget = 30000
	goalChangeFileLimit  = 6000
	goalFileMissing      = "missing"
)

var goalReviewTools = []string{"Read", "Grep", "Find", "Ls"}

// goalChangedFile is one file the goal changed, relative to the workspace.
type goalChangedFile struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"` // new, modified or deleted
	DirtyAtStart bool   `json:"dirty_at_start,omitempty"`
}

func goalGitLines(ctx context.Context, session *agent.AgentSession, command string) ([]string, error) {
	result, err := goalRegistry(session).RunVerificationCommand(ctx, command)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 || result.TimedOut {
		return nil, fmt.Errorf("%s: exit %d: %s", command, result.ExitCode, lastNonemptyLine(result.Stderr))
	}
	var lines []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func goalIgnoredPath(path string) bool {
	return path == ".caretz" || strings.HasPrefix(path, ".caretz/")
}

// goalWorkspaceHash hashes a workspace file; a missing file has a fixed marker.
func goalWorkspaceHash(workspace, rel string) string {
	if _, err := os.Stat(filepath.Join(workspace, rel)); err != nil {
		return goalFileMissing
	}
	return goalFileHash(workspace, rel)
}

// captureGoalStartDirty records files that already differ from HEAD when the
// goal starts, so the goal's own change set can exclude them.
func captureGoalStartDirty(ctx context.Context, session *agent.AgentSession) map[string]string {
	tracked, err := goalGitLines(ctx, session, "git diff --name-only HEAD")
	if err != nil {
		return nil
	}
	untracked, err := goalGitLines(ctx, session, "git ls-files --others --exclude-standard")
	if err != nil {
		return nil
	}
	dirty := map[string]string{}
	for _, path := range append(tracked, untracked...) {
		if !goalIgnoredPath(path) {
			dirty[path] = goalWorkspaceHash(session.WorkspaceDir(), path)
		}
	}
	return dirty
}

// goalChangeSet lists files changed since the goal started: files that differ
// from the base revision or are untracked, minus files that were already dirty
// at the start and have not changed since.
func goalChangeSet(ctx context.Context, session *agent.AgentSession, state goal.State) ([]goalChangedFile, error) {
	base := defaultString(state.BaseRevision, "HEAD")
	statuses, err := goalGitLines(ctx, session, "git diff --name-status "+shellQuote(base))
	if err != nil {
		return nil, err
	}
	untracked, err := goalGitLines(ctx, session, "git ls-files --others --exclude-standard")
	if err != nil {
		return nil, err
	}
	kinds := map[string]string{}
	for _, line := range statuses {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		path, kind := fields[len(fields)-1], "modified"
		switch fields[0][0] {
		case 'A':
			kind = "new"
		case 'D':
			kind = "deleted"
		case 'R', 'C':
			kind = "new"
		}
		kinds[path] = kind
	}
	for _, path := range untracked {
		kinds[path] = "new"
	}
	workspace := session.WorkspaceDir()
	var files []goalChangedFile
	for path, kind := range kinds {
		if goalIgnoredPath(path) {
			continue
		}
		current := goalWorkspaceHash(workspace, path)
		start, dirtyAtStart := state.StartDirty[path]
		if dirtyAtStart && start == current {
			continue
		}
		if kind != "deleted" && current == goalFileMissing {
			continue
		}
		files = append(files, goalChangedFile{Path: path, Kind: kind, DirtyAtStart: dirtyAtStart})
	}
	sort.Slice(files, func(i, j int) bool {
		if goalTestFile(files[i].Path) != goalTestFile(files[j].Path) {
			return goalTestFile(files[i].Path)
		}
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func goalTestFile(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, "_test.") || strings.HasPrefix(base, "test_") || strings.Contains(path, "/test/") || strings.Contains(path, "/tests/") || strings.HasPrefix(path, "scripts/")
}

// renderGoalChanges lists the change set and shows each file's change within
// budget: a diff for tracked files, the content of new files. Tests and
// verification scripts come first.
func renderGoalChanges(ctx context.Context, session *agent.AgentSession, state goal.State, budget int) string {
	files, err := goalChangeSet(ctx, session, state)
	if err != nil {
		return "(change set unavailable: " + goal.Preview(err.Error(), 160) + ")"
	}
	if len(files) == 0 {
		return "(no files changed since the goal started)"
	}
	base := defaultString(state.BaseRevision, "HEAD")
	var list, bodies strings.Builder
	for _, file := range files {
		note := ""
		if file.DirtyAtStart {
			note = " (already modified before the goal started)"
		}
		fmt.Fprintf(&list, "- %s %s%s\n", file.Kind, file.Path, note)
	}
	remaining := budget
	for _, file := range files {
		var body string
		switch {
		case file.Kind == "deleted":
			continue
		case file.DirtyAtStart:
			body = "(this file was already modified before the goal started, so a diff would mix in earlier work; read the file)"
		case file.Kind == "new":
			data, err := os.ReadFile(filepath.Join(session.WorkspaceDir(), file.Path))
			if err != nil {
				continue
			}
			body = string(data)
		default:
			lines, err := goalGitLines(ctx, session, "git diff "+shellQuote(base)+" -- "+shellQuote(file.Path))
			if err != nil {
				body = "(diff unavailable; read the file)"
			} else {
				body = strings.Join(lines, "\n")
			}
		}
		if len(body) > goalChangeFileLimit {
			body = body[:goalChangeFileLimit] + "\n... (truncated; read the file)"
		}
		if len(body) > remaining {
			fmt.Fprintf(&bodies, "--- %s (omitted for size; read the file)\n", file.Path)
			continue
		}
		remaining -= len(body)
		fmt.Fprintf(&bodies, "--- %s (%s)\n%s\n", file.Path, file.Kind, body)
	}
	return fmt.Sprintf("Base: %s\nChanged files:\n%s\n%s", base, list.String(), bodies.String())
}

var goalCitationPattern = regexp.MustCompile(`^(.+?):([0-9]+)(?:-([0-9]+))?$`)

// validGoalCitation checks that path:line (or path:start-end) names a
// workspace file and lines that exist.
func validGoalCitation(workspace, citation string) error {
	match := goalCitationPattern.FindStringSubmatch(strings.TrimSpace(citation))
	if match == nil {
		return fmt.Errorf("citation %q must be path:line", citation)
	}
	lines, err := goalFileLines(workspace, match[1])
	if err != nil {
		return fmt.Errorf("citation %q: %v", citation, err)
	}
	for _, value := range match[2:] {
		if value == "" {
			continue
		}
		line, _ := strconv.Atoi(value)
		if line < 1 || line > lines {
			return fmt.Errorf("citation %q: line %d is outside the file (1-%d)", citation, line, lines)
		}
	}
	return nil
}

func goalFileLines(workspace, rel string) (int, error) {
	if filepath.IsAbs(rel) {
		relative, err := filepath.Rel(workspace, rel)
		if err != nil || strings.HasPrefix(relative, "..") {
			return 0, fmt.Errorf("%s is outside the workspace", rel)
		}
		rel = relative
	}
	data, err := os.ReadFile(filepath.Join(workspace, filepath.Clean(rel)))
	if err != nil {
		return 0, fmt.Errorf("%s does not exist", rel)
	}
	return strings.Count(string(data), "\n") + 1, nil
}

// validateReviewCitations checks that every citation and finding names a
// workspace file and lines that exist.
func validateReviewCitations(workspace string, changes []goalChangedFile, review goal.Review) error {
	for i, item := range review.Criteria {
		for _, citation := range item.Evidence {
			if err := validGoalCitation(workspace, citation); err != nil {
				return fmt.Errorf("criteria[%d]: %v", i, err)
			}
		}
	}
	deleted := map[string]bool{}
	for _, file := range changes {
		if file.Kind == "deleted" {
			deleted[file.Path] = true
		}
	}
	for i, finding := range review.Findings {
		if deleted[finding.File] {
			continue
		}
		lines, err := goalFileLines(workspace, finding.File)
		if err != nil {
			return fmt.Errorf("findings[%d]: %v", i, err)
		}
		if finding.Line < 0 || finding.Line > lines {
			return fmt.Errorf("findings[%d]: line %d is outside %s (1-%d)", i, finding.Line, finding.File, lines)
		}
	}
	return nil
}

type goalReview struct {
	goal.Review

	validate func(goal.Review) error
}

func (r *goalReview) ValidateStructuredCandidate(candidate any) error {
	if r.validate == nil {
		return nil
	}
	review, ok := candidate.(*goalReview)
	if !ok {
		return fmt.Errorf("expected goal review")
	}
	return r.validate(review.Review)
}

// reviewGoal inspects the change with read-only tools on the verifier model.
// It runs only after every check passed; its report goes to the judge.
func reviewGoal(ctx context.Context, session *agent.AgentSession, state goal.State, checks string) (goal.Review, error) {
	changes, err := goalChangeSet(ctx, session, state)
	if err != nil {
		changes = nil
	}
	workspace := session.WorkspaceDir()
	prompt := fmt.Sprintf(`You are an independent code reviewer for a goal that worker subagents implemented. Every deterministic check below already passed; your job is what checks cannot prove: whether the logic is right and the implementation actually does what the goal asks. Use Read, Grep, Find and Ls to inspect the changed files and the code they touch. Worker notes are not evidence.
Review for:
1. Does the change implement every overall criterion and the referenced requirements, with correct logic, including edge cases and error paths? Cite the code that implements each one.
2. Do the tests and verification scripts written for this goal actually exercise the criteria, or are they trivial, skipped, or only assert that a name exists? Look closely at check results the harness marks as reporting a failure.
3. Missing wiring (registration, configuration, documentation the requirements ask for), incorrect behavior, unsafe changes, unrelated scope, and edits that weaken checks or evidence.
Return JSON only: {"criteria":[{"criterion":"...","status":"met|unmet|unclear","evidence":["path:line"]}],"findings":[{"severity":"blocker|major|minor","file":"path","line":12,"criterion":"related criterion or empty","problem":"...","fix":"..."}],"summary":"..."}.
Give one criteria entry per overall criterion, in order. Cite workspace-relative path:line (or path:start-end) for every met criterion and a file and line for every finding; use line 0 for a whole-file finding. Severity: blocker means a requirement is unmet or the change is broken; major means a significant defect or a test that does not prove its criterion; minor means worth fixing but not required by the goal. Report only real problems; an empty findings list is fine.
Goal: %s
Referenced material:
%s
Overall criteria:
%s
Approved evidence:
%s
Check results (all passed):
%s
Tasks:
%s
Changes since the goal started:
%s`, state.Goal, formatGoalReferences(state.References, goalReferencePromptSize), strings.Join(state.Evidence.Criteria, "\n"), goal.FormatEvidence(state.Evidence), checks, goalReviewTaskView(state), renderGoalChanges(ctx, session, state, goalReviewDiffBudget))
	review := goalReview{validate: func(candidate goal.Review) error {
		if err := candidate.Validate(state.Evidence.Criteria); err != nil {
			return err
		}
		return validateReviewCitations(workspace, changes, candidate)
	}}
	stats, err := callGoalJSON(ctx, session, "reviewer", "Review goal changes", prompt, goalReviewTools, state.Models.Verifier, &review)
	if err != nil {
		return goal.Review{}, err
	}
	for i := range review.Criteria {
		// A criterion is met only with code to point at.
		if review.Criteria[i].Status == "met" && len(review.Criteria[i].Evidence) == 0 {
			review.Criteria[i].Status = "unclear"
		}
		review.Criteria[i].Criterion = state.Evidence.Criteria[i]
	}
	review.Summary = goal.Compact(review.Summary, 600)
	review.Tokens = goal.BillableTokens(stats)
	return review.Review, nil
}

// goalReviewTaskView lists tasks without worker prose.
func goalReviewTaskView(state goal.State) string {
	var b strings.Builder
	for _, task := range state.Tasks {
		if task.Status == goal.TaskSuperseded {
			continue
		}
		kind := defaultString(task.Kind, goal.KindWork)
		fmt.Fprintf(&b, "- %s [%s, %s] %s; check: %s\n", state.ShortTaskID(task.ID), kind, task.Status, task.Title, task.Check.Label())
	}
	return b.String()
}

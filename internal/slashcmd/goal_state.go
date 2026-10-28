package slashcmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/goal"
	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
)

// goalStore keeps a goal in the session event log, its only source of
// truth. state.md, tasks/*.log and the session task panel are views it
// refreshes after every event.
type goalStore struct {
	session *agent.AgentSession
	limits  goal.Limits
}

func (s goalStore) Load() ([]eventing.GoalEvent, error) { return s.session.GoalEvents() }

func (s goalStore) Append(event eventing.GoalEvent) error {
	return s.session.AppendAuditEvent(eventing.NewGoalEvent(s.session.ChannelID(), event.Type, event))
}

func (s goalStore) Sync(state goal.State) error {
	if err := projectGoalTasks(s.session, state); err != nil {
		return err
	}
	path := goalStateFile(s.session)
	if path == "" {
		return nil
	}
	markdown := goal.RenderMarkdown(state, s.limits, time.Now())
	if err := atomicGoalFile(path, []byte(markdown)); err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(path), "tasks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, task := range state.Tasks {
		if task.ID == "" {
			continue
		}
		if err := atomicGoalFile(filepath.Join(dir, task.ID+".log"), []byte(goal.TaskLog(task))); err != nil {
			return err
		}
	}
	return nil
}

// goalStateFile is the session's state.md, or empty without a session dir.
func goalStateFile(session *agent.AgentSession) string {
	if dir := session.SessionDir(); dir != "" {
		return filepath.Join(dir, "state.md")
	}
	return ""
}

func loadGoalState(session *agent.AgentSession) (goal.State, bool, error) {
	return newGoalEngine(session).Load()
}

// projectGoalTasks mirrors goal tasks into the session task panel. The goal
// is authoritative; nothing flows back.
func projectGoalTasks(session *agent.AgentSession, state goal.State) error {
	tracked := make(map[string]tasks.Task)
	for _, task := range session.TasksInScope(state.ID) {
		tracked[task.ID] = task
	}
	// Settle every other task before starting one, so a start never collides
	// with a task left in progress by an interrupted run.
	ordered := make([]goal.Task, 0, len(state.Tasks))
	for _, task := range state.Tasks {
		if task.Status != goal.TaskRunning {
			ordered = append(ordered, task)
		}
	}
	for _, task := range state.Tasks {
		if task.Status == goal.TaskRunning {
			ordered = append(ordered, task)
		}
	}
	for _, task := range ordered {
		current, exists := tracked[task.ID]
		validation := strings.Join(task.Validation, "\n")
		if !exists {
			if task.Status == goal.TaskSuperseded {
				continue
			}
			if _, err := session.ApplyScopedTaskUpdate(state.ID, tasks.Update{Op: tasks.OpAdd, ID: task.ID, Title: task.Title, NextAction: task.NextAction, Validation: validation}); err != nil {
				return err
			}
			current = tasks.Task{ID: task.ID, Title: task.Title, NextAction: strings.TrimSpace(task.NextAction), Validation: strings.TrimSpace(validation), Status: tasks.StatusPending}
		}
		if current.Status == tasks.StatusDone {
			continue
		}
		block := ""
		switch task.Status {
		case goal.TaskDone:
			outcome := goal.DefaultString(task.LastCheck, goal.DefaultString(task.LastOutcome, "verified by goal"))
			if _, err := session.CompleteScopedTask(state.ID, task.ID, outcome, task.Stats); err != nil {
				return err
			}
			continue
		case goal.TaskRunning:
			if current.Status != tasks.StatusInProgress {
				if _, err := session.ApplyScopedTaskUpdate(state.ID, tasks.Update{Op: tasks.OpStart, ID: task.ID, NextAction: task.NextAction}); err != nil {
					return err
				}
			}
			continue
		case goal.TaskPending:
			if task.Attempts == 0 {
				if current.Status == tasks.StatusPending && (current.Title != task.Title || current.NextAction != strings.TrimSpace(task.NextAction) || current.Validation != strings.TrimSpace(validation)) {
					if _, err := session.ApplyScopedTaskUpdate(state.ID, tasks.Update{Op: tasks.OpUpdate, ID: task.ID, Title: task.Title, NextAction: task.NextAction, Validation: validation}); err != nil {
						return err
					}
				}
				continue
			}
			block = "retry: " + goal.DefaultString(task.LastCheck, task.LastOutcome)
		case goal.TaskAttempted:
			block = "awaiting check: " + goal.Preview(task.LastOutcome, 160)
		case goal.TaskFailed:
			block = "failed: " + goal.DefaultString(task.LastCheck, task.LastOutcome)
		case goal.TaskSuperseded:
			block = "superseded by revised goal plan"
		}
		block = strings.TrimSpace(goal.Compact(block, 300))
		if current.Status != tasks.StatusBlocked || current.NextAction != block {
			if _, err := session.ApplyScopedTaskUpdate(state.ID, tasks.Update{Op: tasks.OpBlock, ID: task.ID, NextAction: block}); err != nil {
				return err
			}
		}
	}
	return nil
}

func atomicGoalFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".goal-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// goalEvidenceFiles lists the workspace files a check depends on: files its
// arguments name (such as a verification script), and the test files of a
// test runner it invokes. Work tasks cannot write them.
func goalEvidenceFiles(workspace string, check goal.Check) []string {
	words := check.Argv
	if check.Executable() == "" {
		words = strings.Fields(check.Shell)
	}
	seen := map[string]bool{}
	var files []string
	add := func(rel string) {
		if !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	for _, word := range words {
		word = strings.Trim(word, `"'`)
		if word == "" || strings.HasPrefix(word, "-") {
			continue
		}
		path := word
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			add(filepath.ToSlash(rel))
		}
	}
	if match := goalTestFileMatcher(words); match != nil {
		for _, rel := range goalWorkspaceFiles(workspace, match) {
			add(rel)
		}
	}
	sort.Strings(files)
	return files
}

// goalTestFileMatcher recognizes a test runner in a check and returns which
// workspace files it runs, or nil.
func goalTestFileMatcher(words []string) func(rel string) bool {
	has := func(word string) bool { return goal.Contains(words, word) }
	base := ""
	if len(words) > 0 {
		base = filepath.Base(words[0])
	}
	switch {
	case has("go") && has("test"):
		return func(rel string) bool {
			return strings.HasSuffix(rel, "_test.go") || strings.Contains("/"+rel, "/testdata/")
		}
	case has("pytest") || has("py.test") || (has("-m") && has("pytest")):
		return func(rel string) bool {
			name := filepath.Base(rel)
			return strings.HasSuffix(name, ".py") && (strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test.py") || name == "conftest.py")
		}
	case has("jest") || has("vitest") || ((base == "npm" || base == "yarn" || base == "pnpm" || base == "bun" || base == "npx") && has("test")):
		return func(rel string) bool {
			name := filepath.Base(rel)
			return strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") || strings.Contains("/"+rel, "/__tests__/")
		}
	case base == "cargo" && has("test"):
		return func(rel string) bool { return strings.HasPrefix(rel, "tests/") && strings.HasSuffix(rel, ".rs") }
	case base == "make":
		return func(rel string) bool { return rel == "Makefile" || rel == "makefile" || rel == "GNUmakefile" }
	}
	return nil
}

// goalSkippedDirs hold dependencies and build output, never project tests.
var goalSkippedDirs = map[string]bool{".git": true, ".caretz": true, "node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "__pycache__": true, ".venv": true, "venv": true}

const goalMaxEvidenceFiles = 5000

func goalWorkspaceFiles(workspace string, match func(rel string) bool) []string {
	var files []string
	_ = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != workspace && goalSkippedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		if rel = filepath.ToSlash(rel); match(rel) {
			files = append(files, rel)
		}
		if len(files) >= goalMaxEvidenceFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return files
}

func goalFileHash(workspace, rel string) string {
	data, err := os.ReadFile(filepath.Join(workspace, rel))
	if err != nil {
		return goal.MissingFile
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func goalExitCode(stage goal.Stage) int {
	switch stage {
	case goal.AwaitingApproval:
		return 3
	case goal.AwaitingInput:
		return 4
	case goal.Stalled, goal.OverBudget:
		return 5
	case goal.Stopped:
		return 6
	}
	return 0
}

func goalStatusMessage(state goal.State, stateFile string) string {
	var message string
	switch state.Stage {
	case goal.Planning:
		message = "Goal setup is in progress. If the previous run ended, stop it with /goal:stop and start the goal again."
	case goal.AwaitingApproval:
		message = fmt.Sprintf("## Goal awaiting approval\n\n%d tasks, %d evidence checks, %d goal criteria\n\nModels: %s\n\n%s\n\n### Next\n\n- Approve: `/goal:approve`\n- Revise: `/goal:approve --edit \"describe changes\"`", len(state.ActiveTaskIDs()), len(state.Evidence.Items), len(state.Evidence.Criteria), state.Models.Line(), previewGoalPlan(state))
		if state.Question != "" {
			message += "\nQuestion: " + state.Question + "\nReply with an answer to revise the proposal."
		}
	case goal.AwaitingInput:
		message = "Goal is awaiting your answer.\n" + state.Question + "\nNext: /goal:answer <your answer>"
	case goal.Working, goal.Verifying, goal.Replanning:
		message = fmt.Sprintf("Goal is running (%d worker attempts, %s). If the previous run ended, continue with /goal:resume.", state.WorkerAttempts, goal.BudgetLine(state, goalLimits, time.Now()))
	case goal.Done:
		total, verified := 0, 0
		for _, task := range state.Tasks {
			if task.Status == goal.TaskSuperseded {
				continue
			}
			total++
			if task.Status == goal.TaskDone {
				verified++
			}
		}
		message = "Goal complete"
		if summary := goal.Preview(goal.FirstSentence(state.Summary), 180); summary != "(empty response)" {
			message += " — " + summary
		}
		message += fmt.Sprintf("\nTasks verified: %d/%d | Goal checks passed: %d/%d", verified, total, state.Checks.EvidencePassed, len(state.Evidence.Items))
		if state.Checks.RegressionTotal > 0 {
			message += fmt.Sprintf(" | Regressions passed: %d/%d", state.Checks.RegressionPassed, state.Checks.RegressionTotal)
		}
		if state.Review != nil {
			message += fmt.Sprintf(" | Review findings: %d", len(state.Review.Findings))
			if notes := goal.ReviewNotes(state.Review, state.Verdict); len(notes) > 0 {
				if len(notes) > 5 {
					notes = append(notes[:5], fmt.Sprintf("... %d more in Details", len(notes)-5))
				}
				message += "\nReview notes:\n- " + strings.Join(notes, "\n- ")
			}
		}
		message += fmt.Sprintf("\nSpent: %s", goal.BudgetLine(state, goalLimits, time.Now()))
	case goal.Stalled, goal.Stopped, goal.OverBudget:
		message = fmt.Sprintf("Goal %s: %s", state.Stage, goal.Preview(goal.FirstSentence(state.ExitReason), 240))
		if state.EvidenceFailureIDs(state.Failures) != nil {
			message += "\nOnly approved evidence fails, which a retry cannot change.\nNext: /goal:evidence --edit \"CHANGES\" if the check is wrong; otherwise /goal:retry"
		} else if state.Stage != goal.Stopped {
			message += "\nNext: /goal:retry"
		}
	default:
		message = fmt.Sprintf("Goal %s", state.Stage)
	}
	if state.Stage.Terminal() && strings.TrimSpace(state.Goal) != "" {
		message += "\nGoal: " + goal.Preview(state.Goal, 220)
	}
	if state.SessionID != "" && !state.Stage.Terminal() {
		message += fmt.Sprintf("\nSession: %s (%s)", state.SessionID, state.Adapter)
	}
	if stateFile != "" {
		message += "\nDetails: " + stateFile
	}
	if recent := state.Entries(goal.EntryLog, 3); len(recent) > 0 {
		message += "\nRecent:\n- " + strings.Join(recent, "\n- ")
	}
	return message
}

// previewGoalPlan shows the exact commands the harness will run, so approval
// covers the checks and not only their prose.
func previewGoalPlan(state goal.State) string {
	var evidence, plan []string
	for _, criterion := range state.Evidence.Criteria {
		evidence = append(evidence, "- "+goal.Preview(criterion, 240))
	}
	for _, item := range state.Evidence.Items {
		line := fmt.Sprintf("- **%s** `%s`", item.ID, goal.Preview(item.Check.Label(), 200))
		if item.Covers != "" {
			line += " — " + goal.Preview(item.Covers, 200)
		}
		if item.Check.WrittenBy != "" {
			line += " _(written by " + state.ShortTaskID(item.Check.WrittenBy) + ")_"
		}
		evidence = append(evidence, line)
	}
	ordinal := 0
	for _, task := range state.Tasks {
		if task.Status.Closed() {
			continue
		}
		shortID := state.ShortTaskID(task.ID)
		ordinal++
		if shortID == "" {
			shortID = fmt.Sprintf("T%d", ordinal)
		}
		kind := ""
		if task.Kind == goal.KindCheck {
			kind = " (check)"
		}
		line := fmt.Sprintf("%d. **%s — %s**%s", ordinal, shortID, task.Title, kind)
		if len(task.DependsOn) > 0 {
			deps := make([]string, len(task.DependsOn))
			for i, dependency := range task.DependsOn {
				deps[i] = state.ShortTaskID(dependency)
			}
			line += "  \n   After: `" + strings.Join(deps, "`, `") + "`"
		}
		plan = append(plan, line)
		for _, criterion := range task.Validation {
			plan = append(plan, "   - "+goal.Preview(criterion, 240))
		}
		if !task.Check.Empty() {
			plan = append(plan, "   - check: `"+goal.Preview(task.Check.Label(), 200)+"`")
		}
	}
	sections := []string{"### Evidence", strings.Join(evidence, "\n")}
	if summary := strings.TrimSpace(state.PlanSummary); summary != "" {
		sections = append(sections, "### Approach", summary)
	}
	sections = append(sections, "### Tasks", strings.Join(plan, "\n"))
	return strings.Join(sections, "\n\n")
}

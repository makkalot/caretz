package goal

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Journal views are capped when rendered; the event log keeps everything.
const (
	ViewLog         = 80
	ViewFeedback    = 20
	ViewDiscoveries = 40
)

// RenderMarkdown renders state.md, the human-readable view of the goal.
func RenderMarkdown(state State, limits Limits, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Goal %s\n%s\n\nstage: %s   pass: %d   plan: v%d   evidence: v%d   workers: %d   budget: %s\nmodels: %s\n\n# Evidence\n", state.ShortID(), state.Goal, state.Stage, state.Pass, state.PlanVersion, state.Evidence.Version, state.WorkerAttempts, BudgetLine(state, limits, now), state.Models.Line())
	for _, criterion := range state.Evidence.Criteria {
		fmt.Fprintf(&b, "- criterion: %s\n", criterion)
	}
	for _, item := range state.Evidence.Items {
		fmt.Fprintf(&b, "- %s `%s` [%s] covers: %s", item.ID, item.Check.Label(), item.Status, item.Covers)
		if item.Gaps != "" {
			fmt.Fprintf(&b, "; gaps: %s", item.Gaps)
		}
		if item.Check.WrittenBy != "" {
			fmt.Fprintf(&b, "; created by: %s", item.Check.WrittenBy)
		}
		b.WriteByte('\n')
		for _, rel := range sortedKeys(item.Files) {
			fmt.Fprintf(&b, "  - frozen: %s %s\n", rel, shortHash(item.Files[rel]))
		}
	}
	if len(state.References) > 0 {
		b.WriteString("\n# References\n")
		for _, reference := range state.References {
			status := reference.Source
			if reference.Content == "" {
				status = "not fetched: " + reference.Error
			}
			fmt.Fprintf(&b, "- %s (%s)\n", reference.URL, status)
			if title, _, _ := strings.Cut(reference.Content, "\n"); title != "" {
				fmt.Fprintf(&b, "  - %s\n", Preview(title, 200))
			}
		}
	}
	fmt.Fprintf(&b, "\n# Plan v%d\n%s\n", state.PlanVersion, DefaultString(state.PlanSummary, "No plan yet."))
	b.WriteString("\n# Tasks\n")
	for _, task := range state.Tasks {
		if !task.Status.Closed() {
			renderTask(&b, task)
		}
	}
	b.WriteString("\n# Task history\n")
	for _, task := range state.Tasks {
		if task.Status.Closed() {
			renderTask(&b, task)
		}
	}
	if state.Review != nil {
		b.WriteString("\n# Review\n")
		b.WriteString(FormatReview(*state.Review))
		if state.Verdict != nil {
			for _, decision := range state.Verdict.Decisions {
				fmt.Fprintf(&b, "- judge on F%d: %s", decision.Index, decision.Decision)
				if decision.Reason != "" {
					fmt.Fprintf(&b, " — %s", decision.Reason)
				}
				b.WriteByte('\n')
			}
			decision := "not done"
			if state.Verdict.Done {
				decision = "done"
			}
			fmt.Fprintf(&b, "- judge: %s — %s\n", decision, state.Verdict.Reason)
			for _, gap := range state.Verdict.Gaps {
				fmt.Fprintf(&b, "- judge gap: %s\n", gap)
			}
		}
	}
	if len(state.Failures) > 0 {
		b.WriteString("\n# Open failures\n")
		for _, failure := range state.Failures {
			fmt.Fprintf(&b, "- %s [%s] %s — %s\n", failure.Source, failure.Status, failure.Check, failure.Reason)
		}
	}
	for _, section := range []struct {
		title string
		kind  EntryKind
		limit int
	}{{"Feedback", EntryFeedback, ViewFeedback}, {"Discoveries", EntryDiscovery, ViewDiscoveries}, {"Log", EntryLog, ViewLog}} {
		fmt.Fprintf(&b, "\n# %s\n", section.title)
		for _, line := range state.Entries(section.kind, section.limit) {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	if state.Question != "" {
		fmt.Fprintf(&b, "\n# Pending question\n%s\n", state.Question)
	}
	if state.Answer != "" {
		fmt.Fprintf(&b, "\n# Latest user answer\n%s\n", state.Answer)
	}
	if state.ExitReason != "" {
		fmt.Fprintf(&b, "\n# Exit\n%s — %s\n", state.Stage, state.ExitReason)
	}
	return b.String()
}

func renderTask(b *strings.Builder, task Task) {
	kind := ""
	if task.Kind == KindCheck {
		kind = " {check task}"
	}
	mark := " "
	if task.Status == TaskDone {
		mark = "x"
	}
	fmt.Fprintf(b, "- [%s] %s %s%s (status: %s, attempts: %d)\n", mark, task.ID, task.Title, kind, task.Status, task.Attempts)
	fmt.Fprintf(b, "  - next action: %s\n", task.NextAction)
	if len(task.DependsOn) > 0 {
		fmt.Fprintf(b, "  - depends on: %s\n", strings.Join(task.DependsOn, ", "))
	}
	for _, criterion := range task.Validation {
		fmt.Fprintf(b, "  - criterion: %s\n", criterion)
	}
	if !task.Check.Empty() {
		fmt.Fprintf(b, "  - check: `%s`", task.Check.Label())
		if task.CheckStatus != "" {
			fmt.Fprintf(b, " [%s]", task.CheckStatus)
		}
		b.WriteByte('\n')
	}
	if task.LastOutcome != "" {
		fmt.Fprintf(b, "  - last outcome: %s\n", task.LastOutcome)
	}
	if task.LastCheck != "" {
		fmt.Fprintf(b, "  - last check: %s\n", task.LastCheck)
	}
	for _, check := range task.CheckHistory {
		result := "fail"
		if check.Passed {
			result = "pass"
		}
		fmt.Fprintf(b, "  - attempt %d: %s — %s\n", check.Attempt, result, check.Reason)
	}
}

// TaskLog renders tasks/<id>.log: the worker outcome and every check.
func TaskLog(task Task) string {
	var b strings.Builder
	if task.LastOutcome != "" {
		fmt.Fprintf(&b, "executor: %s\n", task.LastOutcome)
	}
	for _, check := range task.CheckHistory {
		status := "failed"
		if check.Passed {
			status = "passed"
		}
		fmt.Fprintf(&b, "attempt %d %s: %s\n", check.Attempt, status, check.Reason)
	}
	if task.LastCheckOutput != "" {
		fmt.Fprintf(&b, "last check output:\n%s\n", task.LastCheckOutput)
	}
	return b.String()
}

func FormatEvidence(evidence Evidence) string {
	var lines []string
	for _, item := range evidence.Items {
		lines = append(lines, fmt.Sprintf("%s: %s; covers: %s; gaps: %s", item.ID, item.Check.Label(), item.Covers, item.Gaps))
	}
	for _, criterion := range evidence.Criteria {
		lines = append(lines, "Criterion: "+criterion)
	}
	return strings.Join(lines, "\n")
}

// FormatReview renders the reviewer's report.
func FormatReview(review Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Summary: %s\nCriteria:\n", DefaultString(review.Summary, "(none)"))
	for _, item := range review.Criteria {
		fmt.Fprintf(&b, "- [%s] %s", item.Status, item.Criterion)
		if len(item.Evidence) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(item.Evidence, ", "))
		}
		b.WriteByte('\n')
	}
	b.WriteString("Findings:\n")
	if len(review.Findings) == 0 {
		b.WriteString("- none\n")
	}
	for i, finding := range review.Findings {
		fmt.Fprintf(&b, "- F%d [%s] %s: %s", i+1, finding.Severity, finding.Location(), finding.Problem)
		if finding.Fix != "" {
			fmt.Fprintf(&b, " — fix: %s", finding.Fix)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// ReviewNotes lists the non-blocking notes of a finished goal: findings the
// judge dismissed and minor findings.
func ReviewNotes(review *Review, verdict *Verdict) []string {
	if review == nil {
		return nil
	}
	var notes []string
	for i, finding := range review.Findings {
		var decision FindingDecision
		if verdict != nil {
			decision, _ = verdict.Decision(i + 1)
		}
		switch {
		case decision.Decision == "dismiss":
			notes = append(notes, fmt.Sprintf("F%d [%s, dismissed: %s] %s: %s", i+1, finding.Severity, Preview(decision.Reason, 120), finding.Location(), Preview(finding.Problem, 160)))
		case finding.Severity == "minor":
			notes = append(notes, fmt.Sprintf("F%d [minor] %s: %s", i+1, finding.Location(), Preview(finding.Problem, 160)))
		}
	}
	return notes
}

// ReviewProgress shows what the reviewer concluded: each criterion and each
// finding, one line apiece, marked for the progress renderer.
func ReviewProgress(review Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "goal review: %d findings (%d blocking) — %s", len(review.Findings), review.BlockingFindings(), Preview(review.Summary, 160))
	for _, item := range review.Criteria {
		mark := "?"
		switch item.Status {
		case "met":
			mark = "✓"
		case "unmet":
			mark = "✗"
		}
		fmt.Fprintf(&b, "\n  %s %s: %s", mark, item.Status, Preview(item.Criterion, 140))
	}
	for i, finding := range review.Findings {
		mark := "•"
		if finding.Blocking() {
			mark = "✗"
		}
		fmt.Fprintf(&b, "\n  %s F%d %s %s — %s", mark, i+1, finding.Severity, finding.Location(), Preview(finding.Problem, 200))
	}
	return b.String()
}

// VerdictProgress shows the judge's conclusion with its decision on each
// review finding and any further gaps.
func VerdictProgress(summary string, review Review, verdict Verdict) string {
	var b strings.Builder
	b.WriteString("goal judge: " + Preview(summary, 180))
	for _, decision := range verdict.Decisions {
		if decision.Index < 1 || decision.Index > len(review.Findings) {
			continue
		}
		finding := review.Findings[decision.Index-1]
		if decision.Decision == "accept" {
			mark := "•"
			if finding.Blocking() {
				mark = "✗"
			}
			fmt.Fprintf(&b, "\n  %s F%d accepted: %s", mark, decision.Index, Preview(finding.Problem, 160))
			continue
		}
		fmt.Fprintf(&b, "\n  ✓ F%d dismissed: %s", decision.Index, Preview(decision.Reason, 160))
	}
	if !verdict.Done {
		for _, gap := range verdict.Gaps {
			fmt.Fprintf(&b, "\n  ✗ gap: %s", Preview(gap, 200))
		}
	}
	return b.String()
}

// BudgetLine shows what the goal spent of its budget.
func BudgetLine(state State, limits Limits, now time.Time) string {
	tokens := fmt.Sprintf("%s tokens", humanCount(state.TokensUsed))
	if limits.MaxTokens > 0 {
		tokens = fmt.Sprintf("%s/%s tokens", humanCount(state.TokensUsed), humanCount(limits.MaxTokens))
	}
	minutes := fmt.Sprintf("%d min", int64(state.Active(now)/time.Minute))
	if limits.MaxActiveTime > 0 {
		minutes = fmt.Sprintf("%d/%d min", int64(state.Active(now)/time.Minute), int64(limits.MaxActiveTime/time.Minute))
	}
	return tokens + ", " + minutes
}

func humanCount(value int64) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	}
	return fmt.Sprintf("%d", value)
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

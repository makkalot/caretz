package output

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/makkalot/caretz/lib/eventing"
)

// goalReplayAttempt is one worker attempt at a task and its harness check.
type goalReplayAttempt struct {
	number  int
	note    string
	check   string
	checked bool
	passed  bool
}

// goalReplayTask folds consecutive events of one task into a single block.
type goalReplayTask struct {
	id, title string
	attempts  []goalReplayAttempt
}

func (t *goalReplayTask) last() *goalReplayAttempt {
	if len(t.attempts) == 0 {
		t.attempts = append(t.attempts, goalReplayAttempt{number: 1})
	}
	return &t.attempts[len(t.attempts)-1]
}

// ReplayGoals renders the goal timeline grouped into setup and runs, with
// each task's attempts folded into one block and outcomes colored.
func (o *CLIOutput) ReplayGoals(events []eventing.GoalEvent) {
	if len(events) == 0 {
		return
	}
	w := os.Stderr
	s := o.stderrStyle
	first := events[0]
	_, _ = fmt.Fprintf(w, "\n%s %s\n", s.Heading("Restored goal"), s.Muted(strings.TrimPrefix(first.GoalID, "goal-")))
	if goal := goalReplayGoal(events); goal != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", truncateLine(goal, 200))
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", s.Heading("Setup"))

	var task *goalReplayTask
	flush := func() {
		if task != nil {
			o.renderGoalReplayTask(w, *task)
			task = nil
		}
	}
	runs := 0
	for _, event := range events {
		if event.Stage == "task" || event.Stage == "task-check" {
			if task == nil || task.id != event.TaskID {
				flush()
				task = &goalReplayTask{id: event.TaskID, title: event.TaskTitle}
			}
			addGoalReplayTaskEvent(task, event)
			continue
		}
		flush()
		if event.Type == eventing.GoalRunStarted {
			runs++
			_, _ = fmt.Fprintf(w, "\n%s\n", s.Heading(fmt.Sprintf("Run %d", runs)))
			continue
		}
		if event.Stage == "created" {
			continue
		}
		mark, text := o.goalReplayMark(event), shortGoalIDs(event.GoalID, event.Summary)
		if text == "" {
			text = event.Status
		}
		_, _ = fmt.Fprintf(w, "  %s %s\n", mark, s.Status(event.Status, truncateLine(text, 220)))
	}
	flush()

	latest := events[len(events)-1]
	_, _ = fmt.Fprintf(w, "\n%s %s\n", s.Muted("Status:"), s.Bold(s.Status(latest.Status, latest.Status)))
	if latest.NextAction != "" {
		_, _ = fmt.Fprintf(w, "%s %s\n", s.Muted("Next:"), s.Bold(shortGoalIDs(latest.GoalID, latest.NextAction)))
	}
}

// shortGoalIDs turns task IDs such as goal-bce2f5f4-T6 into T6.
func shortGoalIDs(goalID, text string) string {
	text = strings.TrimSpace(text)
	if goalID == "" {
		return text
	}
	return strings.ReplaceAll(text, goalID+"-", "")
}

func goalReplayGoal(events []eventing.GoalEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if goal := strings.TrimSpace(events[i].Goal); goal != "" {
			return goal
		}
	}
	return ""
}

func addGoalReplayTaskEvent(task *goalReplayTask, event eventing.GoalEvent) {
	if task.title == "" {
		task.title = event.TaskTitle
	}
	summary := shortGoalIDs(event.GoalID, event.Summary)
	if event.Stage == "task-check" {
		attempt := task.last()
		attempt.checked, attempt.check = true, summary
		// Task status is recorded on newer events; older ones only carry the
		// check reason, where a pass reads "exit 0".
		attempt.passed = event.TaskStatus == "done" || (event.TaskStatus == "" && strings.HasPrefix(summary, "exit 0"))
		return
	}
	var number int
	if _, err := fmt.Sscanf(summary, "Attempt %d started", &number); err == nil {
		task.attempts = append(task.attempts, goalReplayAttempt{number: number})
		return
	}
	task.last().note = summary
}

func (o *CLIOutput) renderGoalReplayTask(w io.Writer, task goalReplayTask) {
	s := o.stderrStyle
	last := task.attempts[len(task.attempts)-1]
	mark := s.Muted(GlyphEllipsis)
	switch {
	case last.checked && last.passed:
		mark = s.Mark(GlyphSuccess)
	case last.checked:
		mark = s.Mark(GlyphFailure)
	}
	id := task.id
	if index := strings.LastIndex(id, "-"); index >= 0 {
		id = id[index+1:]
	}
	header := fmt.Sprintf("  %s %s %s", mark, s.Bold(id), truncateLine(task.title, 100))
	if len(task.attempts) > 1 {
		header += s.Muted(fmt.Sprintf("  (%d attempts)", len(task.attempts)))
	}
	_, _ = fmt.Fprintln(w, header)
	// Show every attempt only when one failed; a clean pass needs no detail.
	if len(task.attempts) > 1 || (last.checked && !last.passed) {
		for _, attempt := range task.attempts {
			result := s.Muted("not checked")
			switch {
			case attempt.checked && attempt.passed:
				result = s.Mark(GlyphSuccess) + " " + truncateLine(attempt.check, 160)
			case attempt.checked:
				result = s.Error(GlyphFailure + " " + truncateLine(attempt.check, 160))
			}
			_, _ = fmt.Fprintf(w, "      %s %s\n", s.Muted(fmt.Sprintf("#%d", attempt.number)), result)
		}
	}
	if last.note != "" {
		_, _ = fmt.Fprintf(w, "      %s\n", s.Muted(truncateLine(last.note, 160)))
	}
}

func (o *CLIOutput) goalReplayMark(event eventing.GoalEvent) string {
	s := o.stderrStyle
	switch glyph := StatusGlyph(event.Status); glyph {
	case GlyphSuccess, GlyphFailure, GlyphQuestion, GlyphStopped:
		return s.Mark(glyph)
	}
	switch event.Type {
	case eventing.GoalReplanCreated, eventing.GoalEscalated:
		return s.Mark(GlyphRetry)
	case eventing.GoalVerificationCompleted:
		if strings.HasPrefix(event.Summary, "Passed") {
			return s.Mark(GlyphSuccess)
		}
		return s.Mark(GlyphFailure)
	}
	return s.Muted(GlyphBullet)
}

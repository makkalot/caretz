package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/lib/eventing"
)

// questionResolution answers an AskUser request in the TUI. Escape dismisses
// the questions and lets the turn continue; Ctrl-C stops the turn.
func questionResolution(terminal *ansi.Terminal, request eventing.PermissionRequest) (eventing.PermissionResolution, error) {
	questions, err := eventing.DecodeQuestions(request)
	if err != nil {
		return eventing.PermissionResolution{}, err
	}
	answers, outcome, err := readQuestionAnswers(terminal, questions, request.ExpiresAt)
	if err != nil {
		return eventing.PermissionResolution{}, err
	}
	resolution := eventing.PermissionResolution{RequestID: request.RequestID, Resolver: "tui"}
	switch outcome {
	case questionsAnswered:
		resolution.Decision = eventing.PermissionDecisionAllow
		resolution.Metadata = eventing.QuestionAnswersMetadata(answers)
	case questionsDismissed:
		resolution.Decision = eventing.PermissionDecisionDeny
	case questionsTimedOut:
		resolution.Decision = eventing.PermissionDecisionDeny
		resolution.Resolver = "permission-timeout"
	case questionsCancelled:
		return eventing.PermissionResolution{}, context.Canceled
	}
	return resolution, nil
}

type questionOutcome int

const (
	questionsAnswered questionOutcome = iota
	questionsDismissed
	questionsTimedOut
	questionsCancelled
)

// questionPicker is the state of the vertical picker: one tab per question,
// one row per option plus a free-text "Other" row.
type questionPicker struct {
	questions []eventing.Question
	states    []questionState
	current   int
	editing   bool // typing into the current question's Other row
}

type questionState struct {
	cursor   int
	chosen   map[int]bool // multi-select picks; single-select uses one entry
	other    string
	answered bool
}

func newQuestionPicker(questions []eventing.Question) *questionPicker {
	p := &questionPicker{questions: questions, states: make([]questionState, len(questions))}
	for i, q := range questions {
		p.states[i].chosen = map[int]bool{}
		for j, option := range q.Options {
			if option.Recommended {
				p.states[i].cursor = j
			}
		}
	}
	return p
}

func (p *questionPicker) otherRow() int { return len(p.questions[p.current].Options) }

// answers returns one answer per question in eventing order.
func (p *questionPicker) answers() []eventing.QuestionAnswer {
	answers := make([]eventing.QuestionAnswer, len(p.questions))
	for i, q := range p.questions {
		state := p.states[i]
		for j, option := range q.Options {
			if state.chosen[j] {
				answers[i].Selected = append(answers[i].Selected, option.Label)
			}
		}
		if state.chosen[len(q.Options)] {
			answers[i].Other = state.other
		}
	}
	return answers
}

// commit marks the current question answered and moves to the next
// unanswered one. It reports whether every question is answered.
func (p *questionPicker) commit() bool {
	p.states[p.current].answered = true
	for offset := 1; offset <= len(p.questions); offset++ {
		next := (p.current + offset) % len(p.questions)
		if !p.states[next].answered {
			p.current = next
			return false
		}
	}
	return true
}

// choose acts on the cursor row as Enter would. It reports whether every
// question is now answered.
func (p *questionPicker) choose() bool {
	q := p.questions[p.current]
	state := &p.states[p.current]
	if state.cursor == p.otherRow() && !p.editing {
		p.editing = true
		return false
	}
	if p.editing {
		p.editing = false
		state.other = strings.TrimSpace(state.other)
		if state.other == "" {
			delete(state.chosen, p.otherRow())
			return false
		}
	}
	if q.MultiSelect {
		if p.states[p.current].cursor == p.otherRow() {
			state.chosen[p.otherRow()] = true
			return false
		}
		if len(state.chosen) == 0 {
			state.chosen[state.cursor] = true
		}
		return p.commit()
	}
	state.chosen = map[int]bool{state.cursor: true}
	return p.commit()
}

// toggle flips a multi-select row; on a single-select question it chooses.
func (p *questionPicker) toggle() bool {
	state := &p.states[p.current]
	if !p.questions[p.current].MultiSelect || state.cursor == p.otherRow() {
		return p.choose()
	}
	if state.chosen[state.cursor] {
		delete(state.chosen, state.cursor)
	} else {
		state.chosen[state.cursor] = true
	}
	return false
}

func (p *questionPicker) move(delta int) {
	rows := p.otherRow() + 1
	state := &p.states[p.current]
	state.cursor = (state.cursor + delta + rows) % rows
}

func (p *questionPicker) switchQuestion(delta int) {
	n := len(p.questions)
	p.current = (p.current + delta + n) % n
}

// handle applies one key and reports the outcome once the picker is done.
func (p *questionPicker) handle(event ansi.Event) (questionOutcome, bool) {
	if p.editing {
		state := &p.states[p.current]
		switch event.Kind {
		case ansi.EventRune:
			state.other += string(event.Rune)
		case ansi.EventPaste:
			state.other += strings.Join(strings.Fields(ansi.Strip(event.Text)), " ")
		case ansi.EventBackspace:
			if r := []rune(state.other); len(r) > 0 {
				state.other = string(r[:len(r)-1])
			}
		case ansi.EventCtrlU:
			state.other = ""
		case ansi.EventEnter:
			if p.choose() {
				return questionsAnswered, true
			}
		case ansi.EventEscape:
			p.editing = false
		case ansi.EventCtrlC:
			return questionsCancelled, true
		}
		return 0, false
	}
	switch event.Kind {
	case ansi.EventUp:
		p.move(-1)
	case ansi.EventDown:
		p.move(1)
	case ansi.EventLeft, ansi.EventShiftTab:
		p.switchQuestion(-1)
	case ansi.EventRight, ansi.EventTab:
		p.switchQuestion(1)
	case ansi.EventEnter:
		if p.choose() {
			return questionsAnswered, true
		}
	case ansi.EventEscape:
		return questionsDismissed, true
	case ansi.EventCtrlC:
		return questionsCancelled, true
	case ansi.EventRune:
		if event.Rune == ' ' {
			if p.toggle() {
				return questionsAnswered, true
			}
			break
		}
		if row := int(event.Rune - '1'); row >= 0 && row <= p.otherRow() {
			p.states[p.current].cursor = row
			if p.toggle() {
				return questionsAnswered, true
			}
		}
	}
	return 0, false
}

// lines renders the current question to fit width cells per line.
func (p *questionPicker) lines(style output.Style, width int) []string {
	q := p.questions[p.current]
	state := p.states[p.current]
	var lines []string
	if len(p.questions) > 1 {
		tabs := make([]string, len(p.questions))
		for i, other := range p.questions {
			label := " " + questionText(other.Header) + " "
			if p.states[i].answered {
				label = " " + output.GlyphSuccess + questionText(other.Header) + " "
			}
			if i == p.current {
				tabs[i] = style.Bold(style.Prompt("[" + label + "]"))
			} else {
				tabs[i] = style.Muted("[" + label + "]")
			}
		}
		lines = append(lines, fitCells(style.Bold(style.Warning(output.GlyphQuestion))+" "+strings.Join(tabs, " "), width))
	} else {
		lines = append(lines, fitCells(style.Bold(style.Warning(output.GlyphQuestion))+" "+style.Bold(questionText(q.Header)), width))
	}
	for _, line := range wrapCells(questionText(q.Question), width-2) {
		lines = append(lines, "  "+style.Bold(line))
	}
	for i := 0; i <= len(q.Options); i++ {
		marker := "  "
		if i == state.cursor {
			marker = style.Prompt("› ")
		}
		box := ""
		if q.MultiSelect {
			box = "[ ] "
			if state.chosen[i] {
				box = "[x] "
			}
		}
		prefix := fmt.Sprintf("%s%d. ", box, i+1)
		var rows []string
		var description string
		if i < len(q.Options) {
			label := questionText(q.Options[i].Label)
			if q.Options[i].Recommended {
				label += " (recommended)"
			}
			description = questionText(q.Options[i].Description)
			rows = []string{fitCells(prefix+label, width-4)}
		} else {
			// The Other answer wraps under its label so long answers stay
			// readable; while typing, the end of the answer stays in view.
			editing := p.editing && i == state.cursor
			label := "Other" + output.GlyphEllipsis
			if editing {
				label = "Other: " + state.other + "▌"
			} else if state.other != "" {
				label = "Other: " + state.other
			}
			indent := strings.Repeat(" ", ansi.VisibleWidth(prefix))
			for j, line := range wrapInput(label, width-4-len(indent), maxOtherLines, editing) {
				if j == 0 {
					rows = append(rows, prefix+line)
				} else {
					rows = append(rows, indent+line)
				}
			}
		}
		for j, row := range rows {
			if i == state.cursor {
				row = style.Bold(style.Prompt(row))
			} else if !q.MultiSelect && state.answered && state.chosen[i] {
				row = style.Success(row)
			}
			if j == 0 {
				lines = append(lines, "  "+marker+row)
			} else {
				lines = append(lines, "    "+row)
			}
		}
		for _, line := range wrapCells(description, width-9) {
			lines = append(lines, "         "+style.Muted(line))
		}
	}
	hint := "↑/↓ move · enter select · esc skip"
	if q.MultiSelect {
		hint = "↑/↓ move · space toggle · enter confirm · esc skip"
	}
	if len(p.questions) > 1 {
		hint += " · ←/→ question"
	}
	if p.editing {
		hint = "type your answer · enter save · esc back"
	}
	lines = append(lines, "  "+style.Muted(fitCells(hint, width-2)))
	return lines
}

// summary is what stays in the transcript once the picker closes.
func (p *questionPicker) summary(style output.Style, outcome questionOutcome, width int) []string {
	switch outcome {
	case questionsDismissed:
		return []string{style.Muted(output.GlyphQuestion + " questions skipped")}
	case questionsTimedOut:
		return []string{style.Warning(output.GlyphQuestion + " no answer in time; the model will use its recommendations")}
	case questionsCancelled:
		return nil
	}
	var lines []string
	for i, answer := range p.answers() {
		parts := append([]string(nil), answer.Selected...)
		if answer.Other != "" {
			parts = append(parts, answer.Other)
		}
		text := questionText(p.questions[i].Header) + " " + output.GlyphArrow + " " + strings.Join(parts, ", ")
		for j, line := range wrapCells(text, width-2) {
			if j == 0 {
				lines = append(lines, style.Muted(output.GlyphQuestion+" ")+line)
			} else {
				lines = append(lines, "  "+line)
			}
		}
	}
	return lines
}

// readQuestionAnswers runs the picker until the user answers every question,
// skips them, stops the turn or expiresAt passes.
func readQuestionAnswers(terminal *ansi.Terminal, questions []eventing.Question, expiresAt time.Time) ([]eventing.QuestionAnswer, questionOutcome, error) {
	out := terminal.Writer()
	style := output.NewStyle(out)
	picker := newQuestionPicker(questions)
	drawn := 0
	width := func() int { return max(terminal.Size().Width-1, 20) }
	draw := func(lines []string) {
		// Each line fits the width, so moving up drawn lines reaches the
		// top of the previous frame.
		if drawn > 0 {
			_, _ = fmt.Fprintf(out, "\r\x1b[%dA", drawn)
		}
		_, _ = fmt.Fprint(out, "\x1b[J")
		for _, line := range lines {
			_, _ = fmt.Fprintln(out, line)
		}
		drawn = len(lines)
	}
	_, _ = fmt.Fprintln(out)
	draw(picker.lines(style, width()))
	finish := func(outcome questionOutcome) ([]eventing.QuestionAnswer, questionOutcome, error) {
		draw(picker.summary(style, outcome, width()))
		if outcome == questionsAnswered {
			return picker.answers(), outcome, nil
		}
		return nil, outcome, nil
	}
	for {
		wait := 50 * time.Millisecond
		if !expiresAt.IsZero() {
			remaining := time.Until(expiresAt)
			if remaining <= 0 {
				return finish(questionsTimedOut)
			}
			wait = min(wait, remaining)
		}
		event, ready, err := terminal.ReadEventWithin(wait)
		if err != nil {
			return nil, 0, err
		}
		if !ready {
			continue
		}
		if outcome, done := picker.handle(event); done {
			return finish(outcome)
		}
		draw(picker.lines(style, width()))
	}
}

// questionText keeps model-written text from redrawing the terminal.
func questionText(text string) string {
	return strings.Join(strings.Fields(ansi.Strip(text)), " ")
}

// fitCells cuts value to width terminal cells, ending it with an ellipsis
// when cut. Styled text is measured by its visible width.
func fitCells(value string, width int) string {
	if ansi.VisibleWidth(value) <= width {
		return value
	}
	plain := ansi.Strip(value)
	var b strings.Builder
	used := 0
	for _, r := range plain {
		w := ansi.CellWidth(r)
		if used+w > width-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + output.GlyphEllipsis
}

// maxOtherLines caps how many lines the Other answer takes in the picker, so
// the redrawn frame stays shorter than the terminal.
const maxOtherLines = 6

// wrapInput breaks typed text into lines of at most width cells, preferring
// to break after a space but keeping every character so the cursor sits where
// the user typed. Beyond maxLines it keeps the tail when tail is set and the
// head otherwise, marking the cut with an ellipsis.
func wrapInput(text string, width, maxLines int, tail bool) []string {
	width = max(width, 10)
	var lines []string
	var line []rune
	used := 0
	for _, r := range text {
		w := ansi.CellWidth(r)
		if used+w > width {
			cut := len(line)
			if space := strings.LastIndexByte(string(line), ' '); space >= 0 {
				cut = len([]rune(string(line)[:space+1]))
			}
			lines = append(lines, string(line[:cut]))
			line = append([]rune(nil), line[cut:]...)
			used = 0
			for _, rest := range line {
				used += ansi.CellWidth(rest)
			}
		}
		line = append(line, r)
		used += w
	}
	lines = append(lines, string(line))
	if len(lines) <= maxLines {
		return lines
	}
	if tail {
		lines = lines[len(lines)-maxLines:]
		lines[0] = output.GlyphEllipsis + string([]rune(lines[0])[1:])
		return lines
	}
	lines = lines[:maxLines]
	last := []rune(lines[maxLines-1])
	lines[maxLines-1] = string(last[:len(last)-1]) + output.GlyphEllipsis
	return lines
}

// wrapCells breaks text at spaces into lines of at most width cells.
func wrapCells(text string, width int) []string {
	if text == "" {
		return nil
	}
	width = max(width, 10)
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if ansi.VisibleWidth(candidate) <= width {
			line = candidate
			continue
		}
		if line != "" {
			lines = append(lines, line)
		}
		line = fitCells(word, width)
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

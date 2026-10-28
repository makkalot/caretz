package app

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/lib/llm"
)

// ErrInterrupted is returned by ReadLine when the user presses Ctrl+C.
var ErrInterrupted = errors.New("interrupted")

const pastedTextPromptPrefix = "Pasted text:\n"

// inputEditor is the application-specific prompt component. Terminal input,
// layout, and repainting are delegated to internal/ansi.
type inputEditor struct {
	terminal *ansi.Terminal
	runtime  *ansi.Runtime
	prompt   string

	buffer                []rune
	cursor                int
	preferredCursorOffset int
	lastSize              ansi.Size
	style                 output.Style

	completion           pathAutocomplete
	slashCompletion      slashAutocomplete
	slashSuggestions     slashSuggestionMenu
	slashCommandRegistry *slashcmd.Registry
	history              commandHistory

	// planMode reports the session mode shown in the footer, and
	// togglePlanMode switches it on Shift+Tab. Either may be nil.
	planMode       func() bool
	togglePlanMode func()

	// status supplies the footer's session details. It is sampled once per
	// prompt because estimating context tokens walks the whole context.
	status         func() editorStatus
	statusSnapshot editorStatus
}

// editorStatus is the session state shown in the footer under the prompt.
type editorStatus struct {
	Model         string
	Reasoning     string
	ContextTokens int
	ContextWindow int
	// Usage is the most recent response's usage; zero until the first reply.
	Usage llm.Usage
}

func newInputEditor(in io.Reader, out io.Writer, prompt string, style output.Style, root string, agentsDirs []string, slashCommandRegistry *slashcmd.Registry) *inputEditor {
	terminal := ansi.NewTerminal(in, out)
	return &inputEditor{
		terminal:              terminal,
		runtime:               ansi.NewRuntime(terminal),
		prompt:                prompt,
		style:                 style,
		preferredCursorOffset: -1,
		lastSize:              ansi.Size{Width: 80, Height: 24},
		completion:            newPathAutocomplete(root, agentsDirs),
		slashCompletion:       slashAutocomplete{registry: slashCommandRegistry},
		slashCommandRegistry:  slashCommandRegistry,
		history:               newCommandHistory(),
	}
}

// ReadLine runs the editor component until it submits or exits.
func (e *inputEditor) ReadLine() (string, error) {
	return e.readLine(nil)
}

func (e *inputEditor) readLine(wake <-chan struct{}) (string, error) {
	e.refreshCompletion()
	if e.status != nil {
		e.statusSnapshot = e.status()
	}
	result := e.runtime.RunWithWake(e, wake)
	if result.External {
		return "", errExternalWake
	}
	if result.Err != nil {
		if errors.Is(result.Err, ErrInterrupted) {
			_, _ = fmt.Fprint(e.terminal.Writer(), "^C\n")
		}
		return "", result.Err
	}
	if result.Exit {
		return "", io.EOF
	}
	return result.Submit, nil
}

// Update applies one normalized terminal event to editor state.
func (e *inputEditor) Update(event ansi.Event) ansi.Result {
	switch event.Kind {
	case ansi.EventCtrlA:
		if e.moveCursorToLineStart() {
			e.afterEdit()
			return ansi.Result{Redraw: true}
		}
	case ansi.EventCtrlC:
		return ansi.Result{Err: ErrInterrupted}
	case ansi.EventCtrlD:
		if len(e.buffer) == 0 {
			return ansi.Result{Exit: true}
		}
	case ansi.EventCtrlE:
		if e.moveCursorToLineEnd() {
			e.afterEdit()
			return ansi.Result{Redraw: true}
		}
	case ansi.EventEnter:
		if e.acceptSlashSuggestion() {
			e.history.edited(string(e.buffer))
			return ansi.Result{Redraw: true}
		}
		if e.completion.visible && e.acceptCompletion() {
			e.history.edited(string(e.buffer))
			return ansi.Result{Redraw: true}
		}
		line := string(e.buffer)
		if isRegisteredSlashCommand(line, e.slashCommandRegistry) {
			e.history.add(line)
		} else {
			e.history.index = -1
			e.history.draft = ""
		}
		e.buffer = nil
		e.cursor = 0
		e.preferredCursorOffset = -1
		return ansi.Result{Submitted: true, Submit: line}
	case ansi.EventPaste:
		text := event.Text
		if e.shouldPrefixPastedText(text) {
			text = pastedTextPromptPrefix + text
		}
		e.insertText(text)
		e.history.edited(string(e.buffer))
		e.refreshCompletion()
		return ansi.Result{Redraw: true}
	case ansi.EventEscape:
		if e.slashSuggestions.active {
			e.slashSuggestions.dismiss()
			return ansi.Result{Redraw: true}
		}
		if e.completion.active {
			e.completion.dismiss()
			return ansi.Result{Redraw: true}
		}
	case ansi.EventTab:
		before := string(e.buffer)
		if !e.completeCurrentSlash() {
			e.completeCurrentMention()
		}
		if string(e.buffer) != before {
			e.history.edited(string(e.buffer))
		}
		e.preferredCursorOffset = -1
		return ansi.Result{Redraw: true}
	case ansi.EventShiftTab:
		if e.togglePlanMode != nil {
			e.togglePlanMode()
			return ansi.Result{Redraw: true}
		}
	case ansi.EventUp:
		if e.slashSuggestions.move(-1) {
			return ansi.Result{Redraw: true}
		}
		if e.completion.move(-1) {
			return ansi.Result{Redraw: true}
		}
		if e.moveCursorVertical(-1) {
			e.refreshCompletion()
			return ansi.Result{Redraw: true}
		}
		if value, ok := e.history.previous(string(e.buffer)); ok {
			e.replaceBuffer(value)
			return ansi.Result{Redraw: true}
		}
	case ansi.EventDown:
		if e.slashSuggestions.move(1) {
			return ansi.Result{Redraw: true}
		}
		if e.completion.move(1) {
			return ansi.Result{Redraw: true}
		}
		if e.moveCursorVertical(1) {
			e.refreshCompletion()
			return ansi.Result{Redraw: true}
		}
		if value, ok := e.history.next(); ok {
			e.replaceBuffer(value)
			return ansi.Result{Redraw: true}
		}
	case ansi.EventLeft:
		if e.cursor > 0 {
			e.cursor--
			e.preferredCursorOffset = -1
			e.refreshCompletion()
			return ansi.Result{Redraw: true}
		}
	case ansi.EventRight:
		if e.cursor < len(e.buffer) {
			e.cursor++
			e.preferredCursorOffset = -1
			e.refreshCompletion()
			return ansi.Result{Redraw: true}
		}
	case ansi.EventBackspace:
		if e.cursor > 0 {
			e.buffer = append(e.buffer[:e.cursor-1], e.buffer[e.cursor:]...)
			e.cursor--
			e.preferredCursorOffset = -1
			e.refreshCompletion()
			e.history.edited(string(e.buffer))
			return ansi.Result{Redraw: true}
		}
	case ansi.EventDelete:
		if e.cursor < len(e.buffer) {
			e.buffer = append(e.buffer[:e.cursor], e.buffer[e.cursor+1:]...)
			e.preferredCursorOffset = -1
			e.refreshCompletion()
			e.history.edited(string(e.buffer))
			return ansi.Result{Redraw: true}
		}
	case ansi.EventCtrlL:
		return ansi.Result{ClearScreen: true}
	case ansi.EventCtrlW:
		if e.deleteWordBackward() {
			e.history.edited(string(e.buffer))
			e.afterEdit()
			return ansi.Result{Redraw: true}
		}
	case ansi.EventRune:
		e.insertRune(event.Rune)
		e.history.edited(string(e.buffer))
		return ansi.Result{Redraw: true}
	}
	return ansi.Result{}
}

// shouldPrefixPastedText keeps multiline terminal output beginning with an
// absolute path from being mistaken for a slash command. Deliberately pasted
// registered commands retain their command behavior.
func (e *inputEditor) shouldPrefixPastedText(text string) bool {
	if len(e.buffer) != 0 || e.cursor != 0 || !strings.Contains(text, "\n") {
		return false
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return false
	}
	firstLine, _, _ := strings.Cut(trimmed, "\n")
	return !isRegisteredSlashCommand(firstLine, e.slashCommandRegistry)
}

func (e *inputEditor) replaceBuffer(value string) {
	e.buffer = []rune(value)
	e.cursor = len(e.buffer)
	e.preferredCursorOffset = -1
	e.completion.dismiss()
	e.refreshCompletion()
}

func (e *inputEditor) moveCursorToLineStart() bool {
	start := e.cursor
	for start > 0 && e.buffer[start-1] != '\n' {
		start--
	}
	if start == e.cursor {
		return false
	}
	e.cursor = start
	return true
}

func (e *inputEditor) moveCursorToLineEnd() bool {
	end := e.cursor
	for end < len(e.buffer) && e.buffer[end] != '\n' {
		end++
	}
	if end == e.cursor {
		return false
	}
	e.cursor = end
	return true
}

func (e *inputEditor) deleteWordBackward() bool {
	start := e.cursor
	for start > 0 && e.buffer[start-1] != '\n' && unicode.IsSpace(e.buffer[start-1]) {
		start--
	}
	for start > 0 && e.buffer[start-1] != '\n' && !unicode.IsSpace(e.buffer[start-1]) {
		start--
	}
	if start == e.cursor {
		return false
	}
	e.buffer = append(e.buffer[:start], e.buffer[e.cursor:]...)
	e.cursor = start
	return true
}

func (e *inputEditor) afterEdit() {
	e.preferredCursorOffset = -1
	e.refreshCompletion()
}

// View lays out the complete prompt frame for the current terminal size.
func (e *inputEditor) View(size ansi.Size) ansi.Frame {
	if size.Width <= 0 {
		size.Width = 80
	}
	e.lastSize = size
	layout := ansi.LayoutText(e.buffer, e.cursor, e.layoutOptions(size.Width))
	cursor := layout.Cursor
	// The rules and footer need three rows beyond the input; on a terminal
	// too short for them, the input keeps the space.
	framed := size.Height <= 0 || len(layout.Rows)+3 <= size.Height
	rows := make([]string, 0, len(layout.Rows)+3)
	rule := e.style.Rule(size.Width - 1)
	if framed {
		rows = append(rows, rule)
		cursor.Row++
	}
	for _, row := range layout.Rows {
		rows = append(rows, row.Text)
	}
	if framed {
		rows = append(rows, rule, e.footer(size.Width))
	}
	rows = e.appendSlashSuggestions(rows, size.Height)
	rows = e.appendMentionSuggestions(rows, size.Height)
	return ansi.Frame{Rows: rows, Cursor: cursor}
}

func (e *inputEditor) insertText(text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	runes := []rune(text)
	updated := make([]rune, 0, len(e.buffer)+len(runes))
	updated = append(updated, e.buffer[:e.cursor]...)
	updated = append(updated, runes...)
	updated = append(updated, e.buffer[e.cursor:]...)
	e.buffer = updated
	e.cursor += len(runes)
	e.preferredCursorOffset = -1
}

func (e *inputEditor) insertRune(r rune) {
	e.buffer = append(e.buffer[:e.cursor], append([]rune{r}, e.buffer[e.cursor:]...)...)
	e.cursor++
	e.preferredCursorOffset = -1
	e.refreshCompletion()
}

func (e *inputEditor) refreshCompletion() {
	e.slashSuggestions.dismiss()
	e.completion.update(string(e.buffer), 0, e.cursor)
}

func (e *inputEditor) completeCurrentMention() {
	e.refreshCompletion()
	if len(e.completion.matches) > 1 {
		if e.completion.visible {
			e.completion.move(1)
		} else {
			e.completion.show()
		}
		return
	}
	e.acceptCompletion()
}

func (e *inputEditor) completeCurrentSlash() bool {
	match, ok := e.slashCompletion.match(string(e.buffer), e.cursor)
	if !ok || match.start < 0 || match.start > match.end || match.end > len(e.buffer) {
		e.slashSuggestions.dismiss()
		return false
	}
	if len(match.candidates) > 1 {
		if e.slashSuggestions.matches(match) {
			e.slashSuggestions.move(1)
		} else {
			e.slashSuggestions.show(match)
		}
		e.completion.dismiss()
		return true
	}
	replacement := match.candidates[0]
	if string(e.buffer[match.start:match.end]) == replacement {
		return false
	}
	e.replaceSlashCompletion(match.start, match.end, replacement)
	e.completion.dismiss()
	return true
}

func (e *inputEditor) acceptSlashSuggestion() bool {
	selection, ok := e.slashSuggestions.selection()
	if !ok {
		return false
	}
	e.replaceSlashCompletion(e.slashSuggestions.start, e.slashSuggestions.end, selection)
	e.slashSuggestions.dismiss()
	return true
}

func (e *inputEditor) replaceSlashCompletion(start, end int, replacement string) {
	e.buffer = append(append([]rune{}, e.buffer[:start]...), append([]rune(replacement), e.buffer[end:]...)...)
	e.cursor = start + len([]rune(replacement))
	e.preferredCursorOffset = -1
}

// menuRow renders one completion-menu entry, highlighting the selected one
// the same way as the prompt.
func (e *inputEditor) menuRow(text string, selected bool) string {
	if selected {
		return e.style.Prompt("  > " + text)
	}
	return "    " + text
}

// footer renders "mode · model reasoning · ctx · cache" under the input.
// Segments are dropped from the right until the row fits, so it never wraps.
func (e *inputEditor) footer(width int) string {
	segments := []string{e.style.Muted("build")}
	if e.planMode != nil && e.planMode() {
		segments[0] = e.style.Warning("⏸ plan")
	}
	status := e.statusSnapshot
	if model := strings.TrimSpace(status.Model + " " + status.Reasoning); model != "" {
		segments = append(segments, e.style.Muted(model))
	}
	if e.status != nil {
		segments = append(segments, e.style.Muted("ctx "+formatFooterContext(status.ContextTokens, status.ContextWindow)))
	}
	if status.Usage.PromptTokens > 0 {
		segments = append(segments, e.style.Muted("cache "+output.CachePercent(status.Usage)))
	}
	if e.togglePlanMode != nil {
		segments = append(segments, e.style.Muted("(shift+tab to toggle)"))
	}
	separator := e.style.Muted(" · ")
	limit := max(width-1, 1)
	for len(segments) > 0 {
		row := "  " + strings.Join(segments, separator)
		if ansi.VisibleWidth(row) <= limit {
			return row
		}
		segments = segments[:len(segments)-1]
	}
	return ""
}

func formatFooterContext(used, window int) string {
	if window <= 0 {
		return output.CompactTokenCount(int64(used))
	}
	percent := float64(used) / float64(window) * 100
	return fmt.Sprintf("%s/%s (%.0f%%)", output.CompactTokenCount(int64(used)), output.CompactTokenCount(int64(window)), percent)
}

func (e *inputEditor) appendSlashSuggestions(rows []string, height int) []string {
	if !e.slashSuggestions.active || len(e.slashSuggestions.candidates) == 0 {
		return rows
	}
	available := height - len(rows)
	if available <= 0 {
		return rows
	}
	showHint := available > 1
	candidateRows := available
	if showHint {
		candidateRows--
	}
	visible := min(len(e.slashSuggestions.candidates), 8, candidateRows)
	start := 0
	if e.slashSuggestions.selected >= visible {
		start = e.slashSuggestions.selected - visible + 1
	}
	end := min(start+visible, len(e.slashSuggestions.candidates))
	for index := start; index < end; index++ {
		candidate := e.slashSuggestions.candidates[index]
		rows = append(rows, e.menuRow(strings.TrimSpace(candidate), index == e.slashSuggestions.selected))
	}
	if showHint && len(rows) < height {
		hint := "    Tab/↑/↓ select · Enter insert · Esc close"
		if hidden := len(e.slashSuggestions.candidates) - visible; hidden > 0 {
			hint = fmt.Sprintf("    Tab/↑/↓ select · Enter insert · %d more, type to narrow", hidden)
		}
		rows = append(rows, e.style.Muted(hint))
	}
	return rows
}

func (e *inputEditor) appendMentionSuggestions(rows []string, height int) []string {
	if e.completion.active && e.completion.indexing() {
		if len(rows) < height {
			rows = append(rows, e.style.Muted("    indexing files"+output.GlyphEllipsis))
		}
		return rows
	}
	if !e.completion.visible || len(e.completion.matches) == 0 {
		return rows
	}
	available := height - len(rows)
	if available <= 0 {
		return rows
	}
	showHint := available > 1
	candidateRows := available
	if showHint {
		candidateRows--
	}
	visible := min(len(e.completion.matches), candidateRows)
	start := 0
	if e.completion.selected >= visible {
		start = e.completion.selected - visible + 1
	}
	end := min(start+visible, len(e.completion.matches))
	for index := start; index < end; index++ {
		rows = append(rows, e.menuRow(e.completion.matches[index].path, index == e.completion.selected))
	}
	if showHint && len(rows) < height {
		rows = append(rows, e.style.Muted("    Tab/↑/↓ select · Enter insert · Esc close"))
	}
	return rows
}

func (e *inputEditor) acceptCompletion() bool {
	selection, ok := e.completion.selection()
	if !ok {
		return false
	}
	_, tokenLen, active := activePathQuery(string(e.buffer), 0, e.cursor)
	if !active {
		return false
	}
	start := max(e.cursor-tokenLen, 0)
	replacement := []rune("@" + selection)
	e.buffer = append(append([]rune{}, e.buffer[:start]...), append(replacement, e.buffer[e.cursor:]...)...)
	e.cursor = start + len(replacement)
	e.completion.dismiss()
	return true
}

func (e *inputEditor) layoutOptions(width int) ansi.LayoutOptions {
	return ansi.LayoutOptions{Width: width, FirstPrefix: e.prompt, TabWidth: 4}
}

func (e *inputEditor) moveCursorVertical(direction int) bool {
	layout := ansi.LayoutText(e.buffer, e.cursor, e.layoutOptions(e.lastSize.Width))
	currentRow := ansi.CursorRow(layout.Rows, e.cursor)
	targetRow := currentRow + direction
	if targetRow < 0 || targetRow >= len(layout.Rows) {
		return false
	}
	if e.preferredCursorOffset < 0 {
		e.preferredCursorOffset = layout.Rows[currentRow].CellOffset(e.cursor)
	}
	e.cursor = layout.Rows[targetRow].SourceIndexAt(e.preferredCursorOffset)
	return true
}

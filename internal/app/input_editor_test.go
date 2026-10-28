package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/lib/llm"
)

// newTestEditor builds an inputEditor over in-memory streams. Because the
// streams are not *os.File, raw mode, term size, and read deadlines are all
// disabled, which is exactly the behavior the tests exercise.
func newTestEditor(t *testing.T, input string, root string) *inputEditor {
	t.Helper()
	e := newInputEditor(bytes.NewBufferString(input), &bytes.Buffer{}, ">>> ", output.NewStyleEnabled(false), root, nil, nil)
	e.completion.waitLoaded()
	return e
}

// waitLoaded blocks until background path discovery finishes so tests can
// exercise completion deterministically.
func (a *pathAutocomplete) waitLoaded() {
	if a.loaded == nil {
		return
	}
	a.candidates = <-a.loaded
	a.loaded = nil
	a.applyAgentFilter()
}

func submit(t *testing.T, input string, root string) (string, error) {
	t.Helper()
	e := newTestEditor(t, input, root)
	return e.ReadLine()
}

func TestEditorTypingAndSubmit(t *testing.T) {
	line, err := submit(t, "hello world\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "hello world" {
		t.Errorf("got %q, want %q", line, "hello world")
	}
}

func TestEditorBracketedPastePreservesMultilineText(t *testing.T) {
	line, err := submit(t, "\x1b[200~line 1\nline 2\nline 3\x1b[201~\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	want := "line 1\nline 2\nline 3"
	if line != want {
		t.Errorf("got %q, want %q", line, want)
	}
}

func TestEditorBracketedPasteRendersOnceWithTerminalNewlines(t *testing.T) {
	var out bytes.Buffer
	e := newInputEditor(
		bytes.NewBufferString("\x1b[200~first line\nsecond line\nthird line\x1b[201~\n"),
		&out,
		">>> ",
		output.NewStyleEnabled(false),
		t.TempDir(),
		nil,
		nil,
	)
	if _, err := e.ReadLine(); err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if got := strings.Count(out.String(), ">>> "); got != 2 {
		t.Fatalf("prompt render count = %d, want 2; output=%q", got, out.String())
	}
	if !strings.Contains(out.String(), "first line\r\n    second line\r\n    third line") {
		t.Fatalf("multiline render does not use CRLF: output=%q", out.String())
	}
}

func TestEditorBracketedPasteNormalizesCRLF(t *testing.T) {
	line, err := submit(t, "\x1b[200~line 1\r\nline 2\x1b[201~\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "line 1\nline 2" {
		t.Errorf("got %q, want %q", line, "line 1\nline 2")
	}
}

func TestEditorBracketedPastePreservesTabsInSubmittedText(t *testing.T) {
	line, err := submit(t, "\x1b[200~status\tpackage\x1b[201~\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "status\tpackage" {
		t.Errorf("got %q, want %q", line, "status\tpackage")
	}
}

func TestEditorBracketedPastePrefixesMultilineTextBeginningWithPath(t *testing.T) {
	registry := slashcmd.NewRegistry()
	if err := registry.Register(slashcmd.ClearCommand{}); err != nil {
		t.Fatal(err)
	}
	e := newInputEditor(
		bytes.NewBufferString("\x1b[200~/home/runner/errors.rb:47: warning\nFailure: test failed\x1b[201~\n"),
		&bytes.Buffer{},
		">>> ",
		output.NewStyleEnabled(false),
		t.TempDir(),
		nil,
		registry,
	)

	line, err := e.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	want := "Pasted text:\n/home/runner/errors.rb:47: warning\nFailure: test failed"
	if line != want {
		t.Fatalf("got %q, want %q", line, want)
	}
}

func TestEditorBracketedPastePreservesRegisteredSlashCommand(t *testing.T) {
	registry := slashcmd.NewRegistry()
	if err := registry.Register(slashcmd.ClearCommand{}); err != nil {
		t.Fatal(err)
	}
	e := newInputEditor(
		bytes.NewBufferString("\x1b[200~/clear\n\x1b[201~\n"),
		&bytes.Buffer{},
		">>> ",
		output.NewStyleEnabled(false),
		t.TempDir(),
		nil,
		registry,
	)

	line, err := e.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "/clear\n" {
		t.Fatalf("got %q, want registered command unchanged", line)
	}
}

func TestEditorUpArrowEditsPreviousPastedLine(t *testing.T) {
	line, err := submit(t, "\x1b[200~abc\ndef\x1b[201~\x1b[AX\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "abcX\ndef" {
		t.Errorf("got %q, want %q", line, "abcX\ndef")
	}
}

func TestEditorVerticalMovementPreservesPreferredColumn(t *testing.T) {
	line, err := submit(t, "\x1b[200~abcdef\nx\nabcdef\x1b[201~\x1b[A\x1b[AZ\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "abcdefZ\nx\nabcdef" {
		t.Errorf("got %q, want %q", line, "abcdefZ\nx\nabcdef")
	}
}

func TestEditorVerticalMovementUsesWrappedRows(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	e.lastSize = ansi.Size{Width: 12, Height: 24} // prompt width 4 leaves 7 text cells per visual row
	e.buffer = []rune("abcdefghijkl")
	e.cursor = len(e.buffer)

	if !e.moveCursorVertical(-1) {
		t.Fatal("up movement did not move")
	}
	if e.cursor != 5 {
		t.Fatalf("cursor = %d, want 5", e.cursor)
	}
}

func TestEditorEnterSubmitsOutsideBracketedPaste(t *testing.T) {
	line, err := submit(t, "line 1\nline 2\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "line 1" {
		t.Errorf("got %q, want %q", line, "line 1")
	}
}

func TestEditorTypingDoesNotRedrawPromptPerCharacter(t *testing.T) {
	var out bytes.Buffer
	e := newInputEditor(bytes.NewBufferString("hello\n"), &out, ">>> ", output.NewStyleEnabled(false), t.TempDir(), nil, nil)
	line, err := e.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "hello" {
		t.Errorf("got %q, want %q", line, "hello")
	}
	if got := strings.Count(out.String(), ">>> "); got != 1 {
		t.Fatalf("prompt render count = %d, want 1; output=%q", got, out.String())
	}
}

func TestEditorBackspace(t *testing.T) {
	line, err := submit(t, "abc\x7f\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "ab" {
		t.Errorf("got %q, want %q", line, "ab")
	}
}

func TestEditorCtrlAMovesToBeginningOfLine(t *testing.T) {
	line, err := submit(t, "abc\x01X\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "Xabc" {
		t.Errorf("got %q, want %q", line, "Xabc")
	}
}

func TestEditorCtrlEMovesToEndOfLine(t *testing.T) {
	line, err := submit(t, "abc\x1b[D\x1b[D\x05X\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "abcX" {
		t.Errorf("got %q, want %q", line, "abcX")
	}
}

func TestEditorCtrlAAndCtrlEUseLogicalLineBoundaries(t *testing.T) {
	line, err := submit(t, "\x1b[200~first\nsecond\x1b[201~\x01<\x05>\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "first\n<second>" {
		t.Errorf("got %q, want %q", line, "first\n<second>")
	}
}

func TestEditorCtrlWDeletesPreviousWord(t *testing.T) {
	line, err := submit(t, "hello world   \x17caretz\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "hello caretz" {
		t.Errorf("got %q, want %q", line, "hello caretz")
	}
}

func TestEditorCtrlWDoesNotCrossLineBoundary(t *testing.T) {
	line, err := submit(t, "\x1b[200~first\nsecond\x1b[201~\x01\x17X\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "first\nXsecond" {
		t.Errorf("got %q, want %q", line, "first\nXsecond")
	}
}

func TestEditorCursorMovementInsertsAtCursor(t *testing.T) {
	// "abc", left twice, insert "x" -> "axbc"
	line, err := submit(t, "abc\x1b[D\x1b[Dx\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "axbc" {
		t.Errorf("got %q, want %q", line, "axbc")
	}
}

func TestEditorRightArrowBounded(t *testing.T) {
	// "ab", right (no-op at end), left, insert "x" -> "axb"
	line, err := submit(t, "ab\x1b[C\x1b[Dx\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "axb" {
		t.Errorf("got %q, want %q", line, "axb")
	}
}

func TestEditorDeleteKey(t *testing.T) {
	// "abc", left twice (cursor before 'b'), delete (ESC [ 3 ~) -> "ac"
	line, err := submit(t, "abc\x1b[D\x1b[D\x1b[3~\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "ac" {
		t.Errorf("got %q, want %q", line, "ac")
	}
}

func TestEditorUTF8Insertion(t *testing.T) {
	line, err := submit(t, "héllo\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "héllo" {
		t.Errorf("got %q, want %q", line, "héllo")
	}
}

func TestEditorViewWrapsOverflowOntoNextRow(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	e.buffer = []rune("abcdefghij")
	e.cursor = len(e.buffer)

	frame := e.View(ansi.Size{Width: 10, Height: 24})
	if got, want := strings.Join(frame.Rows, "\n"), "─────────\n>>> abcde\n    fghij\n─────────\n  build"; got != want {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	if got, want := frame.Cursor, (ansi.Position{Row: 2, Col: 9}); got != want {
		t.Fatalf("cursor = %#v, want %#v", got, want)
	}
}

func TestEditorViewDropsFrameWhenTerminalIsShort(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	e.buffer = []rune("abc")
	e.cursor = len(e.buffer)

	frame := e.View(ansi.Size{Width: 20, Height: 3})
	if got, want := strings.Join(frame.Rows, "\n"), ">>> abc"; got != want {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	if got, want := frame.Cursor, (ansi.Position{Row: 0, Col: 7}); got != want {
		t.Fatalf("cursor = %#v, want %#v", got, want)
	}
}

func TestEditorFooterShowsSessionStatus(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	planMode := true
	e.planMode = func() bool { return planMode }
	e.status = func() editorStatus {
		return editorStatus{
			Model:         "gpt-5.6",
			Reasoning:     "high",
			ContextTokens: 42000,
			ContextWindow: 400000,
			Usage:         llm.Usage{PromptTokens: 1000, CachedPromptTokens: 870},
		}
	}
	e.statusSnapshot = e.status()

	if got, want := e.footer(120), "  ⏸ plan · gpt-5.6 high · ctx 42.0k/400.0k (10%) · cache 87%"; got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
	planMode = false
	if got := e.footer(120); !strings.HasPrefix(got, "  build · ") {
		t.Fatalf("footer = %q, want build mode label without re-sampling status", got)
	}
}

func TestEditorFooterHidesCacheBeforeFirstResponse(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	e.status = func() editorStatus { return editorStatus{Model: "m", ContextTokens: 950} }
	e.statusSnapshot = e.status()

	if got, want := e.footer(80), "  build · m · ctx 950"; got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
}

func TestEditorFooterDropsSegmentsToFitWidth(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	e.togglePlanMode = func() {}
	e.status = func() editorStatus {
		return editorStatus{Model: "some-long-model-name", ContextTokens: 42000, ContextWindow: 400000}
	}
	e.statusSnapshot = e.status()

	for _, width := range []int{1, 10, 30, 50} {
		frame := e.View(ansi.Size{Width: width, Height: 24})
		for _, row := range frame.Rows {
			if got := ansi.VisibleWidth(row); got > max(width-1, 1) && row != frame.Rows[1] {
				t.Fatalf("width %d: row %q is %d cells wide", width, row, got)
			}
		}
	}
	if got, want := e.footer(31), "  build · some-long-model-name"; got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
}

func TestEditorViewWrapsWithStyledPrompt(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	e.prompt = "\x1b[32m>>> \x1b[0m"
	e.buffer = []rune("abcdefghij")
	e.cursor = len(e.buffer)

	frame := e.View(ansi.Size{Width: 10, Height: 24})
	if got, want := len(frame.Rows), 5; got != want {
		t.Fatalf("row count = %d, want %d: %#v", got, want, frame.Rows)
	}
	if got, want := ansi.Strip(strings.Join(frame.Rows[1:3], "\n")), ">>> abcde\n    fghij"; got != want {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}

func TestEditorCtrlCExit(t *testing.T) {
	_, err := submit(t, "partial\x03", t.TempDir())
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v, want ErrInterrupted", err)
	}
}

func TestEditorCtrlLClearsScreenAndPreservesInput(t *testing.T) {
	var out bytes.Buffer
	e := newInputEditor(
		bytes.NewBufferString("draft\x0c!\n"),
		&out,
		">>> ",
		output.NewStyleEnabled(false),
		t.TempDir(),
		nil,
		nil,
	)

	line, err := e.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "draft!" {
		t.Fatalf("line = %q, want draft!", line)
	}
	if !strings.Contains(out.String(), "\x1b[2J\x1b[H") {
		t.Fatalf("full-screen clear sequence missing: %q", out.String())
	}
}

func TestEditorCtrlDEmptyExits(t *testing.T) {
	_, err := submit(t, "\x04", t.TempDir())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestEditorCtrlDNonEmptyIgnored(t *testing.T) {
	line, err := submit(t, "a\x04\n", t.TempDir())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "a" {
		t.Errorf("got %q, want %q", line, "a")
	}
}

func TestActivePathQuery(t *testing.T) {
	cases := []struct {
		value  string
		column int
		query  string
		token  int
		active bool
	}{
		{"@abc", 4, "abc", 4, true},
		{"foo @abc", 8, "abc", 4, true},
		{"@abc def", 4, "abc", 4, true},
		{"@", 1, "", 1, true},
		{"foo@abc", 8, "", 0, false}, // no space before @
		{"a@b", 3, "", 0, false},
		{"abc", 3, "", 0, false},
		{"@abc", 2, "a", 2, true},
	}
	for _, tc := range cases {
		query, tokenLen, active := activePathQuery(tc.value, 0, tc.column)
		if query != tc.query || tokenLen != tc.token || active != tc.active {
			t.Errorf("activePathQuery(%q, 0, %d) = (%q, %d, %v), want (%q, %d, %v)",
				tc.value, tc.column, query, tokenLen, active, tc.query, tc.token, tc.active)
		}
	}
}

// completionRoot creates a temp project root with two known files.
func completionRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"alpha.txt", "beta.txt", "hello.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func TestEditorCompletionOpens(t *testing.T) {
	root := completionRoot(t)
	// Drive the editor key by key so we can inspect completion state.
	e := newTestEditor(t, "", root)
	for _, b := range []byte("@a") {
		e.buffer = append(e.buffer, rune(b))
		e.cursor = len(e.buffer)
		e.refreshCompletion()
	}
	if !e.completion.active {
		t.Fatal("completion not active after typing @a")
	}
	if len(e.completion.matches) == 0 {
		t.Fatal("no matches for @a")
	}
	if e.completion.matches[0].path != "alpha.txt" {
		t.Errorf("top match = %q, want alpha.txt", e.completion.matches[0].path)
	}
}

func TestEditorTabShowsAndSelectsAmbiguousMention(t *testing.T) {
	root := completionRoot(t)
	e := newTestEditor(t, "", root)
	e.buffer = []rune("@")
	e.cursor = len(e.buffer)

	if result := e.Update(ansi.Event{Kind: ansi.EventTab}); !result.Redraw {
		t.Fatal("Tab did not request a redraw")
	}
	if got := string(e.buffer); got != "@" {
		t.Fatalf("Tab changed ambiguous mention to %q", got)
	}
	frame := e.View(ansi.Size{Width: 80, Height: 24})
	got := strings.Join(frame.Rows, "\n")
	for _, want := range []string{"> alpha.txt", "beta.txt", "hello.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("mention suggestions missing %q: %q", want, got)
		}
	}

	e.Update(ansi.Event{Kind: ansi.EventDown})
	e.Update(ansi.Event{Kind: ansi.EventEnter})
	if got := string(e.buffer); got != "@beta.txt" {
		t.Fatalf("accepted mention = %q, want @beta.txt", got)
	}
}

func TestEditorTabCompletesSlashArgumentWithoutChangingEnter(t *testing.T) {
	registry := slashcmd.NewRegistry()
	if err := registry.Register(slashcmd.ModelsCommand{Choices: []config.ModelChoice{{Ref: "local/qwen"}}}); err != nil {
		t.Fatal(err)
	}
	e := newInputEditor(
		bytes.NewBufferString("/models qwen\t\r"),
		&bytes.Buffer{},
		">>> ",
		output.NewStyleEnabled(false),
		t.TempDir(),
		nil,
		registry,
	)
	line, err := e.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "/models local/qwen" {
		t.Fatalf("submitted line = %q, want completed slash command", line)
	}
}

func TestEditorSlashCompletionRendersNoSuggestions(t *testing.T) {
	registry := slashcmd.NewRegistry()
	if err := registry.Register(slashcmd.ModelsCommand{Choices: []config.ModelChoice{{Ref: "local/qwen"}}}); err != nil {
		t.Fatal(err)
	}
	e := newInputEditor(bytes.NewBufferString(""), &bytes.Buffer{}, ">>> ", output.NewStyleEnabled(false), t.TempDir(), nil, registry)
	e.buffer = []rune("/models q")
	e.cursor = len(e.buffer)
	frame := e.View(ansi.Size{Width: 80, Height: 24})
	if got := strings.Join(frame.Rows, "\n"); strings.Contains(got, "local/qwen") {
		t.Fatalf("render output shows slash suggestions: %q", got)
	}
}

func TestEditorTabShowsAndSelectsModelSuggestions(t *testing.T) {
	registry := slashcmd.NewRegistry()
	choices := []config.ModelChoice{
		{Ref: "local/llama"},
		{Ref: "local/qwen"},
		{Ref: "openai/gpt"},
	}
	if err := registry.Register(slashcmd.ModelsCommand{Choices: choices}); err != nil {
		t.Fatal(err)
	}
	e := newInputEditor(bytes.NewBufferString(""), &bytes.Buffer{}, ">>> ", output.NewStyleEnabled(false), t.TempDir(), nil, registry)
	e.buffer = []rune("/models ")
	e.cursor = len(e.buffer)

	if result := e.Update(ansi.Event{Kind: ansi.EventTab}); !result.Redraw {
		t.Fatal("Tab did not request a redraw")
	}
	if got := string(e.buffer); got != "/models " {
		t.Fatalf("Tab changed ambiguous input to %q", got)
	}
	frame := e.View(ansi.Size{Width: 80, Height: 24})
	got := strings.Join(frame.Rows, "\n")
	for _, want := range []string{"> local/llama", "local/qwen", "openai/gpt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("suggestion view missing %q: %q", want, got)
		}
	}

	e.Update(ansi.Event{Kind: ansi.EventDown})
	e.Update(ansi.Event{Kind: ansi.EventEnter})
	if got := string(e.buffer); got != "/models local/qwen" {
		t.Fatalf("accepted model = %q, want /models local/qwen", got)
	}
	if e.slashSuggestions.active {
		t.Fatal("suggestions remained active after accepting a model")
	}
}

func TestEditorTabCompletesPartialMention(t *testing.T) {
	root := completionRoot(t)
	line, err := submit(t, "@hel\t\r", root)
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "@hello.txt" {
		t.Errorf("got %q, want %q", line, "@hello.txt")
	}
}

func TestEditorEnterSubmitsWithoutAcceptingCompletion(t *testing.T) {
	root := completionRoot(t)
	line, err := submit(t, "@a\r", root)
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "@a" {
		t.Errorf("got %q, want %q", line, "@a")
	}
}

func TestEditorCompletionDismissWithEsc(t *testing.T) {
	root := completionRoot(t)
	// Type "@", ESC (dismiss), Enter (submit raw "@").
	// readEscape consumes the byte immediately after ESC to disambiguate a
	// lone ESC from an escape sequence, so a throwaway space stands in for the
	// real-terminal read-timeout; the following Enter then submits the line.
	line, err := submit(t, "@\x1b \r", root)
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "@" {
		t.Errorf("got %q, want %q", line, "@")
	}
}

func TestEditorCompletionArrowSelection(t *testing.T) {
	root := completionRoot(t)
	// Tab opens ambiguous matches, Down selects the next file, and Enter
	// inserts it. The second Enter submits the completed prompt.
	line, err := submit(t, "@\t\x1b[B\r\r", root)
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "@beta.txt" {
		t.Errorf("got %q, want %q", line, "@beta.txt")
	}
}

func TestEditorShowsIndexingHintWhilePathsLoad(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	e.completion = pathAutocomplete{loaded: make(chan []pathCandidate)}
	e.buffer = []rune("@al")
	e.cursor = len(e.buffer)
	e.refreshCompletion()
	frame := e.View(ansi.Size{Width: 80, Height: 24})
	if s := strings.Join(frame.Rows, "\n"); !strings.Contains(s, "indexing files…") {
		t.Errorf("render output missing indexing hint; got %q", s)
	}
}

func TestEditorRenderDoesNotShowCompletionList(t *testing.T) {
	root := completionRoot(t)
	e := newInputEditor(bytes.NewBufferString(""), &bytes.Buffer{}, ">>> ", output.NewStyleEnabled(false), root, nil, nil)
	e.completion.waitLoaded()
	for _, b := range []byte("@") {
		e.buffer = append(e.buffer, rune(b))
		e.cursor = len(e.buffer)
		e.refreshCompletion()
	}
	frame := e.View(ansi.Size{Width: 80, Height: 24})
	s := strings.Join(frame.Rows, "\n")
	if strings.Contains(s, "alpha.txt") || strings.Contains(s, "beta.txt") {
		t.Errorf("render output shows completion list; got %q", s)
	}
}

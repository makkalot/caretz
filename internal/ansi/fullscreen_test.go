package ansi

import (
	"bytes"
	"strings"
	"testing"
)

type countingComponent struct {
	updates int
}

func (c *countingComponent) Update(event Event) Result {
	c.updates++
	if event.Kind == EventRune && event.Rune == 'q' {
		return Result{Exit: true}
	}
	return Result{Redraw: true}
}

func (c *countingComponent) View(size Size) Frame {
	return Frame{Rows: []string{"top", strings.Repeat("x", c.updates)}}
}

func TestRunFullScreenRestoresMainScreen(t *testing.T) {
	var out bytes.Buffer
	terminal := NewTerminal(bytes.NewBufferString("aq"), &out)
	terminal.sizeFn = func() Size { return Size{Width: 10, Height: 3} }
	component := &countingComponent{}

	result := RunFullScreen(terminal, component)

	if !result.Exit || component.updates != 2 {
		t.Fatalf("result = %#v, updates = %d", result, component.updates)
	}
	got := out.String()
	if !strings.HasPrefix(got, enableBracketedPaste+enterFullScreen) {
		t.Fatalf("output does not enter the alternate screen: %q", got)
	}
	if !strings.HasSuffix(got, exitFullScreen+disableBracketedPaste) {
		t.Fatalf("output does not restore the main screen: %q", got)
	}
	// Two frames of three rows each; only rows between frames get newlines.
	if frames := strings.Count(got, "\x1b[H"); frames != 2 {
		t.Fatalf("frames = %d, want 2: %q", frames, got)
	}
	if newlines := strings.Count(got, "\r\n"); newlines != 4 {
		t.Fatalf("row separators = %d, want 4", newlines)
	}
	if !strings.Contains(got, "top\x1b[0m\x1b[K\r\nx\x1b[0m\x1b[K\r\n\x1b[0m\x1b[K") {
		t.Fatalf("second frame missing: %q", got)
	}
}

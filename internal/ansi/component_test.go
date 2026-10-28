package ansi

import (
	"bytes"
	"testing"
)

type testComponent struct {
	value []rune
}

func (c *testComponent) Update(event Event) Result {
	switch event.Kind {
	case EventRune:
		c.value = append(c.value, event.Rune)
		return Result{Redraw: true}
	case EventCtrlL:
		return Result{ClearScreen: true}
	case EventEnter:
		return Result{Submitted: true, Submit: string(c.value)}
	}
	return Result{}
}

func TestRuntimeCtrlLClearsScreenAndPreservesComponentState(t *testing.T) {
	var output bytes.Buffer
	terminal := NewTerminal(bytes.NewBufferString("ok\x0c!\n"), &output)
	result := NewRuntime(terminal).Run(&testComponent{})

	if !result.Submitted || result.Submit != "ok!" {
		t.Fatalf("result = %#v, want submitted value ok!", result)
	}
	if !bytes.Contains(output.Bytes(), []byte("\x1b[2J\x1b[H")) {
		t.Fatalf("full-screen clear sequence missing: %q", output.String())
	}
	clearAt := bytes.Index(output.Bytes(), []byte("\x1b[2J\x1b[H"))
	if !bytes.Contains(output.Bytes()[clearAt:], []byte(">>> ok")) {
		t.Fatalf("component was not redrawn after clear: %q", output.String())
	}
}

func (c *testComponent) View(Size) Frame {
	return Frame{Rows: []string{">>> " + string(c.value)}, Cursor: Position{Col: 4 + len(c.value)}}
}

func TestRuntimeDispatchesComponentAndRestoresTerminal(t *testing.T) {
	var output bytes.Buffer
	terminal := NewTerminal(bytes.NewBufferString("ok\n"), &output)
	restored := false
	terminal.rawMode = func() (func(), error) {
		return func() { restored = true }, nil
	}
	result := NewRuntime(terminal).Run(&testComponent{})
	if !result.Submitted || result.Submit != "ok" {
		t.Fatalf("result = %#v", result)
	}
	if !restored {
		t.Fatal("terminal raw mode was not restored")
	}
	if !bytes.HasSuffix(output.Bytes(), []byte(disableBracketedPaste)) {
		t.Fatalf("bracketed paste was not disabled: %q", output.String())
	}
}

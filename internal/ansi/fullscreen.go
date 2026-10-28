package ansi

import (
	"io"
	"strings"
	"time"
)

const (
	// enterFullScreen switches to the alternate screen, hides the cursor and
	// disables autowrap so an over-long row cannot scroll the frame.
	enterFullScreen = "\x1b[?1049h\x1b[?25l\x1b[?7l"
	exitFullScreen  = "\x1b[?7h\x1b[?25h\x1b[?1049l"
)

// RunFullScreen runs component on the alternate screen until it exits,
// submits or fails. Each redraw repaints every terminal row, and a terminal
// resize redraws without an input event. The main screen and its scrollback
// are restored on return.
func RunFullScreen(t *Terminal, component Component) Result {
	restore, err := t.BeginInput()
	if err != nil {
		return Result{Err: err}
	}
	writeString(t.out, enterFullScreen)
	defer func() {
		writeString(t.out, exitFullScreen)
		restore()
	}()

	var drawn Size
	draw := func() {
		drawn = t.Size()
		drawFullFrame(t.out, component.View(drawn), drawn)
	}
	draw()
	for {
		event, ready, err := t.ReadEventWithin(100 * time.Millisecond)
		if err != nil {
			return Result{Err: err}
		}
		if !ready {
			if t.Size() != drawn {
				draw()
			}
			continue
		}
		result := component.Update(event)
		if result.Err != nil || result.Exit || result.Submitted {
			return result
		}
		if result.Redraw || result.ClearScreen || t.Size() != drawn {
			draw()
		}
	}
}

// drawFullFrame paints exactly size.Height rows from the top-left corner,
// clearing the remainder of each row.
func drawFullFrame(out io.Writer, frame Frame, size Size) {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i := 0; i < size.Height; i++ {
		row := ""
		if i < len(frame.Rows) {
			row = frame.Rows[i]
		}
		b.WriteString(row + "\x1b[0m")
		// Without autowrap the cursor stays on the last cell of a full row,
		// so clearing there would erase that cell.
		if VisibleWidth(row) < size.Width {
			b.WriteString("\x1b[K")
		}
		if i < size.Height-1 {
			b.WriteString("\r\n")
		}
	}
	writeString(out, b.String())
}

func writeString(out io.Writer, s string) { _, _ = io.WriteString(out, s) }

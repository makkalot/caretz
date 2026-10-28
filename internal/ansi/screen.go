package ansi

import (
	"fmt"
	"io"
	"strings"
)

type Screen struct {
	out      io.Writer
	rows     int
	cursor   Position
	frame    Frame
	hasFrame bool
}

func NewScreen(out io.Writer) *Screen { return &Screen{out: out} }

func (s *Screen) Draw(frame Frame) {
	if s.canAppend(frame) {
		row := s.cursor.Row
		_, _ = fmt.Fprint(s.out, strings.TrimPrefix(frame.Rows[row], s.frame.Rows[row]))
		s.frame = cloneFrame(frame)
		s.cursor = frame.Cursor
		return
	}
	s.Clear()
	if len(frame.Rows) == 0 {
		frame.Rows = []string{""}
	}
	_, _ = fmt.Fprint(s.out, strings.Join(frame.Rows, "\r\n"))
	cursor := clampPosition(frame.Cursor, len(frame.Rows))
	rowsUp := len(frame.Rows) - 1 - cursor.Row
	if rowsUp > 0 {
		_, _ = fmt.Fprintf(s.out, "\x1b[%dA", rowsUp)
	}
	_, _ = fmt.Fprintf(s.out, "\r\x1b[%dG", cursor.Col+1)
	s.rows = len(frame.Rows)
	s.cursor = cursor
	frame.Cursor = cursor
	s.frame = cloneFrame(frame)
	s.hasFrame = true
}

func (s *Screen) canAppend(frame Frame) bool {
	if !s.hasFrame || len(frame.Rows) != len(s.frame.Rows) || frame.Cursor.Row != s.cursor.Row {
		return false
	}
	for i := range frame.Rows {
		if i == s.cursor.Row {
			continue
		}
		if frame.Rows[i] != s.frame.Rows[i] {
			return false
		}
	}
	oldRow := s.frame.Rows[s.cursor.Row]
	newRow := frame.Rows[s.cursor.Row]
	return strings.HasPrefix(newRow, oldRow) &&
		s.cursor.Col == VisibleWidth(oldRow) &&
		frame.Cursor.Col == VisibleWidth(newRow)
}

func (s *Screen) Clear() {
	if !s.hasFrame {
		return
	}
	rowsBelow := s.rows - 1 - s.cursor.Row
	if rowsBelow > 0 {
		_, _ = fmt.Fprintf(s.out, "\x1b[%dB", rowsBelow)
	}
	_, _ = fmt.Fprint(s.out, "\r\x1b[2K")
	for row := 1; row < s.rows; row++ {
		_, _ = fmt.Fprint(s.out, "\x1b[1A\r\x1b[2K")
	}
	s.rows = 0
	s.cursor = Position{}
	s.frame = Frame{}
	s.hasFrame = false
}

// ClearAll clears the full terminal viewport and moves the cursor home.
func (s *Screen) ClearAll() {
	_, _ = fmt.Fprint(s.out, "\x1b[2J\x1b[H")
	s.rows = 0
	s.cursor = Position{}
	s.frame = Frame{}
	s.hasFrame = false
}

func cloneFrame(frame Frame) Frame {
	frame.Rows = append([]string(nil), frame.Rows...)
	return frame
}

func clampPosition(position Position, rows int) Position {
	position.Row = max(0, min(position.Row, rows-1))
	position.Col = max(0, position.Col)
	return position
}

package output

import (
	"fmt"
	"io"
)

// Errorf writes an "error: ..." line to w, colored when w is a terminal.
func Errorf(w io.Writer, format string, args ...any) {
	s := NewStyle(w)
	_, _ = fmt.Fprintln(w, s.Error("error:")+" "+fmt.Sprintf(format, args...))
}

// Warnf writes a "warning: ..." line to w, colored when w is a terminal.
func Warnf(w io.Writer, format string, args ...any) {
	s := NewStyle(w)
	_, _ = fmt.Fprintln(w, s.Warning("warning:")+" "+fmt.Sprintf(format, args...))
}

// Notef writes a "note: ..." line to w, colored when w is a terminal.
func Notef(w io.Writer, format string, args ...any) {
	s := NewStyle(w)
	_, _ = fmt.Fprintln(w, s.Muted("note:")+" "+fmt.Sprintf(format, args...))
}

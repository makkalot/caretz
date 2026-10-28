package output

import (
	"fmt"
	"io"
	"time"
)

var thinkingFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type terminalSpinner struct {
	stop chan struct{}
	done chan struct{}
}

func startTerminalSpinner(w io.Writer, style Style) *terminalSpinner {
	spinner := &terminalSpinner{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(spinner.done)
		ticker := time.NewTicker(90 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			_, _ = fmt.Fprintf(w, "\r\x1b[2K%s", style.Muted("Thinking "+thinkingFrames[frame]))
			frame = (frame + 1) % len(thinkingFrames)
			select {
			case <-spinner.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return spinner
}

func (s *terminalSpinner) Stop(w io.Writer) {
	if s == nil {
		return
	}
	close(s.stop)
	<-s.done
	_, _ = fmt.Fprint(w, "\r\x1b[2K")
}

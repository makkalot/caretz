package ansi

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	enableBracketedPaste  = "\x1b[?2004h"
	disableBracketedPaste = "\x1b[?2004l"
)

type Terminal struct {
	in      io.Reader
	out     io.Writer
	decoder *Decoder
	rawMode func() (func(), error)
	sizeFn  func() Size
}

func NewTerminal(in io.Reader, out io.Writer) *Terminal {
	decoder := NewDecoder(in)
	t := &Terminal{
		in:      in,
		out:     out,
		decoder: decoder,
		sizeFn:  func() Size { return Size{Width: 80, Height: 24} },
	}
	if file, ok := in.(*os.File); ok {
		t.rawMode = func() (func(), error) { return makeInputMode(int(file.Fd())) }
		decoder.waitInput = newInputWaiter(int(file.Fd()))
		decoder.setDeadline = file.SetReadDeadline
	}
	if file, ok := out.(*os.File); ok {
		t.sizeFn = func() Size {
			width, height, err := term.GetSize(int(file.Fd()))
			if err != nil || width <= 0 || height <= 0 {
				return Size{Width: 80, Height: 24}
			}
			return Size{Width: width, Height: height}
		}
	}
	return t
}

func (t *Terminal) Size() Size { return t.sizeFn() }

func (t *Terminal) Writer() io.Writer { return t.out }

// ReadEvent reads one normalized input event. It is useful for application
// phases that need keyboard input without running a full Component.
func (t *Terminal) ReadEvent() (Event, error) { return t.decoder.Read() }

// ReadEventWithin waits up to timeout for one input event. The boolean is
// false when the timeout expires without consuming input.
func (t *Terminal) ReadEventWithin(timeout time.Duration) (Event, bool, error) {
	if t.decoder.waitInput != nil && t.decoder.reader.Buffered() == 0 {
		deadline := time.Now().Add(timeout)
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return Event{}, false, nil
			}
			ready, err := t.decoder.waitInput(remaining)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil || !ready {
				return Event{}, false, err
			}
			break
		}
	}
	event, err := t.decoder.Read()
	return event, true, err
}

func (t *Terminal) BeginInput() (func(), error) {
	restoreRaw := func() {}
	if t.rawMode != nil {
		var err error
		restoreRaw, err = t.rawMode()
		if err != nil {
			return nil, fmt.Errorf("failed to enable raw mode: %w", err)
		}
	}
	_, _ = fmt.Fprint(t.out, enableBracketedPaste)
	return func() {
		_, _ = fmt.Fprint(t.out, disableBracketedPaste)
		restoreRaw()
	}, nil
}

// RunExternal hands the terminal to an interactive program such as $EDITOR
// while input mode is active, and restores the input state afterwards.
func (t *Terminal) RunExternal(run func() error) error {
	_, _ = fmt.Fprint(t.out, disableBracketedPaste)
	defer func() { _, _ = fmt.Fprint(t.out, enableBracketedPaste) }()
	file, ok := t.in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return run()
	}
	state, err := term.GetState(int(file.Fd()))
	if err != nil {
		return run()
	}
	defer func() { _ = term.Restore(int(file.Fd()), state) }()
	return run()
}

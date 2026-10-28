//go:build darwin

package ansi

import "golang.org/x/sys/unix"

// makeInputMode makes keystrokes available immediately while deliberately
// preserving output processing (notably NL -> CR-NL). term.MakeRaw disables
// OPOST, which makes concurrently streamed output stair-step across the screen.
func makeInputMode(fd int) (func(), error) {
	original, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	state := *original
	state.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	state.Cc[unix.VMIN] = 1
	state.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &state); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TIOCSETA, original) }, nil
}

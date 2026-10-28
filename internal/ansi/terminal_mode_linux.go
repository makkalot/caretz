//go:build linux

package ansi

import "golang.org/x/sys/unix"

func makeInputMode(fd int) (func(), error) {
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	state := *original
	state.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	state.Cc[unix.VMIN] = 1
	state.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &state); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, original) }, nil
}

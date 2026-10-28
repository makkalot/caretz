//go:build !darwin && !linux

package ansi

import "golang.org/x/term"

func makeInputMode(fd int) (func(), error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, state) }, nil
}

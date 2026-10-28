//go:build darwin || linux

package ansi

import (
	"time"

	"golang.org/x/sys/unix"
)

func newInputWaiter(fd int) func(time.Duration) (bool, error) {
	return func(timeout time.Duration) (bool, error) {
		milliseconds := int(timeout.Milliseconds())
		if milliseconds < 1 {
			milliseconds = 1
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, milliseconds)
		return n > 0 && fds[0].Revents&unix.POLLIN != 0, err
	}
}

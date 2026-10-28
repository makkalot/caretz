//go:build darwin

package sandbox

import (
	"syscall"
	"testing"
)

// parity: session.control-channel
func TestMarkStrayFDCLOEXECKeepsSocketOpen(t *testing.T) {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if fd >= 64 {
		t.Skipf("socket descriptor %d is outside the scanned range", fd)
	}

	markStrayFDCLOEXEC()

	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFD), 0)
	if errno != 0 {
		t.Fatalf("socket descriptor was closed: %v", errno)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("socket descriptor was not marked close-on-exec")
	}
}

package peers

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func sameUser(conn *net.UnixConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	same := false
	_ = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		same = err == nil && int(cred.Uid) == os.Getuid()
	})
	return same
}

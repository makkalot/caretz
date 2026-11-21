package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func main() {
	base, _ := os.Getwd()
	base = filepath.Join(base, ".tmp", "probe2")
	_ = os.MkdirAll(base, 0o755)
	// candidates of increasing total length under the workspace
	for _, n := range []int{70, 80, 90, 94, 95, 96, 100, 104, 110} {
		dir := filepath.Join(base, fmt.Sprintf("d%03d", n))
		_ = os.MkdirAll(dir, 0o755)
		// pad the path to exactly n chars via the sock filename
		prefix := dir + "/x"
		if len(prefix) > n-4 {
			fmt.Printf("n=%d prefix too long (%d)\n", n, len(prefix))
			continue
		}
		pad := n - len(prefix) - len(".sock")
		name := prefix + string(makePad(pad)) + ".sock"
		l, err := net.Listen("unix", name)
		if err != nil {
			fmt.Printf("n=%d len=%d ERR=%v\n", n, len(name), err)
		} else {
			fmt.Printf("n=%d len=%d OK\n", n, len(name))
			l.Close()
		}
		_ = os.Remove(name)
	}
}

func makePad(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'y'
	}
	return b
}

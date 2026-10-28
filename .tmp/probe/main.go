package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func main() {
	base, _ := os.MkdirTemp("", "probe")
	long := filepath.Join(base, "01234567890123456789012345678901234567890123456789012345678901234567890.sock")
	cases := map[string]string{
		"tmpdeep-96": long,
		"tmp-57":     filepath.Join(base, "x.sock"),
	}
	wd, _ := os.Getwd()
	short := filepath.Join(wd, ".tmp", "probe-sock.sock")
	cases["wdrel-65"] = short
	for name, path := range cases {
		l, err := net.Listen("unix", path)
		if err != nil {
			fmt.Printf("%s len=%d ERR=%v\n", name, len(path), err)
			continue
		}
		fmt.Printf("%s len=%d OK\n", name, len(path))
		l.Close()
		os.Remove(path)
	}
}

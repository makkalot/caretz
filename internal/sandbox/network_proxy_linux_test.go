//go:build linux

package sandbox

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
)

// parity: policy.proxy-endpoint
func TestStartSandboxProxyUsesSeparateHTTPAndSOCKSUnixSockets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy, err := startSandboxProxy(ctx, []string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	if proxy.httpSocketPath == proxy.socksSocketPath {
		t.Fatalf("HTTP and SOCKS share socket %q", proxy.httpSocketPath)
	}
	conn, err := net.Dial("unix", proxy.httpSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	_, err = conn.Write([]byte("GET http://blocked.test/ HTTP/1.1\r\nHost: blocked.test\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "403 Forbidden") {
		t.Fatalf("proxy response = %q, want 403", string(buf[:n]))
	}

	socksConn, err := net.Dial("unix", proxy.socksSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = socksConn.Close() }()
	socksGreeting(t, socksConn)
	_, _ = socksConn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 22})
	reply := make([]byte, 10)
	if _, err := io.ReadFull(socksConn, reply); err != nil || reply[1] != 2 {
		t.Fatalf("SOCKS response = %v, %v; want blocked", reply, err)
	}
}

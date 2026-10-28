package sandbox

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// socksConnectArg makes the caretz binary act as an SSH ProxyCommand that
// relays stdin/stdout through the sandbox SOCKS proxy. Routing SSH through the
// proxy, rather than letting it dial directly, is what lets a blocked
// destination surface as an approval request instead of a kernel-level
// "Operation not permitted".
const socksConnectArg = "__caretz-socks-connect"

// sshProxyCommandEnv names the variable holding a ready-to-use SSH
// ProxyCommand, so sandboxed commands need no SOCKS-capable nc.
const sshProxyCommandEnv = "CARETZ_SSH_PROXY_COMMAND"

func init() {
	if len(os.Args) == 4 && os.Args[1] == socksConnectArg {
		os.Exit(runSOCKSConnect(os.Getenv(socksProxyEnv), os.Args[2], os.Args[3], os.Stdin, os.Stdout, os.Stderr))
	}
}

// sshProxyEnv returns the SSH routing variables for a sandbox whose SOCKS
// proxy is reachable through CARETZ_SOCKS_PROXY. GIT_SSH_COMMAND sends
// Git-over-SSH through the proxy automatically; an explicit grant of
// GIT_SSH_COMMAND overrides it.
func sshProxyEnv() []string {
	self, err := os.Executable()
	if err != nil || strings.ContainsAny(self, "'\"`$\\\n") {
		return nil
	}
	proxyCommand := "'" + self + "' " + socksConnectArg + " %h %p"
	return []string{
		sshProxyCommandEnv + "=" + proxyCommand,
		`GIT_SSH_COMMAND=ssh -o "ProxyCommand=` + proxyCommand + `"`,
	}
}

func runSOCKSConnect(proxyURL, host, port string, stdin io.Reader, stdout, stderr io.Writer) int {
	proxy := strings.TrimPrefix(strings.TrimPrefix(proxyURL, "socks5h://"), "socks5://")
	if proxy == "" {
		_, _ = fmt.Fprintf(stderr, "caretz: %s is not set; restricted networking is not active\n", socksProxyEnv)
		return 1
	}
	conn, err := dialSOCKS(proxy, host, port)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "caretz: %v\n", err)
		return 1
	}
	defer func() { _ = conn.Close() }()
	go func() {
		_, _ = io.Copy(conn, stdin)
		closeWrite(conn)
	}()
	_, _ = io.Copy(stdout, conn)
	return 0
}

func dialSOCKS(proxy, host, portText string) (net.Conn, error) {
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port %q", portText)
	}
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		request = append(append(request, 1), ip.To4()...)
	} else if ip != nil {
		request = append(append(request, 4), ip.To16()...)
	} else if len(host) > 0 && len(host) <= 255 {
		request = append(append(request, 3, byte(len(host))), host...)
	} else {
		return nil, fmt.Errorf("invalid host %q", host)
	}
	request = append(request, byte(port>>8), byte(port))

	conn, err := net.DialTimeout("tcp", proxy, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to sandbox SOCKS proxy: %w", err)
	}
	fail := func(err error) (net.Conn, error) {
		_ = conn.Close()
		return nil, err
	}
	// Authorization may wait for the user, so only the greeting is bounded.
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	greeting := make([]byte, 2)
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	if _, err := io.ReadFull(conn, greeting); err != nil || greeting[0] != 5 || greeting[1] != 0 {
		return fail(errors.New("sandbox SOCKS proxy rejected the handshake"))
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err := conn.Write(request); err != nil {
		return fail(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fail(fmt.Errorf("sandbox SOCKS proxy closed the connection to %s", net.JoinHostPort(host, portText)))
	}
	if reply[1] != 0 {
		if reply[1] == 2 {
			return fail(fmt.Errorf("sandbox network policy blocked tcp://%s", net.JoinHostPort(host, portText)))
		}
		return fail(fmt.Errorf("sandbox SOCKS proxy could not connect to %s (code %d)", net.JoinHostPort(host, portText), reply[1]))
	}
	var addressLen int
	switch reply[3] {
	case 1:
		addressLen = net.IPv4len
	case 4:
		addressLen = net.IPv6len
	case 3:
		size := []byte{0}
		if _, err := io.ReadFull(conn, size); err != nil {
			return fail(err)
		}
		addressLen = int(size[0])
	default:
		return fail(errors.New("sandbox SOCKS proxy sent an invalid reply"))
	}
	if _, err := io.ReadFull(conn, make([]byte, addressLen+2)); err != nil {
		return fail(err)
	}
	return conn, nil
}

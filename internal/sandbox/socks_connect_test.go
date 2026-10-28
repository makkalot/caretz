package sandbox

import (
	"bytes"
	"strings"
	"testing"
)

func TestSOCKSConnectRelaysStdioThroughProxy(t *testing.T) {
	upstream := startTCPEchoServer(t, "tcp4", "127.0.0.1:0")
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), []string{"tcp://" + upstream.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	host, port := splitRequestHost(upstream.Addr().String(), "")
	var stdout, stderr bytes.Buffer
	if code := runSOCKSConnect("socks5h://"+socksAddr, host, port, strings.NewReader("ssh-banner"), &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "ssh-banner" {
		t.Fatalf("relayed %q, want ssh-banner", stdout.String())
	}
}

func TestSOCKSConnectReportsBlockedDestination(t *testing.T) {
	upstream := startTCPEchoServer(t, "tcp4", "127.0.0.1:0")
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	host, port := splitRequestHost(upstream.Addr().String(), "")
	var stdout, stderr bytes.Buffer
	if code := runSOCKSConnect("socks5h://"+socksAddr, host, port, strings.NewReader(""), &stdout, &stderr); code == 0 {
		t.Fatal("blocked destination relayed")
	}
	if want := "blocked tcp://" + upstream.Addr().String(); !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr=%q, want %q", stderr.String(), want)
	}
	if code := runSOCKSConnect("", host, port, strings.NewReader(""), &stdout, &stderr); code == 0 {
		t.Fatal("connect without a proxy succeeded")
	}
}

func TestSSHProxyEnvRoutesGitThroughHelper(t *testing.T) {
	env := sshProxyEnv()
	values := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	proxyCommand := values[sshProxyCommandEnv]
	if !strings.HasSuffix(proxyCommand, " "+socksConnectArg+" %h %p") {
		t.Fatalf("%s=%q", sshProxyCommandEnv, proxyCommand)
	}
	if want := `ssh -o "ProxyCommand=` + proxyCommand + `"`; values["GIT_SSH_COMMAND"] != want {
		t.Fatalf("GIT_SSH_COMMAND=%q, want %q", values["GIT_SSH_COMMAND"], want)
	}
}

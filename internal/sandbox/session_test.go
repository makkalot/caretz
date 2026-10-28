package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSessionFrameRejectsMalformedInput(t *testing.T) {
	for _, data := range [][]byte{{0, 0, 0, 0}, {0xff, 0xff, 0xff, 0xff}, {0, 0, 0, 2, 'x', 'x'}} {
		var value sessionResponse
		if err := readSessionFrame(bytes.NewReader(data), &value); err == nil {
			t.Fatalf("accepted frame %v", data)
		}
	}
	var framed bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(`{"id":1,"type":"result","unknown":true}`)))
	framed.Write(header[:])
	framed.WriteString(`{"id":1,"type":"result","unknown":true}`)
	var value sessionResponse
	if err := readSessionFrame(&framed, &value); err == nil {
		t.Fatal("accepted unknown response field")
	}
	framed.Reset()
	payload := []byte(`{"id":1,"type":"result"} {}`)
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	framed.Write(header[:])
	framed.Write(payload)
	if err := readSessionFrame(&framed, &value); err == nil {
		t.Fatal("accepted trailing frame data")
	}
}

func FuzzSessionFrameDecoder(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		var response sessionResponse
		_ = readSessionFrame(bytes.NewReader(data), &response)
	})
}

func TestSessionNetworkHelper(t *testing.T) {
	mode := os.Getenv("CARETZ_SESSION_TEST_HELPER")
	if mode == "" {
		return
	}
	addr := os.Getenv("CARETZ_SESSION_TEST_ADDR")
	if mode == "unix-echo" {
		listener, err := net.Listen("unix", os.ExpandEnv(addr))
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(40)
		}
		defer func() { _ = listener.Close() }()
		go func() {
			if conn, err := listener.Accept(); err == nil {
				_, _ = io.Copy(conn, conn)
				_ = conn.Close()
			}
		}()
		conn, err := net.DialTimeout("unix", listener.Addr().String(), time.Second)
		if err != nil {
			os.Exit(41)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		echo := make([]byte, len("unix-ok"))
		if _, err := conn.Write([]byte("unix-ok")); err != nil {
			os.Exit(42)
		}
		if _, err := io.ReadFull(conn, echo); err != nil {
			os.Exit(43)
		}
		_, _ = os.Stdout.Write(echo)
		os.Exit(0)
	}
	if mode == "ssh-agent" {
		conn, err := net.DialTimeout("unix", os.Getenv("SSH_AUTH_SOCK"), time.Second)
		if err != nil {
			os.Exit(21)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		data, err := io.ReadAll(conn)
		if err != nil {
			os.Exit(22)
		}
		_, _ = os.Stdout.Write(data)
		os.Exit(0)
	}
	if mode == "socks-echo" {
		proxy := strings.TrimPrefix(os.Getenv("CARETZ_SOCKS_PROXY"), "socks5h://")
		host, portText, err := net.SplitHostPort(addr)
		port, portErr := strconv.Atoi(portText)
		ip := net.ParseIP(host).To4()
		if proxy == "" || err != nil || portErr != nil || ip == nil {
			os.Exit(30)
		}
		conn, err := net.DialTimeout("tcp", proxy, 5*time.Second)
		if err != nil {
			os.Exit(31)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		greeting := make([]byte, 2)
		if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
			os.Exit(32)
		}
		if _, err := io.ReadFull(conn, greeting); err != nil || greeting[1] != 0 {
			os.Exit(32)
		}
		request := append([]byte{5, 1, 0, 1}, ip...)
		request = append(request, byte(port>>8), byte(port))
		reply := make([]byte, 10)
		if _, err := conn.Write(request); err != nil {
			os.Exit(33)
		}
		if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
			os.Exit(34)
		}
		if _, err := conn.Write([]byte("socks-ping")); err != nil {
			os.Exit(35)
		}
		echo := make([]byte, len("socks-ping"))
		if _, err := io.ReadFull(conn, echo); err != nil {
			os.Exit(36)
		}
		_, _ = os.Stdout.Write(echo)
		os.Exit(0)
	}
	if mode == "bind-once" {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			os.Exit(2)
		}
		_ = listener.Close()
		_, _ = os.Stdout.WriteString("bound")
		return
	}
	if mode == "loopback-http" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(6)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("loopback-ok")) })}
		go func() { _ = server.Serve(listener) }()
		defer func() { _ = server.Close() }()
		proxyURL, err := url.Parse(os.Getenv("HTTP_PROXY"))
		if err != nil || proxyURL.Host == "" {
			os.Exit(7)
		}
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 10 * time.Second}
		resp, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			os.Exit(8)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || string(data) != "loopback-ok" {
			os.Exit(9)
		}
		_, _ = os.Stdout.Write(data)
		return
	}
	if mode == "proxy-get" || mode == "direct-get" {
		transport := &http.Transport{Proxy: nil}
		if mode == "proxy-get" {
			proxyURL, err := url.Parse(os.Getenv("HTTP_PROXY"))
			if err != nil || proxyURL.Host == "" {
				os.Exit(14)
			}
			transport.Proxy = http.ProxyURL(proxyURL)
		}
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + addr)
		if err != nil {
			os.Exit(15)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(16)
		}
		_, _ = os.Stdout.Write(data)
		os.Exit(0)
	}
	if mode == "daemon" {
		if _, err := syscall.Setsid(); err != nil {
			os.Exit(18)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "daemon.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(19)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if mode == "setuid" {
		if err := syscall.Setuid(0); err == nil {
			_, _ = os.Stdout.WriteString("escalated")
			os.Exit(0)
		}
		os.Exit(17)
	}
	if mode == "loopback-direct" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(10)
		}
		defer func() { _ = listener.Close() }()
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			os.Exit(11)
		}
		_ = conn.Close()
		_, _ = os.Stdout.WriteString("direct-connected")
		return
	}
	if mode == "server" {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			os.Exit(2)
		}
		for {
			conn, err := listener.Accept()
			if err != nil {
				os.Exit(3)
			}
			_, _ = conn.Write([]byte("pong"))
			_ = conn.Close()
		}
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		os.Exit(4)
	}
	data := make([]byte, 4)
	_, err = conn.Read(data)
	_ = conn.Close()
	if err != nil || string(data) != "pong" {
		os.Exit(5)
	}
	_, _ = os.Stdout.Write(data)
	os.Exit(0)
}

func TestSessionDescriptorHelper(t *testing.T) {
	if os.Getenv("CARETZ_SESSION_DESCRIPTOR_HELPER") != "1" {
		return
	}
	_, _ = fmt.Fprintf(os.Stdout, "%s|", os.Getenv("CARETZ_ACTOR_ID"))
	for fd := 3; fd < 64; fd++ {
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil {
			continue
		}
		kind := stat.Mode & syscall.S_IFMT
		if kind == syscall.S_IFIFO || kind == syscall.S_IFSOCK {
			_, _ = fmt.Fprintf(os.Stdout, "%d ", fd)
		}
	}
	os.Exit(0)
}

func TestDirectSessionSharesTempAndKeepsShellStateFresh(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if _, stderr, err := s.Run(ctx, t.TempDir(), `printf shared > "$TMPDIR/value"; export SHOULD_NOT_LEAK=yes`, RunOptions{}); err != nil {
		t.Fatalf("first call: %v (%s)", err, stderr)
	}
	out, stderr, err := s.Run(ctx, t.TempDir(), `cat "$TMPDIR/value"; printf '|%s' "${SHOULD_NOT_LEAK-unset}"`, RunOptions{})
	if err != nil {
		t.Fatalf("second call: %v (%s)", err, stderr)
	}
	if out != "shared|unset" {
		t.Fatalf("output=%q", out)
	}
}

func TestSessionFilesystemGrantRecreatesRunningSupervisor(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	workspace := t.TempDir()
	if _, stderr, err := s.Run(context.Background(), workspace, `printf old > "$TMPDIR/value"`, RunOptions{}); err != nil {
		t.Fatalf("seed session: %v (%s)", err, stderr)
	}
	restarted, err := s.AddFilesystemRoots([]string{t.TempDir()}, nil)
	if err != nil || !restarted {
		t.Fatalf("AddFilesystemRoots() = (%v, %v), want restarted", restarted, err)
	}
	out, stderr, err := s.Run(context.Background(), workspace, `test ! -e "$TMPDIR/value" && printf fresh`, RunOptions{})
	if err != nil {
		t.Fatalf("recreated session: %v (%s)", err, stderr)
	}
	if !strings.Contains(out, "Session execution supervisor was recreated") || !strings.HasSuffix(out, "fresh") {
		t.Fatalf("recreated output = %q", out)
	}
	restarted, err = s.AddFilesystemRoots([]string{s.runner.config.ReadableRoots[len(s.runner.config.ReadableRoots)-1]}, nil)
	if err != nil || restarted {
		t.Fatalf("duplicate AddFilesystemRoots() = (%v, %v), want unchanged", restarted, err)
	}
}

func TestSessionFilesystemGrantBeforeStartDoesNotReportRecreation(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	restarted, err := s.AddFilesystemRoots([]string{t.TempDir()}, []string{t.TempDir()})
	if err != nil || restarted {
		t.Fatalf("AddFilesystemRoots() = (%v, %v), want no running supervisor", restarted, err)
	}
	out, stderr, err := s.Run(context.Background(), t.TempDir(), `printf ready`, RunOptions{})
	if err != nil || out != "ready" {
		t.Fatalf("first run = %q, %q, %v", out, stderr, err)
	}
}

func TestDirectSessionPassesActorEnvironmentWithoutControlDescriptors(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(`CARETZ_SESSION_DESCRIPTOR_HELPER=1 %q -test.run '^TestSessionDescriptorHelper$'`, helper)
	out, stderr, err := s.Run(context.Background(), t.TempDir(), command, RunOptions{Env: map[string]string{"CARETZ_ACTOR_ID": "agent-child"}})
	if err != nil || out != "agent-child|" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestDirectSessionPreservesDockerHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///custom/docker.sock")
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	out, stderr, err := s.Run(context.Background(), t.TempDir(), `printf %s "$DOCKER_HOST"`, RunOptions{})
	if err != nil || out != "unix:///custom/docker.sock" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestSanitizeSessionEnvGatesDockerHost(t *testing.T) {
	env := []string{"PATH=/usr/bin", "DOCKER_HOST=unix:///custom/docker.sock", sessionAllowDockerEnv + "=1"}
	denied := strings.Join(sanitizeSessionEnv(append([]string{}, env...), false), "\n")
	if strings.Contains(denied, "DOCKER_HOST=") {
		t.Fatalf("Docker host survived without permission: %q", denied)
	}
	allowed := strings.Join(sanitizeSessionEnv(append([]string{}, env...), true), "\n")
	if !strings.Contains(allowed, "DOCKER_HOST=unix:///custom/docker.sock") {
		t.Fatalf("Docker host was removed with permission: %q", allowed)
	}
	if strings.Contains(allowed, sessionAllowDockerEnv+"=") {
		t.Fatalf("internal Docker marker survived sanitization: %q", allowed)
	}
}

func TestSanitizeSessionCommandEnvPreservesExplicitCredentials(t *testing.T) {
	env := []string{
		"OPENAI_API_KEY=explicit-openai",
		"TAVILY_API_KEY=explicit-tavily",
		"DOCKER_HOST=unix:///custom/docker.sock",
		sessionSupervisorEnv + "=1",
		"_SANDBOX_PROXY_SOCKET=/tmp/proxy.sock",
	}
	got := strings.Join(sanitizeSessionCommandEnv(append([]string{}, env...), false), "\n")
	for _, want := range []string{"OPENAI_API_KEY=explicit-openai", "TAVILY_API_KEY=explicit-tavily"} {
		if !strings.Contains(got, want) {
			t.Fatalf("explicit grant %q was removed: %q", want, got)
		}
	}
	for _, forbidden := range []string{"DOCKER_HOST=", sessionSupervisorEnv + "=", "_SANDBOX_PROXY_SOCKET="} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("supervisor-only value %q survived: %q", forbidden, got)
		}
	}
}

func TestDirectSessionsHaveSeparateTemporaryState(t *testing.T) {
	a := NewRunner(Config{}).NewSession(true)
	b := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	cwd := t.TempDir()
	if _, _, err := a.Run(context.Background(), cwd, `printf private > "$TMPDIR/value"`, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	out, _, err := b.Run(context.Background(), cwd, `test ! -e "$TMPDIR/value" && printf isolated`, RunOptions{})
	if err != nil || out != "isolated" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestDirectSessionBackgroundLifecycle(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	cwd := t.TempDir()
	out, stderr, err := s.Run(context.Background(), cwd, `while :; do echo tick; sleep .02; done`, RunOptions{Background: true})
	if err != nil {
		t.Fatalf("background: %v (%s)", err, stderr)
	}
	if !strings.Contains(out, "Background command started.") {
		t.Fatalf("output=%q", out)
	}
	time.Sleep(80 * time.Millisecond)
	logPath := strings.TrimPrefix(strings.Split(out, "\n")[2], "Log: $TMPDIR")
	data, err := os.ReadFile(s.tmpDir + logPath)
	if err != nil || !strings.Contains(string(data), "tick") {
		t.Fatalf("log=%q err=%v", data, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimPrefix(strings.Split(out, "\n")[1], "PID: "))
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("background process %d survived close", pid)
	}
}

func TestDirectSessionForegroundCancellationKeepsBackground(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	cwd := t.TempDir()
	out, _, err := s.Run(context.Background(), cwd, `while :; do echo alive; sleep .02; done`, RunOptions{Background: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, _, err := s.Run(ctx, cwd, "sleep 30", RunOptions{}); err == nil {
		t.Fatal("canceled foreground succeeded")
	}
	logPath := strings.TrimPrefix(strings.Split(out, "\n")[2], "Log: $TMPDIR")
	check, stderr, err := s.Run(context.Background(), cwd, `sleep .05; tail -n 1 "$TMPDIR`+logPath+`"`, RunOptions{})
	if err != nil || !strings.Contains(check, "alive") {
		t.Fatalf("background lost: out=%q stderr=%q err=%v", check, stderr, err)
	}
}

func TestDirectSessionCloseTerminatesProcessTree(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	cwd := t.TempDir()
	out, _, err := s.Run(context.Background(), cwd, `sh -c 'sleep 30 & echo $!; wait'`, RunOptions{Background: true})
	if err != nil {
		t.Fatal(err)
	}
	logPath := strings.TrimPrefix(strings.Split(out, "\n")[2], "Log: $TMPDIR")
	var childPID int
	deadline := time.Now().Add(time.Second)
	for childPID == 0 && time.Now().Before(deadline) {
		data, _ := os.ReadFile(s.tmpDir + logPath)
		childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("child PID was not logged")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(childPID, 0); err == nil {
		t.Fatalf("descendant %d survived session close", childPID)
	}
}

func TestDirectSessionCloseTerminatesDescendantAfterForegroundShellExits(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	cwd := t.TempDir()
	out, stderr, err := s.Run(context.Background(), cwd, `sleep 30 >/dev/null 2>&1 & echo $!`, RunOptions{})
	if err != nil {
		t.Fatalf("foreground command: %v (%s)", err, stderr)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("descendant PID %q: %v", out, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err = syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("foreground descendant %d survived session close", pid)
	}
}

func TestDirectSessionBoundsCompletedBackgroundLog(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	cwd := t.TempDir()
	out, _, err := s.Run(context.Background(), cwd, `dd if=/dev/zero bs=1048576 count=2 2>/dev/null | tr '\0' x`, RunOptions{Background: true})
	if err != nil {
		t.Fatal(err)
	}
	logPath := strings.TrimPrefix(strings.Split(out, "\n")[2], "Log: $TMPDIR")
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, statErr := os.Stat(s.tmpDir + logPath)
		if statErr == nil && info.Size() == backgroundLogLimit {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log size did not settle at %d: info=%v err=%v", backgroundLogLimit, info, statErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDirectSessionBackgroundLimitCountsOnlyRunningJobs(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	cwd := t.TempDir()
	firstPID := ""
	for i := 0; i < maxBackgroundJobs; i++ {
		out, _, err := s.Run(context.Background(), cwd, "sleep 30", RunOptions{Background: true})
		if err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
		if i == 0 {
			firstPID = strings.TrimPrefix(strings.Split(out, "\n")[1], "PID: ")
		}
	}
	if _, _, err := s.Run(context.Background(), cwd, "sleep 30", RunOptions{Background: true}); err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Fatalf("limit error=%v", err)
	}
	if _, _, err := s.Run(context.Background(), cwd, "kill -TERM -- -"+firstPID, RunOptions{}); err != nil {
		t.Fatalf("stop one: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, err := s.Run(context.Background(), cwd, "true", RunOptions{Background: true}); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("completed job did not release slot")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDirectSessionRecreatesAfterSupervisorFailure(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	t.Cleanup(func() { _ = s.Close() })
	cwd := t.TempDir()
	if _, _, err := s.Run(context.Background(), cwd, "true", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	out, _, err := s.Run(context.Background(), cwd, "printf recovered", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "supervisor was recreated") || !strings.Contains(out, "recovered") {
		t.Fatalf("recovery output=%q", out)
	}
}

// This documents the stated best-effort cleanup limitation when native,
// unsandboxed execution has no cgroup or PID namespace and a command
// deliberately escapes its process group.
func TestDirectSessionDaemonizedDescendantLimitation(t *testing.T) {
	s := NewRunner(Config{}).NewSession(true)
	cwd := t.TempDir()
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	daemon := fmt.Sprintf(`CARETZ_SESSION_TEST_HELPER=daemon %q -test.run '^TestSessionNetworkHelper$' >/dev/null 2>&1 &`, helper)
	if _, _, err := s.Run(context.Background(), cwd, daemon, RunOptions{Background: true}); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(s.tmpDir, "daemon.pid"))
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		_ = s.Close()
		t.Fatal("daemon PID was not recorded")
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("expected deliberately detached process to demonstrate best-effort limitation: %v", err)
	}
}

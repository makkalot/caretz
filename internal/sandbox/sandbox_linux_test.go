//go:build linux

package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func requireBwrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("bwrap (bubblewrap) is required but not installed")
	}
}

// parity: linux.bwrap-feature-probe
func TestInspectBubblewrapSessionExecution(t *testing.T) {
	tests := []struct {
		name    string
		help    string
		version string
		want    bool
	}{
		{name: "supported", help: "usage: bwrap [OPTIONS]\n  --disable-userns Disable further user namespaces\n", want: true},
		{name: "older version", help: "usage: bwrap [OPTIONS]\n", version: "bubblewrap 0.4.0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(args ...string) ([]byte, error) {
				if len(args) == 1 && args[0] == "--version" {
					return []byte(tt.version), nil
				}
				return []byte(tt.help), nil
			}
			supported, version, err := inspectBubblewrapSessionExecution(run)
			if err != nil {
				t.Fatalf("inspectBubblewrapSessionExecution() error = %v", err)
			}
			if supported != tt.want {
				t.Fatalf("inspectBubblewrapSessionExecution() supported = %v, want %v", supported, tt.want)
			}
			if !tt.want && version != "bubblewrap 0.4.0" {
				t.Fatalf("inspectBubblewrapSessionExecution() version = %q", version)
			}
		})
	}
}

// parity: linux.bwrap-feature-probe
func TestSessionBwrapPrefixUsesSupportedHardening(t *testing.T) {
	if args := sessionBwrapPrefix(false); containsArg(args, "--disable-userns") {
		t.Fatalf("legacy args unexpectedly contain --disable-userns: %v", args)
	}
	if args := sessionBwrapPrefix(true); !containsArg(args, "--disable-userns") {
		t.Fatalf("supported args omit --disable-userns: %v", args)
	}
}

// parity: policy.docker-socket
func TestSessionBwrapRunMountFollowsDockerPermission(t *testing.T) {
	base := bwrapArgsForContainerMode("/work", "/caretz", false, nil, false, nil, nil, "", false)
	withoutDocker := removeBind(append([]string{}, base...), "/run")
	if containsSequence(withoutDocker, "--ro-bind", "/run", "/run") {
		t.Fatalf("session args expose /run without Docker permission: %v", withoutDocker)
	}
	if !containsSequence(base, "--ro-bind", "/run", "/run") {
		t.Fatalf("session args omit /run with Docker permission: %v", base)
	}
}

// parity: policy.hidden-paths
func TestAppendHiddenPathMasks(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	if err := os.WriteFile(file, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	hiddenDir := filepath.Join(dir, "sessions")
	if err := os.Mkdir(hiddenDir, 0700); err != nil {
		t.Fatal(err)
	}
	visible := filepath.Join(dir, "visible")
	if err := os.WriteFile(visible, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	got, err := appendHiddenPathMasks(nil, []string{file, hiddenDir, missing}, dir, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\x00")
	for _, want := range []string{"--ro-bind\x00/dev/null\x00" + file, "--perms\x00000\x00--tmpfs\x00" + hiddenDir} {
		if !strings.Contains(joined, want) {
			t.Fatalf("masks=%q missing %q", got, want)
		}
	}
	for _, path := range []string{dir, visible, missing} {
		if containsArg(got, path) {
			t.Fatalf("visible or absent path was mounted: %q", got)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("hidden path was created on host: %v", err)
	}
}

// parity: policy.hidden-paths policy.read-only
func TestAppendHiddenPathMasksPreservesReadOnlyAndUnmappedPolicy(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent-parent", "missing")
	got, err := appendHiddenPathMasks(nil, []string{missing}, dir, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("missing parent added mounts: %q", got)
	}

	unmapped := filepath.Join(t.TempDir(), "missing")
	got, err = appendHiddenPathMasks(nil, []string{unmapped}, dir, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unmapped hidden path added mounts: %q", got)
	}
}

// parity: policy.hidden-paths
func TestHiddenPathDiscoveryTracksAliasesAndTypes(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "secret")
	alias := filepath.Join(cwd, "alias")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	masks, err := discoverHiddenPathMasks([]string{alias, target}, cwd, nil, nil)
	if err != nil || len(masks) != 1 || masks[0].path != target || masks[0].directory {
		t.Fatalf("file aliases=%#v err=%v", masks, err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	current, err := discoverHiddenPathMasks([]string{alias}, cwd, nil, nil)
	if err != nil || len(current) != 1 || !current[0].directory {
		t.Fatalf("directory alias=%#v err=%v", current, err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(cwd, "other")
	if err := os.WriteFile(other, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	current, err = discoverHiddenPathMasks([]string{alias}, cwd, nil, nil)
	if err != nil || len(current) != 1 || current[0].path != other {
		t.Fatalf("retargeted alias=%#v err=%v", current, err)
	}
}

// parity: session.control-channel policy.dangerous-operations
func TestSessionControlChannelRecoverySyscallsAreBlocked(t *testing.T) {
	blocked := blockedSyscalls()
	for _, want := range []uint32{uint32(syscall.SYS_PTRACE), uint32(unix.SYS_PROCESS_VM_READV), uint32(unix.SYS_PROCESS_VM_WRITEV), uint32(unix.SYS_PIDFD_GETFD)} {
		found := false
		for _, got := range blocked {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("syscall %d is not blocked", want)
		}
	}
}

func TestSessionLinuxControlAttackHelper(t *testing.T) {
	if os.Getenv("CARETZ_SESSION_CONTROL_ATTACK") != "1" {
		return
	}
	if _, err := os.Readlink("/proc/1/fd/0"); err == nil {
		os.Exit(10)
	}
	if err := unix.PtraceAttach(1); err == nil {
		os.Exit(11)
	}
	local := []byte{0}
	if _, err := unix.ProcessVMReadv(1, []unix.Iovec{{Base: &local[0], Len: 1}}, []unix.RemoteIovec{{Base: 0, Len: 1}}, 0); err == nil {
		os.Exit(12)
	}
	pidfd, err := unix.PidfdOpen(1, 0)
	if err == nil {
		defer func() { _ = unix.Close(pidfd) }()
		if dup, err := unix.PidfdGetfd(pidfd, 0, 0); err == nil {
			_ = unix.Close(dup)
			os.Exit(13)
		}
	}
	_, _ = os.Stdout.WriteString("blocked")
	os.Exit(0)
}

// parity: policy.network-modes
func TestBwrapArgsNetworkMode(t *testing.T) {
	restricted := bwrapArgs("/work", "/caretz-test", false, nil, false, nil, nil, "")
	if !containsArg(restricted, "--unshare-net") {
		t.Fatalf("expected restricted args to include --unshare-net: %v", restricted)
	}

	unrestricted := bwrapArgs("/work", "/caretz-test", true, nil, false, nil, nil, "")
	if containsArg(unrestricted, "--unshare-net") {
		t.Fatalf("expected unrestricted args to omit --unshare-net: %v", unrestricted)
	}
}

// parity: env.go-state
func TestBwrapArgsKeepsGoStateInCaretzScratch(t *testing.T) {
	args := bwrapArgs("/work", "/caretz-test", false, nil, false, nil, nil, "")
	for _, want := range [][]string{
		{"--setenv", "GOCACHE", "/work/.caretz/scratch/go-build"},
		{"--setenv", "GOPATH", "/work/.caretz/scratch/go"},
	} {
		if !containsSequence(args, want...) {
			t.Fatalf("expected args to contain %q: %v", want, args)
		}
	}
}

// parity: linux.container-mode
func TestBwrapArgsHostModeUsesFullIsolation(t *testing.T) {
	args := bwrapArgsForContainerMode("/work", "/caretz-test", false, nil, false, nil, nil, "", false)

	if !containsArg(args, "--unshare-cgroup") {
		t.Fatalf("expected host args to include --unshare-cgroup: %v", args)
	}
	if !containsSequence(args, "--ro-bind", "/sys", "/sys") {
		t.Fatalf("expected host args to require /sys read-only bind: %v", args)
	}
	if containsSequence(args, "--ro-bind-try", "/sys", "/sys") {
		t.Fatalf("expected host args not to use optional /sys bind: %v", args)
	}
}

// parity: linux.container-mode
func TestBwrapArgsContainerModeUsesDockerCompatibleIsolation(t *testing.T) {
	args := bwrapArgsForContainerMode("/work", "/caretz-test", false, nil, false, nil, nil, "", true)

	if containsArg(args, "--unshare-cgroup") {
		t.Fatalf("expected container args to omit --unshare-cgroup: %v", args)
	}
	if !containsSequence(args, "--ro-bind-try", "/sys", "/sys") {
		t.Fatalf("expected container args to use optional /sys bind: %v", args)
	}
	if containsSequence(args, "--ro-bind", "/sys", "/sys") {
		t.Fatalf("expected container args not to require /sys bind: %v", args)
	}
}

// parity: policy.extra-binaries
func TestBwrapArgsExtraBinaries(t *testing.T) {
	args := bwrapArgs("/work", "/caretz-test", false, []string{"/custom/bin/rg", "/usr/bin/fd"}, false, nil, nil, "")

	if !containsSequence(args, "--ro-bind-try", "/custom/bin/rg", "/custom/bin/rg") {
		t.Fatalf("expected args to bind extra binary outside default binds: %v", args)
	}
	if containsSequence(args, "--ro-bind-try", "/usr/bin/fd", "/usr/bin/fd") {
		t.Fatalf("expected args not to bind extra binary already covered by /usr: %v", args)
	}
}

// parity: policy.read-only
func TestBwrapArgsReadOnlyWorkDir(t *testing.T) {
	writable := bwrapArgs("/work", "/caretz-test", false, nil, false, nil, nil, "")
	if !containsSequence(writable, "--bind", "/work", "/work") {
		t.Fatalf("expected writable args to bind workdir read-write: %v", writable)
	}
	if containsSequence(writable, "--ro-bind", "/work", "/work") {
		t.Fatalf("expected writable args not to ro-bind workdir: %v", writable)
	}

	readOnly := bwrapArgs("/work", "/caretz-test", false, nil, true, nil, nil, "")
	if !containsSequence(readOnly, "--ro-bind", "/work", "/work") {
		t.Fatalf("expected read-only args to ro-bind workdir: %v", readOnly)
	}
	if containsSequence(readOnly, "--bind", "/work", "/work") {
		t.Fatalf("expected read-only args not to bind workdir read-write: %v", readOnly)
	}
}

// parity: policy.writable-roots
func TestBwrapArgsWritableRoots(t *testing.T) {
	writable := bwrapArgs("/work", "/caretz-test", false, nil, false, nil, []string{"/other"}, "")
	if !containsSequence(writable, "--bind", "/other", "/other") {
		t.Fatalf("expected writable args to bind extra writable root read-write: %v", writable)
	}

	readOnly := bwrapArgs("/work", "/caretz-test", false, nil, true, nil, []string{"/other"}, "")
	if containsSequence(readOnly, "--bind", "/other", "/other") {
		t.Fatalf("expected read-only args not to bind extra writable root read-write: %v", readOnly)
	}
	if !containsSequence(readOnly, "--ro-bind", "/other", "/other") {
		t.Fatalf("expected read-only args to bind extra writable root read-only: %v", readOnly)
	}
}

// parity: policy.readable-roots policy.filesystem
func TestBwrapArgsReadableRoots(t *testing.T) {
	writable := bwrapArgs("/work", "/caretz-test", false, nil, false, []string{"/readonly"}, []string{"/other"}, "")
	if !containsSequence(writable, "--ro-bind", "/readonly", "/readonly") {
		t.Fatalf("expected args to bind readable root read-only: %v", writable)
	}
	if containsSequence(writable, "--bind", "/readonly", "/readonly") {
		t.Fatalf("expected args not to bind readable root read-write: %v", writable)
	}

	readOnly := bwrapArgs("/work", "/caretz-test", false, nil, true, []string{"/readonly"}, nil, "")
	if !containsSequence(readOnly, "--ro-bind", "/readonly", "/readonly") {
		t.Fatalf("expected read-only args to bind readable root read-only: %v", readOnly)
	}
}

// parity: policy.proxy-endpoint
func TestBwrapArgsProxySocketDir(t *testing.T) {
	args := bwrapArgs("/work", "/caretz-test", false, nil, false, nil, nil, "/tmp/caretz-proxy")
	if !containsSequence(args, "--bind", "/tmp/caretz-proxy", "/tmp/caretz-proxy") {
		t.Fatalf("expected args to bind proxy socket dir: %v", args)
	}
}

// parity: linux.container-mode
func TestSeccompDisabledInsideContainer(t *testing.T) {
	oldStat, oldReadFile := containerStat, containerReadFile
	t.Cleanup(func() {
		containerStat = oldStat
		containerReadFile = oldReadFile
	})
	containerStat = func(path string) (os.FileInfo, error) {
		return nil, fs.ErrNotExist
	}
	containerReadFile = func(path string) ([]byte, error) {
		return []byte("0::/docker/abc123\n"), nil
	}

	if seccompEnabled() {
		t.Fatal("seccompEnabled() = true in container, want false")
	}
}

// parity: linux.container-mode
func TestSeccompDisabledInsideContainerFromSentinelFile(t *testing.T) {
	oldStat, oldReadFile := containerStat, containerReadFile
	t.Cleanup(func() {
		containerStat = oldStat
		containerReadFile = oldReadFile
	})
	containerStat = func(path string) (os.FileInfo, error) {
		if path == "/run/.containerenv" {
			return fakeFileInfo{name: ".containerenv"}, nil
		}
		return nil, fs.ErrNotExist
	}
	containerReadFile = func(path string) ([]byte, error) {
		return nil, fs.ErrNotExist
	}

	if seccompEnabled() {
		t.Fatal("seccompEnabled() = true in container, want false")
	}
}

// parity: linux.container-mode
func TestSeccompDisabledInsideContainerFromMountinfo(t *testing.T) {
	oldStat, oldReadFile := containerStat, containerReadFile
	t.Cleanup(func() {
		containerStat = oldStat
		containerReadFile = oldReadFile
	})
	containerStat = func(path string) (os.FileInfo, error) {
		return nil, fs.ErrNotExist
	}
	containerReadFile = func(path string) ([]byte, error) {
		switch path {
		case "/proc/1/cgroup", "/proc/self/cgroup":
			return []byte("0::/\n"), nil
		case "/proc/self/mountinfo":
			return []byte("28 23 0:26 /var/lib/docker/containers/abc123/hostname /etc/hostname rw,relatime - ext4 /dev/sda1 rw\n"), nil
		default:
			return nil, fs.ErrNotExist
		}
	}

	if seccompEnabled() {
		t.Fatal("seccompEnabled() = true in container, want false")
	}
}

// parity: linux.container-mode
func TestSeccompEnabledOutsideContainer(t *testing.T) {
	oldStat, oldReadFile := containerStat, containerReadFile
	t.Cleanup(func() {
		containerStat = oldStat
		containerReadFile = oldReadFile
	})
	containerStat = func(path string) (os.FileInfo, error) {
		return nil, fs.ErrNotExist
	}
	containerReadFile = func(path string) ([]byte, error) {
		switch path {
		case "/proc/1/cgroup", "/proc/self/cgroup":
			return []byte("0::/user.slice/session.scope\n"), nil
		case "/proc/self/mountinfo":
			return []byte("23 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n"), nil
		default:
			return nil, fs.ErrNotExist
		}
	}

	if !seccompEnabled() {
		t.Fatal("seccompEnabled() = false outside container, want true")
	}
}

// parity: env.docker-host
func TestSandboxChildEnvPreservesDockerHost(t *testing.T) {
	env := sandboxChildEnv("docker info", []string{
		"HOME=/home/test",
		"TERM=xterm",
		"LANG=C.UTF-8",
		"DOCKER_HOST=unix:///custom/docker.sock",
	}, statMissing, nil, true, "", "")

	if got := envValue(env, "DOCKER_HOST"); got != "unix:///custom/docker.sock" {
		t.Fatalf("DOCKER_HOST = %q, want parent value", got)
	}
}

// parity: env.docker-host
func TestSandboxChildEnvAutoSetsDockerHostForRunSocket(t *testing.T) {
	env := sandboxChildEnv("docker info", []string{
		"HOME=/home/test",
		"TERM=xterm",
		"LANG=C.UTF-8",
	}, func(path string) (os.FileInfo, error) {
		if path == "/run/docker.sock" {
			return fakeFileInfo{name: "docker.sock"}, nil
		}
		return nil, fs.ErrNotExist
	}, nil, true, "", "")

	if got := envValue(env, "DOCKER_HOST"); got != "unix:///run/docker.sock" {
		t.Fatalf("DOCKER_HOST = %q, want unix:///run/docker.sock", got)
	}
}

// parity: env.docker-host
func TestSandboxChildEnvOmitsDockerHostWhenSocketMissing(t *testing.T) {
	env := sandboxChildEnv("docker info", []string{
		"HOME=/home/test",
		"TERM=xterm",
		"LANG=C.UTF-8",
	}, statMissing, nil, true, "", "")

	if got := envValue(env, "DOCKER_HOST"); got != "" {
		t.Fatalf("DOCKER_HOST = %q, want empty", got)
	}
}

// parity: env.docker-host
func TestSandboxChildEnvAutoSetsDockerHostForWritableRun(t *testing.T) {
	env := sandboxChildEnv("docker info", []string{
		"HOME=/home/test",
		"TERM=xterm",
		"LANG=C.UTF-8",
	}, statMissing, []string{"/run"}, true, "", "")

	if got := envValue(env, "DOCKER_HOST"); got != "unix:///run/docker.sock" {
		t.Fatalf("DOCKER_HOST = %q, want unix:///run/docker.sock", got)
	}
}

// parity: env.docker-host
func TestSandboxChildEnvOmitsDockerHostByDefault(t *testing.T) {
	env := sandboxChildEnv("docker info", []string{
		"HOME=/home/test",
		"TERM=xterm",
		"LANG=C.UTF-8",
		"DOCKER_HOST=unix:///custom/docker.sock",
	}, func(path string) (os.FileInfo, error) {
		if path == "/run/docker.sock" {
			return fakeFileInfo{name: "docker.sock"}, nil
		}
		return nil, fs.ErrNotExist
	}, []string{"/run"}, false, "", "")

	if got := envValue(env, "DOCKER_HOST"); got != "" {
		t.Fatalf("DOCKER_HOST = %q, want empty when Docker is not allowed", got)
	}
}

// parity: env.proxy
func TestSandboxChildEnvSetsProxy(t *testing.T) {
	env := sandboxChildEnv("curl https://example.com", []string{
		"HOME=/home/test",
		"TERM=xterm",
		"LANG=C.UTF-8",
	}, statMissing, nil, false, "/tmp/proxy/http.sock", "/tmp/proxy/socks.sock")

	if got := envValue(env, "_SANDBOX_PROXY_SOCKET"); got != "/tmp/proxy/http.sock" {
		t.Fatalf("_SANDBOX_PROXY_SOCKET = %q, want socket path", got)
	}
	if got := envValue(env, "_SANDBOX_SOCKS_SOCKET"); got != "/tmp/proxy/socks.sock" {
		t.Fatalf("_SANDBOX_SOCKS_SOCKET = %q", got)
	}
	if got := envValue(env, "HTTPS_PROXY"); got != "http://"+sandboxHTTPProxyAddr {
		t.Fatalf("HTTPS_PROXY = %q, want proxy addr", got)
	}
	if got := envValue(env, "ALL_PROXY"); got != "http://"+sandboxHTTPProxyAddr {
		t.Fatalf("ALL_PROXY = %q, want HTTP proxy", got)
	}
	if got := envValue(env, socksProxyEnv); got != "socks5h://"+sandboxSOCKSProxyAddr {
		t.Fatalf("%s = %q", socksProxyEnv, got)
	}
	if got := envValue(env, "GIT_SSH_COMMAND"); !strings.Contains(got, socksConnectArg) || !strings.Contains(envValue(env, sshProxyCommandEnv), socksConnectArg) {
		t.Fatalf("Git-over-SSH not routed through the SOCKS helper: GIT_SSH_COMMAND=%q", got)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func statMissing(path string) (os.FileInfo, error) {
	return nil, fmt.Errorf("%w: %s", fs.ErrNotExist, path)
}

type fakeFileInfo struct {
	name  string
	isDir bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0600 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.isDir }
func (f fakeFileInfo) Sys() any           { return nil }

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsSequence(args []string, want ...string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j := range want {
			if args[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// parity: session.control-channel policy.dangerous-operations
func TestSandboxLinuxSessionHardeningIntegration(t *testing.T) {
	requireSandboxIntegration(t)
	requireBwrap(t)
	cwd := t.TempDir()
	s := NewRunner(Config{}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	out, stderr, err := s.Run(t.Context(), cwd, `awk '/^Seccomp:/{print $2}' /proc/self/status`, RunOptions{})
	if err != nil || strings.TrimSpace(out) != "2" {
		t.Fatalf("child seccomp mode: out=%q stderr=%q err=%v", out, stderr, err)
	}
	out, stderr, err = s.Run(t.Context(), cwd, `printf shared > "$TMPDIR/value"; test "$TMPDIR" = /tmp && cat /tmp/value`, RunOptions{})
	if err != nil || out != "shared" {
		t.Fatalf("literal /tmp: out=%q stderr=%q err=%v", out, stderr, err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	attack := fmt.Sprintf("CARETZ_SESSION_CONTROL_ATTACK=1 %q -test.run '^TestSessionLinuxControlAttackHelper$'", helper)
	out, stderr, err = s.Run(t.Context(), cwd, attack, RunOptions{})
	if err != nil || out != "blocked" {
		t.Fatalf("control-channel attack: out=%q stderr=%q err=%v", out, stderr, err)
	}
}

// parity: env.minimal
func TestSandboxChildEnvIsMinimal(t *testing.T) {
	env := sandboxChildEnv("true", []string{
		"HOME=/home/test",
		"TERM=xterm",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"OPENAI_API_KEY=secret",
		"UNRELATED_HOST_VALUE=hidden",
		"LD_PRELOAD=/evil.so",
		"DOCKER_HOST=unix:///custom/docker.sock",
	}, statMissing, nil, false, "", "")
	for _, key := range []string{"OPENAI_API_KEY", "UNRELATED_HOST_VALUE", "LD_PRELOAD", "DOCKER_HOST", "HTTPS_PROXY"} {
		if got := envValue(env, key); got != "" {
			t.Fatalf("%s=%q leaked into sandbox environment", key, got)
		}
	}
	for key, want := range map[string]string{"TERM": "xterm", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"} {
		if got := envValue(env, key); got != want {
			t.Fatalf("%s=%q want %q", key, got, want)
		}
	}
	for _, key := range []string{"LD_PRELOAD", "BASH_ENV", "_SANDBOX_CMD"} {
		for _, entry := range sanitizeEnv([]string{key + "=x", "KEEP=1"}) {
			if strings.HasPrefix(entry, key+"=") {
				t.Fatalf("sanitizeEnv kept %s", key)
			}
		}
	}
}

// parity: linux.var-run-link
func TestBwrapArgsRecreateVarRunLink(t *testing.T) {
	original := hostVarRunLink
	t.Cleanup(func() { hostVarRunLink = original })

	hostVarRunLink = func() string { return "../run" }
	args := bwrapArgsForContainerMode("/work", "/caretz-test", false, nil, false, nil, nil, "", false)
	if !containsSequence(args, "--symlink", "../run", "/var/run") {
		t.Fatalf("expected /var/run link: %v", args)
	}

	for target, want := range map[string]string{"../run": "/run", "/run": "/run", "/run/": "/run", "/srv/run": "/srv/run"} {
		if got := varRunLinkTarget(target); got != want {
			t.Fatalf("varRunLinkTarget(%q) = %q, want %q", target, got, want)
		}
	}

	hostVarRunLink = func() string { return "" }
	args = bwrapArgsForContainerMode("/work", "/caretz-test", false, nil, false, nil, nil, "", false)
	if containsArg(args, "--symlink") {
		t.Fatalf("unexpected /var/run link without a host link: %v", args)
	}
}

// parity: policy.docker-socket
func TestMaskHostSocketsHidesDaemonSockets(t *testing.T) {
	originalDocker, originalControl := hostDockerSockets, hostControlSockets
	t.Cleanup(func() { hostDockerSockets, hostControlSockets = originalDocker, originalControl })
	hostDockerSockets = func() []string { return []string{"/run/docker.sock", "/run/user/1000/docker.sock"} }
	hostControlSockets = func() []string { return []string{"/run/user/1000/bus", "/run/dbus/system_bus_socket"} }
	args := maskHostSockets(nil, false, false)
	for _, socket := range []string{"/run/docker.sock", "/run/user/1000/docker.sock", "/run/user/1000/bus", "/run/dbus/system_bus_socket"} {
		if !containsSequence(args, "--ro-bind", "/dev/null", socket) {
			t.Fatalf("socket %s not masked: %v", socket, args)
		}
	}
	args = maskHostSockets(nil, true, false)
	if containsSequence(args, "--ro-bind", "/dev/null", "/run/docker.sock") || !containsSequence(args, "--ro-bind", "/dev/null", "/run/user/1000/bus") {
		t.Fatalf("allowDocker should expose only docker: %v", args)
	}
}

// parity: linux.system-bus
func TestMaskHostSocketsAllowSystemBusOnlyWithoutRoot(t *testing.T) {
	originalControl, originalEUID := hostControlSockets, currentEUID
	t.Cleanup(func() { hostControlSockets, currentEUID = originalControl, originalEUID })
	hostControlSockets = func() []string {
		return []string{"/run/user/1000/bus", "/run/dbus/system_bus_socket", "/run/systemd/private"}
	}

	currentEUID = func() int { return 1000 }
	args := maskHostSockets(nil, true, true)
	if containsSequence(args, "--ro-bind", "/dev/null", "/run/dbus/system_bus_socket") {
		t.Fatalf("system bus masked for a non-root user: %v", args)
	}
	for _, socket := range []string{"/run/user/1000/bus", "/run/systemd/private"} {
		if !containsSequence(args, "--ro-bind", "/dev/null", socket) {
			t.Fatalf("socket %s not masked: %v", socket, args)
		}
	}

	currentEUID = func() int { return 0 }
	args = maskHostSockets(nil, true, true)
	if !containsSequence(args, "--ro-bind", "/dev/null", "/run/dbus/system_bus_socket") {
		t.Fatalf("system bus exposed to root: %v", args)
	}
}

// parity: linux.gpu-devices
func TestAppendGPUDevicesBindsHostNodesOnlyWhenAllowed(t *testing.T) {
	original := hostGPUDevices
	t.Cleanup(func() { hostGPUDevices = original })
	hostGPUDevices = func() []string { return []string{"/dev/nvmap", "/dev/nvhost-ctrl"} }
	if args := appendGPUDevices(nil, false); len(args) != 0 {
		t.Fatalf("GPU devices bound without allowGPU: %v", args)
	}
	args := appendGPUDevices(nil, true)
	for _, device := range []string{"/dev/nvmap", "/dev/nvhost-ctrl"} {
		if !containsSequence(args, "--dev-bind", device, device) {
			t.Fatalf("device %s not bound: %v", device, args)
		}
	}
}

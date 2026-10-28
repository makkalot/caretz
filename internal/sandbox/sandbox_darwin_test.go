//go:build darwin

package sandbox

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDarwinProfile(readOnly bool, readable, writable, hidden []string, proxy proxyEndpoints) string {
	return generateSandboxProfileForRun("/work", "/private/tmp/caretz-private", []string{`/opt/path"with\chars/rg`}, false, false, false, readOnly, readable, writable, hidden, proxy)
}

func darwinEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

// parity: policy.filesystem policy.readable-roots policy.writable-roots
func TestSandboxProfileFilesystemPolicy(t *testing.T) {
	profile := testDarwinProfile(false, []string{"/read"}, []string{"/write"}, nil, proxyEndpoints{})
	for _, want := range []string{
		"(deny file-read*)", "(deny file-write*)",
		`(allow file-read* (subpath "/work"))`,
		`(allow file-read* (subpath "/read"))`,
		`(allow file-read* (subpath "/write"))`,
		`(allow file-write* (subpath "/work"))`,
		`(allow file-write* (subpath "/write"))`,
		`(allow file-write* (subpath "/private/tmp/caretz-private"))`,
		`(allow file-read-metadata (literal "/"))`,
		`(allow file-read* (subpath "/System"))`,
		`(allow file-read* (subpath "/Applications"))`,
		`(allow file-read* (literal "/etc"))`,
		`(allow file-read* (literal "/var/db/xcode_select_link"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing %q", want)
		}
	}
	if strings.Contains(profile, `(allow file-write* (subpath "/private/tmp"))`) {
		t.Fatal("profile grants the shared system temp directory")
	}
	if strings.Contains(profile, `(allow file-write* (subpath "/read"))`) {
		t.Fatal("readable root is writable")
	}
}

// parity: policy.read-only
func TestSandboxProfileReadOnly(t *testing.T) {
	profile := testDarwinProfile(true, nil, []string{"/write"}, nil, proxyEndpoints{})
	for _, denied := range []string{`(allow file-write* (subpath "/work"))`, `(allow file-write* (subpath "/write"))`} {
		if strings.Contains(profile, denied) {
			t.Fatalf("read-only profile contains %q", denied)
		}
	}
	if !strings.Contains(profile, `(allow file-write* (subpath "/private/tmp/caretz-private"))`) {
		t.Fatal("read-only profile must retain private temporary storage")
	}
}

// parity: policy.hidden-paths
func TestSandboxProfileHiddenPathsAreFinalAndCoverFutureDirectories(t *testing.T) {
	hidden := "/work/.caretz/missing"
	profile := testDarwinProfile(false, nil, nil, []string{hidden}, proxyEndpoints{})
	literal := `(deny file-read* file-write* (literal "` + hidden + `"))`
	subpath := `(deny file-read* file-write* (subpath "` + hidden + `"))`
	if !strings.Contains(profile, literal) || !strings.Contains(profile, subpath) {
		t.Fatalf("hidden rules missing:\n%s", profile)
	}
	if strings.LastIndex(profile, literal) < strings.LastIndex(profile, `(allow file-write* (subpath "/work"))`) {
		t.Fatal("hidden deny must follow broader workspace grants")
	}
}

// parity: policy.path-escaping policy.extra-binaries
func TestSandboxProfileEscapesLiteralPaths(t *testing.T) {
	profile := testDarwinProfile(false, nil, nil, nil, proxyEndpoints{})
	if !strings.Contains(profile, `/opt/path\"with\\chars/rg`) {
		t.Fatalf("escaped binary path missing:\n%s", profile)
	}
	if strings.Count(profile, "(regex ") != len(pathVariants(os.TempDir())) {
		t.Fatal("profile contains unexpected regex path filters")
	}
	if !strings.Contains(profile, `xcrun_db-[^/]+$`) {
		t.Fatal("profile does not permit Apple's per-command xcrun cache file")
	}
	if !strings.Contains(profile, fmt.Sprintf(`(allow file-read* file-write* (literal "%s"))`, filepath.Join(pathVariants(os.TempDir())[0], "xcrun_db"))) {
		t.Fatal("profile does not permit reading Apple's completed xcrun cache file")
	}
}

// parity: policy.network-modes policy.proxy-endpoint
func TestSandboxProfileNetworkModes(t *testing.T) {
	restricted := testDarwinProfile(false, nil, nil, nil, proxyEndpoints{HTTP: "127.0.0.1:43127", SOCKS: "127.0.0.1:43128"})
	if !strings.Contains(restricted, "(deny network*)") ||
		!strings.Contains(restricted, `(allow network-outbound (remote tcp "localhost:43127"))`) ||
		!strings.Contains(restricted, `(allow network-outbound (remote tcp "localhost:43128"))`) {
		t.Fatalf("restricted proxy policy missing:\n%s", restricted)
	}
	var nonSocket []string
	for _, line := range strings.Split(restricted, "\n") {
		if !strings.Contains(line, "unix-socket") {
			nonSocket = append(nonSocket, line)
		}
	}
	for _, oldGrant := range []string{"network-bind", "network-inbound", `localhost:*`, "*:80", "*:443", "*:22", "mDNSResponder"} {
		if strings.Contains(strings.Join(nonSocket, "\n"), oldGrant) {
			t.Fatalf("restricted profile retains broad network grant %q", oldGrant)
		}
	}
	loopback := generateSandboxProfileForRun("/work", "/tmp/private", nil, false, true, false, false, nil, nil, nil, proxyEndpoints{HTTP: "127.0.0.1:43127"})
	for _, grant := range []string{`(allow network-bind (local tcp "localhost:*"))`, `(allow network-inbound (local tcp "localhost:*"))`, `(allow network-outbound (remote tcp "localhost:*"))`} {
		if !strings.Contains(loopback, grant) {
			t.Fatalf("loopback profile missing %q:\n%s", grant, loopback)
		}
	}
	unrestricted := generateSandboxProfileForRun("/work", "/tmp/private", nil, true, false, false, false, nil, nil, nil, proxyEndpoints{})
	if !strings.Contains(unrestricted, "(allow network*)") || strings.Contains(unrestricted, "(deny network*)") {
		t.Fatalf("unexpected unrestricted authorizationPolicy:\n%s", unrestricted)
	}
}

// parity: policy.unix-sockets
func TestSandboxProfileUnixSocketsFollowWritableRoots(t *testing.T) {
	bind := func(path string) string {
		return `(allow network-bind network-inbound (local unix-socket (subpath "` + path + `")))`
	}
	connect := func(path string) string {
		return `(allow network-outbound (remote unix-socket (subpath "` + path + `")))`
	}
	profile := testDarwinProfile(false, []string{"/read"}, []string{"/write"}, []string{"/work/.secret"}, proxyEndpoints{})
	for _, path := range []string{"/private/tmp/caretz-private", "/work", "/write"} {
		if !strings.Contains(profile, bind(path)) || !strings.Contains(profile, connect(path)) {
			t.Fatalf("socket grant missing for %s:\n%s", path, profile)
		}
	}
	if strings.Contains(profile, bind("/read")) || strings.Contains(profile, connect("/read")) {
		t.Fatal("readable-only root grants Unix sockets")
	}
	deny := `(deny network-outbound (remote unix-socket (subpath "/work/.secret")))`
	if strings.LastIndex(profile, deny) < strings.LastIndex(profile, connect("/work")) {
		t.Fatal("hidden path socket deny must follow workspace socket grant")
	}
	readOnly := testDarwinProfile(true, nil, []string{"/write"}, nil, proxyEndpoints{})
	if strings.Contains(readOnly, bind("/work")) || strings.Contains(readOnly, bind("/write")) || !strings.Contains(readOnly, bind("/private/tmp/caretz-private")) {
		t.Fatalf("read-only socket grants wrong:\n%s", readOnly)
	}
	if !strings.HasPrefix(sandboxTempRoot, "/private/tmp") {
		t.Fatalf("sandbox temp root %q is not short", sandboxTempRoot)
	}
}

// parity: policy.dangerous-operations
func TestSandboxProfileDeniesDangerousExecutables(t *testing.T) {
	profile := testDarwinProfile(false, nil, nil, nil, proxyEndpoints{})
	for _, binary := range []string{"/sbin/mount", "/usr/sbin/reboot", "/usr/bin/passwd", "/usr/bin/launchctl", "/usr/bin/open"} {
		if !strings.Contains(profile, `(deny process-exec (literal "`+binary+`"))`) {
			t.Fatalf("profile does not deny %s", binary)
		}
	}
}

// parity: policy.docker-socket
func TestSandboxProfileDockerSocketFollowsPermission(t *testing.T) {
	t.Setenv("HOME", "/Users/tester")
	t.Setenv("DOCKER_HOST", "unix:///custom/docker.sock")
	denied := generateSandboxProfileForRun("/work", "/tmp/private", nil, false, false, false, false, nil, nil, nil, proxyEndpoints{})
	allowed := generateSandboxProfileForRun("/work", "/tmp/private", nil, false, false, true, false, nil, nil, nil, proxyEndpoints{})
	for _, socket := range []string{"/var/run/docker.sock", "/custom/docker.sock", "/Users/tester/.docker/run/docker.sock"} {
		rule := fmt.Sprintf(`(allow network-outbound (literal "%s"))`, socket)
		if strings.Contains(denied, rule) || !strings.Contains(allowed, rule) {
			t.Fatalf("Docker socket permission mismatch for %s", socket)
		}
	}

	// A read grant naming the Docker socket must not bypass AllowDocker.
	dir, err := os.MkdirTemp("", "dk-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket = filepath.Join(socket, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("DOCKER_HOST", "unix://"+socket)
	rule := fmt.Sprintf(`(allow network-outbound (literal "%s"))`, socket)
	denied = generateSandboxProfileForRun("/work", "/tmp/private", nil, false, false, false, false, []string{socket}, nil, nil, proxyEndpoints{})
	allowed = generateSandboxProfileForRun("/work", "/tmp/private", nil, false, false, true, false, []string{socket}, nil, nil, proxyEndpoints{})
	if strings.Contains(denied, rule) || !strings.Contains(allowed, rule) {
		t.Fatalf("read-root Docker socket permission mismatch for %s", socket)
	}
}

// parity: env.minimal env.proxy env.go-state
func TestMacOSSandboxEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("UNRELATED_HOST_VALUE", "hidden")
	env := macOSSandboxEnvironment("/work", "/tmp/private", proxyEndpoints{HTTP: "127.0.0.1:1234", SOCKS: "127.0.0.1:1235"}, false, map[string]string{"CI": "true", "NPM_TOKEN": "explicit"})
	for _, key := range []string{"OPENAI_API_KEY", "UNRELATED_HOST_VALUE"} {
		if darwinEnvValue(env, key) != "" {
			t.Fatalf("%s leaked into sandbox environment", key)
		}
	}
	for key, want := range map[string]string{
		"HOME": "/work", "TMPDIR": "/tmp/private", "CI": "true", "NPM_TOKEN": "explicit",
		"HTTPS_PROXY": "http://127.0.0.1:1234", "GOCACHE": "/work/.caretz/scratch/go-build",
		"ALL_PROXY":   "http://127.0.0.1:1234",
		socksProxyEnv: "socks5h://127.0.0.1:1235",
		"GOPATH":      "/work/.caretz/scratch/go",
	} {
		if got := darwinEnvValue(env, key); got != want {
			t.Fatalf("%s=%q want %q", key, got, want)
		}
	}
	if got := darwinEnvValue(env, "GIT_SSH_COMMAND"); !strings.Contains(got, socksConnectArg) || !strings.Contains(darwinEnvValue(env, sshProxyCommandEnv), socksConnectArg) {
		t.Fatalf("Git-over-SSH not routed through the SOCKS helper: GIT_SSH_COMMAND=%q", got)
	}
	granted := macOSSandboxEnvironment("/work", "/tmp/private", proxyEndpoints{HTTP: "127.0.0.1:1234", SOCKS: "127.0.0.1:1235"}, false, map[string]string{"GIT_SSH_COMMAND": "ssh -i key"})
	if got := darwinEnvValue(granted, "GIT_SSH_COMMAND"); got != "ssh -i key" {
		t.Fatalf("granted GIT_SSH_COMMAND overridden: %q", got)
	}
	if got := darwinEnvValue(macOSSandboxEnvironment("/work", "/tmp/private", proxyEndpoints{}, false, nil), "GIT_SSH_COMMAND"); got != "" {
		t.Fatalf("GIT_SSH_COMMAND set without restricted networking: %q", got)
	}
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		if got := darwinEnvValue(env, key); got != "" {
			t.Fatalf("%s=%q, want unset", key, got)
		}
	}
}

// parity: env.docker-host
func TestMacOSSandboxEnvironmentDockerHostFollowsPermission(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///custom/docker.sock")
	if got := darwinEnvValue(macOSSandboxEnvironment("/work", "/tmp/private", proxyEndpoints{}, false, nil), "DOCKER_HOST"); got != "" {
		t.Fatalf("DOCKER_HOST=%q without Docker permission", got)
	}
	if got := darwinEnvValue(macOSSandboxEnvironment("/work", "/tmp/private", proxyEndpoints{}, true, nil), "DOCKER_HOST"); got != "unix:///custom/docker.sock" {
		t.Fatalf("DOCKER_HOST=%q, want parent unix socket", got)
	}
	t.Setenv("DOCKER_HOST", "tcp://docker.example:2375")
	if got := darwinEnvValue(macOSSandboxEnvironment("/work", "/tmp/private", proxyEndpoints{}, true, nil), "DOCKER_HOST"); got != "" {
		t.Fatalf("DOCKER_HOST=%q, want non-unix hosts dropped", got)
	}
}

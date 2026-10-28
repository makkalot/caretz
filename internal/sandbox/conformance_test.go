//go:build linux || darwin

package sandbox

// Conformance tests describe sandbox behavior that must hold on every native
// backend. They drive only the OS-neutral Runner and Session API, so one test
// body runs against Seatbelt on macOS and bubblewrap/seccomp on Linux. Put new
// behavioral sandbox tests here; per-OS files are for implementation details
// and must carry a "parity:" tag (see parity_test.go).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/turnscope"
)

// sandboxBehavior lists the deliberate, documented differences between native
// backends. Each OS defines nativeSandboxBehavior; tests branch on these
// fields, never on runtime.GOOS.
type sandboxBehavior struct {
	// privateNetworkNamespace: each command or session gets its own network
	// namespace, so loopback binds succeed but stay invisible to the host.
	privateNetworkNamespace bool
	// stopsNewSessionJobs: a one-shot job that leaves its process group with
	// setsid is still stopped when Caretz exits. Linux finds it by its
	// inherited environment; macOS hides other processes' environments and
	// can only stop the command's process group.
	stopsNewSessionJobs bool
}

func requireConformance(t *testing.T) {
	t.Helper()
	requireSandboxIntegration(t)
	requireNativeSandbox(t)
}

// conformanceModes runs fn once through a one-shot Runner and once through a
// persistent sandboxed Session built from the same runner.
func conformanceModes(t *testing.T, runner *Runner, fn func(t *testing.T, run func(cwd, command string, opts RunOptions) (string, string, error))) {
	t.Helper()
	t.Run("one-shot", func(t *testing.T) {
		fn(t, func(cwd, command string, opts RunOptions) (string, string, error) {
			return runner.RunContextOptions(t.Context(), cwd, command, opts)
		})
	})
	t.Run("session", func(t *testing.T) {
		session := runner.NewSession(false)
		t.Cleanup(func() { _ = session.Close() })
		fn(t, func(cwd, command string, opts RunOptions) (string, string, error) {
			return session.Run(t.Context(), cwd, command, opts)
		})
	})
}

// conformanceHelper returns a shell command that re-executes the test binary
// as TestSessionNetworkHelper in the given mode.
func conformanceHelper(t *testing.T, mode string, env ...string) string {
	t.Helper()
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	vars := append([]string{"CARETZ_SESSION_TEST_HELPER=" + mode}, env...)
	for i, value := range vars {
		key, val, _ := strings.Cut(value, "=")
		vars[i] = fmt.Sprintf("%s=%q", key, val)
	}
	return fmt.Sprintf("%s %q -test.run '^TestSessionNetworkHelper$'", strings.Join(vars, " "), helper)
}

// hostHTTPServer serves body on a host loopback port and returns host:port.
func hostHTTPServer(t *testing.T, body string) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}

func requireNetworkBlocked(t *testing.T, out, stderr string, err error) {
	t.Helper()
	var blocked *NetworkBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("want *NetworkBlockedError, got out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func requireHostAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("sandboxed command created %s on the host (stat err=%v)", path, err)
	}
}

func TestConformanceFilesystemReadsSystemFiles(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		out, stderr, err := run(t.TempDir(), `cat /etc/hosts`, RunOptions{})
		if err != nil || strings.TrimSpace(out) == "" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
}

func TestConformanceFilesystemWorkspaceWritable(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		cwd := t.TempDir()
		if out, stderr, err := run(cwd, `mkdir -p sub && printf ok > sub/value && mv sub/value renamed && rm -r sub`, RunOptions{}); err != nil {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
		if data, err := os.ReadFile(filepath.Join(cwd, "renamed")); err != nil || string(data) != "ok" {
			t.Fatalf("workspace write=%q err=%v", data, err)
		}
		requireHostAbsent(t, filepath.Join(cwd, "sub"))
	})
}

func TestConformanceFilesystemDeniesUnlistedHostPaths(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		outside := t.TempDir()
		secret := filepath.Join(outside, "secret")
		if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
			t.Fatal(err)
		}
		created := filepath.Join(outside, "created")
		command := fmt.Sprintf(`cat %q 2>/dev/null; printf bad > %q 2>/dev/null; if printf bad > /usr/caretz-conformance 2>/dev/null; then exit 20; fi; true`, secret, created)
		out, stderr, err := run(t.TempDir(), command, RunOptions{})
		if err != nil || strings.Contains(out, "secret") {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
		requireHostAbsent(t, created)
	})
}

func TestConformanceFilesystemExtraRoots(t *testing.T) {
	requireConformance(t)
	readRoot, writeRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(readRoot, "input"), []byte("readable"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{ReadableRoots: []string{readRoot}, WritableRoots: []string{writeRoot}})
	conformanceModes(t, runner, func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		output := filepath.Join(writeRoot, t.Name()[strings.LastIndex(t.Name(), "/")+1:])
		denied := filepath.Join(readRoot, "denied")
		command := fmt.Sprintf(`cat %q; printf ok > %q; if printf bad > %q 2>/dev/null; then exit 20; fi`, filepath.Join(readRoot, "input"), output, denied)
		out, stderr, err := run(t.TempDir(), command, RunOptions{})
		if err != nil || out != "readable" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
		if data, err := os.ReadFile(output); err != nil || string(data) != "ok" {
			t.Fatalf("writable root write=%q err=%v", data, err)
		}
		requireHostAbsent(t, denied)
	})
}

func TestConformanceFilesystemPerCommandRoots(t *testing.T) {
	requireConformance(t)
	readRoot, writeRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(readRoot, "input"), []byte("readable"), 0600); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(`cat %q && printf ok > %q`, filepath.Join(readRoot, "input"), filepath.Join(writeRoot, "output"))
	runner := NewRunner(Config{})
	if out, _, err := runner.Run(t.TempDir(), command); err == nil {
		t.Fatalf("roots were reachable without a grant: out=%q", out)
	}
	out, stderr, err := runner.RunContextOptions(t.Context(), t.TempDir(), command, RunOptions{ReadableRoots: []string{readRoot}, WritableRoots: []string{writeRoot}})
	if err != nil || out != "readable" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
	if data, err := os.ReadFile(filepath.Join(writeRoot, "output")); err != nil || string(data) != "ok" {
		t.Fatalf("per-command writable root=%q err=%v", data, err)
	}
}

func TestConformanceFilesystemReadOnlyWorkspace(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{ReadOnly: true}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, "source"), []byte("visible"), 0600); err != nil {
			t.Fatal(err)
		}
		out, stderr, err := run(cwd, `cat source; if printf bad > forbidden 2>/dev/null; then exit 20; fi; if rm source 2>/dev/null; then exit 21; fi`, RunOptions{})
		if err != nil || out != "visible" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
		requireHostAbsent(t, filepath.Join(cwd, "forbidden"))
	})
}

func TestConformanceFilesystemPerCommandReadOnly(t *testing.T) {
	requireConformance(t)
	runner := NewRunner(Config{})
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "source"), []byte("visible"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runner.RunContextOptions(t.Context(), cwd, `cat source; if printf bad > forbidden 2>/dev/null; then exit 20; fi; if rm source 2>/dev/null; then exit 21; fi`, RunOptions{ReadOnly: true})
	if err != nil || out != "visible" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
	requireHostAbsent(t, filepath.Join(cwd, "forbidden"))
	if _, _, err := runner.RunContextOptions(t.Context(), cwd, `printf ok > allowed`, RunOptions{}); err != nil {
		t.Fatalf("a later command without ReadOnly should write again: %v", err)
	}

	session := runner.NewSession(false)
	t.Cleanup(func() { _ = session.Close() })
	if _, _, err := session.Run(t.Context(), cwd, `true`, RunOptions{ReadOnly: true}); err == nil {
		t.Fatal("a writable session must reject read-only commands")
	}
}

func TestConformanceFilesystemPrivateTemp(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		shared := filepath.Join("/tmp", fmt.Sprintf("caretz-conformance-%d-%d", os.Getpid(), time.Now().UnixNano()))
		t.Cleanup(func() { _ = os.Remove(shared) })
		command := fmt.Sprintf(`tmp="${TMPDIR:-/tmp}"; printf ok > "$tmp/value" && cat "$tmp/value"; printf bad > %q 2>/dev/null; true`, shared)
		out, stderr, err := run(t.TempDir(), command, RunOptions{})
		if err != nil || out != "ok" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
		requireHostAbsent(t, shared)
	})
}

func TestConformanceSystemGitCanDiscoverRepository(t *testing.T) {
	requireConformance(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	out, stderr, err := NewRunner(Config{}).Run(t.TempDir(), `git init -q -b sandbox-test && git branch --show-current`)
	if err != nil || strings.TrimSpace(out) != "sandbox-test" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestConformanceHiddenPaths(t *testing.T) {
	requireConformance(t)
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-shot", true: "session"}[persistent], func(t *testing.T) {
			cwd := t.TempDir()
			hiddenFile := filepath.Join(cwd, "config.json")
			hiddenDir := filepath.Join(cwd, "sessions")
			missing := filepath.Join(cwd, "future")
			visible := filepath.Join(cwd, "visible")
			if err := os.WriteFile(hiddenFile, []byte("secret"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(hiddenDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hiddenDir, "history"), []byte("secret"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(visible, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			runner := NewRunner(Config{HiddenPaths: []string{hiddenFile, hiddenDir, missing}})
			command := `printf 'file='; cat config.json 2>/dev/null; printf '|dir='; cat sessions/history 2>/dev/null; printf '|future='; cat future/value 2>/dev/null; printf changed > replacement; mv replacement visible; printf new > new-file; mkdir new-directory; mv new-directory renamed-directory; printf delete > delete-me; rm delete-me; true`
			var out, stderr string
			var err error
			if persistent {
				session := runner.NewSession(false)
				t.Cleanup(func() { _ = session.Close() })
				out, stderr, err = session.Run(t.Context(), cwd, command, RunOptions{})
			} else {
				out, stderr, err = runner.Run(cwd, command)
			}
			if err != nil || out != "file=|dir=|future=" {
				t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
			}
			requireHostAbsent(t, missing)
			for path, want := range map[string]string{"visible": "changed", "new-file": "new", "config.json": "secret"} {
				if data, err := os.ReadFile(filepath.Join(cwd, path)); err != nil || string(data) != want {
					t.Fatalf("host file %s=%q err=%v", path, data, err)
				}
			}
			if info, err := os.Stat(filepath.Join(cwd, "renamed-directory")); err != nil || !info.IsDir() {
				t.Fatalf("directory rename: %v", err)
			}
			requireHostAbsent(t, filepath.Join(cwd, "delete-me"))
		})
	}
}

func TestConformanceHiddenPathCreatedAfterSessionStart(t *testing.T) {
	requireConformance(t)
	cwd := t.TempDir()
	hidden := filepath.Join(cwd, ".caretz", "future")
	s := NewRunner(Config{HiddenPaths: []string{hidden}}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	if _, stderr, err := s.Run(t.Context(), cwd, "true", RunOptions{}); err != nil {
		t.Fatalf("start session: %v (%s)", err, stderr)
	}
	if err := os.MkdirAll(hidden, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "value"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := s.Run(t.Context(), cwd, `cat .caretz/future/value 2>/dev/null || true`, RunOptions{})
	if err != nil || strings.Contains(out, "secret") {
		t.Fatalf("late hidden path visible: out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestConformanceHistoryCreatedAfterSessionStartIsReadOnly(t *testing.T) {
	requireConformance(t)
	cwd := t.TempDir()
	s := NewRunner(Config{}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	if _, stderr, err := s.Run(t.Context(), cwd, "true", RunOptions{}); err != nil {
		t.Fatalf("start session: %v (%s)", err, stderr)
	}
	history := filepath.Join(cwd, ".caretz", "sessions")
	if err := os.MkdirAll(history, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, "events"), []byte("history"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := s.Run(t.Context(), cwd, `cat .caretz/sessions/events; if printf bad > .caretz/sessions/events 2>/dev/null; then exit 20; fi`, RunOptions{})
	if err != nil || !strings.HasSuffix(out, "history") {
		t.Fatalf("late history protection: out=%q stderr=%q err=%v", out, stderr, err)
	}
	if data, err := os.ReadFile(filepath.Join(history, "events")); err != nil || string(data) != "history" {
		t.Fatalf("history modified: %q %v", data, err)
	}
}

func TestConformanceSubagentDefinitionsReadOnly(t *testing.T) {
	requireConformance(t)
	cwd := t.TempDir()
	agents := filepath.Join(cwd, ".caretz", "agents")
	if err := os.MkdirAll(agents, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "reviewer.md"), []byte("reviewer"), 0600); err != nil {
		t.Fatal(err)
	}
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		out, stderr, err := run(cwd, `cat .caretz/agents/reviewer.md; if printf bad > .caretz/agents/reviewer.md 2>/dev/null; then exit 20; fi; if printf bad > .caretz/agents/escalate.md 2>/dev/null; then exit 21; fi; if rm .caretz/agents/reviewer.md 2>/dev/null; then exit 22; fi`, RunOptions{})
		if err != nil || out != "reviewer" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
	if _, err := os.Stat(filepath.Join(agents, "escalate.md")); !os.IsNotExist(err) {
		t.Fatalf("subagent definition created: %v", err)
	}
}

func TestConformanceSessionHistoryReadOnly(t *testing.T) {
	requireConformance(t)
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-shot", true: "session"}[persistent], func(t *testing.T) {
			workspace, external := t.TempDir(), t.TempDir()
			local := filepath.Join(workspace, ".caretz", "sessions")
			private := filepath.Join(external, "sessions")
			for _, dir := range []string{local, private} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "events"), []byte("history"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runner := NewRunner(Config{SessionPaths: []string{private}, WritableRoots: []string{external}, ReadOnlyPaths: []string{local}})
			session := runner.NewSession(false)
			defer func() { _ = session.Close() }()
			run := func(command string) (string, string, error) {
				if persistent {
					return session.Run(t.Context(), workspace, command, RunOptions{})
				}
				return runner.RunContextOptions(t.Context(), workspace, command, RunOptions{WritableRoots: []string{local}})
			}
			command := fmt.Sprintf(`cat .caretz/sessions/events; if printf bad > .caretz/sessions/events 2>/dev/null; then exit 20; fi; if printf bad > .caretz/sessions/new 2>/dev/null; then exit 21; fi; if rm .caretz/sessions/events 2>/dev/null; then exit 22; fi; printf replacement > replacement; if mv replacement .caretz/sessions/events 2>/dev/null; then exit 23; fi; if cat %q 2>/dev/null; then exit 24; fi; printf fixed > source.go`, filepath.Join(private, "events"))
			out, stderr, err := run(command)
			if err != nil || out != "history" {
				t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
			}
			if err := os.WriteFile(filepath.Join(local, "later"), []byte("host-append"), 0600); err != nil {
				t.Fatal(err)
			}
			out, stderr, err = run("cat .caretz/sessions/later")
			if err != nil || out != "host-append" {
				t.Fatalf("host persistence: out=%q stderr=%q err=%v", out, stderr, err)
			}
			if data, err := os.ReadFile(filepath.Join(workspace, "source.go")); err != nil || string(data) != "fixed" {
				t.Fatalf("source edit: %q %v", data, err)
			}
		})
	}
}

func TestConformanceSessionFilesystemGrant(t *testing.T) {
	requireConformance(t)
	workspace, external, writable := t.TempDir(), t.TempDir(), t.TempDir()
	secret := filepath.Join(external, "secret.txt")
	if err := os.WriteFile(secret, []byte("granted"), 0600); err != nil {
		t.Fatal(err)
	}
	session := NewRunner(Config{UnrestrictedNetwork: true}).NewSession(false)
	t.Cleanup(func() { _ = session.Close() })
	if _, _, err := session.Run(t.Context(), workspace, fmt.Sprintf(`cat %q`, secret), RunOptions{}); err == nil {
		t.Fatal("external file was readable before the grant")
	}
	restarted, err := session.AddFilesystemRoots([]string{external}, nil)
	if err != nil || !restarted {
		t.Fatalf("AddFilesystemRoots() = (%v, %v), want restarted", restarted, err)
	}
	out, stderr, err := session.Run(t.Context(), workspace, fmt.Sprintf(`cat %q`, secret), RunOptions{})
	if err != nil || !strings.HasSuffix(out, "granted") {
		t.Fatalf("granted read: out=%q stderr=%q err=%v", out, stderr, err)
	}
	restarted, err = session.AddFilesystemRoots(nil, []string{writable})
	if err != nil || !restarted {
		t.Fatalf("writable AddFilesystemRoots() = (%v, %v), want restarted", restarted, err)
	}
	created := filepath.Join(writable, "created.txt")
	out, stderr, err = session.Run(t.Context(), workspace, fmt.Sprintf(`printf writable > %q; cat %q`, created, created), RunOptions{})
	if err != nil || !strings.HasSuffix(out, "writable") {
		t.Fatalf("granted write: out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestConformanceEnvironment(t *testing.T) {
	requireConformance(t)
	t.Setenv("CARETZ_CONFORMANCE_HOST_ONLY", "leaked")
	runner := NewRunner(Config{Environment: []string{"PROFILE_VALUE=configured"}})
	conformanceModes(t, runner, func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		cwd := t.TempDir()
		command := `printf '%s|%s|%s|%s|' "$HOME" "$PROFILE_VALUE" "$PER_COMMAND" "$CARETZ_CONFORMANCE_HOST_ONLY"; env | grep -c '^_SANDBOX_' || true`
		out, stderr, err := run(cwd, command, RunOptions{Env: map[string]string{"PER_COMMAND": "per-command"}})
		want := cwd + "|configured|per-command||0\n"
		if err != nil || out != want {
			t.Fatalf("out=%q want=%q stderr=%q err=%v", out, want, stderr, err)
		}
	})
}

func TestConformanceSSHAgentExactSocketGrant(t *testing.T) {
	requireConformance(t)
	// Keep the Unix socket path below the platform's length limit.
	dir, err := os.MkdirTemp("", "agent-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "sock")
	socket, err = filepath.Abs(socket)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("agent-ok"))
			_ = conn.Close()
		}
	}()
	socket, err = filepath.EvalSymlinks(socket)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{ReadableRoots: []string{socket}, Environment: []string{"SSH_AUTH_SOCK=" + socket}})
	conformanceModes(t, runner, func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		out, stderr, err := run(t.TempDir(), conformanceHelper(t, "ssh-agent"), RunOptions{})
		if err != nil || out != "agent-ok" {
			t.Fatalf("agent access: out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
}

func TestConformanceUnixSocketsFollowWritableRoots(t *testing.T) {
	requireConformance(t)
	readOnly, err := os.MkdirTemp("", "ro-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(readOnly) }()
	readOnly, err = filepath.EvalSymlinks(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	conformanceModes(t, NewRunner(Config{ReadableRoots: []string{readOnly}}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		// Test servers and tools create sockets in TMPDIR or the workspace;
		// the path must also stay below the platform's socket length limit.
		cwd := t.TempDir()
		for _, path := range []string{"$TMPDIR/caretz-test.sock", "workspace.sock"} {
			out, stderr, err := run(cwd, conformanceHelper(t, "unix-echo", "CARETZ_SESSION_TEST_ADDR="+path), RunOptions{})
			if err != nil || out != "unix-ok" {
				t.Fatalf("socket %s: out=%q stderr=%q err=%v", path, out, stderr, err)
			}
		}
		if out, _, err := run(cwd, conformanceHelper(t, "unix-echo", "CARETZ_SESSION_TEST_ADDR="+filepath.Join(readOnly, "s")), RunOptions{}); err == nil {
			t.Fatalf("socket created in readable-only root: out=%q", out)
		}
	})
}

func TestConformanceEnvironmentRejectsSandboxControls(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		if out, _, err := run(t.TempDir(), "true", RunOptions{Env: map[string]string{"_SANDBOX_CMD": "id"}}); err == nil {
			t.Fatalf("sandbox control variable accepted: out=%q", out)
		}
	})
}

func TestConformanceNetworkAllowlistedHostReachable(t *testing.T) {
	requireConformance(t)
	host := hostHTTPServer(t, "proxy-ok")
	conformanceModes(t, NewRunner(Config{NetworkAllowlist: []string{host}}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		out, stderr, err := run(t.TempDir(), conformanceHelper(t, "proxy-get", "CARETZ_SESSION_TEST_ADDR="+host), RunOptions{})
		if err != nil || out != "proxy-ok" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
}

// hostTCPEchoServer echoes TCP data on a host loopback port and returns
// host:port.
func hostTCPEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func TestConformanceNetworkSOCKSAllowlistedTCP(t *testing.T) {
	requireConformance(t)
	host := hostTCPEchoServer(t)
	command := conformanceHelper(t, "socks-echo", "CARETZ_SESSION_TEST_ADDR="+host)
	t.Run("allowed", func(t *testing.T) {
		conformanceModes(t, NewRunner(Config{NetworkAllowlist: []string{"tcp://" + host}}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
			out, stderr, err := run(t.TempDir(), command, RunOptions{})
			if err != nil || out != "socks-ping" {
				t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
			}
		})
	})
	// A plain HOST:PORT rule authorizes HTTP only, never SOCKS.
	t.Run("http-rule-only", func(t *testing.T) {
		conformanceModes(t, NewRunner(Config{NetworkAllowlist: []string{host}}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
			out, stderr, err := run(t.TempDir(), command, RunOptions{})
			requireNetworkBlocked(t, out, stderr, err)
			var blocked *NetworkBlockedError
			if errors.As(err, &blocked) && blocked.Target != "tcp://"+host {
				t.Fatalf("blocked target = %q, want tcp://%s", blocked.Target, host)
			}
		})
	})
}

// Git-over-SSH must reach the proxy rather than dial directly, so a blocked
// destination becomes an approval request instead of a kernel-level
// "Operation not permitted".
func TestConformanceNetworkGitSSHRoutesThroughSOCKS(t *testing.T) {
	requireConformance(t)
	for _, tool := range []string{"git", "ssh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	// ssh refuses to run for a uid without a passwd entry.
	if _, err := user.Current(); err != nil {
		t.Skipf("current user has no passwd entry: %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan struct{}, 16)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()
	host := listener.Addr().String()
	command := fmt.Sprintf(`git ls-remote ssh://git@%s/repo.git`, host)
	t.Run("blocked-asks", func(t *testing.T) {
		conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
			out, stderr, err := run(t.TempDir(), command, RunOptions{})
			requireNetworkBlocked(t, out, stderr, err)
			var blocked *NetworkBlockedError
			if errors.As(err, &blocked) && blocked.Target != "tcp://"+host {
				t.Fatalf("blocked target = %q, want tcp://%s", blocked.Target, host)
			}
		})
	})
	t.Run("allowed-connects", func(t *testing.T) {
		conformanceModes(t, NewRunner(Config{NetworkAllowlist: []string{"tcp://" + host}}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
			// The fake server closes at once, so git fails after connecting.
			out, stderr, _ := run(t.TempDir(), command, RunOptions{})
			select {
			case <-accepted:
			case <-time.After(5 * time.Second):
				t.Fatalf("git never reached the server: out=%q stderr=%q", out, stderr)
			}
		})
	})
}

func TestConformanceNetworkUnlistedHostBlocked(t *testing.T) {
	requireConformance(t)
	host := hostHTTPServer(t, "should-not-reach")
	for name, allowlist := range map[string][]string{"empty": nil, "other-host": {"blocked.invalid"}} {
		t.Run(name, func(t *testing.T) {
			conformanceModes(t, NewRunner(Config{NetworkAllowlist: allowlist}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
				out, stderr, err := run(t.TempDir(), conformanceHelper(t, "proxy-get", "CARETZ_SESSION_TEST_ADDR="+host), RunOptions{})
				requireNetworkBlocked(t, out, stderr, err)
				if strings.Contains(out, "should-not-reach") {
					t.Fatalf("blocked request reached upstream: %q", out)
				}
			})
		})
	}
}

func TestConformanceNetworkPerCommandAllowlist(t *testing.T) {
	requireConformance(t)
	host := hostHTTPServer(t, "per-command-ok")
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		command := conformanceHelper(t, "proxy-get", "CARETZ_SESSION_TEST_ADDR="+host)
		out, stderr, err := run(t.TempDir(), command, RunOptions{NetworkAllowlist: []string{host}})
		if err != nil || !strings.HasSuffix(out, "per-command-ok") {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
		out, stderr, err = run(t.TempDir(), command, RunOptions{})
		requireNetworkBlocked(t, out, stderr, err)
	})
}

func TestConformanceNetworkLiveApproval(t *testing.T) {
	requireConformance(t)
	host := hostHTTPServer(t, "approved-ok")
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprintf("approve=%t", approve), func(t *testing.T) {
			conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
				var targets []string
				opts := RunOptions{AuthorizeNetwork: func(_ context.Context, target string) (bool, error) {
					targets = append(targets, target)
					return approve, nil
				}}
				out, stderr, err := run(t.TempDir(), conformanceHelper(t, "proxy-get", "CARETZ_SESSION_TEST_ADDR="+host), opts)
				if approve && (err != nil || !strings.HasSuffix(out, "approved-ok")) {
					t.Fatalf("approved request: out=%q stderr=%q err=%v", out, stderr, err)
				}
				// A denied approval fails the request without a NetworkBlockedError,
				// so the agent does not prompt for the same target again.
				if !approve && (err == nil || strings.Contains(out, "approved-ok")) {
					t.Fatalf("denied request: out=%q stderr=%q err=%v", out, stderr, err)
				}
				if len(targets) != 1 || targets[0] != host {
					t.Fatalf("approval targets = %#v, want [%s]", targets, host)
				}
			})
		})
	}
}

func TestConformanceNetworkDirectBypassFails(t *testing.T) {
	requireConformance(t)
	host := hostHTTPServer(t, "should-not-reach")
	conformanceModes(t, NewRunner(Config{NetworkAllowlist: []string{host}}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		out, stderr, err := run(t.TempDir(), conformanceHelper(t, "direct-get", "CARETZ_SESSION_TEST_ADDR="+host), RunOptions{})
		if err == nil || strings.Contains(out, "should-not-reach") {
			t.Fatalf("direct connection bypassed the proxy: out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
}

func TestConformanceNetworkUnrestricted(t *testing.T) {
	requireConformance(t)
	host := hostHTTPServer(t, "direct-ok")
	conformanceModes(t, NewRunner(Config{UnrestrictedNetwork: true}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		out, stderr, err := run(t.TempDir(), conformanceHelper(t, "direct-get", "CARETZ_SESSION_TEST_ADDR="+host), RunOptions{})
		if err != nil || out != "direct-ok" {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
}

func TestConformanceNetworkAllowLoopbackInsideSandbox(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{AllowLoopback: true}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		out, stderr, err := run(t.TempDir(), conformanceHelper(t, "loopback-direct"), RunOptions{})
		if err != nil || !strings.Contains(out, "direct-connected") {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
	})
}

// Without AllowLoopback a command must not offer a loopback service to the
// host. macOS denies the bind outright; Linux runs commands in a private
// network namespace, where the bind succeeds but is unreachable from the host
// and shared only by commands of the same session.
func TestConformanceNetworkSessionLoopbackIsolation(t *testing.T) {
	requireConformance(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	s := NewRunner(Config{}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	cwd := t.TempDir()
	if !nativeSandboxBehavior.privateNetworkNamespace {
		out, stderr, err := s.Run(t.Context(), cwd, conformanceHelper(t, "bind-once", "CARETZ_SESSION_TEST_ADDR="+addr), RunOptions{})
		if err == nil {
			t.Fatalf("loopback bind succeeded: out=%q stderr=%q", out, stderr)
		}
		return
	}
	serverOut, stderr, err := s.Run(t.Context(), cwd, conformanceHelper(t, "server", "CARETZ_SESSION_TEST_ADDR="+addr), RunOptions{Background: true})
	if err != nil {
		t.Fatalf("background server: %v (%s)", err, stderr)
	}
	serverLog := s.tmpDir + strings.TrimPrefix(strings.Split(serverOut, "\n")[2], "Log: $TMPDIR")
	client := conformanceHelper(t, "client", "CARETZ_SESSION_TEST_ADDR="+addr)
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, _, err := s.Run(t.Context(), cwd, client, RunOptions{})
		if err == nil && out == "pong" {
			break
		}
		if time.Now().After(deadline) {
			logData, _ := os.ReadFile(serverLog)
			t.Fatalf("session-shared loopback unavailable: out=%q err=%v server_log=%q", out, err, logData)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("sandboxed loopback service %s is reachable from the host", addr)
	}
}

func TestConformanceNetworkSessionProxyLifecycle(t *testing.T) {
	requireConformance(t)
	host := hostHTTPServer(t, "ok")
	session := NewRunner(Config{NetworkAllowlist: []string{host}}).NewSession(false)
	proxyAddr := func() net.Addr {
		t.Helper()
		if _, stderr, err := session.Run(t.Context(), t.TempDir(), "true", RunOptions{}); err != nil {
			t.Fatalf("session run: %v (%s)", err, stderr)
		}
		if session.proxy == nil || session.proxy.listener == nil {
			t.Fatal("restricted session has no network proxy")
		}
		return session.proxy.listener.Addr()
	}
	requireClosed := func(addr net.Addr, when string) {
		t.Helper()
		if conn, err := net.DialTimeout(addr.Network(), addr.String(), 200*time.Millisecond); err == nil {
			_ = conn.Close()
			t.Fatalf("session proxy %s remained reachable after %s", addr, when)
		}
	}
	first := proxyAddr()
	if err := session.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	second := proxyAddr()
	requireClosed(first, "supervisor failure")
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	requireClosed(second, "close")
}

func TestConformanceProcessTimeout(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		start := time.Now()
		out, stderr, err := run(t.TempDir(), "sleep 10", RunOptions{TimeoutSeconds: 1})
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
		}
		if elapsed := time.Since(start); elapsed > 8*time.Second {
			t.Fatalf("timeout took %s", elapsed)
		}
	})
}

func TestConformanceProcessOutputBounded(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		out, stderr, err := run(t.TempDir(), "seq 1 2000000", RunOptions{})
		if err != nil {
			t.Fatalf("stderr=%q err=%v", stderr, err)
		}
		if len(out) > sandboxMaxOutput+256 {
			t.Fatalf("output length %d exceeds limit %d", len(out), sandboxMaxOutput)
		}
	})
	out, _, err := NewRunner(Config{}).Run(t.TempDir(), "seq 1 2000000")
	if err != nil || !strings.HasSuffix(out, "[output truncated - exceeded 1MB limit]") {
		t.Fatalf("one-shot truncation marker missing: tail=%q err=%v", out[max(0, len(out)-80):], err)
	}
}

func TestConformanceProcessTimeoutKillsDescendants(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		cwd := t.TempDir()
		if _, _, err := run(cwd, `(sleep 2; printf survived > descendant-marker) & wait`, RunOptions{TimeoutSeconds: 1}); err == nil {
			t.Fatal("command outlived its timeout")
		}
		time.Sleep(1500 * time.Millisecond)
		requireHostAbsent(t, filepath.Join(cwd, "descendant-marker"))
	})
}

func TestConformanceProcessCancellationKillsDescendants(t *testing.T) {
	requireConformance(t)
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := NewRunner(Config{}).RunContext(ctx, cwd, `(sleep 1; printf survived > descendant-marker) & wait`); err == nil {
		t.Fatal("canceled command reported success")
	}
	time.Sleep(1500 * time.Millisecond)
	requireHostAbsent(t, filepath.Join(cwd, "descendant-marker"))
}

// One-shot commands share the host's process space, so a job they detach must
// still be found and stopped when Caretz exits.
func TestConformanceProcessDetachedJobsStopWithCaretz(t *testing.T) {
	requireConformance(t)
	jobs := map[string]string{"nohup": `nohup sh -c 'sleep 2; printf survived > detached-marker' >/dev/null 2>&1 &`}
	if nativeSandboxBehavior.stopsNewSessionJobs {
		jobs["setsid"] = `setsid sh -c 'sleep 2; printf survived > detached-marker' </dev/null >/dev/null 2>&1 &`
	}
	for name, command := range jobs {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			if _, stderr, err := NewRunner(Config{}).RunContextOptions(t.Context(), cwd, command, RunOptions{}); err != nil {
				t.Fatalf("start detached job: %v (%s)", err, stderr)
			}
			TerminateSandboxedProcesses()
			time.Sleep(2500 * time.Millisecond)
			requireHostAbsent(t, filepath.Join(cwd, "detached-marker"))
		})
	}
}

func TestConformanceSessionCloseStopsDetachedJobs(t *testing.T) {
	requireConformance(t)
	cwd := t.TempDir()
	s := NewRunner(Config{}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	if _, stderr, err := s.Run(t.Context(), cwd, `nohup sh -c 'sleep 2; printf survived > detached-marker' >/dev/null 2>&1 &`, RunOptions{}); err != nil {
		t.Fatalf("start detached job: %v (%s)", err, stderr)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	time.Sleep(2500 * time.Millisecond)
	requireHostAbsent(t, filepath.Join(cwd, "detached-marker"))
}

func TestConformanceSessionStateAndClose(t *testing.T) {
	requireConformance(t)
	cwd := t.TempDir()
	s := NewRunner(Config{}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	if _, stderr, err := s.Run(t.Context(), cwd, `printf shared > "$TMPDIR/session-value"; export SHELL_STATE=leaked`, RunOptions{}); err != nil {
		t.Fatalf("write shared temp: %v (%s)", err, stderr)
	}
	out, stderr, err := s.Run(t.Context(), cwd, `cat "$TMPDIR/session-value"; printf '|%s' "$SHELL_STATE"`, RunOptions{})
	if err != nil || out != "shared|" {
		t.Fatalf("session temp/shell state: out=%q stderr=%q err=%v", out, stderr, err)
	}
	if _, stderr, err = s.Run(t.Context(), cwd, `sh -c 'sleep 30 & wait'`, RunOptions{Background: true}); err != nil {
		t.Fatalf("background tree: %v (%s)", err, stderr)
	}
	supervisorPID := s.cmd.Process.Pid
	if err = s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err = syscall.Kill(supervisorPID, 0); err == nil {
		t.Fatalf("supervisor %d survived close", supervisorPID)
	}
}

func TestConformanceTurnCancellationPreservesEarlierJobs(t *testing.T) {
	requireConformance(t)
	cwd := t.TempDir()
	s := NewRunner(Config{}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	start := func(ctx context.Context) int {
		t.Helper()
		out, stderr, err := s.Run(ctx, cwd, "sleep 60", RunOptions{Background: true})
		if err != nil {
			t.Fatalf("start: %v (%s)", err, stderr)
		}
		var pid int
		if _, err := fmt.Sscanf(out, "Background command started.\nPID: %d", &pid); err != nil {
			t.Fatalf("pid: %q: %v", out, err)
		}
		return pid
	}
	earlier := start(t.Context())
	scope := &turnscope.Scope{}
	owned := start(turnscope.With(t.Context(), scope))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		out, _, err := s.Run(ctx, cwd, "echo partial-output; touch ready; sleep 60", RunOptions{})
		if !strings.Contains(out, "partial-output") {
			done <- fmt.Errorf("lost partial output: %q (%v)", out, err)
			return
		}
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(cwd, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("foreground command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := time.Now()
	notes, err := scope.Stop()
	if err != nil || !strings.Contains(strings.Join(notes, "\n"), fmt.Sprint(owned)) {
		t.Fatalf("cleanup: %v, %v", notes, err)
	}
	// Scope cleanup must finish despite the unrelated foreground request.
	if time.Since(started) > time.Second {
		t.Fatal("cleanup waited for foreground request")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("command cancellation waited for pipes")
	}
	command := fmt.Sprintf("kill -0 -- -%d && ! kill -0 -- -%d", earlier, owned)
	if _, stderr, err := s.Run(t.Context(), cwd, command, RunOptions{}); err != nil {
		t.Fatalf("job ownership: %v (%s)", err, stderr)
	}
}

func TestConformanceSessionPreservesExplicitCredentials(t *testing.T) {
	requireConformance(t)
	s := NewRunner(Config{Environment: []string{"OPENAI_API_KEY=configured"}}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	out, stderr, err := s.Run(t.Context(), t.TempDir(), `printf '%s|%s' "$OPENAI_API_KEY" "$TAVILY_API_KEY"`, RunOptions{Env: map[string]string{"TAVILY_API_KEY": "per-command"}})
	if err != nil || out != "configured|per-command" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestConformanceSessionCommandsCannotSeeControlDescriptors(t *testing.T) {
	requireConformance(t)
	s := NewRunner(Config{}).NewSession(false)
	t.Cleanup(func() { _ = s.Close() })
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("CARETZ_SESSION_DESCRIPTOR_HELPER=1 %q -test.run '^TestSessionDescriptorHelper$' </dev/null 2>/dev/null", helper)
	out, stderr, err := s.Run(t.Context(), t.TempDir(), command, RunOptions{})
	if err != nil || strings.TrimSpace(strings.TrimPrefix(out, "|")) != "" {
		t.Fatalf("command inherited pipe/socket descriptors: out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestConformancePrivilegeEscalationFails(t *testing.T) {
	requireConformance(t)
	conformanceModes(t, NewRunner(Config{}), func(t *testing.T, run func(string, string, RunOptions) (string, string, error)) {
		cwd := t.TempDir()
		for name, command := range map[string]string{
			"mount":  `mkdir -p mnt && mount -t tmpfs none mnt`,
			"mknod":  `mknod node c 1 5`,
			"setuid": conformanceHelper(t, "setuid"),
		} {
			out, stderr, err := run(cwd, command, RunOptions{})
			if err == nil || strings.Contains(out, "escalated") {
				t.Fatalf("%s succeeded: out=%q stderr=%q", name, out, stderr)
			}
		}
		requireHostAbsent(t, filepath.Join(cwd, "node"))
	})
}

// Package harness builds the real caretz binary and runs it in an isolated
// home, workspace, and config root for end-to-end tests.
package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// DefaultTimeout bounds a single caretz run.
const DefaultTimeout = 2 * time.Minute

var binaryPath string

// BuildBinary compiles caretz once for the test package. Call it from TestMain
// and pass the returned cleanup to defer. CARETZ_E2E_BINARY skips the build
// and uses a prebuilt binary instead.
func BuildBinary() (cleanup func(), err error) {
	if prebuilt := os.Getenv("CARETZ_E2E_BINARY"); prebuilt != "" {
		binaryPath = prebuilt
		return func() {}, nil
	}
	dir, err := os.MkdirTemp("", "caretz-e2e-bin-")
	if err != nil {
		return nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	binaryPath = filepath.Join(dir, "caretz")
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/caretz")
	cmd.Dir = repoRoot()
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return nil, fmt.Errorf("build caretz: %w", err)
	}
	return cleanup, nil
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Env is an isolated filesystem for one caretz run: its own HOME, workspace,
// and config root, so nothing from the developer's machine leaks in.
type Env struct {
	t          testing.TB
	Home       string
	Workspace  string
	ConfigRoot string
	// BaseArgs are prepended to every Run, typically the model endpoint flags.
	BaseArgs []string
	// ExtraEnv entries (KEY=VALUE) are added to the process environment.
	ExtraEnv []string
}

// New creates an Env whose model endpoint is baseURL.
func New(t testing.TB, baseURL, model string) *Env {
	t.Helper()
	if binaryPath == "" {
		t.Fatal("harness: BuildBinary was not called from TestMain")
	}
	root := t.TempDir()
	e := &Env{
		t:          t,
		Home:       filepath.Join(root, "home"),
		Workspace:  filepath.Join(root, "workspace"),
		ConfigRoot: filepath.Join(root, "config-root"),
		BaseArgs: []string{
			"-base-url", baseURL,
			"-model", model,
			"-api-key", "fake-key",
			"-no-model-discovery",
			"-llm-max-attempts", "1",
		},
	}
	for _, dir := range []string{e.Home, e.Workspace, e.ConfigRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// Result is the outcome of one caretz run.
type Result struct {
	t        testing.TB
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes caretz with BaseArgs followed by args.
func (e *Env) Run(args ...string) *Result {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	full := append(append([]string{}, e.BaseArgs...), "-workspace", e.Workspace, "-config-root", e.ConfigRoot)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, binaryPath, full...)
	cmd.Dir = e.Workspace
	cmd.Env = e.environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res := &Result{t: e.t, Args: full, Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		e.t.Fatalf("caretz timed out after %s\n%s", DefaultTimeout, res.Dump())
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case err != nil:
		e.t.Fatalf("run caretz: %v", err)
	}
	return res
}

// environ builds a minimal environment: no OPENAI_*/CARETZ_* settings from the
// developer's shell, and HOME pointing at the isolated home.
func (e *Env) environ() []string {
	env := []string{
		"HOME=" + e.Home,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
		"TERM=dumb",
		"NO_COLOR=1",
	}
	if v := os.Getenv("USER"); v != "" {
		env = append(env, "USER="+v)
	}
	return append(env, e.ExtraEnv...)
}

// WriteFile creates a file relative to the workspace.
func (e *Env) WriteFile(rel, content string) {
	e.t.Helper()
	path := filepath.Join(e.Workspace, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// ReadFile returns a workspace file's content, failing the test if it is missing.
func (e *Env) ReadFile(rel string) string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Workspace, rel))
	if err != nil {
		e.t.Fatalf("read workspace file: %v", err)
	}
	return string(data)
}

// Success fails the test unless caretz exited 0.
func (r *Result) Success() *Result {
	r.t.Helper()
	if r.ExitCode != 0 {
		r.t.Fatalf("caretz exited %d\n%s", r.ExitCode, r.Dump())
	}
	return r
}

// Dump renders the run's arguments and output for failure messages.
func (r *Result) Dump() string {
	return fmt.Sprintf("args: %q\n--- stdout ---\n%s\n--- stderr ---\n%s", r.Args, r.Stdout, r.Stderr)
}

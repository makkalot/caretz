package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/makkalot/caretz/cli"
	"github.com/makkalot/caretz/lib/llm"
)

func TestSplitArgs(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want arguments
	}{
		{"default", nil, arguments{}},
		{"base", []string{"origin/main", "--model", "model", "-v"}, arguments{base: "origin/main", rest: []string{"--model", "model", "-v"}}},
		{"owned", []string{"main", "--keep-session", "--workspace=../repo", "--model=x"}, arguments{base: "main", workspace: "../repo", keep: true, rest: []string{"--model=x"}}},
		{"help", []string{"-h"}, arguments{help: true}},
		{"flag value", []string{"--model", "session"}, arguments{rest: []string{"--model", "session"}}},
		{"boolean", []string{"--keep-session=false", "--session-exec=false"}, arguments{rest: []string{"--session-exec=false"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := splitArgs(test.args)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, %v; want %#v", got, err, test.want)
			}
		})
	}
	for _, args := range [][]string{
		{"main", "--plan=false"}, {"-tools", "Bash"}, {"--p=oops"},
		{"-prompt-file=x"}, {"-s", "id"}, {"--adapter=slack"}, {"--incognito=false"},
		{"--tasks"}, {"--no-tasks=false"}, {"session"}, {"acp"}, {"setup"},
		{"auth"}, {"models"}, {"main", "session"}, {"--"}, {"--model"},
		{"--keep-session=wat"}, {"--workspace"},
	} {
		if _, err := splitArgs(args); err == nil {
			t.Errorf("expected rejection of %q", args)
		}
	}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.com",
		"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func repository(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-b", "main")
	write(t, root, "app.txt", "original\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initial")
	git(t, root, "checkout", "-b", "feature")
	write(t, root, "app.txt", "changed\n")
	write(t, root, "other.txt", "other change\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "fix app", "-m", "Explain the intent")
	return root
}

func TestGitTools(t *testing.T) {
	root := repository(t)
	ctx := context.Background()
	r, err := prepareReview(ctx, root, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.base != "main" || !strings.Contains(r.stat, "2 files changed") {
		t.Fatalf("unexpected preflight: %#v", r)
	}
	// Dirty files remain untouched and are excluded from the committed diff.
	write(t, root, "app.txt", "uncommitted\n")
	write(t, root, "untracked.txt", "keep me\n")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	before := git(t, root, "status", "--porcelain=v1")
	custom := r.tools()
	diff, err := custom[0].Handler(ctx, llm.ToolCall{Arguments: `{"path":"app.txt"}`})
	if err != nil || !strings.Contains(diff, "+changed") || strings.Contains(diff, "uncommitted") || strings.Contains(diff, "other.txt") {
		t.Fatalf("diff = %q, %v", diff, err)
	}
	all, err := custom[0].Handler(ctx, llm.ToolCall{Arguments: `{}`})
	if err != nil || !strings.Contains(all, "other.txt") {
		t.Fatalf("whole diff = %q, %v", all, err)
	}
	log, err := custom[1].Handler(ctx, llm.ToolCall{Arguments: `{}`})
	if err != nil || !strings.Contains(log, "fix app") || !strings.Contains(log, "Explain the intent") || strings.Contains(log, "initial") {
		t.Fatalf("log = %q, %v", log, err)
	}
	for _, path := range []string{"../x", "-p", "/etc/passwd", "../../etc/passwd"} {
		payload, _ := json.Marshal(map[string]string{"path": path})
		if _, err := custom[0].Handler(ctx, llm.ToolCall{Arguments: string(payload)}); err == nil {
			t.Errorf("accepted unsafe path %q", path)
		}
	}
	if _, err := custom[0].Handler(ctx, llm.ToolCall{Arguments: `{"path":"outside/secret"}`}); err == nil {
		t.Fatal("accepted symlink path")
	}
	if _, err := custom[0].Handler(ctx, llm.ToolCall{Arguments: `broken`}); err == nil {
		t.Fatal("accepted malformed JSON")
	}
	after := git(t, root, "status", "--porcelain=v1")
	if after != before {
		t.Fatalf("tools changed repository status: before %q, after %q", before, after)
	}
	// Later commits must not move the review's pinned range.
	git(t, root, "add", "app.txt")
	git(t, root, "commit", "-m", "later")
	pinned, err := custom[0].Handler(ctx, llm.ToolCall{Arguments: `{"path":"app.txt"}`})
	if err != nil || pinned != diff {
		t.Fatalf("review range moved: %q, %v", pinned, err)
	}
}

func TestRun(t *testing.T) {
	root := repository(t)
	var stdout, stderr bytes.Buffer
	called := false
	launch := func(_ context.Context, args []string, out, errOut io.Writer, opts cli.Options) int {
		called = true
		joined := strings.Join(args, "\n")
		for _, want := range []string{"-plan", "-no-tasks", "-incognito=true", "Read,Grep,Find,Ls,GitDiff,GitLog", root, "Review this branch"} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing %q in %q", want, args)
			}
		}
		if len(opts.Tools) != 2 || opts.SystemPromptSuffix != reviewRubric || out != &stdout || errOut != &stderr {
			t.Fatal("incorrect CLI options or output writers")
		}
		return 7
	}
	if code := run(context.Background(), []string{"main", "--workspace", root, "--model=test"}, &stdout, &stderr, launch); code != 7 || !called {
		t.Fatalf("run = %d, called = %v, stderr = %s", code, called, &stderr)
	}
	called = false
	for _, test := range []struct {
		args []string
		code int
		text string
	}{
		{[]string{"--help"}, 0, "Usage:"},
		{[]string{"missing-base", "--workspace", root}, 2, "unknown base"},
		{[]string{"HEAD", "--workspace", root}, 0, "Nothing to review"},
		{[]string{"--workspace", t.TempDir()}, 2, "not a Git repository"},
	} {
		stdout.Reset()
		stderr.Reset()
		code := run(context.Background(), test.args, &stdout, &stderr, launch)
		if code != test.code || called || !strings.Contains(stdout.String()+stderr.String(), test.text) {
			t.Errorf("run(%q) = %d; stdout=%s stderr=%s", test.args, code, &stdout, &stderr)
		}
	}
}

func TestFallbackAndLiteralPath(t *testing.T) {
	root := repository(t)
	git(t, root, "branch", "-m", "main", "master")
	write(t, root, "[x].txt", "literal\n")
	write(t, root, "x.txt", "wildcard\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "literal paths")
	r, err := prepareReview(context.Background(), root, "")
	if err != nil || r.base != "master" {
		t.Fatalf("fallback = %#v, %v", r, err)
	}
	diff, err := r.tools()[0].Handler(context.Background(), llm.ToolCall{Arguments: `{"path":"[x].txt"}`})
	if err != nil || !strings.Contains(diff, "+literal") || strings.Contains(diff, "+wildcard") {
		t.Fatalf("literal path diff = %q, %v", diff, err)
	}
}

func TestGitDiffDisablesExternalCommands(t *testing.T) {
	root := repository(t)
	// Either configured command would fail the diff if Git invoked it.
	git(t, root, "config", "diff.external", "false")
	git(t, root, "config", "diff.example.textconv", "false")
	write(t, root, ".gitattributes", "*.txt diff=example\n")
	git(t, root, "add", ".gitattributes")
	git(t, root, "commit", "-m", "custom diff driver")
	r, err := prepareReview(context.Background(), root, "main")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := r.tools()[0].Handler(context.Background(), llm.ToolCall{Arguments: `{"path":"app.txt"}`})
	if err != nil || !strings.Contains(diff, "+changed") {
		t.Fatalf("configured diff commands were not bypassed: %q, %v", diff, err)
	}
}

func TestKeepSession(t *testing.T) {
	root := repository(t)
	var output bytes.Buffer
	called := false
	code := run(context.Background(), []string{"--workspace", root, "--keep-session"}, &output, &output,
		func(_ context.Context, args []string, _, _ io.Writer, _ cli.Options) int {
			called = true
			for _, arg := range args {
				if arg == "-incognito=false" {
					return 0
				}
			}
			t.Fatal("--keep-session did not disable incognito mode")
			return 1
		})
	if code != 0 || !called {
		t.Fatalf("run = %d, called = %v, output = %s", code, called, &output)
	}
}

func TestOutputLimitAndCancellation(t *testing.T) {
	root := repository(t)
	write(t, root, "large.txt", strings.Repeat("large line\n", maxGitOutput))
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "large diff")
	r, err := prepareReview(context.Background(), root, "main")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.tools()[0].Handler(context.Background(), llm.ToolCall{Arguments: `{}`})
	if err != nil || len(out) > maxGitOutput+200 || !strings.Contains(out, "truncated") {
		t.Fatalf("output length=%d, err=%v", len(out), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runGit(ctx, root, "status"); err == nil {
		t.Fatal("ignored context cancellation")
	}
}

package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/clicmd"
)

func TestRunDoesNotAcceptIntegrationStartupFlags(t *testing.T) {
	for _, flagName := range []string{"--slack", "--telegram"} {
		t.Run(flagName, func(t *testing.T) {
			var stderr strings.Builder
			code := Run(context.Background(), []string{flagName}, nil, &stderr)
			if code != 2 {
				t.Fatalf("Run(%s) exit code = %d, want 2", flagName, code)
			}
			if !strings.Contains(stderr.String(), "flag provided but not defined") {
				t.Fatalf("stderr = %q, want unknown flag error", stderr.String())
			}
		})
	}
}

func TestExecutableCLICommandsHaveRunners(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "root"},
		{name: "setup", args: []string{"setup"}},
		{name: "acp", args: []string{"acp"}},
		{name: "session list", args: []string{"session", "list"}},
		{name: "session explore", args: []string{"session", "explore", "session-id"}},
		{name: "session inspect", args: []string{"session", "inspect", "session-id"}},
		{name: "session repair", args: []string{"session", "repair", "session-id"}},
		{name: "session trim", args: []string{"session", "trim", "session-id", "--from-version", "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newCaretzCLICommand(cliEnvironment{stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard})
			result, err := root.Parse(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if result.Help {
				t.Fatal("executable route unexpectedly selected help")
			}
			if result.Command.Run == nil {
				t.Fatalf("selected command %q has no runner", result.Command.Use)
			}
		})
	}
}

func TestRunSelectedCLICommandRejectsMissingRunner(t *testing.T) {
	var stderr strings.Builder
	result := &clicmd.Result{Command: &clicmd.Command{Use: "caretz broken"}}
	if code := runSelectedCLICommand(context.Background(), result, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got := stderr.String(); got != "error: internal error: selected command has no runner\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRootHelpShowsCanonicalSurface(t *testing.T) {
	var stdout, stderr strings.Builder
	code := Run(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run(--help) exit code = %d, want 0", code)
	}
	for _, want := range []string{"session    List", "acp        Start", "-v", "-vv", "(default 5m)"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "enable-tool") || strings.Contains(stdout.String(), "list-sessions") {
		t.Fatalf("help exposes removed options:\n%s", stdout.String())
	}
	for _, unwanted := range []string{"\n  skill ", "\n  agent "} {
		if strings.Contains(stdout.String(), unwanted) {
			t.Fatalf("help exposes removed command %q:\n%s", unwanted, stdout.String())
		}
	}
}

func TestRootHelpGroupsEveryFlag(t *testing.T) {
	var stdout strings.Builder
	if code := Run(context.Background(), []string{"--help"}, &stdout, io.Discard); code != 0 {
		t.Fatalf("code=%d", code)
	}
	want := map[string][]string{
		"Session and input":           {"adapter", "incognito", "p", "prompt-file", "s"},
		"Workspace and configuration": {"config-root", "profile", "workspace"},
		"Model and context":           {"api-key", "base-url", "context-window", "keep-recent-tokens", "llm-max-attempts", "llm-timeout", "model", "no-model-discovery", "reasoning", "reserved-tokens", "summary-tokens"},
		"Agent behavior":              {"disable-tool", "no-tasks", "system-prompt", "system-prompt-file", "tasks", "tools"},
		"Execution and sandbox":       {"allow-docker", "allow-loopback", "ask-sandbox-off", "network-allow", "permission-timeout", "plan", "read-root", "sandbox-env", "sandbox-env-file", "session-exec", "unrestricted-network", "writable-root"},
		"Output and diagnostics":      {"output", "theme", "v", "vv"},
		"General":                     {"version"},
	}
	got := helpFlagGroups(stdout.String())
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("help groups = %#v, want %#v\n%s", got, want, stdout.String())
	}
}

func TestCommandFlagsAreScoped(t *testing.T) {
	var stderr strings.Builder
	if code := Run(context.Background(), []string{"acp", "--output", "detailed"}, io.Discard, &stderr); code != 2 {
		t.Fatalf("acp --output code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr=%q", stderr.String())
	}

	stderr.Reset()
	if code := Run(context.Background(), []string{"--model", "test", "session", "list"}, io.Discard, &stderr); code != 2 {
		t.Fatalf("root flag before session code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not valid with subcommand") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestSubcommandHelpShowsOnlyApplicableFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		include []string
		exclude []string
	}{
		{"session list", []string{"session", "list", "--help"}, []string{"Session location:", "Filters:", "-workspace", "-config-root", "-adapter", "-include-subagents"}, []string{"-model", "-from-version"}},
		{"session trim", []string{"session", "trim", "--help"}, []string{"Session location:", "Trimming:", "-workspace", "-config-root", "-adapter", "-from-version"}, []string{"-model", "-include-subagents"}},
		{"acp", []string{"acp", "--help"}, []string{"Workspace and configuration:", "Model and context:", "Agent behavior:", "Execution and sandbox:", "Session state:", "Output and diagnostics:", "-model", "-workspace", "-plan"}, []string{"-output", "-prompt-file", "-version", "-adapter"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if code := Run(context.Background(), tt.args, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			for _, want := range tt.include {
				prefix := "\n  "
				if strings.HasSuffix(want, ":") {
					prefix = "\n"
				}
				if !strings.Contains(stdout.String(), prefix+want) {
					t.Fatalf("help missing %q:\n%s", want, stdout.String())
				}
			}
			for _, unwanted := range tt.exclude {
				prefix := "\n  "
				if strings.HasSuffix(unwanted, ":") {
					prefix = "\n"
				}
				if strings.Contains(stdout.String(), prefix+unwanted) {
					t.Fatalf("help unexpectedly contains %q:\n%s", unwanted, stdout.String())
				}
			}
		})
	}
}

func TestRunRejectsRemovedAliasesAndUnexpectedArguments(t *testing.T) {
	for _, arg := range []string{"--list-sessions", "--enable-tool", "-vvv", "skill", "agent", "skills", "agents", "unexpected"} {
		t.Run(arg, func(t *testing.T) {
			var stderr strings.Builder
			if code := Run(context.Background(), []string{arg}, io.Discard, &stderr); code != 2 {
				t.Fatalf("Run(%q) code=%d stderr=%q, want 2", arg, code, stderr.String())
			}
		})
	}
}

func TestRemovedResourceCommandsRejectBeforeRuntimeConfig(t *testing.T) {
	for _, args := range [][]string{
		{"skill", "install", "owner/repo/setup"},
		{"skill", "upgrade"},
		{"agent", "install", "owner/repo/setup"},
		{"agent", "upgrade"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("CARETZ_CONFIG_ROOT", t.TempDir())
			var stderr strings.Builder
			if code := Run(context.Background(), args, io.Discard, &stderr); code != 2 {
				t.Fatalf("code=%d stderr=%q, want 2", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "unexpected argument") {
				t.Fatalf("stderr=%q, want argument error", stderr.String())
			}
			for _, unexpected := range []string{"OPENAI_API_MODEL", "OPENAI_API_BASE_URL"} {
				if strings.Contains(stderr.String(), unexpected) {
					t.Fatalf("removed command loaded runtime config: %q", stderr.String())
				}
			}
		})
	}
}

func TestRunVersionBypassesRuntimeConfig(t *testing.T) {
	var stdout, stderr strings.Builder
	code := RunWithOptions(context.Background(), []string{"-version"}, &stdout, &stderr, Options{
		BuildInfo: BuildInfo{
			Version:   "v1.2.3",
			Commit:    "abc1234",
			BuildDate: "2026-08-30T12:00:00Z",
		},
	})
	if code != 0 {
		t.Fatalf("RunWithOptions(-version) exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	want := "caretz v1.2.3 (commit abc1234, built 2026-08-30T12:00:00Z)\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if strings.Contains(stderr.String(), "OPENAI_API_MODEL") || strings.Contains(stderr.String(), "OPENAI_API_BASE_URL") {
		t.Fatalf("-version loaded runtime config unexpectedly: %q", stderr.String())
	}
}

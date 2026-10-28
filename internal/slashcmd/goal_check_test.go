package slashcmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/goal"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/tools"
)

func TestGoalCheckCommands(t *testing.T) {
	tests := []struct {
		name  string
		check goal.Check
		want  string
	}{
		{"script", goal.Check{Argv: []string{"./scripts/verify month.py", "--month", "2026-02"}}, `'./scripts/verify month.py' '--month' '2026-02'`},
		{"test", goal.Check{Argv: []string{"go", "test", "-count=1", "./internal/app/..."}}, `'go' 'test' '-count=1' './internal/app/...'`},
		{"quoting", goal.Check{Argv: []string{"jq", "-e", ".a == 'x'", "out.json"}}, `'jq' '-e' '.a == '"'"'x'"'"'' 'out.json'`},
		{"shell", goal.Check{Shell: "cmp a b"}, `cmp a b`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.check.Command()
			if err != nil || got != test.want {
				t.Fatalf("command = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestGoalCheckRejectsIncompleteChecks(t *testing.T) {
	for _, check := range []goal.Check{{}, {Argv: []string{""}}, {Argv: []string{"x"}, Shell: "y"}} {
		if _, err := check.Command(); err == nil {
			t.Fatalf("Command(%+v) accepted", check)
		}
	}
}

func TestGoalCheckRejectsMultiwordExecutable(t *testing.T) {
	_, err := (goal.Check{Argv: []string{"go test", "./..."}}).Command()
	if err == nil || !strings.Contains(err.Error(), "one executable") || !strings.Contains(err.Error(), "later argv entries") {
		t.Fatalf("Command() error = %v, want argv guidance", err)
	}
}

func TestGoalVerificationCommandReportsLifecycle(t *testing.T) {
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: t.TempDir(), RunSandboxedOptions: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		if command == "pass" {
			return "ok", "", nil
		}
		return "", "failed", errors.New("exit status 7")
	}}
	var progress []string
	ctx := WithProgress(context.Background(), func(message string) { progress = append(progress, message) })

	passed, err := runGoalVerificationCommand(ctx, registry, "focused tests", "pass", 0)
	if err != nil || passed.ExitCode != 0 {
		t.Fatalf("pass result = %+v, %v", passed, err)
	}
	failed, err := runGoalVerificationCommand(ctx, registry, "lint", "fail", 0)
	if err != nil || failed.ExitCode != 7 {
		t.Fatalf("fail result = %+v, %v", failed, err)
	}
	got := strings.Join(progress, "\n")
	for _, want := range []string{
		"check ▶ focused tests",
		"check ✓ focused tests (exit 0)",
		"check ▶ lint",
		"check ✗ lint (exit 7, failure output detected)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress missing %q:\n%s", want, got)
		}
	}
	if len(progress) != 4 {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestLastNonemptyLine(t *testing.T) {
	if got := lastNonemptyLine("first\nMISMATCH count\n"); got != "MISMATCH count" {
		t.Fatalf("got %q", got)
	}
	if got := lastNonemptyLine(""); got != "no output" {
		t.Fatalf("got %q", got)
	}
}

func TestClassifyGoalCheckResult(t *testing.T) {
	gotest := goal.Check{Argv: []string{"go", "test"}}
	tests := []struct {
		name   string
		check  goal.Check
		result tools.VerificationResult
		want   string
	}{
		{"pass", goal.Check{Shell: "x"}, tools.VerificationResult{ExitCode: 0}, goal.CheckPassed},
		{"zero exit with failure text", goal.Check{Shell: "x"}, tools.VerificationResult{ExitCode: 0, OutputFailure: true, Stdout: "FAIL x"}, goal.CheckPassed},
		{"make target failure", goal.Check{Argv: []string{"make", "test"}}, tools.VerificationResult{ExitCode: 2, Stderr: "make: *** [test] Error 1"}, goal.CheckFailed},
		{"python traceback", goal.Check{Argv: []string{"pytest"}}, tools.VerificationResult{ExitCode: 1, Stdout: strings.Repeat("line\n", 20) + "Traceback (most recent call last):\nAssertionError"}, goal.CheckFailed},
		{"missing executable", goal.Check{Argv: []string{"./scripts/v.sh"}}, tools.VerificationResult{ExitCode: 127, Stderr: "scripts/v.sh: No such file or directory"}, goal.CheckInvalid},
		{"not executable", goal.Check{Argv: []string{"./scripts/v.sh"}}, tools.VerificationResult{ExitCode: 126}, goal.CheckInvalid},
		{"rg error", goal.Check{Argv: []string{"rg", "("}}, tools.VerificationResult{ExitCode: 2, Stderr: "regex parse error"}, goal.CheckInvalid},
		{"sandbox socket denial", gotest, tools.VerificationResult{ExitCode: 1, Stderr: "bind: operation not permitted", Denials: []sandbox.Denial{{Kind: sandbox.DenialSocket, Target: "bind: operation not permitted"}}}, goal.CheckEnvBlocked},
		{"sandbox network denial", gotest, tools.VerificationResult{ExitCode: 1, Denials: []sandbox.Denial{{Kind: sandbox.DenialNetwork, Target: "proxy.golang.org:443"}}}, goal.CheckEnvBlocked},
		{"denial text without a sandbox denial", gotest, tools.VerificationResult{ExitCode: 1, Stderr: "chmod x: operation not permitted"}, goal.CheckFailed},
		{"usage error", goal.Check{Argv: []string{"go", "."}}, tools.VerificationResult{ExitCode: 2, Stderr: "go .: unknown command\nRun 'go help' for usage."}, goal.CheckInvalid},
		{"unknown flag", goal.Check{Argv: []string{"go", "test", "-bogus"}}, tools.VerificationResult{ExitCode: 2, Stderr: "flag provided but not defined: -bogus"}, goal.CheckInvalid},
		{"test log mentions usage text", gotest, tools.VerificationResult{ExitCode: 1, Stdout: strings.Repeat("=== RUN TestCLI\n", 10) + "    cli_test.go:9: got \"unknown command\"\nFAIL"}, goal.CheckFailed},
		{"timeout", goal.Check{Argv: []string{"go"}, TimeoutSeconds: 300}, tools.VerificationResult{ExitCode: -1, TimedOut: true}, goal.CheckFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyGoalCheckResult(test.check, test.result)
			if got.Status != test.want {
				t.Fatalf("status = %q (%s), want %q", got.Status, got.Reason, test.want)
			}
		})
	}
	suspect := classifyGoalCheckResult(goal.Check{Shell: "x"}, tools.VerificationResult{OutputFailure: true, Stdout: "--- FAIL: TestX\nok"})
	if !suspect.Suspect || !strings.Contains(suspect.Reason, "FAIL: TestX") {
		t.Fatalf("suspect pass = %+v", suspect)
	}
	timeout := classifyGoalCheckResult(goal.Check{Argv: []string{"go"}, TimeoutSeconds: 300}, tools.VerificationResult{TimedOut: true})
	if !strings.Contains(timeout.Reason, "timed out after 300 seconds") {
		t.Fatalf("timeout reason = %q", timeout.Reason)
	}
}

func TestGoalCheckTimeoutPassesThroughAndIsClassified(t *testing.T) {
	var timeout int
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: t.TempDir(), RunSandboxedOptions: func(_ context.Context, _, _ string, opts sandbox.RunOptions) (string, string, error) {
		timeout = opts.TimeoutSeconds
		return "", "", errors.New("command timed out after 600 seconds")
	}}
	outcome, err := runAndClassifyGoalCheck(context.Background(), registry, goal.Check{Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 600})
	if err != nil || outcome.Status != goal.CheckFailed || timeout != 600 {
		t.Fatalf("outcome = %+v, %v, timeout %d", outcome, err, timeout)
	}
	if _, err := (goal.Check{Shell: "x", TimeoutSeconds: goal.MaxCheckTimeoutSeconds + 1}).Command(); err == nil {
		t.Fatal("oversized timeout accepted")
	}
}

func TestGoalCheckValidatorCachesProbes(t *testing.T) {
	calls := 0
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: t.TempDir(), RunSandboxedOptions: func(_ context.Context, _, _ string, _ sandbox.RunOptions) (string, string, error) {
		calls++
		return "", "", nil
	}}
	validator := newGoalCheckValidator(registry)
	check := goal.Check{Argv: []string{"go", "test"}}
	for i := 0; i < 3; i++ {
		if err := validator.Validate(context.Background(), check); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2 (syntax + executable)", calls)
	}
}

func TestGoalReviewAndVerdictProgressShowFindings(t *testing.T) {
	review := goal.Review{
		Summary:  "Semaphore keyed by the wrong name.",
		Criteria: []goal.CriterionResult{{Criterion: "Requests queue per resolved model", Status: "unmet"}, {Criterion: "Docs explain slots", Status: "met"}},
		Findings: []goal.Finding{
			{Severity: "blocker", File: "llmctl.go", Line: 210, Problem: "semaphore uses the requested name, not the resolved one"},
			{Severity: "minor", File: "README.md", Problem: "typo"},
		},
	}
	progress := goal.ReviewProgress(review)
	for _, want := range []string{"goal review: 2 findings (1 blocking)", "✗ unmet: Requests queue per resolved model", "✓ met: Docs explain slots", "✗ F1 blocker llmctl.go:210 — semaphore uses the requested name", "• F2 minor README.md — typo"} {
		if !strings.Contains(progress, want) {
			t.Fatalf("review progress missing %q:\n%s", want, progress)
		}
	}
	verdict := goal.Verdict{Decisions: []goal.FindingDecision{{Index: 1, Decision: "accept"}, {Index: 2, Decision: "dismiss", Reason: "cosmetic"}}, Gaps: []string{"tests do not exercise queueing"}}
	judged := goal.VerdictProgress("Not complete.", review, verdict)
	for _, want := range []string{"goal judge: Not complete.", "✗ F1 accepted: semaphore uses", "✓ F2 dismissed: cosmetic", "✗ gap: tests do not exercise queueing"} {
		if !strings.Contains(judged, want) {
			t.Fatalf("verdict progress missing %q:\n%s", want, judged)
		}
	}
}

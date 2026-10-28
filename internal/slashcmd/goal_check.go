package slashcmd

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/makkalot/caretz/internal/goal"
	"github.com/makkalot/caretz/internal/tools"
)

const goalCheckTailSize = goal.CheckTailSize

func shellQuote(v string) string { return goal.ShellQuote(v) }

func runAndClassifyGoalCheck(ctx context.Context, registry *tools.Registry, check goal.Check) (goal.CheckOutcome, error) {
	command, err := check.Command()
	if err != nil {
		return goal.CheckOutcome{Status: goal.CheckInvalid, Reason: err.Error()}, nil
	}
	result, err := runGoalVerificationCommand(ctx, registry, check.Label(), command, check.TimeoutSeconds)
	if err != nil {
		return goal.CheckOutcome{}, err
	}
	return classifyGoalCheckResult(check, result), nil
}

// goalUsageErrorOutput matches a tool rejecting how it was invoked, such as
// "go .: unknown command". The check is malformed; work cannot fix it.
var goalUsageErrorOutput = regexp.MustCompile(`(?i)unknown (sub)?command|unknown (shorthand )?flag|flag provided but not defined|run '[^']+ help' for usage`)

// goalUsageError reports short output that only says the invocation is wrong.
// Long output is a real run whose logs may merely mention such text.
func goalUsageError(output string) bool {
	lines := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	return lines <= 5 && goalUsageErrorOutput.MatchString(output)
}

// classifyGoalCheckResult grades a check from its exit status and the
// sandbox's report. A failing check the sandbox refused something is blocked
// by the environment: work cannot fix it.
func classifyGoalCheckResult(check goal.Check, result tools.VerificationResult) goal.CheckOutcome {
	output := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	out := goal.CheckOutcome{Command: result.Command, ExitCode: result.ExitCode, Tail: goal.OutputTail(output, goalCheckTailSize)}
	switch {
	case result.TimedOut:
		timeout := check.TimeoutSeconds
		if timeout == 0 {
			timeout = 120
		}
		out.Status, out.Reason = goal.CheckFailed, fmt.Sprintf("timed out after %d seconds; set timeout_seconds on the check if it needs longer", timeout)
	case result.ExitCode == 0:
		out.Status, out.Reason = goal.CheckPassed, "exit 0"
		if result.OutputFailure {
			out.Suspect, out.Reason = true, "exit 0, but the output reports a failure: "+goal.Compact(lastFailureLine(output), 200)
		}
	case len(result.Denials) > 0:
		denials := make([]string, 0, len(result.Denials))
		for _, denial := range result.Denials {
			denials = append(denials, "sandbox denied "+denial.String())
		}
		out.Status, out.Reason = goal.CheckEnvBlocked, fmt.Sprintf("exit %d: %s", result.ExitCode, goal.Compact(strings.Join(denials, "; "), 240))
	case result.ExitCode == 126 || result.ExitCode == 127 || strings.Contains(strings.ToLower(output), "command not found"):
		out.Status, out.Reason = goal.CheckInvalid, fmt.Sprintf("exit %d: %s", result.ExitCode, lastNonemptyLine(output))
	case goalUsageError(output):
		out.Status, out.Reason = goal.CheckInvalid, fmt.Sprintf("exit %d: the command is malformed: %s", result.ExitCode, goal.Compact(output, 240))
	case (check.Executable() == "rg" || check.Executable() == "jq") && result.ExitCode >= 2:
		// rg and jq reserve exit codes above 1 for usage or input errors.
		out.Status, out.Reason = goal.CheckInvalid, fmt.Sprintf("exit %d: %s", result.ExitCode, lastNonemptyLine(output))
	default:
		// Build tools such as make exit 2 for an ordinary failing target, so
		// every other nonzero status is work evidence, not a malformed check.
		out.Status, out.Reason = goal.CheckFailed, fmt.Sprintf("exit %d: %s", result.ExitCode, lastNonemptyLine(output))
	}
	return out
}

// lastFailureLine is the last output line that reports a failure.
func lastFailureLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToUpper(lines[i]), "FAIL") || strings.Contains(lines[i], "BAD") {
			return strings.TrimSpace(lines[i])
		}
	}
	return lastNonemptyLine(output)
}

func runGoalVerificationCommand(ctx context.Context, registry *tools.Registry, label, command string, timeoutSeconds int) (tools.VerificationResult, error) {
	label = goal.Preview(label, 140)
	EmitProgress(ctx, "check ▶ "+label)
	result, err := registry.RunVerificationCommandWithTimeout(ctx, command, timeoutSeconds)
	if err != nil {
		EmitProgress(ctx, "check ■ "+label+" ("+previewLine(err.Error(), 120)+")")
		return result, err
	}
	status := fmt.Sprintf("exit %d", result.ExitCode)
	if result.TimedOut {
		status = "timed out"
	}
	if result.OutputFailure {
		status += ", failure output detected"
	}
	mark := "✓"
	if result.ExitCode != 0 || result.OutputFailure || result.TimedOut {
		mark = "✗"
	}
	EmitProgress(ctx, "check "+mark+" "+label+" ("+status+")")
	return result, nil
}

// validateGoalCheck proves a check can run without running it: the command
// parses and argv[0] resolves.
func validateGoalCheck(ctx context.Context, registry *tools.Registry, check goal.Check) error {
	command, err := check.Command()
	if err != nil {
		return err
	}
	syntax, err := registry.RunVerificationCommand(ctx, "bash -n -c "+shellQuote(command))
	if err != nil {
		return err
	}
	if syntax.ExitCode != 0 {
		return fmt.Errorf("invalid shell syntax: %s", lastNonemptyLine(syntax.Stderr))
	}
	executable := check.Executable()
	if executable == "" {
		return nil // bash syntax is the safe validation available for the escape hatch.
	}
	probe, err := registry.RunVerificationCommand(ctx, "command -v -- "+shellQuote(executable))
	if err != nil {
		return err
	}
	if probe.ExitCode != 0 {
		hint := ""
		if !strings.Contains(executable, "/") {
			hint = "; use ./" + executable + " for a workspace script"
		}
		return fmt.Errorf("executable not found: %s%s", filepath.ToSlash(executable), hint)
	}
	return nil
}

// goalCheckValidator memoizes validateGoalCheck within one planning step, so
// repeated plan validation does not spawn the same sandboxed probes again.
type goalCheckValidator struct {
	registry *tools.Registry
	seen     map[string]error
}

func newGoalCheckValidator(registry *tools.Registry) *goalCheckValidator {
	return &goalCheckValidator{registry: registry, seen: map[string]error{}}
}

func (v *goalCheckValidator) Validate(ctx context.Context, check goal.Check) error {
	key := check.Key()
	if err, ok := v.seen[key]; ok {
		return err
	}
	err := validateGoalCheck(ctx, v.registry, check)
	if ctx.Err() == nil {
		v.seen[key] = err
	}
	return err
}

func lastNonemptyLine(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) == "" {
		return "no output"
	}
	return goal.Compact(lines[len(lines)-1], 240)
}

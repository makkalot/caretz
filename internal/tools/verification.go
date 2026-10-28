package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/llm"
)

// VerificationResult preserves the process status that the interactive Bash
// tool normally folds into its textual response.
type VerificationResult struct {
	Command       string `json:"command"`
	ExitCode      int    `json:"exit_code"`
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	OutputFailure bool   `json:"output_failure,omitempty"`
	TimedOut      bool   `json:"timed_out,omitempty"`
	// Denials are what the sandbox refused the command. A failing command
	// with denials failed because of the environment, not the code.
	Denials []sandbox.Denial `json:"denials,omitempty"`
	// Displayed means the command ran on the user's terminal, which already
	// showed its output.
	Displayed bool `json:"-"`
}

var verificationExitStatus = regexp.MustCompile(`(?:exit status|status) ([0-9]+)`)
var verificationTimeout = regexp.MustCompile(`(?i)timed out after [0-9]+ seconds`)
var verificationFailureOutput = regexp.MustCompile(`(?im)(?:^|[[:space:]])(?:FAIL(?:ED)?|utf8BAD|jsonBAD)(?:[[:space:]]|$)`)

// RunVerificationCommand uses the same configured sandbox as other tools.
func (r *Registry) RunVerificationCommand(ctx context.Context, command string) (VerificationResult, error) {
	return r.RunVerificationCommandWithTimeout(ctx, command, 0)
}

// RunCommandWithPermissions runs a foreground command through the configured
// sandbox. Unlike verification commands it can pause for live proxy permissions.
// It never attempts a sandbox-off retry and does not call the model.
func (r *Registry) RunCommandWithPermissions(ctx context.Context, command string) (VerificationResult, error) {
	result := VerificationResult{Command: command, ExitCode: -1}
	if r == nil {
		return result, fmt.Errorf("tool registry is nil")
	}
	if command == "" {
		return result, fmt.Errorf("command is empty")
	}
	if !r.IsToolEnabled("Bash") {
		return result, fmt.Errorf("tool Bash is disabled")
	}
	cwd, err := r.workspaceDir()
	if err != nil {
		return result, err
	}
	arguments, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return result, err
	}
	call := llm.ToolCall{ID: "command-" + uuid.NewV4().String(), Name: "Bash", Arguments: string(arguments)}
	stdout, stderr, runErr := r.runSandboxed(ctx, cwd, command, sandbox.RunOptions{
		TimeoutSeconds: sandbox.DefaultTimeoutSeconds,
		Env:            ActorMetadataEnv(ctx), ReadOnly: r.PlanMode(),
		AuthorizeNetwork: networkAuthorizer(ctx, call, command),
	})
	return r.classifyCommandResult(ctx, cwd, command, stdout, stderr, runErr, "run command")
}

// RunVerificationCommandWithTimeout runs command with timeoutSeconds, or the
// sandbox default when it is zero. A timeout is reported as a result with
// TimedOut set, not as an error, so callers can classify it like a failure.
func (r *Registry) RunVerificationCommandWithTimeout(ctx context.Context, command string, timeoutSeconds int) (VerificationResult, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = sandbox.DefaultTimeoutSeconds
	}
	result := VerificationResult{Command: command, ExitCode: -1}
	if command == "" {
		return result, fmt.Errorf("verification command is empty")
	}
	cwd, err := r.workspaceDir()
	if err != nil {
		return result, err
	}
	return r.runOneShotCommand(ctx, cwd, command, timeoutSeconds, "run verification command")
}

func (r *Registry) runOneShotCommand(ctx context.Context, cwd, command string, timeoutSeconds int, action string) (VerificationResult, error) {
	stdout, stderr, runErr := r.runSandboxed(ctx, cwd, command, sandbox.RunOptions{TimeoutSeconds: timeoutSeconds, Env: ActorMetadataEnv(ctx)})
	return r.classifyCommandResult(ctx, cwd, command, stdout, stderr, runErr, action)
}

// RunInteractiveCommand runs a model-free shell shortcut. Inside an agent
// session it shares the persistent Bash supervisor; standalone callers retain
// the configured one-shot sandbox behavior. The live session is writable, so
// plan mode runs a read-only one-shot command instead.
func (r *Registry) RunInteractiveCommand(ctx context.Context, command string) (VerificationResult, error) {
	if r == nil {
		return VerificationResult{Command: command, ExitCode: -1}, fmt.Errorf("tool registry is nil")
	}
	if r.executionState == nil {
		return r.RunVerificationCommand(ctx, command)
	}
	result := VerificationResult{Command: command, ExitCode: -1}
	if command == "" {
		return result, fmt.Errorf("interactive command is empty")
	}
	cwd, err := r.workspaceDir()
	if err != nil {
		return result, err
	}
	if r.PlanMode() {
		return r.runOneShotCommand(ctx, cwd, command, sandbox.DefaultTimeoutSeconds, "run interactive command")
	}
	session, err := r.executionState.get()
	if err != nil {
		return result, err
	}
	readable, writable, network := r.permissionTargets()
	if _, err := session.AddFilesystemRoots(readable, writable); err != nil {
		return result, fmt.Errorf("refreshing session filesystem access: %w", err)
	}
	stdout, stderr, runErr := session.Run(ctx, cwd, command, sandbox.RunOptions{
		TimeoutSeconds:   sandbox.DefaultTimeoutSeconds,
		Env:              ActorMetadataEnv(ctx),
		NetworkAllowlist: network,
	})
	return r.classifyCommandResult(ctx, cwd, command, stdout, stderr, runErr, "run interactive command")
}

// InteractiveSandboxOff returns the request to rerun a user's shell shortcut
// outside the sandbox when its sandboxed run shows a refusal and
// --ask-sandbox-off is on, or nil when there is nothing to ask.
func (r *Registry) InteractiveSandboxOff(result VerificationResult) *PermissionError {
	if r == nil || !r.canAskSandboxOff() {
		return nil
	}
	cwd, err := r.workspaceDir()
	if err != nil {
		return nil
	}
	escalation := r.sandboxOffEscalation(cwd, result.Command, result.Stdout, result.Stderr, false, llm.Message{}, result.ExitCode == 0)
	if escalation == nil {
		return nil
	}
	request := escalation.(*PermissionError)
	request.Fallback = nil
	return request
}

// RunApprovedInteractiveCommand reruns a user's shell shortcut outside the
// sandbox after the user approved it.
func (r *Registry) RunApprovedInteractiveCommand(ctx context.Context, command string) (VerificationResult, error) {
	result := VerificationResult{Command: command, ExitCode: -1}
	cwd, err := r.workspaceDir()
	if err != nil {
		return result, err
	}
	env, err := r.outsideSandboxEnv(ctx)
	if err != nil {
		return result, err
	}
	run, onTerminal := r.outsideSandboxRunner(ctx)
	r.logInfo("Shell shortcut running outside the sandbox: %s", command)
	stdout, stderr, runErr := run(ctx, cwd, command, sandbox.RunOptions{TimeoutSeconds: sandbox.DefaultTimeoutSeconds, Env: env})
	result, err = r.classifyCommandResult(ctx, cwd, command, stdout, stderr, runErr, "run approved command")
	// Refusal text from a host run is not a sandbox refusal.
	result.Denials = nil
	result.Displayed = onTerminal
	return result, err
}

func (r *Registry) classifyCommandResult(ctx context.Context, cwd, command, stdout, stderr string, runErr error, errorPrefix string) (VerificationResult, error) {
	result := VerificationResult{Command: command, ExitCode: -1}
	result.OutputFailure = verificationFailureOutput.MatchString(stdout) || verificationFailureOutput.MatchString(stderr)
	result.Stdout = TruncateBashOutput(stdout, MaxBashOutputBytes)
	result.Stderr = TruncateBashOutput(stderr, MaxBashOutputBytes)
	if runErr == nil {
		result.ExitCode = 0
		return result, nil
	}
	if ctx.Err() != nil {
		if errors.Is(runErr, ctx.Err()) {
			return result, runErr
		}
		return result, ctx.Err()
	}
	result.Denials = r.outputDenials(cwd, stdout+"\n"+stderr)
	var blocked *sandbox.NetworkBlockedError
	if errors.As(runErr, &blocked) {
		result.Denials = append([]sandbox.Denial{{Kind: sandbox.DenialNetwork, Target: blocked.Target}}, result.Denials...)
		result.ExitCode = 1
		return result, nil
	}
	if verificationTimeout.MatchString(runErr.Error()) {
		result.TimedOut = true
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if match := verificationExitStatus.FindStringSubmatch(runErr.Error()); len(match) == 2 {
		result.ExitCode, _ = strconv.Atoi(match[1])
		return result, nil
	}
	return result, fmt.Errorf("%s: %w", errorPrefix, runErr)
}

// outputDenials reports refusals of the configured sandbox. Without a
// sandbox nothing is refused, whatever the output says.
func (r *Registry) outputDenials(cwd, output string) []sandbox.Denial {
	if r.SandboxDisabled {
		return nil
	}
	// Approvals add writable roots while background commands run.
	mu := r.permissionMutex()
	mu.RLock()
	writable := append([]string{cwd}, r.WritableRoots...)
	mu.RUnlock()
	return sandbox.OutputDenials(output, cwd, func(path string) bool {
		if r.isReadOnlyPath(path) || r.isHiddenPath(path) {
			return false
		}
		for _, root := range writable {
			if sandbox.PathContains(root, path) {
				return true
			}
		}
		return false
	})
}

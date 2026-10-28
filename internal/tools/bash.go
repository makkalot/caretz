package tools

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

type BashToolArguments struct {
	Command        string `json:"command"`
	TimeoutSeconds *int   `json:"timeout_seconds"`
	Background     bool   `json:"background"`
}

func TruncateBashOutput(output string, maxBytes int) string {
	if len(output) <= maxBytes {
		return output
	}
	chunkSize := maxBytes / 3
	head := output[:chunkSize]
	tail := output[len(output)-chunkSize:]
	return fmt.Sprintf(
		"%s\n\n...[output truncated: %d bytes total, showing first and last %d bytes]...\n\n%s",
		head, len(output), chunkSize, tail,
	)
}

func (r *Registry) handleBashToolCall(ctx context.Context, bashTool llm.ToolCall) (llm.Message, error) {
	var args BashToolArguments
	if err := parseToolArgs(bashTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("unmarshaling arguments: %w", err)
	}

	command := args.Command
	if command == "" {
		return llm.Message{}, fmt.Errorf("empty command")
	}
	// Read the mode once so a switch mid-call cannot mix plan and build rules.
	planMode := r.PlanMode()
	if args.Background && !r.SessionExec {
		return llm.Message{}, fmt.Errorf("background execution is not available")
	}
	if args.Background && planMode {
		return llm.Message{}, fmt.Errorf("background execution is not available in plan mode; run the command in the foreground, or ask the user to switch to build mode")
	}
	if args.Background && args.TimeoutSeconds != nil {
		return llm.Message{}, fmt.Errorf("background cannot be combined with timeout_seconds")
	}
	requestedTimeout := 0
	if args.TimeoutSeconds != nil {
		requestedTimeout = *args.TimeoutSeconds
	}
	timeoutSeconds, err := normalizeBashTimeoutSeconds(requestedTimeout)
	if err != nil {
		return llm.Message{}, err
	}

	r.logInfo("Bash tool called: %s", command)

	cwd, err := r.workspaceDir()
	if err != nil {
		return llm.Message{}, fmt.Errorf("getting workspace directory: %w", err)
	}
	// A command the user already approved for the session goes straight to
	// the host, so it does not first run, and partly succeed, in the sandbox.
	if !args.Background && r.canAskSandboxOff() && sandboxOffGranted(ctx, command) {
		return r.runUnsandboxedBash(ctx, bashTool, cwd, command, timeoutSeconds)
	}
	// The live session keeps its writable policy, so plan mode runs each
	// command in a fresh read-only sandbox instead.
	if r.SessionExec && !planMode {
		return r.runSessionBash(ctx, bashTool, cwd, command, timeoutSeconds, args.Background)
	}

	authorizeNetwork := networkAuthorizer(ctx, bashTool, command)
	stdout, stderr, err := r.runSandboxed(ctx, cwd, command, sandbox.RunOptions{TimeoutSeconds: timeoutSeconds, Env: ActorMetadataEnv(ctx), Background: args.Background, ReadOnly: planMode, AuthorizeNetwork: authorizeNetwork})

	if err != nil {
		if ctx.Err() != nil {
			return llm.Message{Role: llm.RoleTool, ToolCallID: bashTool.ID, Content: TruncateBashOutput(stdout+stderr+"\n[Command interrupted; output may be incomplete.]", MaxBashOutputBytes)}, ctx.Err()
		}
		var blocked *sandbox.NetworkBlockedError
		if errors.As(err, &blocked) {
			return llm.Message{}, &PermissionError{Capability: PermissionNetwork, Target: blocked.Target, Reason: "sandbox network policy blocked destination", Command: command, MayHaveRun: true}
		}
		var failed llm.Message
		if stderr != "" {
			r.logInfo("Bash tool sandbox error: %s", stderr)
			failed = llm.Message{Role: llm.RoleTool, Content: TruncateBashOutput(stdout+"sandbox error: "+stderr, MaxBashOutputBytes), ToolCallID: bashTool.ID}
		} else {
			r.logInfo("Bash tool error: %v", err)
			failed = llm.Message{Role: llm.RoleTool, Content: TruncateBashOutput(stdout+fmt.Sprintf("error: %v", err), MaxBashOutputBytes), ToolCallID: bashTool.ID}
		}
		if escalation := r.sandboxOffEscalation(cwd, command, stdout, stderr, args.Background, failed, false); escalation != nil {
			return llm.Message{}, escalation
		}
		return failed, nil
	}

	r.logInfo("Bash tool completed: %d bytes output", len(stdout))
	result := llm.Message{Role: llm.RoleTool, Content: TruncateBashOutput(stdout, MaxBashOutputBytes), ToolCallID: bashTool.ID}
	if escalation := r.sandboxOffEscalation(cwd, command, stdout, stderr, args.Background, result, true); escalation != nil {
		return llm.Message{}, escalation
	}
	return result, nil
}

// ForegroundRunner runs an approved unsandboxed command on the user's
// terminal, so programs such as sudo can prompt there. Interactive adapters
// that own a terminal provide one; elsewhere commands run without a TTY.
type ForegroundRunner func(ctx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error)

type foregroundRunnerKey struct{}

func WithForegroundRunner(ctx context.Context, run ForegroundRunner) context.Context {
	return context.WithValue(ctx, foregroundRunnerKey{}, run)
}

func (r *Registry) canAskSandboxOff() bool {
	return r.AskSandboxOff && !r.SandboxDisabled && !r.PlanMode() && r.RunUnsandboxed != nil
}

// sandboxOffGranted reports whether the user approved running this exact
// command outside the sandbox. Approvals never cover what a command's output
// claims it needed, such as a host service: the model writes the command and
// so its output, and could print a refusal to escape the sandbox.
func sandboxOffGranted(ctx context.Context, command string) bool {
	return permissionGranted(ctx, PermissionSandboxOff, command) || sessionPermissionGranted(ctx, PermissionSandboxOff, command)
}

// commandRunsSudo matches sudo as a command word, not inside another word.
var commandRunsSudo = regexp.MustCompile(`(?:^|[\s;&|(` + "`" + `])sudo(?:\s|$)`)

// sandboxOffEscalation turns a command whose output shows a sandbox refusal
// into a request for the user to rerun it outside the sandbox. The user, not
// the model, decides: models often retry blocked commands instead of
// reporting the refusal. Generic refusals count only for failed commands, so
// a successful one that merely prints refusal text, such as a log search,
// keeps its result. A hidden host service counts even when the command
// succeeded, since "; echo $?" or a pipe hides its failure, and so does sudo's
// own refusal from a command that runs sudo: models probe with
// "sudo -n true; echo $?" and then conclude sudo can never work. The model writes
// the command and so its output; the refusal is a hint for the user, never a
// reason to trust the command. result is what the model sees when the user
// declines or cannot be asked.
func (r *Registry) sandboxOffEscalation(cwd, command, stdout, stderr string, background bool, result llm.Message, succeeded bool) error {
	if background || !r.canAskSandboxOff() {
		return nil
	}
	denials := r.outputDenials(cwd, stdout+"\n"+stderr)
	if succeeded {
		sudoRefused := commandRunsSudo.MatchString(command) && sandbox.SudoRefused(stdout+"\n"+stderr)
		denials = slices.DeleteFunc(denials, func(denial sandbox.Denial) bool {
			return denial.Kind != sandbox.DenialService && (denial != sandbox.SudoDenial || !sudoRefused)
		})
	}
	if len(denials) == 0 {
		return nil
	}
	blocked := make([]string, 0, len(denials))
	for _, denial := range denials {
		blocked = append(blocked, denial.String())
	}
	reason := "sandbox blocked: " + strings.Join(blocked, "; ")
	if r.SessionExec {
		reason += "; the rerun is a separate process without the session's /tmp or background jobs"
	}
	r.logInfo("Bash tool sandbox refusal, asking the user to rerun outside the sandbox: %s", strings.Join(blocked, "; "))
	return &PermissionError{Capability: PermissionSandboxOff, Target: command, Reason: reason, Command: command, MayHaveRun: true, Fallback: &result}
}

// runUnsandboxedBash reruns one user-approved command directly on the host.
// It deliberately bypasses the session supervisor, so it neither sees nor
// changes persistent session state. It runs with the sandbox's granted
// environment. Its result says where it ran, so the model does not take the
// host access it saw for the sandbox's and misread later sandboxed failures.
func (r *Registry) runUnsandboxedBash(ctx context.Context, bashTool llm.ToolCall, cwd, command string, timeoutSeconds int) (llm.Message, error) {
	env, err := r.outsideSandboxEnv(ctx)
	if err != nil {
		return llm.Message{}, err
	}
	run, onTerminal := r.outsideSandboxRunner(ctx)
	where := "without a terminal"
	if onTerminal {
		where = "on the user's terminal"
	}
	r.logInfo("Bash tool running outside the sandbox %s: %s", where, command)
	stdout, stderr, err := run(ctx, cwd, command, sandbox.RunOptions{TimeoutSeconds: timeoutSeconds, Env: env})
	if err != nil && ctx.Err() != nil {
		return llm.Message{Role: llm.RoleTool, ToolCallID: bashTool.ID, Content: TruncateBashOutput(stdout+stderr+"\n[Command interrupted; output may be incomplete.]", MaxBashOutputBytes)}, ctx.Err()
	}
	content := stdout
	if err != nil {
		r.logInfo("Bash tool outside the sandbox failed: %v", err)
		content += stderr + fmt.Sprintf("error: %v", err)
	}
	// An empty result such as "sudo tee FILE > /dev/null" must not read as
	// nothing having happened.
	if content == "" {
		content = "(no output)"
	}
	content = TruncateBashOutput(content, MaxBashOutputBytes)
	return llm.Message{Role: llm.RoleTool, Content: unsandboxedResultNote + content, ToolCallID: bashTool.ID}, nil
}

// outsideSandboxRunner runs approved commands on the user's terminal when the
// context provides one, so the user sees the output as it runs.
func (r *Registry) outsideSandboxRunner(ctx context.Context) (run ForegroundRunner, onTerminal bool) {
	if foreground, ok := ctx.Value(foregroundRunnerKey{}).(ForegroundRunner); ok && foreground != nil {
		return foreground, true
	}
	return r.RunUnsandboxed, false
}

// outsideSandboxEnv is the sandbox's granted environment, which approved
// commands keep when they run on the host.
func (r *Registry) outsideSandboxEnv(ctx context.Context) (map[string]string, error) {
	env := map[string]string{}
	if r.SandboxEnvironment != nil {
		granted, err := r.SandboxEnvironment()
		if err != nil {
			return nil, fmt.Errorf("resolving sandbox environment: %w", err)
		}
		maps.Copy(env, granted)
	}
	maps.Copy(env, ActorMetadataEnv(ctx))
	return env, nil
}

const unsandboxedResultNote = "[The user approved running this command outside the sandbox; later commands run sandboxed again.]\n"

func normalizeBashTimeoutSeconds(requested int) (int, error) {
	if requested < 0 {
		return 0, fmt.Errorf("timeout_seconds must be >= 0")
	}
	if requested == 0 {
		return sandbox.DefaultTimeoutSeconds, nil
	}
	if requested > sandbox.MaxTimeoutSeconds {
		return sandbox.MaxTimeoutSeconds, nil
	}
	return requested, nil
}

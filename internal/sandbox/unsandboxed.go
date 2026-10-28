package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// caretzSecretEnv names Caretz's own credentials. Sandboxed commands never see
// them, so neither does a command the user approved to leave the sandbox.
var caretzSecretEnv = []string{"OPENAI_API_KEY", "TAVILY_API_KEY", "BRAVE_SEARCH_API_KEY"}

// hostWaitDelay is how long a finished command's background jobs may keep its
// output open before Caretz stops reading it.
var hostWaitDelay = 5 * time.Second

const outputLeftOpenNote = "\n[The command exited, but a background process it started still held its output open; later output from it was not captured.]\n"

// RunUnsandboxed runs command directly on the host with the caller's full
// permissions and environment. It is only for runs where the sandbox was
// disabled as a whole.
func RunUnsandboxed(ctx context.Context, cwd, command string, opts RunOptions) (string, string, error) {
	return runOnHost(ctx, cwd, command, opts, os.Environ(), nil, nil)
}

// RunApproved runs one command the user approved to leave the sandbox. It is
// a one-shot process: it never goes through a session supervisor and shares
// none of its state, and it does not inherit Caretz's own credentials.
func RunApproved(ctx context.Context, cwd, command string, opts RunOptions) (string, string, error) {
	return runOnHost(ctx, cwd, command, opts, approvedEnvironment(), nil, nil)
}

// RunApprovedForeground is RunApproved as the terminal's foreground job, so
// programs such as sudo can prompt on tty. The caller must have released the
// terminal (no raw mode, no concurrent reads). Output is shown on display
// while it runs and returned for the model.
func RunApprovedForeground(ctx context.Context, cwd, command string, opts RunOptions, tty *os.File, display io.Writer) (string, string, error) {
	return runOnHost(ctx, cwd, command, opts, approvedEnvironment(), tty, display)
}

func approvedEnvironment() []string {
	return slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(caretzSecretEnv, name) || strings.HasPrefix(name, "_SANDBOX_")
	})
}

func runOnHost(ctx context.Context, cwd, command string, opts RunOptions, env []string, tty *os.File, display io.Writer) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := resolveTimeoutSeconds(opts)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = mergeEnv(env, opts.Env)
	var stdout, stderr bytes.Buffer
	var out, errOut io.Writer = &limitedWriter{w: &stdout, maxBytes: sandboxMaxOutput}, &limitedWriter{w: &stderr, maxBytes: sandboxMaxOutput}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if tty != nil {
		cmd.Stdin = tty
		out, errOut = io.MultiWriter(out, display), io.MultiWriter(errOut, display)
		// The job gets its own process group in the terminal's foreground, so
		// Ctrl-C reaches it rather than Caretz.
		cmd.SysProcAttr.Foreground = true
		cmd.SysProcAttr.Ctty = int(tty.Fd())
	}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = hostWaitDelay
	err := cmd.Run()
	if tty != nil {
		reclaimForeground(tty)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), fmt.Errorf("command timed out after %d seconds", timeout)
	}
	// Wait reports ErrWaitDelay only when the command itself succeeded: a
	// background job such as "cd x && setsid build > log &" kept the output
	// pipes open after the shell exited. That is no failure, but the model
	// should know later output was not captured.
	if errors.Is(err, exec.ErrWaitDelay) {
		if display != nil {
			_, _ = io.WriteString(display, outputLeftOpenNote)
		}
		stdout.WriteString(outputLeftOpenNote)
		err = nil
	}
	if err == nil {
		err = ctx.Err()
	}
	return stdout.String(), stderr.String(), err
}

// reclaimForeground makes Caretz's process group the terminal's foreground
// again. A background group changing the foreground would be stopped by
// SIGTTOU, so it is ignored for the call.
func reclaimForeground(tty *os.File) {
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	_ = unix.IoctlSetPointerInt(int(tty.Fd()), unix.TIOCSPGRP, unix.Getpgrp())
}

// Package herdr reports Caretz's interactive lifecycle to a containing Herdr pane.
package herdr

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	StateIdle    = "idle"
	StateWorking = "working"
	StateBlocked = "blocked"

	defaultTimeout = 500 * time.Millisecond
	maxResumeArgs  = 64
	maxResumeBytes = 8 * 1024
)

// Runner invokes the Herdr binary without a shell.
type Runner func(context.Context, string, []string) error

// Options describes the Caretz session that Herdr may restore.
type Options struct {
	Adapter            string
	ConfigRoot         string
	ConfigRootSupplied bool
	Resumable          bool
	Getenv             func(string) string
	Run                Runner
	Timeout            time.Duration
	Now                func() time.Time

	// ResumeCommand replaces the default "caretz -s <id> --adapter <a>"
	// resume command for sessions reopened another way, such as peer bots.
	ResumeCommand []string
	// DisplayName replaces "caretz" as the pane's name in Herdr's agent list,
	// so peer bots show up by name.
	DisplayName string
}

type report struct {
	state     string
	message   string
	sessionID string
	seq       uint64
	release   bool
}

// Reporter asynchronously publishes only the latest Caretz lifecycle state.
type Reporter struct {
	bin                string
	pane               string
	adapter            string
	configRoot         string
	configRootSupplied bool
	resumable          bool
	resumeCommand      []string
	displayName        string
	run                Runner
	timeout            time.Duration
	now                func() time.Time

	mu      sync.Mutex
	closed  bool
	lastSeq uint64
	reports chan report
}

// NewFromEnvironment returns nil outside a valid Herdr pane.
func NewFromEnvironment(opts Options) *Reporter {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv("HERDR_ENV")) != "1" {
		return nil
	}
	bin := strings.TrimSpace(getenv("HERDR_BIN_PATH"))
	pane := strings.TrimSpace(getenv("HERDR_PANE_ID"))
	socket := strings.TrimSpace(getenv("HERDR_SOCKET_PATH"))
	if bin == "" || pane == "" || socket == "" || !filepath.IsAbs(bin) {
		return nil
	}
	run := opts.Run
	if run == nil {
		run = runCommand
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	r := &Reporter{
		bin: bin, pane: pane, adapter: strings.TrimSpace(opts.Adapter),
		configRoot: strings.TrimSpace(opts.ConfigRoot), configRootSupplied: opts.ConfigRootSupplied, resumable: opts.Resumable,
		resumeCommand: append([]string(nil), opts.ResumeCommand...), displayName: strings.TrimSpace(opts.DisplayName), run: run, timeout: timeout, now: now,
		reports: make(chan report, 1),
	}
	go r.loop()
	return r
}

// Report queues a state update without waiting for Herdr.
func (r *Reporter) Report(state, message, sessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	rep := report{state: state, message: message, sessionID: strings.TrimSpace(sessionID), seq: r.nextSequenceLocked()}
	r.replaceQueued(rep)
	r.mu.Unlock()
}

// Close sends a final release report and waits only for the configured timeout.
func (r *Reporter) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	rep := report{release: true, seq: r.nextSequenceLocked()}
	close(r.reports)
	r.mu.Unlock()
	if r.displayName != "" {
		// The pane outlives the bot; don't leave its name behind.
		r.sendMetadata("--clear-display-agent")
	}
	r.send(rep)
}

func (r *Reporter) replaceQueued(rep report) {
	select {
	case r.reports <- rep:
		return
	default:
	}
	select {
	case <-r.reports:
	default:
	}
	r.reports <- rep
}

func (r *Reporter) nextSequenceLocked() uint64 {
	now := uint64(r.now().UnixNano())
	if now <= r.lastSeq {
		now = r.lastSeq + 1
	}
	r.lastSeq = now
	return now
}

func (r *Reporter) loop() {
	named := false
	for rep := range r.reports {
		r.send(rep)
		if !named && r.displayName != "" {
			// Herdr attaches the name to the agent, so register it first.
			r.sendMetadata("--display-agent", r.displayName)
			named = true
		}
	}
}

func (r *Reporter) sendMetadata(args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	_ = r.run(ctx, r.bin, append([]string{"pane", "report-metadata", r.pane, "--source", "caretz"}, args...))
}

func (r *Reporter) send(rep report) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	args := []string{"pane", "report-agent", r.pane, "--source", "caretz", "--agent", "caretz", "--state", rep.state, "--seq", strconv.FormatUint(rep.seq, 10)}
	if rep.release {
		args = []string{"pane", "release-agent", r.pane, "--source", "caretz", "--agent", "caretz", "--seq", strconv.FormatUint(rep.seq, 10)}
	} else {
		if rep.message != "" {
			args = append(args, "--message", rep.message)
		}
		if resume := r.resumeArgs(rep.sessionID); len(resume) > 0 {
			args = append(args, "--agent-session-id", rep.sessionID, "--")
			args = append(args, resume...)
		}
	}
	_ = r.run(ctx, r.bin, args)
}

func (r *Reporter) resumeArgs(sessionID string) []string {
	if !r.resumable || sessionID == "" {
		return nil
	}
	args := []string{"caretz", "-s", sessionID}
	if r.adapter != "" {
		args = append(args, "--adapter", r.adapter)
	}
	if len(r.resumeCommand) > 0 {
		args = append([]string(nil), r.resumeCommand...)
	}
	if r.configRootSupplied && r.configRoot != "" {
		// The resolved root is required only when the caller explicitly selected it.
		args = append(args, "--config-root", r.configRoot)
	}
	if !validResumeArgs(args) {
		return nil
	}
	return args
}

func validResumeArgs(args []string) bool {
	if len(args) == 0 || len(args) > maxResumeArgs {
		return false
	}
	// Herdr rejects the whole report when the command is a path.
	if strings.ContainsRune(args[0], '/') {
		return false
	}
	total := 0
	for _, arg := range args {
		total += len(arg)
		if strings.ContainsRune(arg, '\'') || strings.IndexFunc(arg, unicode.IsControl) >= 0 {
			return false
		}
	}
	return total <= maxResumeBytes
}

func runCommand(ctx context.Context, path string, args []string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

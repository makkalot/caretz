// Package tmux opens panes for background processes in the containing tmux
// session.
package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/herdr"
)

// OutputRunner invokes the tmux binary without a shell and returns stdout.
type OutputRunner func(context.Context, string, []string) ([]byte, error)

// Spawner opens windows in the tmux session caretz runs in.
type Spawner struct {
	bin     string
	run     OutputRunner
	timeout time.Duration
}

// SpawnerOptions configures NewSpawnerFromEnvironment.
type SpawnerOptions struct {
	Getenv   func(string) string
	LookPath func(string) (string, error)
	Run      OutputRunner
	Timeout  time.Duration
}

// NewSpawnerFromEnvironment returns nil unless caretz runs inside tmux. Having
// tmux installed is not enough: panes in a session nobody is attached to would
// leave bots waiting on prompts no one sees.
func NewSpawnerFromEnvironment(opts SpawnerOptions) *Spawner {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv("TMUX")) == "" {
		return nil
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	bin, err := lookPath("tmux")
	if err != nil {
		return nil
	}
	run := opts.Run
	if run == nil {
		run = runOutputCommand
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Spawner{bin: bin, run: run, timeout: timeout}
}

// SpawnTab opens one background window named tabLabel in cwd and starts each
// command in its own pane, tiled unless layout asks for rows or columns.
// Commands are typed into each pane's shell, so a command that exits leaves
// its output on screen. It returns the pane IDs it started, in order, even
// when a later step fails.
func (s *Spawner) SpawnTab(ctx context.Context, tabLabel, cwd string, layout herdr.Layout, commands []herdr.PaneCommand) ([]string, error) {
	if s == nil || len(commands) == 0 {
		return nil, nil
	}
	tmuxLayout := "tiled"
	switch layout {
	case herdr.LayoutRows:
		tmuxLayout = "even-vertical"
	case herdr.LayoutColumns:
		tmuxLayout = "even-horizontal"
	}
	out, err := s.call(ctx, "new-window", "-d", "-P", "-F", "#{window_id} #{pane_id}", "-n", tabLabel, "-c", cwd)
	if err != nil {
		return nil, fmt.Errorf("create tmux window: %w", err)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return nil, fmt.Errorf("create tmux window: unexpected output %q", out)
	}
	window, pane := fields[0], fields[1]
	var started []string
	for index, command := range commands {
		if index > 0 {
			out, err := s.call(ctx, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", window, "-c", cwd)
			if err != nil {
				return started, fmt.Errorf("split tmux pane for %s: %w", command.Label, err)
			}
			if pane = strings.TrimSpace(out); pane == "" {
				return started, fmt.Errorf("split tmux pane for %s: no pane id", command.Label)
			}
			// Re-tile after every split so later splits still have room.
			_, _ = s.call(ctx, "select-layout", "-t", window, tmuxLayout)
		}
		if command.Label != "" {
			_, _ = s.call(ctx, "select-pane", "-t", pane, "-T", command.Label)
		}
		if _, err := s.call(ctx, "send-keys", "-t", pane, "-l", herdr.ShellQuote(command.Command)); err != nil {
			return started, fmt.Errorf("start %s in tmux pane: %w", command.Label, err)
		}
		if _, err := s.call(ctx, "send-keys", "-t", pane, "Enter"); err != nil {
			return started, fmt.Errorf("start %s in tmux pane: %w", command.Label, err)
		}
		started = append(started, pane)
	}
	return started, nil
}

func (s *Spawner) call(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	data, err := s.run(ctx, s.bin, args)
	return string(data), err
}

func runOutputCommand(ctx context.Context, path string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil
	return cmd.Output()
}

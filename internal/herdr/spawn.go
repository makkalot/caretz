package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// OutputRunner invokes the Herdr binary without a shell and returns stdout.
type OutputRunner func(context.Context, string, []string) ([]byte, error)

// Spawner opens panes for background processes in the containing Herdr
// session.
type Spawner struct {
	bin       string
	workspace string
	run       OutputRunner
	timeout   time.Duration
}

// SpawnerOptions configures NewSpawnerFromEnvironment.
type SpawnerOptions struct {
	Getenv  func(string) string
	Run     OutputRunner
	Timeout time.Duration
}

// NewSpawnerFromEnvironment returns nil outside a valid Herdr pane.
func NewSpawnerFromEnvironment(opts SpawnerOptions) *Spawner {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv("HERDR_ENV")) != "1" {
		return nil
	}
	bin := strings.TrimSpace(getenv("HERDR_BIN_PATH"))
	if bin == "" || !filepath.IsAbs(bin) || strings.TrimSpace(getenv("HERDR_PANE_ID")) == "" || strings.TrimSpace(getenv("HERDR_SOCKET_PATH")) == "" {
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
	return &Spawner{bin: bin, workspace: strings.TrimSpace(getenv("HERDR_WORKSPACE_ID")), run: run, timeout: timeout}
}

// PaneCommand is a labelled command to start in its own pane.
type PaneCommand struct {
	Label string
	// Command is shell-quoted by the spawner before Herdr types it.
	Command []string
}

// Layout arranges the panes of a spawned tab.
type Layout string

const (
	// LayoutDefault keeps the multiplexer's usual arrangement: stacked rows
	// in Herdr, tiled in tmux.
	LayoutDefault Layout = ""
	// LayoutRows stacks panes top to bottom.
	LayoutRows Layout = "rows"
	// LayoutColumns places panes side by side.
	LayoutColumns Layout = "columns"
)

// SpawnTab opens one background tab labelled tabLabel in cwd and starts each
// command in its own pane, stacked vertically unless layout is columns. It
// returns the pane IDs it started, in order, even when a later step fails.
func (s *Spawner) SpawnTab(ctx context.Context, tabLabel, cwd string, layout Layout, commands []PaneCommand) ([]string, error) {
	if s == nil || len(commands) == 0 {
		return nil, nil
	}
	args := []string{"tab", "create", "--label", tabLabel, "--cwd", cwd, "--no-focus"}
	if s.workspace != "" {
		args = append(args, "--workspace", s.workspace)
	}
	var created struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := s.call(ctx, args, &created); err != nil {
		return nil, fmt.Errorf("create herdr tab: %w", err)
	}
	pane := created.Result.RootPane.PaneID
	if pane == "" {
		return nil, fmt.Errorf("create herdr tab: response has no root pane")
	}
	direction := "down"
	if layout == LayoutColumns {
		direction = "right"
	}
	var started []string
	for index, command := range commands {
		if index > 0 {
			var split struct {
				Result struct {
					Pane struct {
						PaneID string `json:"pane_id"`
					} `json:"pane"`
				} `json:"result"`
			}
			if err := s.call(ctx, []string{"pane", "split", pane, "--direction", direction, "--cwd", cwd, "--no-focus"}, &split); err != nil {
				return started, fmt.Errorf("split herdr pane for %s: %w", command.Label, err)
			}
			if split.Result.Pane.PaneID == "" {
				return started, fmt.Errorf("split herdr pane for %s: response has no pane", command.Label)
			}
			pane = split.Result.Pane.PaneID
		}
		if command.Label != "" {
			_ = s.call(ctx, []string{"pane", "rename", pane, command.Label}, nil)
		}
		if err := s.call(ctx, []string{"pane", "run", pane, ShellQuote(command.Command)}, nil); err != nil {
			return started, fmt.Errorf("start %s in herdr pane: %w", command.Label, err)
		}
		started = append(started, pane)
	}
	return started, nil
}

func (s *Spawner) call(ctx context.Context, args []string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	data, err := s.run(ctx, s.bin, args)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// ShellQuote joins args into one POSIX shell command line.
func ShellQuote(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg != "" && strings.IndexFunc(arg, needsShellQuote) < 0 {
			quoted[i] = arg
			continue
		}
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

func needsShellQuote(r rune) bool {
	safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r)
	return !safe
}

func runOutputCommand(ctx context.Context, path string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil
	return cmd.Output()
}

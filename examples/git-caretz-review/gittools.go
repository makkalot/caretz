package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/tools"
)

const maxGitOutput = 200 * 1024

// Keep draining the pipe after the limit so large diffs cannot block Git.
type cappedOutput struct {
	data      []byte
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxGitOutput - len(w.data)
	if n > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	w.data = append(w.data, p...)
	return n, nil
}

func (w *cappedOutput) String() string {
	result := string(w.data)
	if w.truncated {
		result += "\n[Git output truncated at 200 KiB; request GitDiff for individual files.]\n"
	}
	return result
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "--literal-pathspecs", "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_PAGER=cat", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr cappedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

type review struct {
	root, base, mergeBase, head, stat string
}

func prepareReview(ctx context.Context, workspace, base string) (review, error) {
	var r review
	root, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return r, fmt.Errorf("not a Git repository: %w", err)
	}
	r.root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return r, err
	}
	resolve := func(ref string) (string, error) {
		value, err := runGit(ctx, r.root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		return strings.TrimSpace(value), err
	}
	if base == "" {
		base = "main"
		if _, err := resolve(base); err != nil {
			base = "master"
		}
	}
	r.base = base
	baseID, err := resolve(base)
	if err != nil {
		return r, fmt.Errorf("unknown base %q: %w", base, err)
	}
	r.head, err = resolve("HEAD")
	if err != nil {
		return r, err
	}
	mergeBase, err := runGit(ctx, r.root, "merge-base", baseID, r.head)
	if err != nil {
		return r, fmt.Errorf("base %q has no common ancestor with HEAD: %w", base, err)
	}
	r.mergeBase = strings.TrimSpace(mergeBase)
	stat, err := runGit(ctx, r.root, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--stat", r.mergeBase, r.head, "--")
	r.stat = strings.TrimSpace(stat)
	return r, err
}

func (r review) tools() []tools.Tool {
	return []tools.Tool{
		{
			Definition: llm.ToolDefinition{
				Name: "GitDiff", Description: "Read the committed branch diff, optionally for one repository-relative file. Use individual files if output is truncated.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Optional literal repository-relative file path."},
				}, "additionalProperties": false},
			},
			Handler: func(ctx context.Context, call llm.ToolCall) (string, error) {
				var params struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal([]byte(call.Arguments), &params); err != nil {
					return "", fmt.Errorf("invalid GitDiff arguments: %w", err)
				}
				args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", r.mergeBase, r.head, "--"}
				if params.Path != "" {
					if filepath.IsAbs(params.Path) || strings.HasPrefix(params.Path, "-") {
						return "", fmt.Errorf("path must be relative to the repository and cannot start with '-'")
					}
					if safe, reason := tools.IsPathSafe(filepath.Join(r.root, params.Path), r.root); !safe {
						return "", fmt.Errorf("unsafe diff path: %s", reason)
					}
					args = append(args, params.Path)
				}
				return runGit(ctx, r.root, args...)
			},
		},
		{
			Definition: llm.ToolDefinition{
				Name: "GitLog", Description: "Read commit subjects and bodies for the reviewed branch.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			},
			Handler: func(ctx context.Context, _ llm.ToolCall) (string, error) {
				return runGit(ctx, r.root, "log", "--no-color", "--no-decorate", "--format=%h %s%n%b", r.mergeBase+".."+r.head, "--")
			},
		},
	}
}

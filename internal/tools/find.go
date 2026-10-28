package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

type FindToolArguments struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Limit   int    `json:"limit"`
}

func Shellquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func (r *Registry) handleFindToolCall(ctx context.Context, findTool llm.ToolCall) (llm.Message, error) {
	var args FindToolArguments
	if err := parseToolArgs(findTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}

	if args.Pattern == "" {
		return llm.Message{}, fmt.Errorf("pattern is required")
	}

	limit := args.Limit
	if limit <= 0 {
		limit = DefaultFindLimit
	}

	r.logDebug("Find tool called: pattern=%q path=%q limit=%d", args.Pattern, args.Path, limit)

	cwd, err := r.workspaceDir()
	if err != nil {
		return llm.Message{}, fmt.Errorf("getting workspace directory: %w", err)
	}
	searchPath := cwd
	if args.Path != "" {
		searchPath = args.Path
		if !filepath.IsAbs(searchPath) {
			searchPath = filepath.Join(cwd, searchPath)
		}
	}
	absCwd, _ := filepath.Abs(cwd)
	absSearch, _ := filepath.Abs(searchPath)
	if absSearch != absCwd {
		safe, reason := r.isPathReadableContext(ctx, searchPath, cwd)
		if !safe {
			return llm.Message{}, r.outsideRootPermission(PermissionPathRead, searchPath, reason, true)
		}
	}

	var command string
	if _, err := exec.LookPath("fd"); err == nil {
		command = fmt.Sprintf("fd --glob %s --max-results %d --color never %s",
			Shellquote(args.Pattern), limit, Shellquote(searchPath))
	} else {
		command = fmt.Sprintf("find %s -name %s -type f 2>/dev/null | head -n %d",
			Shellquote(searchPath), Shellquote(args.Pattern), limit)
	}

	stdout, stderr, err := r.runSandboxed(ctx, cwd, command, sandbox.RunOptions{})

	if err != nil {
		if ctx.Err() != nil {
			return llm.Message{}, ctx.Err()
		}
		if strings.Contains(err.Error(), "exit status 1") && stderr == "" {
			return llm.Message{Role: llm.RoleTool, Content: "No files found.", ToolCallID: findTool.ID}, nil
		}
		if stderr != "" {
			return llm.Message{}, fmt.Errorf("find error: %s", stderr)
		}
		return llm.Message{}, fmt.Errorf("find error: %v", err)
	}

	if strings.TrimSpace(stdout) == "" {
		return llm.Message{Role: llm.RoleTool, Content: "No files found.", ToolCallID: findTool.ID}, nil
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	var relativized []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rel, err := filepath.Rel(cwd, line)
		if err != nil {
			relativized = append(relativized, line)
		} else {
			relativized = append(relativized, rel)
		}
	}

	resultCount := len(relativized)
	output := strings.Join(relativized, "\n")

	output, byteTruncated := TruncateHead(output, MaxToolOutputBytes)

	var notices []string
	if resultCount >= limit {
		notices = append(notices, fmt.Sprintf("[%d results limit reached. Use limit=%d for more, or refine pattern]", limit, limit*2))
	}
	if byteTruncated {
		notices = append(notices, "[Output truncated at 50KB limit]")
	}
	if len(notices) > 0 {
		output += "\n" + strings.Join(notices, "\n")
	}

	r.logDebug("Find tool completed: %d results", resultCount)
	return llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: findTool.ID}, nil
}

package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/lib/llm"
)

type LsToolArguments struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
}

func (r *Registry) handleLsToolCall(ctx context.Context, lsTool llm.ToolCall) (llm.Message, error) {
	var args LsToolArguments
	if err := parseToolArgs(lsTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}

	limit := args.Limit
	if limit <= 0 {
		limit = DefaultLsLimit
	}

	cwd, err := r.workspaceDir()
	if err != nil {
		return llm.Message{}, fmt.Errorf("getting workspace directory: %w", err)
	}
	targetPath := cwd
	if args.Path != "" {
		targetPath = args.Path
		if !filepath.IsAbs(targetPath) {
			targetPath = filepath.Join(cwd, targetPath)
		}
	}

	r.logInfo("Ls tool called: path=%q limit=%d", targetPath, limit)

	absCwd, _ := filepath.Abs(cwd)
	absTarget, _ := filepath.Abs(targetPath)
	if absTarget != absCwd {
		safe, reason := r.isPathReadableContext(ctx, targetPath, cwd)
		if !safe {
			return llm.Message{}, r.outsideRootPermission(PermissionPathRead, targetPath, reason, true)
		}
	}

	entries, err := os.ReadDir(targetPath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("listing directory: %v", err)
	}

	var lines []string
	for i, entry := range entries {
		if i >= limit {
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}

	output := strings.Join(lines, "\n")

	output, byteTruncated := TruncateHead(output, MaxToolOutputBytes)

	totalEntries := len(entries)
	var notices []string
	if totalEntries > limit {
		notices = append(notices, fmt.Sprintf("[%d entries limit reached (%d total). Use limit=%d for more]", limit, totalEntries, limit*2))
	}
	if byteTruncated {
		notices = append(notices, "[Output truncated at 50KB limit]")
	}
	if len(notices) > 0 {
		output += "\n" + strings.Join(notices, "\n")
	}

	if len(lines) == 0 {
		output = "(empty directory)"
	}

	r.logInfo("Ls tool completed: %d entries (of %d total)", len(lines), totalEntries)
	return llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: lsTool.ID}, nil
}

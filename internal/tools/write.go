package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/makkalot/caretz/lib/llm"
)

type WriteToolArguments struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func (r *Registry) handleWriteToolCall(ctx context.Context, writeTool llm.ToolCall) (llm.Message, error) {
	var args WriteToolArguments
	if err := parseToolArgs(writeTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}

	if args.FilePath == "" {
		return llm.Message{}, fmt.Errorf("file_path is required")
	}

	filePath, cwd, err := r.resolveWorkspacePath(args.FilePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("getting workspace path: %w", err)
	}
	safe, reason := r.isPathWritableContext(ctx, filePath, cwd)
	if !safe {
		return llm.Message{}, r.outsideRootPermission(PermissionPathWrite, filePath, reason, false)
	}

	r.logDebug("Write tool called: %s", args.FilePath)

	// Ensure parent directories exist.
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return llm.Message{}, fmt.Errorf("creating parent directories: %w", err)
	}

	contentLen := len(args.Content)
	if err := os.WriteFile(filePath, []byte(args.Content), 0644); err != nil {
		return llm.Message{}, fmt.Errorf("writing file: %w", err)
	}

	r.logDebug("Write tool completed: %d bytes written to %s", contentLen, filePath)
	result := fmt.Sprintf("%d bytes written to path %s", contentLen, args.FilePath)
	return llm.Message{Role: llm.RoleTool, Content: result, ToolCallID: writeTool.ID}, nil
}

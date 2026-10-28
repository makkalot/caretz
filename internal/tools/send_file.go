package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/lib/llm"
)

const MaxSendFileBytes = 49 * 1024 * 1024

type FileSender interface {
	SendFile(context.Context, string, string, string) (string, error)
}

type sendFileMessageIDContextKey struct{}

func WithSendFileMessageID(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, sendFileMessageIDContextKey{}, messageID)
}

type SendFileToolArguments struct {
	FilePath string `json:"file_path"`
	Caption  string `json:"caption"`
}

func (r *Registry) sendFileEnabled() bool {
	return r != nil && r.FileSender != nil
}

func sendFileToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "SendFile",
		Description: "Send an existing workspace file to the user. Supports common images, PDFs, archives, text/data files, and Office documents. Use this only when the user explicitly asks you to send a file, image, PDF, screenshot, report, or artifact.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the existing workspace file to send. Relative paths are resolved from the current workspace.",
				},
				"caption": map[string]any{
					"type":        "string",
					"description": "Optional short caption to include with the file.",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

func (r *Registry) handleSendFileToolCall(ctx context.Context, sendFileTool llm.ToolCall) (llm.Message, error) {
	var args SendFileToolArguments
	if err := parseToolArgs(sendFileTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.FilePath) == "" {
		return llm.Message{}, fmt.Errorf("file_path is required")
	}
	if r == nil || r.FileSender == nil {
		return llm.Message{}, fmt.Errorf("file sender is not configured")
	}

	filePath, cwd, err := r.resolveWorkspacePath(args.FilePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("getting workspace path: %w", err)
	}
	safe, reason := r.isPathReadableContext(ctx, filePath, cwd)
	if !safe {
		return llm.Message{}, r.outsideRootPermission(PermissionPathRead, filePath, reason, false)
	}
	if !isSendFileExtensionAllowed(filePath) {
		return llm.Message{}, fmt.Errorf("unsupported file type: %s", filepath.Ext(filePath))
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("file not found: %s", args.FilePath)
	}
	if !info.Mode().IsRegular() {
		return llm.Message{}, fmt.Errorf("not a regular file: %s", args.FilePath)
	}
	if info.Size() > MaxSendFileBytes {
		return llm.Message{}, fmt.Errorf("file exceeds %d bytes: %s", MaxSendFileBytes, args.FilePath)
	}

	result, err := r.FileSender.SendFile(ctx, filePath, args.Caption, sendFileMessageID(ctx))
	if err != nil {
		return llm.Message{}, err
	}
	if strings.TrimSpace(result) == "" {
		result = fmt.Sprintf("sent file %s", filepath.Base(filePath))
	}
	return llm.Message{Role: llm.RoleTool, Content: result, ToolCallID: sendFileTool.ID}, nil
}

func sendFileMessageID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(sendFileMessageIDContextKey{}).(string)
	return value
}

func isSendFileExtensionAllowed(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".pdf",
		".zip", ".tar", ".gz", ".tgz",
		".txt", ".md", ".log", ".json", ".csv", ".html",
		".xlsx", ".docx", ".pptx",
		".xml", ".yaml", ".yml", ".toml":
		return true
	default:
		return false
	}
}

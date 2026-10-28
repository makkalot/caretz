package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/makkalot/caretz/lib/llm"
)

type ReadToolArguments struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func FormatBinaryReadOutput(content []byte, filePath string) string {
	return fmt.Sprintf("[File: %s]\nBinary file contents are not shown.\nSize: %d bytes\nType: %s", filePath, len(content), binaryFileType(filePath))
}

func IsBinaryContent(content []byte, filePath string) bool {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".apk", ".zip", ".jar", ".war", ".ear", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf", ".dex", ".class", ".so", ".dylib", ".dll", ".exe":
		return true
	}
	if len(content) == 0 {
		return false
	}
	sample := content
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	if bytesContainZero(sample) {
		return true
	}
	if !utf8.Valid(sample) {
		return true
	}
	control := 0
	for _, b := range sample {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			control++
		}
	}
	return control > len(sample)/20
}

func bytesContainZero(content []byte) bool {
	for _, b := range content {
		if b == 0 {
			return true
		}
	}
	return false
}

func binaryFileType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		return "binary"
	}
	return ext[1:] + " binary"
}

func FormatReadOutput(content []byte, filePath string, offset, limit int) string {
	lines := strings.Split(string(content), "\n")

	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}

	totalLines := len(lines)

	startIdx := offset - 1
	if startIdx > totalLines {
		startIdx = totalLines
	}
	endIdx := startIdx + limit
	if endIdx > totalLines {
		endIdx = totalLines
	}

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "[File: %s, lines %d-%d of %d total]\n", filePath, startIdx+1, endIdx, totalLines)

	for i := startIdx; i < endIdx; i++ {
		_, _ = fmt.Fprintf(&b, "%d: %s\n", i+1, lines[i])
	}

	result := b.String()

	if len(result) > MaxReadBytes {
		cutoff := MaxReadBytes
		for cutoff > 0 && result[cutoff-1] != '\n' {
			cutoff--
		}
		if cutoff == 0 {
			cutoff = MaxReadBytes
		}
		result = result[:cutoff]

		actualEnd := startIdx + strings.Count(result, "\n") - 1
		if actualEnd < startIdx {
			actualEnd = startIdx
		}
		result += fmt.Sprintf("\n[Showing lines %d-%d of %d (20KB limit). Use offset=%d to continue.]",
			startIdx+1, actualEnd, totalLines, actualEnd+1)
	} else if endIdx < totalLines {
		result += fmt.Sprintf("\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
			startIdx+1, endIdx, totalLines, endIdx+1)
	}

	return result
}

func (r *Registry) handleReadToolCall(ctx context.Context, readTool llm.ToolCall) (llm.Message, error) {
	var args ReadToolArguments
	if err := parseToolArgs(readTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("json unmarshall read tool arguments: %w", err)
	}

	r.logInfo("Read tool called: %s (offset=%d, limit=%d)", args.FilePath, args.Offset, args.Limit)
	r.logDebug("Read arguments: %s", readTool.Arguments)

	filePath, cwd, err := r.resolveWorkspacePath(args.FilePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("getting workspace path: %w", err)
	}
	safe, reason := r.isPathReadableContext(ctx, filePath, cwd)
	if !safe {
		r.logInfo("Read tool blocked: %s", reason)
		return llm.Message{}, r.outsideRootPermission(PermissionPathRead, filePath, reason, false)
	}

	res, err := os.ReadFile(filePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("reading the file failed")
	}
	if IsBinaryContent(res, args.FilePath) {
		r.logInfo("Read tool skipped binary contents for %s: %d bytes", filePath, len(res))
		return llm.Message{Role: llm.RoleTool, Content: FormatBinaryReadOutput(res, args.FilePath), ToolCallID: readTool.ID}, nil
	}

	totalLines := len(strings.Split(string(res), "\n"))
	offset := args.Offset
	limit := args.Limit
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	endLine := offset - 1 + limit
	if endLine > totalLines {
		endLine = totalLines
	}
	r.logInfo("Read tool completed for %s: lines %d-%d of %d, %d bytes", filePath, offset, endLine, totalLines, len(res))
	output := FormatReadOutput(res, args.FilePath, args.Offset, args.Limit)
	return llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: readTool.ID}, nil
}

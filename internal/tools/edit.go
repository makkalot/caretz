package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/makkalot/caretz/lib/llm"
)

type EditEntry struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type EditToolArguments struct {
	FilePath string      `json:"file_path"`
	Edits    []EditEntry `json:"edits"`
}

func NormalizeText(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	result := strings.Join(lines, "\n")

	replacer := strings.NewReplacer(
		"\u2018", "'",
		"\u2019", "'",
		"\u201C", "\"",
		"\u201D", "\"",
		"\u2013", "-",
		"\u2014", "-",
		"\u2015", "-",
		"\u00A0", " ",
		"\u2002", " ",
		"\u2003", " ",
		"\u2009", " ",
	)
	return replacer.Replace(result)
}

func FindEditMatch(content string, oldText string) (int, int, error) {
	idx := strings.Index(content, oldText)
	if idx >= 0 {
		if strings.Count(content, oldText) > 1 {
			return 0, 0, fmt.Errorf("matches multiple locations; make it more specific")
		}
		return idx, idx + len(oldText), nil
	}

	normContent := NormalizeText(content)
	normOld := NormalizeText(oldText)
	normIdx := strings.Index(normContent, normOld)
	if normIdx < 0 {
		return 0, 0, fmt.Errorf("not found in file")
	}
	if strings.Count(normContent, normOld) > 1 {
		return 0, 0, fmt.Errorf("matches multiple locations (after normalization); make it more specific")
	}

	origLines := strings.Split(content, "\n")
	normLines := strings.Split(normContent, "\n")

	normOffset := 0
	startLine := -1
	startCol := -1
	endLine := -1
	endCol := -1

	for lineIdx := 0; lineIdx < len(normLines); lineIdx++ {
		lineEnd := normOffset + len(normLines[lineIdx])
		if startLine == -1 && normIdx <= lineEnd {
			startLine = lineIdx
			startCol = normIdx - normOffset
		}
		normEnd := normIdx + len(normOld)
		if endLine == -1 && normEnd <= lineEnd+1 {
			endLine = lineIdx
			endCol = normEnd - normOffset
		}
		normOffset = lineEnd + 1
	}

	if startLine == -1 || endLine == -1 {
		return 0, 0, fmt.Errorf("could not map normalized match to original content")
	}

	origStart := 0
	for i := 0; i < startLine; i++ {
		origStart += len(origLines[i]) + 1
	}
	origStart += startCol

	origEnd := 0
	for i := 0; i <= endLine; i++ {
		if i == endLine {
			origEnd += endCol
		} else {
			origEnd += len(origLines[i]) + 1
		}
	}

	return origStart, origEnd, nil
}

func ApplyEdits(content string, edits []EditEntry) (string, error) {
	type resolvedEdit struct {
		start, end int
		newText    string
	}
	var resolved []resolvedEdit

	for i, edit := range edits {
		start, end, err := FindEditMatch(content, edit.OldText)
		if err != nil {
			return "", fmt.Errorf("edit %d: %w", i+1, err)
		}
		resolved = append(resolved, resolvedEdit{
			start:   start,
			end:     end,
			newText: edit.NewText,
		})
	}

	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].start < resolved[j].start
	})

	for i := 1; i < len(resolved); i++ {
		if resolved[i].start < resolved[i-1].end {
			return "", fmt.Errorf("edits overlap: edit regions must not intersect")
		}
	}

	result := content
	for i := len(resolved) - 1; i >= 0; i-- {
		e := resolved[i]
		result = result[:e.start] + e.newText + result[e.end:]
	}
	return result, nil
}

func GenerateDiff(original, modified string, contextLines int) string {
	origLines := strings.Split(original, "\n")
	modLines := strings.Split(modified, "\n")

	prefixLen := 0
	for prefixLen < len(origLines) && prefixLen < len(modLines) &&
		origLines[prefixLen] == modLines[prefixLen] {
		prefixLen++
	}

	suffixLen := 0
	for suffixLen < len(origLines)-prefixLen && suffixLen < len(modLines)-prefixLen &&
		origLines[len(origLines)-1-suffixLen] == modLines[len(modLines)-1-suffixLen] {
		suffixLen++
	}

	if prefixLen == len(origLines) && prefixLen == len(modLines) {
		return ""
	}

	origStart := prefixLen
	origEnd := len(origLines) - suffixLen
	modStart := prefixLen
	modEnd := len(modLines) - suffixLen

	ctxStart := origStart - contextLines
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxOrigEnd := origEnd + contextLines
	if ctxOrigEnd > len(origLines) {
		ctxOrigEnd = len(origLines)
	}

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
		ctxStart+1, ctxOrigEnd-ctxStart,
		ctxStart+1, (modEnd-modStart)+(ctxOrigEnd-ctxStart)-(origEnd-origStart))

	for i := ctxStart; i < origStart; i++ {
		_, _ = fmt.Fprintf(&b, " %s\n", origLines[i])
	}
	for i := origStart; i < origEnd; i++ {
		_, _ = fmt.Fprintf(&b, "-%s\n", origLines[i])
	}
	for i := modStart; i < modEnd; i++ {
		_, _ = fmt.Fprintf(&b, "+%s\n", modLines[i])
	}
	for i := origEnd; i < ctxOrigEnd; i++ {
		_, _ = fmt.Fprintf(&b, " %s\n", origLines[i])
	}

	return b.String()
}

func (r *Registry) handleEditToolCall(ctx context.Context, editTool llm.ToolCall) (llm.Message, error) {
	var args EditToolArguments
	if err := parseToolArgs(editTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}

	if len(args.Edits) == 0 {
		return llm.Message{}, fmt.Errorf("edits array is empty")
	}

	r.logInfo("Edit tool called: %s (%d edits)", args.FilePath, len(args.Edits))

	filePath, cwd, err := r.resolveWorkspacePath(args.FilePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("getting workspace path: %w", err)
	}
	safe, reason := r.isPathWritableContext(ctx, filePath, cwd)
	if !safe {
		return llm.Message{}, r.outsideRootPermission(PermissionPathWrite, filePath, reason, false)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return llm.Message{}, fmt.Errorf("file not found: %s", args.FilePath)
	}

	original := string(data)
	modified, err := ApplyEdits(original, args.Edits)
	if err != nil {
		return llm.Message{}, err
	}

	if err := os.WriteFile(filePath, []byte(modified), 0644); err != nil {
		return llm.Message{}, fmt.Errorf("writing file: %w", err)
	}

	diff := GenerateDiff(original, modified, DiffContextLines)
	result := fmt.Sprintf("Successfully replaced %d block(s) in %s.\n%s", len(args.Edits), args.FilePath, diff)

	r.logInfo("Edit tool completed: %d edits applied to %s", len(args.Edits), filePath)
	return llm.Message{Role: llm.RoleTool, Content: result, ToolCallID: editTool.ID}, nil
}

package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

type GrepToolArguments struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
	Literal    bool   `json:"literal"`
	Context    int    `json:"context"`
	Limit      int    `json:"limit"`
}

func TruncateLine(line string, maxChars int) string {
	if len(line) <= maxChars {
		return line
	}
	return line[:maxChars] + "... [truncated]"
}

func TruncateHead(output string, maxBytes int) (string, bool) {
	if len(output) <= maxBytes {
		return output, false
	}
	cutoff := maxBytes
	for cutoff > 0 && output[cutoff-1] != '\n' {
		cutoff--
	}
	if cutoff == 0 {
		cutoff = maxBytes
	}
	return output[:cutoff], true
}

func Shelljoin(args []string) string {
	var parts []string
	for _, arg := range args {
		parts = append(parts, Shellquote(arg))
	}
	return strings.Join(parts, " ")
}

func (r *Registry) handleGrepToolCall(ctx context.Context, grepTool llm.ToolCall) (llm.Message, error) {
	var args GrepToolArguments
	if err := parseToolArgs(grepTool, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}

	if args.Pattern == "" {
		return llm.Message{}, fmt.Errorf("pattern is required")
	}

	limit := args.Limit
	if limit <= 0 {
		limit = DefaultGrepLimit
	}

	r.logInfo("Grep tool called: pattern=%q path=%q limit=%d", args.Pattern, args.Path, limit)

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
		absCwd, _ := filepath.Abs(cwd)
		absSearch, _ := filepath.Abs(searchPath)
		if absSearch != absCwd {
			safe, reason := r.isPathReadableContext(ctx, searchPath, cwd)
			if !safe {
				return llm.Message{}, r.outsideRootPermission(PermissionPathRead, searchPath, reason, true)
			}
		}
	}

	command, err := r.grepCommand(ctx, cwd, args, searchPath, limit)
	if err != nil {
		return llm.Message{}, err
	}
	stdout, stderr, err := r.runSandboxed(ctx, cwd, command, sandbox.RunOptions{})

	if err != nil {
		if ctx.Err() != nil {
			return llm.Message{}, ctx.Err()
		}
		if strings.Contains(err.Error(), "exit status 1") && stderr == "" {
			return llm.Message{Role: llm.RoleTool, Content: "No matches found.", ToolCallID: grepTool.ID}, nil
		}
		if stderr != "" {
			return llm.Message{}, fmt.Errorf("grep error: %s", stderr)
		}
		return llm.Message{}, fmt.Errorf("grep error: %v", err)
	}

	if strings.TrimSpace(stdout) == "" {
		return llm.Message{Role: llm.RoleTool, Content: "No matches found.", ToolCallID: grepTool.ID}, nil
	}

	lines := strings.Split(stdout, "\n")
	var processed []string
	matchCount := 0
	lineTruncated := false
	for _, line := range lines {
		if line == "" {
			processed = append(processed, line)
			continue
		}
		original := line
		line = TruncateLine(line, MaxGrepLineChars)
		if line != original {
			lineTruncated = true
		}
		processed = append(processed, line)
		if !strings.HasPrefix(line, "--") {
			matchCount++
		}
	}

	output := strings.Join(processed, "\n")

	output, byteTruncated := TruncateHead(output, MaxToolOutputBytes)

	var notices []string
	if matchCount >= limit {
		notices = append(notices, fmt.Sprintf("[%d matches limit reached. Use limit=%d for more, or refine pattern]", limit, limit*2))
	}
	if lineTruncated {
		notices = append(notices, fmt.Sprintf("[Some lines truncated to %d chars. Use Read tool to see full lines]", MaxGrepLineChars))
	}
	if byteTruncated {
		notices = append(notices, "[Output truncated at 50KB limit]")
	}
	if len(notices) > 0 {
		output += "\n" + strings.Join(notices, "\n")
	}

	r.logInfo("Grep tool completed: %d matches", matchCount)
	return llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: grepTool.ID}, nil
}

func (r *Registry) grepCommand(ctx context.Context, cwd string, args GrepToolArguments, searchPath string, limit int) (string, error) {
	if ok, err := r.sandboxCommandAvailable(ctx, cwd, "rg"); err != nil {
		return "", err
	} else if ok {
		return ripgrepCommand(args, searchPath, limit), nil
	}
	return grepFallbackCommand(args, searchPath, limit), nil
}

func (r *Registry) sandboxCommandAvailable(ctx context.Context, cwd, name string) (bool, error) {
	_, stderr, err := r.runSandboxed(ctx, cwd, "command -v "+Shellquote(name), sandbox.RunOptions{})
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if stderr != "" && !strings.Contains(stderr, "not found") {
		return false, fmt.Errorf("check %s availability: %s", name, stderr)
	}
	return false, nil
}

func ripgrepCommand(args GrepToolArguments, searchPath string, limit int) string {
	rgArgs := []string{"--no-heading", "--line-number", "--color", "never"}
	if args.IgnoreCase {
		rgArgs = append(rgArgs, "--ignore-case")
	}
	if args.Literal {
		rgArgs = append(rgArgs, "--fixed-strings")
	}
	if args.Glob != "" {
		rgArgs = append(rgArgs, "--glob", args.Glob)
	}
	if args.Context > 0 {
		rgArgs = append(rgArgs, fmt.Sprintf("--context=%d", args.Context))
	}
	rgArgs = append(rgArgs, fmt.Sprintf("--max-count=%d", limit))
	rgArgs = append(rgArgs, args.Pattern, searchPath)

	return "rg " + Shelljoin(rgArgs)
}

func grepFallbackCommand(args GrepToolArguments, searchPath string, limit int) string {
	grepArgs := []string{"-R", "-n"}
	if args.IgnoreCase {
		grepArgs = append(grepArgs, "-i")
	}
	if args.Literal {
		grepArgs = append(grepArgs, "-F")
	} else {
		grepArgs = append(grepArgs, "-E")
	}
	if args.Glob != "" {
		grepArgs = append(grepArgs, "--include="+args.Glob)
	}
	if args.Context > 0 {
		grepArgs = append(grepArgs, "-C", strconv.Itoa(args.Context))
	}
	grepArgs = append(grepArgs, "-m", strconv.Itoa(limit))
	grepArgs = append(grepArgs, args.Pattern, searchPath)
	return "grep " + Shelljoin(grepArgs)
}

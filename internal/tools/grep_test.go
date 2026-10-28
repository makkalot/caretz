package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTruncateLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxChars int
		want     string
	}{
		{"short string unchanged", "short", 100, "short"},
		{"exactly at limit", "exactly10!", 10, "exactly10!"},
		{"over limit", strings.Repeat("x", 600), 500, strings.Repeat("x", 500) + "... [truncated]"},
		{"empty string", "", 100, ""},
		{"one char over", "abcde", 4, "abcd... [truncated]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateLine(tt.input, tt.maxChars)
			if got != tt.want {
				t.Errorf("TruncateLine(%d chars, max=%d) = %q, want %q",
					len(tt.input), tt.maxChars, got, tt.want)
			}
		})
	}
}

func TestTruncateHead(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		maxBytes    int
		wantTrunc   bool
		wantContent string
	}{
		{
			name:      "short output unchanged",
			input:     "short\n",
			maxBytes:  100,
			wantTrunc: false,
		},
		{
			name:        "truncated at line boundary",
			input:       "line1\nline2\nline3\n",
			maxBytes:    10,
			wantTrunc:   true,
			wantContent: "line1\n",
		},
		{
			name:      "empty input",
			input:     "",
			maxBytes:  100,
			wantTrunc: false,
		},
		{
			name:      "input exactly at limit",
			input:     "12345",
			maxBytes:  5,
			wantTrunc: false,
		},
		{
			name:      "no newlines in input - falls back to raw cutoff",
			input:     strings.Repeat("x", 100),
			maxBytes:  50,
			wantTrunc: true,
		},
		{
			name:        "newline exactly at maxBytes boundary",
			input:       "12345\n67890\n",
			maxBytes:    6,
			wantTrunc:   true,
			wantContent: "12345\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := TruncateHead(tt.input, tt.maxBytes)
			if truncated != tt.wantTrunc {
				t.Errorf("TruncateHead truncated=%v, want %v", truncated, tt.wantTrunc)
			}
			if truncated && len(got) > tt.maxBytes {
				t.Errorf("TruncateHead output %d bytes > limit %d", len(got), tt.maxBytes)
			}
			if tt.wantContent != "" && got != tt.wantContent {
				t.Errorf("TruncateHead output = %q, want %q", got, tt.wantContent)
			}
		})
	}
}

func TestShelljoin(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"simple args", []string{"--flag", "value"}, "'--flag' 'value'"},
		{"arg with space", []string{"hello world"}, "'hello world'"},
		{"arg with single quote", []string{"it's"}, "'it'\\''s'"},
		{"arg with double quote", []string{`say "hi"`}, `'say "hi"'`},
		{"empty args", []string{}, ""},
		{"mixed", []string{"--flag", "hello world", "simple"}, "'--flag' 'hello world' 'simple'"},
		{"regex alternation", []string{"expense_extractor|expense_manager|expense_ocr"}, "'expense_extractor|expense_manager|expense_ocr'"},
		{"shell metacharacters", []string{"(foo); bar & baz $HOME `date`"}, "'(foo); bar & baz $HOME `date`'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Shelljoin(tt.args)
			if got != tt.want {
				t.Errorf("Shelljoin() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandleGrepToolCall_QuotesRegexPattern(t *testing.T) {
	tmpDir := t.TempDir()
	resolvedTmpDir, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(resolvedTmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origCwd) }()

	var gotCommand string
	registry := &Registry{
		RunSandboxed: func(cwd, command string) (string, string, error) {
			gotCommand = command
			return "main.py:1:expense_extractor\n", "", nil
		},
	}

	toolCall := makeToolCall("Grep", "g-regex", map[string]any{
		"pattern": "expense_extractor|expense_manager|expense_ocr",
		"path":    "main.py",
	})

	if _, err := registry.HandleToolCall(toolCall); err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}

	wantPattern := "'expense_extractor|expense_manager|expense_ocr'"
	if !strings.Contains(gotCommand, wantPattern) {
		t.Fatalf("command %q does not contain quoted pattern %q", gotCommand, wantPattern)
	}
	if strings.Contains(gotCommand, "expense_extractor|expense_manager|expense_ocr ") {
		t.Fatalf("command exposes raw regex alternation to the shell: %q", gotCommand)
	}
}

func TestHandleGrepToolCall_UsesRipgrepWhenAvailable(t *testing.T) {
	tmpDir := t.TempDir()
	resolvedTmpDir, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(resolvedTmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origCwd) }()

	var grepCommand string
	registry := &Registry{
		RunSandboxed: func(cwd, command string) (string, string, error) {
			if command == "command -v 'rg'" {
				return "/usr/bin/rg\n", "", nil
			}
			grepCommand = command
			return "main.py:1:needle\n", "", nil
		},
	}

	toolCall := makeToolCall("Grep", "g-rg", map[string]any{
		"pattern": "needle",
		"path":    "main.py",
	})

	if _, err := registry.HandleToolCall(toolCall); err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}
	if !strings.HasPrefix(grepCommand, "rg ") {
		t.Fatalf("command = %q, want rg command", grepCommand)
	}
}

func TestHandleGrepToolCall_FallsBackToGrepWhenRipgrepMissing(t *testing.T) {
	tmpDir := t.TempDir()
	resolvedTmpDir, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(resolvedTmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origCwd) }()

	var grepCommand string
	registry := &Registry{
		RunSandboxed: func(cwd, command string) (string, string, error) {
			if command == "command -v 'rg'" {
				return "", "", errors.New("exit status 1")
			}
			grepCommand = command
			return "Makefile:1:needle\n", "", nil
		},
	}

	toolCall := makeToolCall("Grep", "g-fallback", map[string]any{
		"pattern": "needle",
		"path":    ".",
		"glob":    "Makefile",
	})

	if _, err := registry.HandleToolCall(toolCall); err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}
	if !strings.HasPrefix(grepCommand, "grep ") {
		t.Fatalf("command = %q, want grep fallback command", grepCommand)
	}
	if !strings.Contains(grepCommand, "'--include=Makefile'") {
		t.Fatalf("command = %q, want Makefile include glob", grepCommand)
	}
}

func TestGrepFallbackCommand_UsesExtendedRegex(t *testing.T) {
	command := grepFallbackCommand(GrepToolArguments{
		Pattern: `\$\{?[A-Z_]+\}?`,
	}, ".", 100)

	if !strings.Contains(command, "'-E'") {
		t.Fatalf("command = %q, want extended regex flag", command)
	}
	if strings.Contains(command, "'-F'") {
		t.Fatalf("command = %q, should not use fixed-string mode for regex search", command)
	}
}

func TestGrepFallbackCommand_LiteralUsesFixedStrings(t *testing.T) {
	command := grepFallbackCommand(GrepToolArguments{
		Pattern: `\$\{?[A-Z_]+\}?`,
		Literal: true,
	}, ".", 100)

	if !strings.Contains(command, "'-F'") {
		t.Fatalf("command = %q, want fixed-string flag", command)
	}
	if strings.Contains(command, "'-E'") {
		t.Fatalf("command = %q, should not use extended regex mode for literal search", command)
	}
}

func TestShellquote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple string", "hello", "'hello'"},
		{"string with spaces", "hello world", "'hello world'"},
		{"string with single quote", "it's", "'it'\\''s'"},
		{"empty string", "", "''"},
		{"glob pattern", "*.go", "'*.go'"},
		{"path with spaces", "/path/to my/file", "'/path/to my/file'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Shellquote(tt.input)
			if got != tt.want {
				t.Errorf("Shellquote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

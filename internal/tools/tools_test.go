package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/llm"
)

type recordingFileSender struct {
	paths      []string
	captions   []string
	messageIDs []string
}

func (s *recordingFileSender) SendFile(_ context.Context, path, caption, messageID string) (string, error) {
	s.paths = append(s.paths, path)
	s.captions = append(s.captions, caption)
	s.messageIDs = append(s.messageIDs, messageID)
	return "sent " + filepath.Base(path), nil
}

func TestRegistryCustomToolRegistrationAndDispatch(t *testing.T) {
	registry := &Registry{}
	err := registry.RegisterTool(Tool{
		Definition: llm.ToolDefinition{
			Name:        "DeployPreview",
			Description: "Deploy a preview build",
			Parameters:  map[string]any{"type": "object"},
		},
		Handler: func(ctx context.Context, call llm.ToolCall) (string, error) {
			if call.Arguments != `{"branch":"main"}` {
				t.Fatalf("arguments = %q, want branch payload", call.Arguments)
			}
			return "preview deployed", nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}

	if !hasToolDefinition(registry.ToolDefinitions(), "DeployPreview") {
		t.Fatal("custom tool definition was not advertised")
	}

	msg, err := registry.HandleToolCallContext(context.Background(), llm.ToolCall{
		ID:        "custom-1",
		Name:      "DeployPreview",
		Arguments: `{"branch":"main"}`,
	})
	if err != nil {
		t.Fatalf("HandleToolCallContext() error = %v", err)
	}
	if msg.Role != llm.RoleTool || msg.ToolCallID != "custom-1" || msg.Content != "preview deployed" {
		t.Fatalf("custom tool message = %+v", msg)
	}
}

func TestRegistrySessionToolValidation(t *testing.T) {
	registry := &Registry{}
	handler := func(ctx context.Context, call llm.ToolCall) (string, error) { return "noted", nil }
	if _, err := registry.WithSessionTool(Tool{Definition: llm.ToolDefinition{Name: "Bash"}, Handler: handler}); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("WithSessionTool(Bash) error = %v, want built-in error", err)
	}
	if err := registry.RegisterTool(Tool{Definition: llm.ToolDefinition{Name: "DeployPreview"}, Handler: handler}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.WithSessionTool(Tool{Definition: llm.ToolDefinition{Name: "DeployPreview"}, Handler: handler}); err == nil || !strings.Contains(err.Error(), "custom tool") {
		t.Fatalf("WithSessionTool(DeployPreview) error = %v, want custom tool error", err)
	}
	// An integration's own tool needs no entry in a fixed list.
	withNote, err := registry.WithSessionTool(Tool{Definition: llm.ToolDefinition{Name: "LeaveNote"}, Handler: handler})
	if err != nil {
		t.Fatalf("WithSessionTool(LeaveNote) error = %v", err)
	}
	msg, err := withNote.HandleToolCall(llm.ToolCall{ID: "n1", Name: "LeaveNote"})
	if err != nil || msg.Content != "noted" {
		t.Fatalf("LeaveNote = %+v, %v", msg, err)
	}
}

func TestRegistryCustomToolValidation(t *testing.T) {
	registry := &Registry{}
	handler := func(ctx context.Context, call llm.ToolCall) (string, error) { return "ok", nil }
	if err := registry.RegisterTool(Tool{Definition: llm.ToolDefinition{Name: "Bash"}, Handler: handler}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("RegisterTool(Bash) error = %v, want reserved error", err)
	}
	if err := registry.RegisterTool(Tool{Definition: llm.ToolDefinition{Name: "DeployPreview"}}); err == nil || !strings.Contains(err.Error(), "handler") {
		t.Fatalf("RegisterTool(nil handler) error = %v, want handler error", err)
	}
	if err := registry.RegisterTool(Tool{Definition: llm.ToolDefinition{Name: "DeployPreview"}, Handler: handler}); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}
	if err := registry.RegisterTool(Tool{Definition: llm.ToolDefinition{Name: "DeployPreview"}, Handler: handler}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("RegisterTool(duplicate) error = %v, want duplicate error", err)
	}
}

func TestRegistryCustomToolConfigValidation(t *testing.T) {
	registry := &Registry{
		ToolConfig: Config{
			"DeployPreview": {
				"env": "",
			},
		},
	}
	if err := registry.RegisterTool(Tool{
		Definition: llm.ToolDefinition{Name: "DeployPreview"},
		Handler:    func(ctx context.Context, call llm.ToolCall) (string, error) { return "ok", nil },
		ValidateConfig: func(config map[string]any) error {
			if strings.TrimSpace(fmt.Sprint(config["env"])) == "" {
				return fmt.Errorf("env is required")
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}

	err := registry.ValidateConfig()
	if err == nil || !strings.Contains(err.Error(), "tools.DeployPreview: env is required") {
		t.Fatalf("ValidateConfig() error = %v, want custom config validation error", err)
	}
}

func TestRegistryCustomToolsRespectDisabledAndCloneHelpers(t *testing.T) {
	registry := &Registry{}
	if err := registry.RegisterTool(Tool{
		Definition: llm.ToolDefinition{Name: "DeployPreview", Parameters: map[string]any{"type": "object"}},
		Handler:    func(ctx context.Context, call llm.ToolCall) (string, error) { return "ok", nil },
	}); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}

	clone := registry.WithWorkspaceDir(t.TempDir())
	if !hasToolDefinition(clone.ToolDefinitions(), "DeployPreview") {
		t.Fatal("clone lost custom tool definition")
	}
	disabled := clone.WithDisabledTools(map[string]bool{"DeployPreview": true})
	if hasToolDefinition(disabled.ToolDefinitions(), "DeployPreview") {
		t.Fatal("disabled custom tool definition should be hidden")
	}
	_, err := disabled.HandleToolCall(llm.ToolCall{ID: "custom-1", Name: "DeployPreview"})
	if err == nil || !strings.Contains(err.Error(), "tool DeployPreview is disabled") {
		t.Fatalf("disabled custom tool error = %v, want disabled error", err)
	}
}

func TestRegistryWithEnabledToolsFiltersDefinitionsAndDispatch(t *testing.T) {
	registry := (&Registry{
		TavilyAPIKey: "test-key",
		RunSandboxed: func(cwd, command string) (string, string, error) {
			return "match", "", nil
		},
	}).WithEnabledTools("Read", "Grep")

	defs := registry.ToolDefinitions()
	if !hasToolDefinition(defs, "Read") {
		t.Fatal("Read tool definition should be available")
	}
	if !hasToolDefinition(defs, "Grep") {
		t.Fatal("Grep tool definition should be available")
	}
	if hasToolDefinition(defs, "Write") {
		t.Fatal("Write tool definition should be hidden")
	}
	if hasToolDefinition(defs, "WebSearch") {
		t.Fatal("WebSearch tool definition should be hidden even when provider is configured")
	}

	if !registry.IsToolEnabled("Read") {
		t.Fatal("Read should be enabled")
	}
	if registry.IsToolEnabled("Write") {
		t.Fatal("Write should be disabled by allowlist")
	}
	if _, err := registry.HandleToolCall(makeToolCall("Write", "write-1", map[string]any{
		"file_path": "out.txt",
		"content":   "blocked",
	})); err == nil || !strings.Contains(err.Error(), "tool Write is disabled") {
		t.Fatalf("Write error = %v, want disabled", err)
	}
}

func TestRegistryToolPolicyIsCaseInsensitive(t *testing.T) {
	registry := &Registry{
		EnabledTools:  map[string]bool{"read": true, "gReP": true, "write": true},
		DisabledTools: map[string]bool{"WRITE": true},
		RunSandboxed: func(cwd, command string) (string, string, error) {
			return "match", "", nil
		},
	}

	defs := registry.ToolDefinitions()
	if !hasToolDefinition(defs, "Read") {
		t.Fatal("Read should be advertised by lowercase allowlist")
	}
	if !hasToolDefinition(defs, "Grep") {
		t.Fatal("Grep should be advertised by mixed-case allowlist")
	}
	if hasToolDefinition(defs, "Write") {
		t.Fatal("Write should be hidden by differently-cased disable entry")
	}
	if !registry.IsToolEnabled("Read") {
		t.Fatal("Read should be enabled")
	}
	if registry.IsToolEnabled("Write") {
		t.Fatal("Write should be disabled")
	}
	if _, err := registry.HandleToolCall(makeToolCall("Write", "write-1", map[string]any{
		"file_path": "out.txt",
		"content":   "blocked",
	})); err == nil || !strings.Contains(err.Error(), "tool Write is disabled") {
		t.Fatalf("Write error = %v, want disabled", err)
	}
}

func TestBashToolDescriptionExplainsSOCKSAndSSH(t *testing.T) {
	definition, ok := toolDefinitionByName((&Registry{}).ToolDefinitions(), "Bash")
	if !ok {
		t.Fatal("Bash tool definition missing")
	}
	for _, want := range []string{
		"tcp://HOST:PORT",
		"$CARETZ_SOCKS_PROXY",
		"Git-over-SSH is routed through the proxy automatically",
		"ProxyCommand=$CARETZ_SSH_PROXY_COMMAND",
		"Operation not permitted",
	} {
		if !strings.Contains(definition.Description, want) {
			t.Fatalf("Bash description missing %q:\n%s", want, definition.Description)
		}
	}
}

func TestRegistryWithEnabledToolsAppliesToCustomTools(t *testing.T) {
	registry := &Registry{}
	if err := registry.RegisterTool(Tool{
		Definition: llm.ToolDefinition{Name: "DeployPreview", Parameters: map[string]any{"type": "object"}},
		Handler:    func(ctx context.Context, call llm.ToolCall) (string, error) { return "ok", nil },
	}); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}

	filtered := registry.WithEnabledTools("Read")
	if hasToolDefinition(filtered.ToolDefinitions(), "DeployPreview") {
		t.Fatal("custom tool should be hidden when not allowlisted")
	}
	if _, err := filtered.HandleToolCall(llm.ToolCall{ID: "custom-1", Name: "DeployPreview"}); err == nil || !strings.Contains(err.Error(), "tool DeployPreview is disabled") {
		t.Fatalf("custom tool error = %v, want disabled", err)
	}

	enabled := filtered.WithEnabledTools("DeployPreview")
	if !hasToolDefinition(enabled.ToolDefinitions(), "DeployPreview") {
		t.Fatal("custom tool should be available after being allowlisted")
	}
}

func TestIsPathSafe(t *testing.T) {
	cwd, _ := os.Getwd()

	tests := []struct {
		name       string
		path       string
		cwd        string
		wantOk     bool
		wantReason string
	}{
		{
			name:       "relative path inside cwd",
			path:       "./app/main.go",
			cwd:        cwd,
			wantOk:     true,
			wantReason: "",
		},
		{
			name:       "absolute path inside cwd",
			path:       filepath.Join(cwd, "app", "main.go"),
			cwd:        cwd,
			wantOk:     true,
			wantReason: "",
		},
		{
			name:       "outside cwd - /etc/passwd",
			path:       "/etc/passwd",
			cwd:        cwd,
			wantOk:     false,
			wantReason: "path outside allowed directory",
		},
		{
			name:       "outside cwd - /tmp",
			path:       "/tmp/evil.txt",
			cwd:        cwd,
			wantOk:     false,
			wantReason: "path outside allowed directory",
		},
		{
			name:       "path traversal attempt",
			path:       filepath.Join(cwd, "..", "etc", "passwd"),
			cwd:        cwd,
			wantOk:     false,
			wantReason: "path outside allowed directory",
		},
		{
			name:       "subpath traversal",
			path:       filepath.Join(cwd, "app", "..", "..", "tmp", "evil.txt"),
			cwd:        cwd,
			wantOk:     false,
			wantReason: "path outside allowed directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := IsPathSafe(tt.path, tt.cwd)
			if ok != tt.wantOk {
				t.Errorf("IsPathSafe(%q, %q) = %v, want %v", tt.path, tt.cwd, ok, tt.wantOk)
			}
			if !ok && reason != tt.wantReason {
				t.Errorf("IsPathSafe(%q, %q) reason = %q, want %q", tt.path, tt.cwd, reason, tt.wantReason)
			}
		})
	}
}

func TestHandleToolCallContextPropagatesSandboxCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	registry := &Registry{
		RunSandboxedContext: func(ctx context.Context, cwd, command string) (string, string, error) {
			called = true
			if err := ctx.Err(); err != nil {
				return "", "", err
			}
			return "unexpected", "", nil
		},
	}

	_, err := registry.HandleToolCallContext(ctx, makeToolCall("Bash", "bash-1", map[string]any{
		"command": "sleep 10",
	}))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if called {
		t.Fatal("sandbox runner should not be called when context is already canceled")
	}
}

func TestIsPathSafe_Symlinks(t *testing.T) {
	cwd, _ := os.Getwd()
	tmpDir := filepath.Join(cwd, "test-symlink-tmp-tools")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	symlinkPath := filepath.Join(tmpDir, "mylink")
	if err := os.Symlink("/etc", symlinkPath); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(symlinkPath) }()

	ok, reason := IsPathSafe(symlinkPath, tmpDir)
	if ok {
		t.Errorf("IsPathSafe(symlink) = %v, want false (symlink in path not allowed)", ok)
	}
	if reason != "symlink in path not allowed" {
		t.Errorf("IsPathSafe(symlink) reason = %q, want %q", reason, "symlink in path not allowed")
	}
}

func TestIsPathSafeInRoots_ExtraRoot(t *testing.T) {
	cwd := t.TempDir()
	extraRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}

	insideExtra := filepath.Join(extraRoot, "file.txt")
	ok, reason := IsPathSafeInRoots(insideExtra, cwd, []string{extraRoot})
	if !ok {
		t.Fatalf("expected path inside extra root to be safe, got reason %q", reason)
	}

	outside := filepath.Join(t.TempDir(), "file.txt")
	ok, reason = IsPathSafeInRoots(outside, cwd, []string{extraRoot})
	if ok {
		t.Fatalf("expected unrelated path to be unsafe")
	}
	if reason != "path outside allowed directory" {
		t.Fatalf("unexpected rejection reason %q", reason)
	}
}

func TestWriteToolAllowsConfiguredWritableRoot(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving cwd temp dir: %v", err)
	}
	extraRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving writable root temp dir: %v", err)
	}
	registry := &Registry{WritableRoots: []string{extraRoot}}

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origCwd) }()

	filePath := filepath.Join(extraRoot, "new.txt")
	_, err = registry.HandleToolCall(makeToolCall("Write", "w-extra", map[string]any{
		"file_path": filePath,
		"content":   "allowed",
	}))
	if err != nil {
		t.Fatalf("expected write inside configured writable root to succeed, got %v", err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(content) != "allowed" {
		t.Fatalf("unexpected content %q", string(content))
	}
}

func TestReadableRootsAllowReadToolsOnly(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving cwd temp dir: %v", err)
	}
	readRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving readable root temp dir: %v", err)
	}
	filePath := filepath.Join(readRoot, "notes.txt")
	if err := os.WriteFile(filePath, []byte("needle\n"), 0644); err != nil {
		t.Fatalf("writing readable root fixture: %v", err)
	}

	origCwd, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origCwd) }()

	var sandboxCalls []string
	registry := &Registry{
		ReadableRoots: []string{readRoot},
		RunSandboxed: func(cwd, command string) (string, string, error) {
			sandboxCalls = append(sandboxCalls, command)
			if command == "command -v 'rg'" {
				return "", "", fmt.Errorf("exit status 1")
			}
			if strings.HasPrefix(command, "grep ") {
				return filePath + ":1:needle\n", "", nil
			}
			return filePath + "\n", "", nil
		},
	}

	if _, err := registry.HandleToolCall(makeToolCall("Read", "r-read-root", map[string]any{
		"file_path": filePath,
	})); err != nil {
		t.Fatalf("expected Read inside configured read root to succeed, got %v", err)
	}
	if _, err := registry.HandleToolCall(makeToolCall("Ls", "ls-read-root", map[string]any{
		"path": readRoot,
	})); err != nil {
		t.Fatalf("expected Ls inside configured read root to succeed, got %v", err)
	}
	if _, err := registry.HandleToolCall(makeToolCall("Find", "find-read-root", map[string]any{
		"pattern": "*.txt",
		"path":    readRoot,
	})); err != nil {
		t.Fatalf("expected Find inside configured read root to succeed, got %v", err)
	}
	if _, err := registry.HandleToolCall(makeToolCall("Grep", "grep-read-root", map[string]any{
		"pattern": "needle",
		"path":    readRoot,
	})); err != nil {
		t.Fatalf("expected Grep inside configured read root to succeed, got %v", err)
	}
	if _, err := registry.HandleToolCall(makeToolCall("Write", "w-read-root", map[string]any{
		"file_path": filepath.Join(readRoot, "new.txt"),
		"content":   "blocked",
	})); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected Write inside read root to be blocked, got %v", err)
	}
	if _, err := registry.HandleToolCall(makeToolCall("Edit", "e-read-root", map[string]any{
		"file_path": filePath,
		"edits": []map[string]any{
			{"old_text": "needle", "new_text": "changed"},
		},
	})); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected Edit inside read root to be blocked, got %v", err)
	}

	if len(sandboxCalls) < 3 {
		t.Fatalf("expected Find and Grep to call sandbox, got %v", sandboxCalls)
	}
}

func TestPlanModeToolDefinitions(t *testing.T) {
	// Plan mode can be toggled mid-session, so it must not change the tool
	// list sent to the model; mutating calls fail instead.
	plan := (&Registry{}).WithPlanMode(true).ToolDefinitions()
	build := (&Registry{}).ToolDefinitions()
	if len(plan) != len(build) {
		t.Fatalf("plan mode tools = %d, build mode tools = %d", len(plan), len(build))
	}
	for i := range plan {
		if plan[i].Name != build[i].Name || plan[i].Description != build[i].Description {
			t.Fatalf("tool %d differs between modes: %q vs %q", i, plan[i].Name, build[i].Name)
		}
	}
}

func TestSandboxDisabledToolDefinitionsDescribeUnsandboxedBash(t *testing.T) {
	defs := (&Registry{SandboxDisabled: true}).ToolDefinitions()
	def, ok := toolDefinitionByName(defs, "Bash")
	if !ok {
		t.Fatal("Bash tool should remain available when sandbox is disabled")
	}
	if !strings.Contains(def.Description, "without CARETZ's internal sandbox") {
		t.Fatalf("Bash description = %q, want unsandboxed wording", def.Description)
	}
	if strings.Contains(def.Description, "Network: denied") {
		t.Fatalf("Bash description should not mention sandbox network restrictions: %q", def.Description)
	}
}

func TestSandboxTemporaryDirectoryGuidance(t *testing.T) {
	want := "On macOS, literal /tmp is not writable inside the sandbox; use $TMPDIR for temporary files."
	if got := sandboxTemporaryDirectoryGuidance("darwin", false); !strings.Contains(got, want) {
		t.Fatalf("darwin guidance = %q, want %q", got, want)
	}
	if got := sandboxTemporaryDirectoryGuidance("linux", true); got != "" {
		t.Fatalf("linux guidance = %q, want empty", got)
	}
	if got := sandboxTemporaryDirectoryGuidance("linux", false); !strings.Contains(got, "own empty /tmp") {
		t.Fatalf("linux one-shot guidance = %q", got)
	}
}

func TestTaskToolsAreSessionOnly(t *testing.T) {
	registry := &Registry{}
	if _, err := registry.HandleToolCall(makeToolCall("UpdateTasks", "update-tasks", map[string]any{"op": "add", "id": "T1"})); err == nil || !strings.Contains(err.Error(), "only available inside an agent session") {
		t.Fatalf("UpdateTasks error = %v, want session-only error", err)
	}
	if _, err := registry.HandleToolCall(makeToolCall("ListTasks", "list-tasks", map[string]any{})); err == nil || !strings.Contains(err.Error(), "only available inside an agent session") {
		t.Fatalf("ListTasks error = %v, want session-only error", err)
	}
	if _, err := registry.HandleToolCall(makeToolCall("CallSubagent", "call-subagent", map[string]any{"agent": "review", "task": "check"})); err == nil || !strings.Contains(err.Error(), "only available inside an agent session") {
		t.Fatalf("CallSubagent error = %v, want session-only error", err)
	}
	if !hasToolDefinition(registry.ToolDefinitions(), "UpdateTasks") {
		t.Fatal("UpdateTasks tool definition should be available")
	}
	if !hasToolDefinition(registry.ToolDefinitions(), "ListTasks") {
		t.Fatal("ListTasks tool definition should be available")
	}
	if hasToolDefinition((&Registry{DisableTasks: true}).ToolDefinitions(), "UpdateTasks") {
		t.Fatal("UpdateTasks tool definition should be hidden when tasks are disabled")
	}
	if hasToolDefinition((&Registry{DisableTasks: true}).ToolDefinitions(), "ListTasks") {
		t.Fatal("ListTasks tool definition should be hidden when tasks are disabled")
	}
	if !hasToolDefinition(registry.ToolDefinitions(), "CallSubagent") {
		t.Fatal("CallSubagent tool definition should be available")
	}
	callSubagent, ok := toolDefinitionByName(registry.ToolDefinitions(), "CallSubagent")
	if !ok {
		t.Fatal("CallSubagent tool definition missing")
	}
	properties, _ := callSubagent.Parameters["properties"].(map[string]any)
	for _, name := range []string{"agent", "task", "subagent_type", "prompt", "description"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("CallSubagent properties missing %q: %#v", name, properties)
		}
	}
	if _, ok := callSubagent.Parameters["anyOf"]; !ok {
		t.Fatalf("CallSubagent schema missing anyOf aliases: %#v", callSubagent.Parameters)
	}
	if hasToolDefinition(registry.WithDisabledTools(map[string]bool{"CallSubagent": true}).ToolDefinitions(), "CallSubagent") {
		t.Fatal("CallSubagent tool definition should be hidden when disabled")
	}
}

func TestSendFileToolDefinitionsAreGated(t *testing.T) {
	if hasToolDefinition((&Registry{}).ToolDefinitions(), "SendFile") {
		t.Fatal("SendFile should be hidden without a configured file sender")
	}
	if !hasToolDefinition((&Registry{FileSender: &recordingFileSender{}}).ToolDefinitions(), "SendFile") {
		t.Fatal("SendFile should be available with a configured file sender")
	}
}

func TestSendFileToolValidatesAndSendsWorkspaceFile(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.pdf"), []byte("pdf"), 0644); err != nil {
		t.Fatalf("writing report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "artifact.zip"), []byte("zip"), 0644); err != nil {
		t.Fatalf("writing archive: %v", err)
	}
	sender := &recordingFileSender{}
	registry := (&Registry{FileSender: sender}).WithWorkspaceDir(workspace)

	ctx := WithSendFileMessageID(context.Background(), "telegram-1")
	msg, err := registry.HandleToolCallContext(ctx, makeToolCall("SendFile", "send-1", map[string]any{
		"file_path": "report.pdf",
		"caption":   "latest report",
	}))
	if err != nil {
		t.Fatalf("SendFile failed: %v", err)
	}
	if !strings.Contains(msg.Content, "report.pdf") {
		t.Fatalf("tool message = %q, want report.pdf", msg.Content)
	}
	if len(sender.paths) != 1 || sender.paths[0] != filepath.Join(workspace, "report.pdf") || sender.captions[0] != "latest report" {
		t.Fatalf("sender calls = paths %#v captions %#v", sender.paths, sender.captions)
	}
	if sender.messageIDs[0] != "telegram-1" {
		t.Fatalf("messageID = %q, want telegram-1", sender.messageIDs[0])
	}

	if _, err := registry.HandleToolCall(makeToolCall("SendFile", "send-archive", map[string]any{
		"file_path": "artifact.zip",
	})); err != nil {
		t.Fatalf("SendFile should allow archive artifacts: %v", err)
	}
}

func TestSendFileToolRejectsUnsafeUnsupportedAndOversizedFiles(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving workspace: %v", err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "binary.exe"), []byte("exe"), 0644); err != nil {
		t.Fatalf("writing executable: %v", err)
	}
	largePath := filepath.Join(workspace, "large.pdf")
	large, err := os.Create(largePath)
	if err != nil {
		t.Fatalf("creating large file: %v", err)
	}
	if err := large.Truncate(MaxSendFileBytes + 1); err != nil {
		t.Fatalf("truncating large file: %v", err)
	}
	if err := large.Close(); err != nil {
		t.Fatalf("closing large file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	registry := (&Registry{FileSender: &recordingFileSender{}}).WithWorkspaceDir(workspace)

	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "outside", path: filepath.Join(outside, "report.pdf"), want: "access denied"},
		{name: "unsupported", path: "binary.exe", want: "unsupported file type"},
		{name: "oversized", path: "large.pdf", want: "file exceeds"},
		{name: "symlink", path: filepath.Join("link", "report.pdf"), want: "symlink in path not allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registry.HandleToolCall(makeToolCall("SendFile", "send-"+tc.name, map[string]any{"file_path": tc.path}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestPlanModeBlocksMutationTools(t *testing.T) {
	registry := (&Registry{}).WithPlanMode(true)

	_, err := registry.HandleToolCall(makeToolCall("Write", "w-plan", map[string]any{
		"file_path": "plan.txt",
		"content":   "nope",
	}))
	if err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected plan mode Write rejection, got %v", err)
	}

	_, err = registry.HandleToolCall(makeToolCall("Edit", "e-plan", map[string]any{
		"file_path": "plan.txt",
		"edits": []map[string]any{
			{"old_text": "a", "new_text": "b"},
		},
	}))
	if err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected plan mode Edit rejection, got %v", err)
	}

	_, err = (&Registry{FileSender: &recordingFileSender{}}).WithPlanMode(true).HandleToolCall(makeToolCall("SendFile", "send-plan", map[string]any{
		"file_path": "report.pdf",
	}))
	if err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected plan mode SendFile rejection, got %v", err)
	}
}

func toolDefinitionByName(defs []llm.ToolDefinition, name string) (llm.ToolDefinition, bool) {
	for _, def := range defs {
		if def.Name == name {
			return def, true
		}
	}
	return llm.ToolDefinition{}, false
}

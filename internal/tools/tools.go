package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

const (
	DefaultReadLimit   = 200
	MaxReadBytes       = 20 * 1024
	MaxBashOutputBytes = 30 * 1024
	DiffContextLines   = 4
	DefaultGrepLimit   = 100
	MaxGrepLineChars   = 500
	MaxToolOutputBytes = 50 * 1024
	DefaultFindLimit   = 1000
	DefaultLsLimit     = 500
)

type Registry struct {
	Logger              *log.Logger
	RunSandboxed        func(cwd, command string) (string, string, error)
	RunSandboxedContext func(ctx context.Context, cwd, command string) (string, string, error)
	RunSandboxedOptions func(ctx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error)
	WorkspaceDir        string
	FileSender          FileSender
	TavilyAPIKey        string
	BraveSearchAPIKey   string
	HTTPClient          httpDoer
	ToolConfig          Config
	SandboxDisabled     bool
	SessionExec         bool
	AskSandboxOff       bool
	RunUnsandboxed      func(ctx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error)
	SandboxEnvironment  func() (map[string]string, error)
	NewSandboxSession   func() *sandbox.Session
	DisableTasks        bool
	ReadableRoots       []string
	WritableRoots       []string
	NetworkAllowlist    []string
	HiddenPaths         []string
	ReadOnlyPaths       []string
	sandboxConfig       *sandbox.Config
	EnabledTools        map[string]bool
	DisabledTools       map[string]bool
	customTools         map[string]Tool
	sessionTools        map[string]Tool
	executionState      *sessionExecutionState
	permissionMu        *sync.RWMutex
	permissionSource    *Registry
	// planMode is atomic because the UI switches it while tool calls run.
	// Each clone gets its own copy, so a subagent keeps the mode it was
	// spawned with.
	planMode *atomic.Bool
}

// PlanMode reports whether mutating tools are disabled and Bash runs
// read-only.
func (r *Registry) PlanMode() bool {
	return r != nil && r.planMode != nil && r.planMode.Load()
}

// SetPlanMode switches plan mode for r only; registries already cloned from
// r keep their mode. Call it before handing r to other goroutines.
func (r *Registry) SetPlanMode(enabled bool) {
	if r.planMode == nil {
		r.planMode = &atomic.Bool{}
	}
	r.planMode.Store(enabled)
}

// WithPlanMode returns a clone of r with plan mode set to enabled.
func (r *Registry) WithPlanMode(enabled bool) *Registry {
	clone := r.clone()
	clone.planMode.Store(enabled)
	return clone
}

type ToolHandler func(ctx context.Context, call llm.ToolCall) (string, error)

type Tool struct {
	Definition     llm.ToolDefinition
	Handler        ToolHandler
	ValidateConfig ToolConfigValidator
}

func (r *Registry) WithWorkspaceDir(dir string) *Registry {
	clone := r.clone()
	clone.WorkspaceDir = dir
	clone.applyWorkspaceSandbox()
	return clone
}

func (r *Registry) WithReadableRoots(roots ...string) *Registry {
	clone := r.clone()
	clone.ReadableRoots = append(clone.ReadableRoots, roots...)
	clone.applyWorkspaceSandbox()
	return clone
}

func (r *Registry) WithFileSender(sender FileSender) *Registry {
	clone := r.clone()
	clone.FileSender = sender
	return clone
}

func (r *Registry) workspaceDir() (string, error) {
	if r != nil && strings.TrimSpace(r.WorkspaceDir) != "" {
		return r.WorkspaceDir, nil
	}
	return os.Getwd()
}

func (r *Registry) resolveWorkspacePath(path string) (string, string, error) {
	cwd, err := r.workspaceDir()
	if err != nil {
		return "", "", err
	}
	if filepath.IsAbs(path) {
		return path, cwd, nil
	}
	return filepath.Join(cwd, path), cwd, nil
}

func (r *Registry) clone() *Registry {
	if r == nil {
		return &Registry{}
	}
	mu := r.permissionMutex()
	mu.RLock()
	defer mu.RUnlock()
	clone := *r
	clone.ReadableRoots = append([]string{}, r.ReadableRoots...)
	clone.WritableRoots = append([]string{}, r.WritableRoots...)
	clone.HiddenPaths = append([]string{}, r.HiddenPaths...)
	clone.ReadOnlyPaths = append([]string{}, r.ReadOnlyPaths...)
	clone.NetworkAllowlist = append([]string{}, r.NetworkAllowlist...)
	clone.permissionMu = &sync.RWMutex{}
	clone.planMode = &atomic.Bool{}
	clone.planMode.Store(r.PlanMode())
	clone.ToolConfig = cloneConfig(r.ToolConfig)
	if r.EnabledTools != nil {
		clone.EnabledTools = make(map[string]bool, len(r.EnabledTools))
		for name, enabled := range r.EnabledTools {
			clone.EnabledTools[name] = enabled
		}
	}
	if r.DisabledTools != nil {
		clone.DisabledTools = make(map[string]bool, len(r.DisabledTools))
		for name, disabled := range r.DisabledTools {
			clone.DisabledTools[name] = disabled
		}
	}
	if r.customTools != nil {
		clone.customTools = make(map[string]Tool, len(r.customTools))
		for name, tool := range r.customTools {
			clone.customTools[name] = tool
		}
	}
	if r.sessionTools != nil {
		clone.sessionTools = make(map[string]Tool, len(r.sessionTools))
		for name, tool := range r.sessionTools {
			clone.sessionTools[name] = tool
		}
	}
	return &clone
}

// WithLivePermissionsFrom lets an isolated background child observe later
// filesystem and network grants without sharing its parent's shell. Grants
// made by the child remain local to the child.
func (r *Registry) WithLivePermissionsFrom(parent *Registry) *Registry {
	clone := r.clone()
	clone.permissionSource = parent
	return clone
}

// BindSessionExecution gives a top-level agent tree its own lazily-created
// process state. Registry clones used by child agents intentionally share it.
func (r *Registry) BindSessionExecution() (*Registry, bool) {
	if r == nil || !r.SessionExec || r.executionState != nil {
		return r, false
	}
	clone := r.clone()
	clone.executionState = &sessionExecutionState{factory: r.NewSandboxSession}
	return clone, true
}

// ForNewTopLevelSession detaches execution state inherited through a clone.
func (r *Registry) ForNewTopLevelSession() *Registry {
	clone := r.clone()
	clone.executionState = nil
	return clone
}

func (r *Registry) ShareSessionExecutionFrom(parent *Registry) *Registry {
	clone := r.clone()
	if parent != nil {
		clone.executionState = parent.executionState
		clone.NewSandboxSession = parent.NewSandboxSession
		clone.SessionExec = parent.SessionExec
	}
	return clone
}

func (r *Registry) WithSessionToolsFrom(source *Registry) *Registry {
	clone := r.clone()
	clone.sessionTools = nil
	if source != nil && len(source.sessionTools) > 0 {
		clone.sessionTools = make(map[string]Tool, len(source.sessionTools))
		for name, tool := range source.sessionTools {
			clone.sessionTools[name] = tool
		}
	}
	return clone
}

func (r *Registry) CloseSessionExecution() error {
	if r == nil || r.executionState == nil {
		return nil
	}
	return r.executionState.close()
}

func (r *Registry) RegisterTool(tool Tool) error {
	if r == nil {
		return fmt.Errorf("registry is nil")
	}
	name := strings.TrimSpace(tool.Definition.Name)
	if name == "" {
		return fmt.Errorf("tool name must not be empty")
	}
	if tool.Handler == nil {
		return fmt.Errorf("tool %s handler must not be nil", name)
	}
	if isBuiltInToolName(name) {
		return fmt.Errorf("tool %s is reserved", name)
	}
	if r.customTools == nil {
		r.customTools = map[string]Tool{}
	}
	if _, exists := r.customTools[name]; exists {
		return fmt.Errorf("tool %s already registered", name)
	}
	tool.Definition.Name = name
	r.customTools[name] = tool
	return nil
}

func (r *Registry) WithSessionTool(tool Tool) (*Registry, error) {
	if r == nil {
		return nil, fmt.Errorf("registry is nil")
	}
	name := strings.TrimSpace(tool.Definition.Name)
	if name == "" {
		return nil, fmt.Errorf("tool name must not be empty")
	}
	if tool.Handler == nil {
		return nil, fmt.Errorf("tool %s handler must not be nil", name)
	}
	// Integrations may add their own session tools, but not shadow a built-in
	// or custom tool, apart from the built-ins each session binds itself.
	if isBuiltInToolName(name) && !isSessionToolName(name) {
		return nil, fmt.Errorf("tool %s is a built-in tool", name)
	}
	if _, exists := r.customTools[name]; exists {
		return nil, fmt.Errorf("tool %s is already a custom tool", name)
	}
	clone := r.clone()
	if clone.sessionTools == nil {
		clone.sessionTools = map[string]Tool{}
	}
	tool.Definition.Name = name
	clone.sessionTools[name] = tool
	return clone, nil
}

// WithoutSessionTools returns a registry containing only process-scoped tools.
// A new session binds its own session tool handlers after construction.
func (r *Registry) WithoutSessionTools() *Registry {
	clone := r.clone()
	clone.sessionTools = nil
	return clone
}

func (r *Registry) logInfo(format string, args ...interface{}) {
	if r.Logger != nil {
		r.Logger.Info(format, args...)
	}
}

func (r *Registry) logDebug(format string, args ...interface{}) {
	if r.Logger != nil {
		r.Logger.Debug(format, args...)
	}
}

func (r *Registry) HandleToolCall(toolCall llm.ToolCall) (llm.Message, error) {
	return r.HandleToolCallContext(context.Background(), toolCall)
}

func (r *Registry) HandleToolCallContext(ctx context.Context, toolCall llm.ToolCall) (llm.Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return llm.Message{}, err
	}
	if r != nil && r.isToolExplicitlyDisabled(toolCall.Name) {
		return llm.Message{}, fmt.Errorf("tool %s is disabled", toolCall.Name)
	}
	if r != nil {
		if tool, ok := r.sessionTools[toolCall.Name]; ok {
			content, err := tool.Handler(ctx, toolCall)
			if err != nil {
				return llm.Message{}, err
			}
			return llm.Message{Role: llm.RoleTool, Content: content, ToolCallID: toolCall.ID}, nil
		}
		if tool, ok := r.customTools[toolCall.Name]; ok {
			content, err := tool.Handler(ctx, toolCall)
			if err != nil {
				return llm.Message{}, err
			}
			return llm.Message{Role: llm.RoleTool, Content: content, ToolCallID: toolCall.ID}, nil
		}
	}
	switch toolCall.Name {
	case "Read":
		return r.handleReadToolCall(ctx, toolCall)
	case "Edit":
		if r.PlanMode() {
			return llm.Message{}, fmt.Errorf("plan mode is active: Edit tool is disabled")
		}
		return r.handleEditToolCall(ctx, toolCall)
	case "Write":
		if r.PlanMode() {
			return llm.Message{}, fmt.Errorf("plan mode is active: Write tool is disabled")
		}
		return r.handleWriteToolCall(ctx, toolCall)
	case "Bash":
		return r.handleBashToolCall(ctx, toolCall)
	case "Grep":
		return r.handleGrepToolCall(ctx, toolCall)
	case "Find":
		return r.handleFindToolCall(ctx, toolCall)
	case "Ls":
		return r.handleLsToolCall(ctx, toolCall)
	case "WebSearch":
		if !r.webSearchEnabled() {
			return llm.Message{}, fmt.Errorf("invalid tool")
		}
		return r.handleWebSearchToolCall(ctx, toolCall)
	case "WebFetch":
		if !r.webFetchEnabled() {
			return llm.Message{}, fmt.Errorf("invalid tool")
		}
		return r.handleWebFetchToolCall(ctx, toolCall)
	case "LocalImage":
		if !r.localImageEnabled() {
			return llm.Message{}, fmt.Errorf("invalid tool")
		}
		return r.handleLocalImageToolCall(ctx, toolCall)
	case "UpdateTasks":
		return llm.Message{}, fmt.Errorf("UpdateTasks is only available inside an agent session")
	case "ListTasks":
		return llm.Message{}, fmt.Errorf("ListTasks is only available inside an agent session")
	case "CallSubagent":
		return llm.Message{}, fmt.Errorf("CallSubagent is only available inside an agent session")
	case "SendFile":
		if !r.sendFileEnabled() {
			return llm.Message{}, fmt.Errorf("invalid tool")
		}
		if r.PlanMode() {
			return llm.Message{}, fmt.Errorf("plan mode is active: SendFile tool is disabled")
		}
		return r.handleSendFileToolCall(ctx, toolCall)
	default:
		return llm.Message{}, fmt.Errorf("invalid tool")
	}
}

func (r *Registry) runSandboxed(ctx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts.ReadableRoots = append(opts.ReadableRoots, permissionTargets(ctx, PermissionPathRead)...)
	opts.WritableRoots = append(opts.WritableRoots, permissionTargets(ctx, PermissionPathWrite)...)
	opts.NetworkAllowlist = append(opts.NetworkAllowlist, permissionTargets(ctx, PermissionNetwork)...)
	if r != nil {
		read, write, network := r.permissionTargets()
		opts.ReadableRoots = append(opts.ReadableRoots, read...)
		opts.WritableRoots = append(opts.WritableRoots, write...)
		opts.NetworkAllowlist = append(opts.NetworkAllowlist, network...)
		// The runner is built writable so the mode can switch mid-session;
		// plan mode is applied to each command instead.
		opts.ReadOnly = opts.ReadOnly || r.PlanMode()
	}
	if r != nil && r.RunSandboxedOptions != nil {
		return r.RunSandboxedOptions(ctx, cwd, command, opts)
	}
	// The runners below cannot make a single command read-only, so refuse
	// rather than run it writable.
	if opts.ReadOnly {
		return "", "", fmt.Errorf("read-only command needs a sandbox runner that accepts options")
	}
	if r != nil && r.RunSandboxedContext != nil {
		return r.RunSandboxedContext(ctx, cwd, command)
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if r != nil && r.RunSandboxed != nil {
		stdout, stderr, err := r.RunSandboxed(cwd, command)
		if err != nil {
			return stdout, stderr, err
		}
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		return stdout, stderr, nil
	}
	return "", "", fmt.Errorf("sandbox runner is not configured")
}

type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func (r *Registry) httpClient() httpDoer {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

func (r *Registry) webSearchEnabled() bool {
	return r != nil && (strings.TrimSpace(r.BraveSearchAPIKey) != "" || strings.TrimSpace(r.TavilyAPIKey) != "")
}

func (r *Registry) webFetchEnabled() bool {
	return r != nil
}

func IsPathSafe(filePath string, cwd string) (bool, string) {
	return IsPathSafeInRoots(filePath, cwd, nil)
}

func IsPathSafeInRoots(filePath string, cwd string, extraRoots []string) (bool, string) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return false, "invalid path"
	}

	roots := []string{cwd}
	roots = append(roots, extraRoots...)

	insideAllowedRoot := false
	for _, root := range roots {
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return false, "invalid allowed directory"
		}
		if absPath == absRoot || strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
			insideAllowedRoot = true
			break
		}
	}
	if !insideAllowedRoot {
		return false, "path outside allowed directory"
	}

	parts := strings.Split(absPath, string(filepath.Separator))
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		componentPath := "/" + filepath.Join(parts[1:i+1]...)
		info, err := os.Lstat(componentPath)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return false, "symlink in path not allowed"
		}
	}

	return true, ""
}

func (r *Registry) readableRoots() []string {
	if r == nil {
		return nil
	}
	read, write, _ := r.permissionTargets()
	return append(read, write...)
}

func (r *Registry) isPathReadable(filePath string, cwd string) (bool, string) {
	if r == nil {
		return IsPathSafe(filePath, cwd)
	}
	return IsPathSafeInRoots(filePath, cwd, r.readableRoots())
}

func (r *Registry) isPathWritable(filePath string, cwd string) (bool, string) {
	if r == nil {
		return IsPathSafe(filePath, cwd)
	}
	if r.isReadOnlyPath(filePath) {
		return false, "read-only path"
	}
	_, write, _ := r.permissionTargets()
	return IsPathSafeInRoots(filePath, cwd, write)
}

func (r *Registry) isPathReadableContext(ctx context.Context, filePath, cwd string) (bool, string) {
	if r.isHiddenPath(filePath) {
		return false, "protected path"
	}
	if permissionGranted(ctx, PermissionPathRead, filePath) || r.permissionGranted(PermissionPathRead, filePath) {
		path := canonicalApprovalPath(filePath)
		return IsPathSafeInRoots(path, cwd, []string{path})
	}
	return r.isPathReadable(filePath, cwd)
}

func (r *Registry) isPathWritableContext(ctx context.Context, filePath, cwd string) (bool, string) {
	if r.isHiddenPath(filePath) {
		return false, "protected path"
	}
	if r.isReadOnlyPath(filePath) {
		return false, "read-only path"
	}
	if permissionGranted(ctx, PermissionPathWrite, filePath) || r.permissionGranted(PermissionPathWrite, filePath) {
		path := canonicalApprovalPath(filePath)
		return IsPathSafeInRoots(path, cwd, []string{path})
	}
	return r.isPathWritable(filePath, cwd)
}

func (r *Registry) isHiddenPath(path string) bool {
	if r == nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	hiddenPaths := r.HiddenPaths
	if r.sandboxConfig != nil {
		hiddenPaths = r.sandboxConfig.ForWorkspace(r.WorkspaceDir).HiddenPaths
	}
	for _, hidden := range hiddenPaths {
		if sandbox.PathContains(hidden, path) {
			return true
		}
		hiddenAbs, hiddenErr := filepath.Abs(hidden)
		if hiddenErr == nil && (abs == hiddenAbs || strings.HasPrefix(abs, hiddenAbs+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

func (r *Registry) isReadOnlyPath(path string) bool {
	if r == nil {
		return false
	}
	readOnlyPaths := r.ReadOnlyPaths
	if r.sandboxConfig != nil {
		readOnlyPaths = r.sandboxConfig.ForWorkspace(r.WorkspaceDir).ReadOnlyPaths
	}
	for _, root := range readOnlyPaths {
		if sandbox.PathContains(root, path) {
			return true
		}
	}
	return false
}

// WithProtectedPaths makes paths read-only for every tool of the returned
// registry: file tools refuse to write them and the Bash sandbox mounts them
// read-only. The registry gets its own execution state, so a session shell
// started under a looser policy is never reused.
func (r *Registry) WithProtectedPaths(paths ...string) *Registry {
	clone := r.clone()
	var absolute []string
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(clone.WorkspaceDir, path)
		}
		absolute = append(absolute, filepath.Clean(path))
	}
	if len(absolute) == 0 {
		return clone
	}
	clone.ReadOnlyPaths = append(clone.ReadOnlyPaths, absolute...)
	if clone.sandboxConfig != nil {
		config := *clone.sandboxConfig
		config.ReadOnlyPaths = append(append([]string(nil), config.ReadOnlyPaths...), absolute...)
		clone.sandboxConfig = &config
		clone.applyWorkspaceSandbox()
	}
	clone.executionState = nil
	return clone
}

func (r *Registry) WithSandboxConfig(config sandbox.Config) *Registry {
	clone := r.clone()
	config.SessionPaths = append(append([]string(nil), config.SessionPaths...), filepath.Join(r.WorkspaceDir, ".caretz", "sessions"))
	clone.sandboxConfig = &config
	clone.applyWorkspaceSandbox()
	return clone
}

func (r *Registry) applyWorkspaceSandbox() {
	if r.sandboxConfig == nil {
		return
	}
	config := *r.sandboxConfig
	config.ReadableRoots = append([]string(nil), r.ReadableRoots...)
	config.WritableRoots = append([]string(nil), r.WritableRoots...)
	config = config.ForWorkspace(r.WorkspaceDir)
	r.HiddenPaths, r.ReadOnlyPaths = config.HiddenPaths, config.ReadOnlyPaths
	// Keep the base config on the runner so workspace policy is refreshed at
	// launch, including symlink changes after the registry was built.
	base := *r.sandboxConfig
	base.ReadableRoots, base.WritableRoots = config.ReadableRoots, config.WritableRoots
	runner := sandbox.NewRunner(base)
	if !r.SandboxDisabled {
		r.RunSandboxed, r.RunSandboxedContext, r.RunSandboxedOptions = runner.Run, runner.RunContext, runner.RunContextOptions
	}
	r.NewSandboxSession = func() *sandbox.Session { return runner.NewSession(r.SandboxDisabled) }
}

func ToolDefinitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{
			Name:        "Read",
			Description: "Read file contents. Output is truncated to 200 lines or 20KB (whichever is hit first). Use offset/limit for large files. Truncation notices include the exact offset to use for continuation.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{
						"type":        "string",
						"description": "The path to the file to read",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Line number to start reading from (1-indexed, default: 1)",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of lines to return (default: 200)",
					},
				},
				"required": []string{"file_path"},
			},
		},
		{
			Name:        "Edit",
			Description: "Edit a file using exact text replacement. Every edits[].old_text must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{
						"type":        "string",
						"description": "Path to the file to edit",
					},
					"edits": map[string]any{
						"type":        "array",
						"description": "Array of edits. Each edit replaces old_text with new_text.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old_text": map[string]any{
									"type":        "string",
									"description": "Exact text to find for replacement. Must be unique in the file.",
								},
								"new_text": map[string]any{
									"type":        "string",
									"description": "Replacement text.",
								},
							},
							"required": []string{"old_text", "new_text"},
						},
					},
				},
				"required": []string{"file_path", "edits"},
			},
		},
		{
			Name:        "Write",
			Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories. Use Edit tool for modifying existing files.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{
						"type":        "string",
						"description": "The path to the file to write to",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The content to write to the file",
					},
				},
				"required": []string{"file_path", "content"},
			},
		},
		{
			Name:        "Find",
			Description: "Find files by glob pattern. Respects .gitignore when fd is available. Results capped at 1000 files. Paths are shown relative to the working directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Glob pattern to match files, e.g. '*.go', '*.ts'",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to search in (default: current directory)",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results (default: 1000)",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        "Bash",
			Description: "Execute a shell command in a sandbox. Default timeout: 120 seconds; set timeout_seconds to request longer commands, capped at 600 seconds. Output: last 30KB. Reads: current working directory, --read-root, and --writable-root. Writes: current working directory and --writable-root. Network: denied by default; --network-allow permits HTTP/HTTPS hosts, and tcp://HOST:PORT permits SOCKS5 TCP through $CARETZ_SOCKS_PROXY. Git-over-SSH is routed through the proxy automatically. Direct SSH is blocked, so run plain ssh as ssh -o \"ProxyCommand=$CARETZ_SSH_PROXY_COMMAND\"; if SSH reports Operation not permitted, retry that way so the user can approve the destination. --unrestricted-network allows direct network access.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The command to execute",
					},
					"timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Optional command timeout in seconds. Defaults to 120; values above 600 are capped.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "Grep",
			Description: "Search file contents for a pattern using ripgrep when available, with grep fallback. Ripgrep respects .gitignore; grep fallback may not. Output is truncated to first 100 matches and 50KB. Each matched line is truncated to 500 chars.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Search pattern (regex or literal string)",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory or file to search (default: current directory)",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": "Filter files by glob pattern, e.g. '*.go'",
					},
					"ignore_case": map[string]any{
						"type":        "boolean",
						"description": "Case-insensitive search",
					},
					"literal": map[string]any{
						"type":        "boolean",
						"description": "Treat pattern as literal string instead of regex",
					},
					"context": map[string]any{
						"type":        "integer",
						"description": "Lines of context to show before and after each match",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of matches to return (default: 100)",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        "Ls",
			Description: "List directory contents. Returns sorted entries with '/' suffix for directories. Capped at 500 entries by default.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to list (default: current directory)",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of entries to return (default: 500)",
					},
				},
				"required": []string{},
			},
		},
		{
			Name:        "UpdateTasks",
			Description: "Update the harness-managed task list for multi-step work. Keep exactly one task per scope in progress, mark tasks done immediately with an outcome, and keep next_action specific enough for another agent to resume cold. Add validation instructions when another model or process can check completion.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"op": map[string]any{
						"type":        "string",
						"enum":        []string{"add", "start", "done", "block", "update"},
						"description": "Task operation to apply.",
					},
					"id": map[string]any{
						"type":        "string",
						"description": "Stable task ID, for example T3.",
					},
					"scope": map[string]any{
						"type":        "string",
						"description": "Optional coordination scope. Omit for ordinary tasks; use the exact scope supplied by an orchestrator.",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Task title. Required for add; on start or done of a task not yet added, creates it.",
					},
					"validation": map[string]any{
						"type":        "string",
						"description": "Optional instructions for independently checking task completion, such as tests, lint, or an observable result. Set on add or update; required for tasks planned by /goal.",
					},
					"next_action": map[string]any{
						"type":        "string",
						"description": "Specific next action. Defaults to the title on start; update requires next_action or validation.",
					},
					"outcome": map[string]any{
						"type":        "string",
						"description": "One-sentence result. Required for done.",
					},
				},
				"required": []string{"op", "id"},
			},
		},
		{
			Name:        "ListTasks",
			Description: "List the full harness-managed task state without changing it. Use this when you need to inspect all internal tasks, including older completed tasks that compact reminders may hide.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []string{},
			},
		},
		{
			Name:        "CallSubagent",
			Description: "Run a configured .caretz/agents/<agent>.md subagent in a child session and return its final result. Use this when a specialized subagent should independently research, validate, or perform a delegated task before you continue.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "The subagent invocation name, for example code-researcher. Must be a configured subagent name, not a path.",
					},
					"subagent_type": map[string]any{
						"type":        "string",
						"description": "Alias for agent used by some ACP clients.",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "The complete task prompt to give the subagent.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Alias for task used by some ACP clients.",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "Optional short description of the subagent task. Used as the task only when task and prompt are empty.",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Optional configured provider/model ref to use for this subagent run, overriding the subagent definition's model.",
					},
				},
				"anyOf": []map[string]any{
					{"required": []string{"agent", "task"}},
					{"required": []string{"subagent_type", "prompt"}},
				},
			},
		},
	}
}

func (r *Registry) ToolDefinitions() []llm.ToolDefinition {
	defs := ToolDefinitions()
	if r != nil && r.webSearchEnabled() {
		defs = append(defs, webSearchToolDefinition())
	}
	if r != nil && r.webFetchEnabled() {
		defs = append(defs, webFetchToolDefinition())
	}
	if r != nil && r.localImageEnabled() {
		defs = append(defs, localImageToolDefinition())
	}
	if r != nil && r.sendFileEnabled() {
		defs = append(defs, sendFileToolDefinition())
	}
	if r != nil && len(r.customTools) > 0 {
		names := make([]string, 0, len(r.customTools))
		for name := range r.customTools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			defs = append(defs, r.customTools[name].Definition)
		}
	}
	if r != nil && len(r.sessionTools) > 0 {
		names := make([]string, 0, len(r.sessionTools))
		for name := range r.sessionTools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !containsToolDefinition(defs, name) {
				defs = append(defs, r.sessionTools[name].Definition)
			}
		}
	}

	filtered := defs[:0]
	for _, def := range defs {
		if r != nil && !r.IsToolEnabled(def.Name) {
			continue
		}
		if r != nil && r.SandboxDisabled && def.Name == "Bash" {
			def.Description = "Execute a shell command without CARETZ's internal sandbox. Default timeout: 120 seconds; set timeout_seconds to request longer commands, capped at 600 seconds. Output: last 30KB. Commands run in the current workspace with the process permissions available inside this environment."
		}
		if def.Name == "Bash" && (r == nil || !r.SandboxDisabled) {
			def.Description += sandboxTemporaryDirectoryGuidance(runtime.GOOS, r != nil && r.SessionExec)
		}
		if r != nil && def.Name == "Bash" && r.SessionExec {
			properties, _ := def.Parameters["properties"].(map[string]any)
			properties["background"] = map[string]any{
				"type":        "boolean",
				"description": "Run the command in the background for this live session. Cannot be combined with timeout_seconds.",
			}
		}
		filtered = append(filtered, def)
	}
	return filtered
}

func sandboxTemporaryDirectoryGuidance(goos string, sessionExec bool) string {
	if goos == "darwin" {
		return " On macOS, literal /tmp is not writable inside the sandbox; use $TMPDIR for temporary files."
	}
	if !sessionExec {
		return " Each command gets its own empty /tmp, discarded when it exits; keep files that later commands need in the working directory."
	}
	return ""
}

func (r *Registry) IsToolEnabled(name string) bool {
	if r == nil {
		return true
	}
	if r.isToolDisabledByPolicy(name) {
		return false
	}
	switch name {
	case "WebSearch":
		return r.webSearchEnabled()
	case "WebFetch":
		return r.webFetchEnabled()
	case "LocalImage":
		return r.localImageEnabled()
	case "SendFile":
		return r.sendFileEnabled()
	default:
		if isBuiltInToolName(name) {
			return true
		}
		if _, ok := r.sessionTools[name]; ok {
			return true
		}
		_, ok := r.customTools[name]
		return ok
	}
}

func (r *Registry) isToolDisabledByPolicy(name string) bool {
	if r == nil {
		return false
	}
	return r.isToolExplicitlyDisabled(name)
}

func (r *Registry) isToolExplicitlyDisabled(name string) bool {
	if r == nil {
		return false
	}
	if toolNameInMap(r.DisabledTools, name) {
		return true
	}
	if r.EnabledTools != nil && !toolNameInMap(r.EnabledTools, name) {
		return true
	}
	if r.DisableTasks {
		switch name {
		case "UpdateTasks", "ListTasks":
			return true
		}
	}
	return false
}

func toolNameInMap(names map[string]bool, name string) bool {
	if len(names) == 0 {
		return false
	}
	if names[name] {
		return true
	}
	if names["*"] {
		return true
	}
	for configuredName, enabled := range names {
		if enabled && strings.EqualFold(configuredName, name) {
			return true
		}
	}
	return false
}

func isBuiltInToolName(name string) bool {
	switch name {
	case "Read", "Edit", "Write", "Find", "Bash", "Grep", "Ls",
		"UpdateTasks", "ListTasks", "CallSubagent",
		"WebSearch", "WebFetch", "LocalImage", "SendFile":
		return true
	default:
		return false
	}
}

func isSessionToolName(name string) bool {
	switch name {
	case "CallSubagent", "RegisterSchedule", "UpdateSchedule", "ListSchedules", "RemoveSchedule":
		return true
	default:
		return false
	}
}

func containsToolDefinition(defs []llm.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

func (r *Registry) WithDisabledTools(disabled map[string]bool) *Registry {
	clone := r.clone()
	merged := map[string]bool{}
	for k, v := range clone.DisabledTools {
		if v {
			merged[k] = true
		}
	}
	for k, v := range disabled {
		if v {
			merged[k] = true
		}
	}
	clone.DisabledTools = merged
	return clone
}

func (r *Registry) WithEnabledTools(names ...string) *Registry {
	clone := r.clone()
	enabled := map[string]bool{}
	for name, currentlyEnabled := range clone.EnabledTools {
		if currentlyEnabled {
			enabled[name] = true
		}
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			enabled[name] = true
		}
	}
	clone.EnabledTools = enabled
	return clone
}

// WithOnlyTools limits a child session to the named tools, including custom tools.
func (r *Registry) WithOnlyTools(names ...string) *Registry {
	clone := r.clone()
	clone.EnabledTools = make(map[string]bool, len(names))
	for _, name := range names {
		clone.EnabledTools[name] = true
	}
	return clone
}

func parseToolArgs(toolCall llm.ToolCall, target any) error {
	return json.Unmarshal([]byte(toolCall.Arguments), target)
}

package caretz

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/skills"
	internaltools "github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	libruntime "github.com/makkalot/caretz/lib/runtime"
	"github.com/makkalot/caretz/lib/tools"
	"github.com/makkalot/caretz/lib/types"
)

type ModelOptions struct {
	BaseURL          string
	APIKey           string
	Model            string
	Protocol         string
	ReasoningEffort  string
	ContextWindow    int
	ReservedTokens   int
	KeepRecentTokens int
	SummaryTokens    int
	Timeout          time.Duration
	MaxAttempts      int
}

type ToolMode string

const (
	ToolModeDefault  ToolMode = ""
	ToolModeBuiltIns ToolMode = "builtins"
	ToolModeNone     ToolMode = "none"
)

type SandboxOptions struct {
	Disabled            bool
	SessionExec         bool
	ReadOnly            bool
	ReadableRoots       []string
	WritableRoots       []string
	NetworkAllowlist    []string
	UnrestrictedNetwork bool
	AllowLoopback       bool
	AllowDocker         bool
	// AllowSystemBus lets Linux sandboxed commands reach the host's system
	// D-Bus when Caretz does not run as root, so systemctl and loginctl can
	// query the host. Changes still need polkit authorization.
	AllowSystemBus bool
	// AllowGPU exposes the host's GPU device nodes to Linux sandboxed
	// commands, so tools such as nvidia-smi see the real hardware.
	AllowGPU bool
	// AskSandboxOff asks the user, through the session's permission
	// requests, before rerunning a sandbox-blocked Bash command outside the
	// sandbox.
	AskSandboxOff bool
	HiddenPaths   []string
	ReadOnlyPaths []string
	Env           []string
}

type ToolOptions map[string]map[string]any

type StateOptions struct {
	Dir string
}

type Logger = log.Logger

type SubagentModelResolution struct {
	Ref              string
	Model            string
	Provider         llm.Provider
	ContextWindow    int
	ReservedTokens   int
	KeepRecentTokens int
	SummaryTokens    int
	// BaseURL and APIKey are needed when toolConfig.localImage.model names a
	// provider/model; LocalImage sends the image to BaseURL/chat/completions.
	BaseURL string
	APIKey  string
}

type SubagentModelRequest struct {
	Name            string
	WorkspaceDir    string
	Ref             string
	ReasoningEffort string
	// ValidateOnly resolves the model without starting a child, so a
	// fallback is reported when the child starts, not during validation.
	ValidateOnly bool
}

type SubagentModelResolver interface {
	ResolveSubagentModel(ctx context.Context, request SubagentModelRequest) (SubagentModelResolution, error)
}

type ClientOptions struct {
	Model                 ModelOptions
	Provider              llm.Provider
	WorkspaceDir          string
	SystemPrompt          string
	PreparedSystemPrompt  string
	Tools                 []tools.Tool
	ToolMode              ToolMode
	Sandbox               SandboxOptions
	State                 StateOptions
	Logger                *Logger
	TavilyAPIKey          string
	BraveSearchAPIKey     string
	ToolsConfig           ToolOptions
	EnabledTools          map[string]bool
	DisabledTools         map[string]bool
	AgentsDirs            []string
	AllowedAgents         map[string]bool
	DisableTasks          bool
	SubagentModelResolver SubagentModelResolver
}

type SessionOptions struct {
	Adapter      string
	WorkspaceDir string
	FileSender   tools.FileSender
	Model        *ModelOptions
	// PermissionTimeout limits how long an interactive permission request may
	// remain unanswered before it is denied. Values <= 0 wait until answered.
	PermissionTimeout time.Duration
}

type Client struct {
	ctx           context.Context
	logger        *log.Logger
	provider      llm.Provider
	workspaceDir  string
	systemPrompt  string
	toolRegistry  *internaltools.Registry
	stateDir      string
	inMemory      bool
	model         ModelOptions
	agentsDirs    []string
	allowedAgents map[string]bool
	disableTasks  bool
	resolver      SubagentModelResolver

	mu       sync.Mutex
	sessions map[string]*Session
}

type Session struct {
	inner *agent.AgentSession
}

type PermissionRequest = eventing.PermissionRequest
type PermissionResolution = eventing.PermissionResolution

func NewClient(ctx context.Context, opts ClientOptions) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if hasModelOptions(opts.Model) && opts.Provider != nil {
		return nil, fmt.Errorf("model options and provider cannot both be set")
	}
	if opts.ToolMode != ToolModeDefault && opts.ToolMode != ToolModeBuiltIns && opts.ToolMode != ToolModeNone {
		return nil, fmt.Errorf("unsupported tool mode %q", opts.ToolMode)
	}

	workspaceDir := strings.TrimSpace(opts.WorkspaceDir)
	if workspaceDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current working directory: %w", err)
		}
		workspaceDir = cwd
	}
	absWorkspace, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace dir %q: %w", workspaceDir, err)
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New()
	}
	if opts.Sandbox.SessionExec {
		warning, err := sandbox.CheckSessionExecution(opts.Sandbox.Disabled)
		if err != nil {
			return nil, err
		}
		if warning != "" {
			logger.Warn("%s", warning)
		}
	}
	model := defaultModelOptions(opts.Model)
	provider := opts.Provider
	if provider == nil {
		provider = llm.NewOpenAIProviderWithOptions(model.APIKey, model.BaseURL, model.Model, llm.OpenAIProviderOptions{
			RequestTimeout:        model.Timeout,
			DisableRequestTimeout: model.Timeout == 0,
			MaxAttempts:           model.MaxAttempts,
			ReasoningEffort:       model.ReasoningEffort,
			Protocol:              model.Protocol,
			Logf:                  logger.Info,
		})
	}
	stateDir := strings.TrimSpace(opts.State.Dir)
	inMemory := stateDir == ""
	if stateDir != "" {
		stateDir = filepath.Join(stateDir, "sessions")
	}
	if stateDir != "" {
		stateDir, err = filepath.Abs(stateDir)
		if err != nil {
			return nil, fmt.Errorf("resolving session storage: %w", err)
		}
	}

	sandboxConfig := sandbox.Config{
		UnrestrictedNetwork: opts.Sandbox.UnrestrictedNetwork,
		AllowLoopback:       opts.Sandbox.AllowLoopback,
		NetworkAllowlist:    append([]string{}, opts.Sandbox.NetworkAllowlist...),
		ReadableRoots:       append([]string{}, opts.Sandbox.ReadableRoots...),
		WritableRoots:       append([]string{}, opts.Sandbox.WritableRoots...),
		AllowDocker:         opts.Sandbox.AllowDocker,
		AllowSystemBus:      opts.Sandbox.AllowSystemBus,
		AllowGPU:            opts.Sandbox.AllowGPU,
		HiddenPaths:         append([]string{}, opts.Sandbox.HiddenPaths...),
		ReadOnlyPaths:       append([]string{}, opts.Sandbox.ReadOnlyPaths...),
		SessionPaths:        []string{stateDir},
		Environment:         append([]string{}, opts.Sandbox.Env...),
	}
	// Roots hold only explicit grants. Each session's own workspace is always
	// readable and writable, so defaulting them to the client workspace would
	// let a session with a different WorkspaceDir reach the client workspace
	// and every sibling session's workspace under it.
	if rgPath, err := exec.LookPath("rg"); err == nil {
		if resolved, err := filepath.EvalSymlinks(rgPath); err == nil {
			sandboxConfig.ExtraBinaries = append(sandboxConfig.ExtraBinaries, resolved)
		}
	}
	sandboxRunner := sandbox.NewRunner(sandboxConfig)

	runSandboxedOptions := sandboxRunner.RunContextOptions
	if opts.Sandbox.Disabled {
		runSandboxedOptions = sandbox.RunUnsandboxed
	}
	registry := &internaltools.Registry{
		Logger:              logger,
		RunSandboxed:        sandboxRunner.Run,
		RunSandboxedContext: sandboxRunner.RunContext,
		RunSandboxedOptions: runSandboxedOptions,
		WorkspaceDir:        absWorkspace,
		TavilyAPIKey:        opts.TavilyAPIKey,
		BraveSearchAPIKey:   opts.BraveSearchAPIKey,
		ToolConfig:          internaltools.Config(opts.ToolsConfig),
		SandboxDisabled:     opts.Sandbox.Disabled,
		SessionExec:         opts.Sandbox.SessionExec,
		AskSandboxOff:       opts.Sandbox.AskSandboxOff,
		RunUnsandboxed:      sandbox.RunApproved,
		SandboxEnvironment:  sandboxRunner.Environment,
		NewSandboxSession:   func() *sandbox.Session { return sandboxRunner.NewSession(opts.Sandbox.Disabled) },
		DisableTasks:        opts.DisableTasks,
		ReadableRoots:       sandboxConfig.ReadableRoots,
		WritableRoots:       sandboxConfig.WritableRoots,
		HiddenPaths:         sandboxConfig.HiddenPaths,
		EnabledTools:        cloneBoolMap(opts.EnabledTools),
		DisabledTools:       cloneBoolMap(opts.DisabledTools),
	}
	registry.SetPlanMode(opts.Sandbox.ReadOnly)
	registry = registry.WithSandboxConfig(sandboxConfig)
	if opts.ToolMode == ToolModeNone {
		registry.DisabledTools = map[string]bool{}
		for _, def := range internaltools.ToolDefinitions() {
			registry.DisabledTools[def.Name] = true
		}
	}
	for _, tool := range opts.Tools {
		if err := registry.RegisterTool(tool); err != nil {
			return nil, err
		}
	}
	if err := registry.ValidateConfig(); err != nil {
		return nil, err
	}

	systemPrompt := strings.TrimSpace(opts.PreparedSystemPrompt)
	if systemPrompt == "" {
		systemPrompt = skills.BuildSystemPromptWithOptions(skills.SystemPromptOptions{
			BasePrompt: strings.TrimSpace(opts.SystemPrompt),
			CWD:        absWorkspace,
			Tools:      registry.ToolDefinitions(),
		})
	}
	return &Client{
		ctx:           ctx,
		logger:        logger,
		provider:      provider,
		workspaceDir:  absWorkspace,
		systemPrompt:  systemPrompt,
		toolRegistry:  registry,
		stateDir:      stateDir,
		inMemory:      inMemory,
		model:         model,
		agentsDirs:    append([]string(nil), opts.AgentsDirs...),
		allowedAgents: cloneBoolMap(opts.AllowedAgents),
		disableTasks:  opts.DisableTasks,
		resolver:      opts.SubagentModelResolver,
		sessions:      map[string]*Session{},
	}, nil
}

func (c *Client) NewSession(id string, opts SessionOptions) (*Session, error) {
	if c == nil {
		return nil, fmt.Errorf("client is nil")
	}
	sessionID := strings.TrimSpace(id)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	adapter := strings.TrimSpace(opts.Adapter)
	if adapter == "" {
		adapter = "lib"
	}
	workspaceDir := strings.TrimSpace(opts.WorkspaceDir)
	if workspaceDir == "" {
		workspaceDir = c.workspaceDir
	}
	absWorkspace, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace dir %q: %w", workspaceDir, err)
	}
	registry := c.toolRegistry
	if absWorkspace != c.workspaceDir {
		registry = registry.WithWorkspaceDir(absWorkspace)
	}
	if opts.FileSender != nil {
		registry = registry.WithFileSender(opts.FileSender)
	}
	model := c.model
	provider := c.provider
	if opts.Model != nil {
		model = defaultModelOptions(*opts.Model)
		provider = llm.NewOpenAIProviderWithOptions(model.APIKey, model.BaseURL, model.Model, llm.OpenAIProviderOptions{
			RequestTimeout:        model.Timeout,
			DisableRequestTimeout: model.Timeout == 0,
			MaxAttempts:           model.MaxAttempts,
			ReasoningEffort:       model.ReasoningEffort,
			Protocol:              model.Protocol,
			Logf:                  c.logger.Info,
		})
	}
	key := adapter + "\x00" + sessionID + "\x00" + absWorkspace

	c.mu.Lock()
	if session := c.sessions[key]; session != nil {
		c.mu.Unlock()
		return session, nil
	}
	c.mu.Unlock()

	inner, err := agent.NewAgentSession(adapter, sessionID, agent.SessionParams{
		BaseDir:              c.stateDir,
		WorkspaceDir:         absWorkspace,
		Logger:               c.logger,
		Provider:             provider,
		ToolRegistry:         registry,
		SystemPrompt:         systemPromptWithWorkspace(c.systemPrompt, absWorkspace),
		ContextWindow:        model.ContextWindow,
		ReservedTokens:       model.ReservedTokens,
		KeepRecentTokens:     model.KeepRecentTokens,
		SummaryTokens:        model.SummaryTokens,
		InMemorySessionStore: c.inMemory,
		DisableTasks:         c.disableTasks,
		PermissionTimeout:    opts.PermissionTimeout,
	})
	if err != nil {
		return nil, err
	}
	inner.SetAgentsDirs(c.agentsDirs)
	inner.SetAllowedAgents(c.allowedAgents)
	if c.resolver != nil {
		inner.SetSubagentModelResolver(subagentModelResolverAdapter{inner: c.resolver})
	}
	session := &Session{inner: inner}

	c.mu.Lock()
	c.sessions[key] = session
	c.mu.Unlock()
	return session, nil
}

func (c *Client) Run(ctx context.Context, sessionID, prompt string) (*llm.Response, error) {
	session, err := c.NewSession(sessionID, SessionOptions{})
	if err != nil {
		return nil, err
	}
	return session.Run(ctx, prompt)
}

func (c *Client) Close() error {
	c.mu.Lock()
	sessions := make([]*Session, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.mu.Unlock()
	var closeErr error
	for _, session := range sessions {
		if err := session.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (s *Session) Run(ctx context.Context, prompt string) (*llm.Response, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return s.inner.Call(ctx, prompt)
}

func (s *Session) CallUntilStop(ctx context.Context, prompt string, stop <-chan struct{}) (*llm.Response, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return s.inner.CallUntilStop(ctx, prompt, stop)
}

// CallUntilStopWithPermissions enables adapter-neutral permission requests for
// this invocation. Resolve requests concurrently with ResolvePermission.
func (s *Session) CallUntilStopWithPermissions(ctx context.Context, prompt string, stop <-chan struct{}) (*llm.Response, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return s.inner.CallUntilStopWithPermissions(ctx, prompt, stop)
}

func (s *Session) PendingPermissions() []PermissionRequest {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.PendingPermissions()
}

func (s *Session) ResolvePermission(resolution PermissionResolution) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("session is nil")
	}
	return s.inner.ResolvePermission(resolution)
}

func (s *Session) PendingUserEvents() ([]*types.Event, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return s.inner.PendingUserEvents()
}

func (s *Session) ProcessUserEvent(ctx context.Context, e *types.Event) (*llm.Response, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return s.inner.ProcessUserEvent(ctx, e)
}

func (s *Session) OnContextEvent(eventType string, handler eventing.HandleFunc) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("session is nil")
	}
	return s.inner.OnContextEvent(eventType, handler)
}

func (s *Session) OnAuditEvent(eventType string, handler eventing.HandleFunc) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("session is nil")
	}
	return s.inner.OnAuditEvent(eventType, handler)
}

func (s *Session) SubscribeAuditEvents(eventTypes ...string) (eventing.Subscription, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return s.inner.SubscribeAuditEvents(eventTypes...)
}

func (s *Session) SubscribeContextEvents(eventTypes ...string) (eventing.Subscription, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return s.inner.SubscribeContextEvents(eventTypes...)
}

func (s *Session) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

func (s *Session) AppendAuditEvent(e *types.Event) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("session is nil")
	}
	return s.inner.AppendAuditEvent(e)
}

func (s *Session) AuditEvents() ([]*types.Event, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("session is nil")
	}
	return s.inner.AuditEvents()
}

func (s *Session) Messages() []llm.Message {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.GetContextMessages()
}

func (s *Session) GetContextMessages() []llm.Message {
	return s.Messages()
}

func (s *Session) EstimatedContextTokens() int {
	if s == nil || s.inner == nil {
		return 0
	}
	return s.inner.EstimatedContextTokens()
}

func (s *Session) ContextWindow() int {
	if s == nil || s.inner == nil {
		return 0
	}
	return s.inner.ContextWindow()
}

func (s *Session) Model() string {
	if s == nil || s.inner == nil {
		return ""
	}
	return s.inner.Model()
}

func (s *Session) AdapterType() string {
	if s == nil || s.inner == nil {
		return ""
	}
	return s.inner.AdapterType()
}

func (s *Session) ChannelID() string {
	if s == nil || s.inner == nil {
		return ""
	}
	return s.inner.ChannelID()
}

func (s *Session) WorkspaceDir() string {
	if s == nil || s.inner == nil {
		return ""
	}
	return s.inner.WorkspaceDir()
}

func (s *Session) AgentSession() *agent.AgentSession {
	if s == nil {
		return nil
	}
	return s.inner
}

var _ libruntime.Session = (*Session)(nil)

func hasModelOptions(opts ModelOptions) bool {
	return strings.TrimSpace(opts.BaseURL) != "" ||
		strings.TrimSpace(opts.APIKey) != "" ||
		strings.TrimSpace(opts.Model) != "" ||
		strings.TrimSpace(opts.ReasoningEffort) != "" ||
		opts.ContextWindow != 0 ||
		opts.ReservedTokens != 0 ||
		opts.KeepRecentTokens != 0 ||
		opts.SummaryTokens != 0 ||
		opts.Timeout != 0 ||
		opts.MaxAttempts != 0
}

func defaultModelOptions(opts ModelOptions) ModelOptions {
	if strings.TrimSpace(opts.APIKey) == "" {
		opts.APIKey = "noset"
	}
	if strings.TrimSpace(opts.Model) == "" {
		opts.Model = "gpt-5"
	}
	if opts.ContextWindow <= 0 {
		opts.ContextWindow = 200000
	}
	if opts.ReservedTokens <= 0 {
		opts.ReservedTokens = maxInt(8192, int(float64(opts.ContextWindow)*0.08))
	}
	if opts.KeepRecentTokens <= 0 {
		opts.KeepRecentTokens = maxInt(8192, int(float64(opts.ContextWindow)*0.15))
	}
	if opts.SummaryTokens <= 0 {
		opts.SummaryTokens = defaultSummaryTokens(opts.KeepRecentTokens)
	}
	return opts
}

func defaultSummaryTokens(keepRecentTokens int) int {
	target := keepRecentTokens / 2
	if target < 2000 {
		return 2000
	}
	if target > 6000 {
		return 6000
	}
	return target
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type subagentModelResolverAdapter struct {
	inner SubagentModelResolver
}

func (r subagentModelResolverAdapter) ResolveSubagentModel(ctx context.Context, request agent.SubagentModelRequest) (agent.SubagentModelResolution, error) {
	resolution, err := r.inner.ResolveSubagentModel(ctx, SubagentModelRequest{
		Name:            request.Name,
		WorkspaceDir:    request.WorkspaceDir,
		Ref:             request.Ref,
		ReasoningEffort: request.ReasoningEffort,
		ValidateOnly:    request.ValidateOnly,
	})
	if err != nil {
		return agent.SubagentModelResolution{}, err
	}
	return agent.SubagentModelResolution{
		Ref:              resolution.Ref,
		Model:            resolution.Model,
		Provider:         resolution.Provider,
		ContextWindow:    resolution.ContextWindow,
		ReservedTokens:   resolution.ReservedTokens,
		KeepRecentTokens: resolution.KeepRecentTokens,
		SummaryTokens:    resolution.SummaryTokens,
		BaseURL:          resolution.BaseURL,
		APIKey:           resolution.APIKey,
	}, nil
}

func systemPromptWithWorkspace(systemPrompt, workspaceDir string) string {
	if strings.TrimSpace(workspaceDir) == "" {
		return systemPrompt
	}
	return strings.TrimSpace(systemPrompt) + "\n\nWorkspace: " + workspaceDir
}

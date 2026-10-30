package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ctxstore "github.com/makkalot/caretz/internal/context"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/turnscope"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/eventstore"
	"github.com/makkalot/caretz/lib/llm"
	libruntime "github.com/makkalot/caretz/lib/runtime"
	"github.com/makkalot/caretz/lib/types"
)

type AgentSession struct {
	*AgentLoop

	adapterType           string
	channelId             string
	baseDir               string
	workspaceDir          string
	agentsDirs            []string
	agentsMu              sync.Mutex
	agentPaths            map[string]string
	allowedAgents         map[string]bool
	inMemorySessionStore  bool
	disableContextManager bool

	eventSessionStore       *ctxstore.EventSessionStore
	subagentModelResolver   SubagentModelResolver
	executionMu             executionLock
	lifecycleCtx            context.Context
	lifecycleCancel         context.CancelFunc
	asyncRequests           eventing.Subscription
	asyncWG                 sync.WaitGroup
	asyncStartOnce          sync.Once
	asyncStartErr           error
	asyncOwnedMu            sync.Mutex
	asyncOwned              map[string]*ownedAsyncRun
	integrationMu           sync.Mutex
	integrations            map[string]sessiontools.Integration
	integrationFactories    []sessiontools.Factory
	closeOnce               sync.Once
	permissionMu            sync.Mutex
	pendingPermissions      map[string]*pendingPermission
	permissionResolutions   map[string]eventing.PermissionResolution
	sessionPermissionGrants []sessionPermissionGrant
	permissionTimeout       time.Duration
}

type SubagentModelResolution struct {
	Ref              string
	Model            string
	Provider         llm.Provider
	ContextWindow    int
	ReservedTokens   int
	KeepRecentTokens int
	SummaryTokens    int
	// BaseURL and APIKey let tools that call the model directly, such as
	// LocalImage, reach the same endpoint as Provider.
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

var ErrGracefulStop = libruntime.ErrGracefulStop

const (
	maxConsecutiveEmptyLLMResponses = 3
	maxContextSizeRecoveries        = 1
	contextSizeRetryHeadroomTokens  = 1024
	callSubagentToolName            = "CallSubagent"
)

type ChildSessionOptions struct {
	Prefix       string
	SystemPrompt string
	ToolRegistry *tools.Registry
	// IsolatedExecution gives the child its own session shell instead of the
	// parent's, so a stricter sandbox policy on ToolRegistry applies to Bash.
	IsolatedExecution bool
}

type SessionParams struct {
	BaseDir               string
	WorkspaceDir          string
	Logger                *log.Logger
	Provider              llm.Provider
	ToolRegistry          *tools.Registry
	SystemPrompt          string
	ContextManager        ctxstore.Manager
	DisableContextManager bool
	ContextWindow         int
	ReservedTokens        int
	KeepRecentTokens      int
	SummaryTokens         int
	InMemorySessionStore  bool
	DisableTasks          bool
	// PermissionTimeout denies an unanswered permission request after this
	// long. Zero waits until the user answers or the turn is cancelled.
	PermissionTimeout time.Duration
}

func NewAgentSession(adapterType, channelId string, params SessionParams) (*AgentSession, error) {
	params, err := normalizeSessionParams(params)
	if err != nil {
		return nil, err
	}
	return newAgentSessionWithContextManager(adapterType, channelId, params)
}

func normalizeSessionParams(params SessionParams) (SessionParams, error) {
	if strings.TrimSpace(params.BaseDir) == "" && !params.InMemorySessionStore {
		return SessionParams{}, fmt.Errorf("base dir is required")
	}
	if params.Provider == nil {
		return SessionParams{}, fmt.Errorf("llm provider is required")
	}
	if params.ContextManager != nil && params.DisableContextManager {
		return SessionParams{}, fmt.Errorf("context manager and DisableContextManager cannot both be set")
	}
	if strings.TrimSpace(params.WorkspaceDir) == "" {
		if params.InMemorySessionStore {
			workspaceDir, err := os.Getwd()
			if err != nil {
				return SessionParams{}, fmt.Errorf("failed to get current working directory: %w", err)
			}
			params.WorkspaceDir = workspaceDir
		} else {
			params.WorkspaceDir = filepath.Dir(filepath.Dir(params.BaseDir))
		}
	}
	if params.Logger == nil {
		params.Logger = log.New()
	}
	if params.PermissionTimeout < 0 {
		params.PermissionTimeout = 0
	}
	if params.ToolRegistry == nil {
		params.ToolRegistry = &tools.Registry{Logger: params.Logger}
	}
	if params.ContextWindow <= 0 {
		params.ContextWindow = 200000
	}
	if params.ReservedTokens <= 0 {
		params.ReservedTokens = maxInt(8192, int(float64(params.ContextWindow)*0.08))
	}
	if params.KeepRecentTokens <= 0 {
		params.KeepRecentTokens = maxInt(8192, int(float64(params.ContextWindow)*0.15))
	}
	if params.SummaryTokens <= 0 {
		params.SummaryTokens = defaultSessionSummaryTokens(params.KeepRecentTokens)
	}
	return params, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func defaultSessionSummaryTokens(keepRecentTokens int) int {
	target := keepRecentTokens / 2
	if target < 2000 {
		return 2000
	}
	if target > 6000 {
		return 6000
	}
	return target
}

func newAgentSessionWithContextManager(adapterType, channelId string, params SessionParams) (*AgentSession, error) {
	baseDir := params.BaseDir
	workspaceDir := params.WorkspaceDir
	logger := params.Logger
	llmProvider := params.Provider
	toolRegistry := params.ToolRegistry
	toolRegistry, ownsSessionExecution := toolRegistry.BindSessionExecution()
	contextWindow := params.ContextWindow
	reservedTokens := params.ReservedTokens
	keepRecentTokens := params.KeepRecentTokens
	summaryTokens := params.SummaryTokens
	systemPrompt := params.SystemPrompt

	disableTasks := params.DisableTasks
	if !disableTasks && toolRegistry != nil && (!toolRegistry.IsToolEnabled(updateTasksToolName) || !toolRegistry.IsToolEnabled(listTasksToolName)) {
		disableTasks = true
	}

	if !disableTasks {
		systemPrompt = systemPromptWithTaskGuidance(systemPrompt)
	}
	var eventsLog eventstore.Store
	var contextStore eventstore.Store
	if params.InMemorySessionStore {
		eventsLog = eventstore.NewInMemoryStore()
		contextStore = eventstore.NewInMemoryStore()
	} else {
		finalPath := filepath.Join(baseDir, adapterType, channelId)
		if _, err := os.Stat(finalPath); os.IsNotExist(err) {
			if err := os.MkdirAll(finalPath, 0755); err != nil {
				return nil, err
			}
		}

		eventsPath := filepath.Join(finalPath, "events.jsonpl")
		fileEventsLog, err := eventstore.NewFileMemoryStore(eventsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create events log store: %w", err)
		}
		eventsLog = fileEventsLog

		contextStorePath := filepath.Join(finalPath, "context.jsonpl")
		fileContextStore, err := eventstore.NewFileMemoryStore(contextStorePath)
		if err != nil {
			return nil, fmt.Errorf("failed to create context store: %w", err)
		}
		contextStore = fileContextStore
	}

	if eventsLog == nil {
		return nil, fmt.Errorf("failed to create events log store")
	}
	if contextStore == nil {
		return nil, fmt.Errorf("failed to create context store")
	}

	eventSessionStore, err := ctxstore.NewEventSessionStore(adapterType, channelId, baseDir, eventsLog)
	if err != nil {
		return nil, fmt.Errorf("failed to create event session store: %w", err)
	}

	eventLoop := eventing.NewSyncEventLoop(context.Background())

	contextSessionStore, err := ctxstore.NewContextSessionStore(channelId, eventSessionStore, contextStore, eventLoop)
	if err != nil {
		return nil, fmt.Errorf("failed to create context session store: %w", err)
	}

	llmToolDefinitions := toolRegistry.ToolDefinitions()

	contextManager := params.ContextManager
	if contextManager == nil {
		if params.DisableContextManager {
			contextManager = ctxstore.NewDisabledManager(contextSessionStore, contextWindow, reservedTokens, keepRecentTokens, systemPrompt, summaryTokens)
		} else {
			contextManager = ctxstore.NewCompactionManager(contextSessionStore, contextWindow, reservedTokens, keepRecentTokens, systemPrompt, summaryTokens)
		}
	}
	if debugManager, ok := contextManager.(interface {
		SetDebugLogger(func(format string, args ...interface{}))
	}); ok {
		debugManager.SetDebugLogger(logger.Debug)
	}

	// Restore llmMessages using the context manager so implementations can
	// decide whether summaries, trimming, or raw history should be replayed.
	logger.Debug("Restoring context: adapter=%s session=%s context_window=%d reserved_tokens=%d keep_recent_tokens=%d summary_tokens=%d", adapterType, channelId, contextWindow, reservedTokens, keepRecentTokens, contextManager.SummaryTokens())
	llmMessages, err := contextManager.RestoreContext(channelId)
	if err != nil {
		return nil, fmt.Errorf("failed to restore context: %w", err)
	}
	taskState, mutationsSinceSnapshot, err := contextSessionStore.RestoreTaskState(channelId)
	if err != nil {
		return nil, fmt.Errorf("failed to restore task state: %w", err)
	}
	if len(llmMessages) > 0 {
		logger.Info("context restored %d messages, ~%d tokens", len(llmMessages), contextManager.EstimateRawTokens(llmMessages, llmToolDefinitions))
	}

	loop := NewAgentLoop(AgentLoopParams{
		Logger:                     logger,
		Provider:                   llmProvider,
		ToolRegistry:               toolRegistry,
		SystemPrompt:               systemPrompt,
		ContextManager:             contextManager,
		ContextSessionStore:        contextSessionStore,
		Messages:                   llmMessages,
		ToolDefinitions:            llmToolDefinitions,
		TaskState:                  taskState,
		TaskMutationsSinceSnapshot: mutationsSinceSnapshot,
		DisableTasks:               disableTasks,
	})
	loop.ownsSessionExecution = ownsSessionExecution

	session := &AgentSession{
		AgentLoop: loop,

		adapterType:           adapterType,
		channelId:             channelId,
		baseDir:               baseDir,
		workspaceDir:          workspaceDir,
		agentPaths:            subagents.Snapshot([]string{subagents.AgentsDir(workspaceDir)}),
		inMemorySessionStore:  params.InMemorySessionStore,
		disableContextManager: params.DisableContextManager,

		eventSessionStore:     eventSessionStore,
		integrations:          map[string]sessiontools.Integration{},
		pendingPermissions:    map[string]*pendingPermission{},
		permissionResolutions: map[string]eventing.PermissionResolution{},
		permissionTimeout:     params.PermissionTimeout,
	}
	session.lifecycleCtx, session.lifecycleCancel = context.WithCancel(context.Background())
	if err := session.restorePermissionResolutions(); err != nil {
		return nil, err
	}
	sessionToolRegistry, err := toolRegistry.WithSessionTool(callSubagentTool(session.handleCallSubagentToolCall))
	if err != nil {
		return nil, err
	}
	if err := loop.SwitchRuntime(sessionToolRegistry, loop.systemPrompt); err != nil {
		return nil, err
	}
	return session, nil
}

func (a *AgentSession) Call(ctx context.Context, prompt string) (*llm.Response, error) {
	return a.CallUntilStop(ctx, prompt, nil)
}

// CallUserEvent accepts a new adapter user event only after acquiring the
// session. accepted runs after the audit append, before model execution.
func (a *AgentSession) CallUserEvent(ctx context.Context, e *types.Event, accepted func()) (*llm.Response, error) {
	if err := a.executionMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer a.executionMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil || e.EventType != eventing.LogMessageUserCreated {
		return nil, fmt.Errorf("expected %s event", eventing.LogMessageUserCreated)
	}
	if err := a.AppendAuditEvent(e); err != nil {
		return nil, err
	}
	if accepted != nil {
		accepted()
	}
	return a.processUserEventUntilStop(ctx, e, nil)
}

func (a *AgentSession) CallUntilStop(ctx context.Context, prompt string, stop <-chan struct{}) (*llm.Response, error) {
	if err := a.executionMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer a.executionMu.Unlock()
	return a.callUntilStopUnlocked(ctx, prompt, stop)
}

// CallUntilStopWithPermissions runs one foreground turn that may pause for a
// decision through PendingPermissions and ResolvePermission.
func (a *AgentSession) CallUntilStopWithPermissions(ctx context.Context, prompt string, stop <-chan struct{}) (*llm.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = tools.WithPermissionRequester(ctx, a.requestPermission)
	ctx = tools.WithSessionPermissions(ctx, a.hasSessionPermissionGrant)
	if err := a.executionMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer a.executionMu.Unlock()
	return a.callUntilStopUnlocked(ctx, prompt, stop)
}

func (a *AgentSession) callUntilStopUnlocked(ctx context.Context, prompt string, stop <-chan struct{}) (*llm.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e := eventing.NewLogMessage(a.channelId, prompt)

	if err := a.eventSessionStore.Append(e); err != nil {
		return nil, fmt.Errorf("failed to append event to event session store: %w", err)
	}
	return a.processUserEventUntilStop(ctx, e, stop)
}

// ProcessUserEvent runs the agent for an already durable generic user event.
// Adapters use this entry point so retries never append a second user message.
func (a *AgentSession) ProcessUserEvent(ctx context.Context, e *types.Event) (*llm.Response, error) {
	return a.ProcessUserEventUntilStop(ctx, e, nil)
}

func (a *AgentSession) ProcessUserEventUntilStop(ctx context.Context, e *types.Event, stop <-chan struct{}) (*llm.Response, error) {
	if err := a.executionMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer a.executionMu.Unlock()
	return a.processUserEventUntilStop(ctx, e, stop)
}

func (a *AgentSession) processUserEventUntilStop(ctx context.Context, e *types.Event, stop <-chan struct{}) (response *llm.Response, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = llm.WithSessionID(ctx, a.llmSessionID())
	if e == nil || e.EventType != eventing.LogMessageUserCreated {
		return nil, fmt.Errorf("expected %s event", eventing.LogMessageUserCreated)
	}
	existing, err := a.eventSessionStore.GetLatestAgentResponseForUserMessage(e)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing response: %w", err)
	}
	retryingInterruptedTurn := existing != nil && existing.EventType == eventing.LogMessageAgentInterrupted
	if existing != nil && !retryingInterruptedTurn {
		return nil, nil
	}
	userMessage, err := eventing.DecodeLogMessage(e)
	if err != nil {
		return nil, fmt.Errorf("failed to decode user message: %w", err)
	}
	turnID := uuid.NewV4().String()
	if err := a.AppendAuditEvent(eventing.NewAgentTurnStarted(a.channelId, turnID, userMessage.MessageID)); err != nil {
		return nil, fmt.Errorf("failed to append agent turn started event: %w", err)
	}
	defer func() {
		recovered := recover()
		status := agentTurnStatus(returnErr, ctx)
		errText := ""
		if returnErr != nil {
			errText = returnErr.Error()
		}
		if recovered != nil {
			status = eventing.AgentTurnPanicked
			errText = fmt.Sprint(recovered)
		}
		if err := a.AppendAuditEvent(eventing.NewAgentTurnCompleted(a.channelId, turnID, userMessage.MessageID, status, errText)); err != nil {
			if returnErr == nil && recovered == nil {
				returnErr = fmt.Errorf("failed to append agent turn completed event: %w", err)
			} else {
				a.logger.Warn("Failed to append agent turn completed event: %v", err)
			}
		}
		if recovered != nil {
			panic(recovered)
		}
	}()
	newEvent, converted, err := a.convertToContextEvent(e)
	if err != nil {
		return nil, fmt.Errorf("failed to convert event to context event: %w", err)
	}

	if !converted {
		a.logger.Debug("event is not a user message, skipping context conversion: %v", e)
		return nil, nil
	}

	processed, err := a.IsEventProcessed(newEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to check if event is processed: %w", err)
	}
	inContext, err := a.IsUserEventInContext(newEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to check if event is in context: %w", err)
	}

	// we need to call the llm with the new message
	newLLMmessage, err := a.convertEventToLLMMessage(newEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to convert event to llm message: %w", err)
	}
	parentScope := turnscope.From(ctx)
	scope := &turnscope.Scope{}
	if parentScope != nil {
		parentScope.Register(func() (string, error) { notes, err := scope.Stop(); return strings.Join(notes, "\n"), err })
	}
	ctx = turnscope.With(ctx, scope)
	userPersisted := inContext && !retryingInterruptedTurn
	defer func() {
		if returnErr == nil {
			return
		}
		if ctx.Err() != nil {
			notes, cleanupErr := scope.Stop()
			returnErr = errors.Join(returnErr, cleanupErr)
			if !userPersisted {
				if err := a.AppendUserMessage(newEvent); err != nil {
					returnErr = errors.Join(returnErr, err)
					return
				}
				a.appendActiveMessage(*newLLMmessage)
			}
			if err := a.finalizeInterruptedTurnWithNotes(newEvent.Originator.ID, e.Originator.ID, e.Originator.Version, userMessage.MessageID, classifyRecoveryReason(returnErr), notes); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	preflightCompacted := false
	if a.contextManager != nil {
		pendingMessages := append(a.activeMessages(), *newLLMmessage)
		estimatedTokens := a.contextManager.EstimateTokens(pendingMessages, a.llmToolDefinitions)
		a.logger.Debug("Preflight context estimate: %d tokens (threshold: %d, multiplier: %.2f)",
			estimatedTokens, a.contextManager.ContextWindow()-a.contextManager.ReservedTokens(), a.contextManager.TokenEstimateMultiplier())
		if a.contextManager.IsCompactionRequired(pendingMessages, a.llmToolDefinitions) {
			result, err := a.compactContext(ctx, newEvent.Originator.ID, e.Originator.ID, e.Originator.Version, "preflight", newLLMmessage.Content)
			if err != nil {
				return nil, err
			}
			preflightCompacted = result != nil && result.Compacted
		}
	}

	if retryingInterruptedTurn {
		// The previous attempt was repaired with a terminal interruption so the
		// stored context remains valid. Persist the repeated user turn as well;
		// this keeps a later successful response valid after session restore.
		if err := a.AppendUserMessage(newEvent); err != nil {
			return nil, fmt.Errorf("failed to append retry user message to context session store: %w", err)
		}
	} else if !processed && !inContext {
		if err := a.AppendUserMessage(newEvent); err != nil {
			return nil, fmt.Errorf("failed to append user message to context session store: %w", err)
		}
	} else if len(a.llmMessages) > 0 {
		// After restart RestoreContext already includes this pending user message.
		// runLLmLoop appends newLLMmessage, so remove the restored copy first.
		if inContext && !processed {
			if preflightCompacted {
				a.removeActiveMessage(*newLLMmessage)
			} else {
				a.rollbackActiveMessagesBefore(*newLLMmessage)
			}
		} else {
			last := a.llmMessages[len(a.llmMessages)-1]
			if last.Role == newLLMmessage.Role && last.Content == newLLMmessage.Content {
				a.llmMessages = a.llmMessages[:len(a.llmMessages)-1]
			}
		}
	}
	userPersisted = true

	toolCtx := tools.WithSendFileMessageID(ctx, userMessage.MessageID)
	toolCtx = tools.WithActorMetadata(toolCtx, tools.ActorMetadata{
		Adapter:   userMessage.Adapter,
		ChannelID: userMessage.ChannelID,
		UserID:    userMessage.UserID,
		MessageID: userMessage.MessageID,
	})
	if a.subagentModelResolver != nil {
		toolCtx = tools.WithLocalImageModelResolver(toolCtx, a.resolveLocalImageModel)
	}
	resp, err := a.Run(toolCtx, AgentTurn{
		OriginatorID:            newEvent.Originator.ID,
		ParentOriginatorID:      e.Originator.ID,
		ParentOriginatorVersion: e.Originator.Version,
		Message:                 *newLLMmessage,
		Stop:                    stop,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		if recoveryErr := a.finalizeInterruptedTurn(newEvent.Originator.ID, e.Originator.ID, e.Originator.Version, userMessage.MessageID, classifyRecoveryReason(err)); recoveryErr != nil {
			return nil, fmt.Errorf("failed to run llm loop: %w; additionally failed to persist interruption recovery: %v", err, recoveryErr)
		}
		return nil, fmt.Errorf("failed to run llm loop: %w", err)
	}
	agentEvent, err := a.convertLLMMessageToEventsResponse(e.Originator.ID, userMessage.MessageID, resp.Message)
	if err != nil {
		return nil, fmt.Errorf("failed to convert llm message to event: %w", err)
	}

	if err := a.eventSessionStore.Append(agentEvent); err != nil {
		return nil, fmt.Errorf("failed to append agent event to event session store: %w", err)
	}

	if ctx.Err() == nil && a.contextManager != nil && a.contextManager.IsCompactionRequired(a.activeMessages(), a.llmToolDefinitions) {
		if _, err := a.compactContext(ctx, newEvent.Originator.ID, e.Originator.ID, e.Originator.Version, "post-response", newLLMmessage.Content); err != nil {
			if ctx.Err() != nil {
				return resp, nil
			}
			return nil, err
		}
	}

	return resp, nil
}

func agentTurnStatus(err error, ctx context.Context) string {
	if err == nil {
		return eventing.AgentTurnSucceeded
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return eventing.AgentTurnTimedOut
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return eventing.AgentTurnCancelled
	}
	return eventing.AgentTurnFailed
}

func (a *AgentLoop) SwitchModel(provider llm.Provider, contextWindow, reservedTokens, keepRecentTokens int, summaryTokens ...int) error {
	if provider == nil {
		return fmt.Errorf("llm provider is required")
	}
	if a == nil {
		return fmt.Errorf("agent loop is required")
	}
	a.llmProviderMu.Lock()
	a.llmProvider = provider
	a.llmProviderMu.Unlock()
	if a.contextManager != nil {
		summaryTokenLimit := 0
		if len(summaryTokens) > 0 {
			summaryTokenLimit = summaryTokens[0]
		}
		a.contextManager.SetLimits(contextWindow, reservedTokens, keepRecentTokens, summaryTokenLimit)
	}
	return nil
}

func (a *AgentLoop) SwitchRuntime(toolRegistry *tools.Registry, systemPrompt string) error {
	if toolRegistry == nil {
		return fmt.Errorf("tool registry is required")
	}
	if a == nil {
		return fmt.Errorf("agent loop is required")
	}
	// Background commands read the registry outside the execution lock.
	a.toolRegistryMu.Lock()
	a.toolRegistry = toolRegistry
	a.toolRegistryMu.Unlock()
	a.llmToolDefinitions = toolRegistry.ToolDefinitions()
	a.systemPrompt = systemPrompt
	return nil
}

// SwitchSessionRuntime replaces a top-level session's tools and creates a new
// execution supervisor. ACP uses this when filesystem policy changes.
func (a *AgentSession) SwitchSessionRuntime(toolRegistry *tools.Registry, systemPrompt string) error {
	if a == nil {
		return fmt.Errorf("agent session is required")
	}
	if toolRegistry == nil {
		return fmt.Errorf("tool registry is required")
	}
	if a.ownsSessionExecution && a.toolRegistry != nil {
		_ = a.toolRegistry.CloseSessionExecution()
	}
	bound, owned := toolRegistry.ForNewTopLevelSession().BindSessionExecution()
	bound = bound.WithSessionToolsFrom(a.toolRegistry)
	a.ownsSessionExecution = owned
	return a.SwitchRuntime(bound, systemPrompt)
}

func (a *AgentSession) NewChildSession(prefix string) (*AgentSession, error) {
	return a.NewChildSessionWithOptions(ChildSessionOptions{Prefix: prefix})
}

func (a *AgentSession) NewSiblingSession(channelID string) (*AgentSession, error) {
	if channelID == "" {
		return nil, fmt.Errorf("channel id is required")
	}
	baseRegistry := a.toolRegistry.WithoutSessionTools().ForNewTopLevelSession()
	sibling, err := NewAgentSession(a.adapterType, channelID, SessionParams{
		BaseDir:               a.baseDir,
		WorkspaceDir:          a.workspaceDir,
		Logger:                a.logger,
		Provider:              a.currentProvider(),
		ToolRegistry:          baseRegistry,
		SystemPrompt:          a.systemPrompt,
		ContextWindow:         a.ContextWindow(),
		ReservedTokens:        a.ReservedTokens(),
		KeepRecentTokens:      a.KeepRecentTokens(),
		SummaryTokens:         a.SummaryTokens(),
		InMemorySessionStore:  a.inMemorySessionStore,
		DisableContextManager: a.disableContextManager,
		DisableTasks:          a.disableTasks,
		PermissionTimeout:     a.permissionTimeout,
	})
	if err != nil {
		return nil, err
	}
	sibling.inheritAgents(a)
	sibling.SetAllowedAgents(a.AllowedAgents())
	sibling.SetSubagentModelResolver(a.subagentModelResolver)
	sibling.SetLoopGuard(a.LoopGuard())
	a.integrationMu.Lock()
	factories := append([]sessiontools.Factory(nil), a.integrationFactories...)
	a.integrationMu.Unlock()
	for _, factory := range factories {
		if err := sibling.InstallIntegration(factory); err != nil {
			_ = sibling.Close()
			return nil, err
		}
	}
	return sibling, nil
}

func (a *AgentSession) NewChildSessionWithOptions(opts ChildSessionOptions) (*AgentSession, error) {
	prefix := opts.Prefix
	if prefix == "" {
		prefix = "child"
	}
	channelID := fmt.Sprintf("%s-%s-%d", a.channelId, prefix, time.Now().UnixNano())
	systemPrompt := opts.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = a.systemPrompt
	}
	toolRegistry := opts.ToolRegistry
	if toolRegistry == nil {
		toolRegistry = a.toolRegistry.WithoutSessionTools()
	} else if opts.IsolatedExecution {
		toolRegistry = toolRegistry.WithoutSessionTools().ForNewTopLevelSession()
	} else {
		toolRegistry = toolRegistry.WithoutSessionTools().ShareSessionExecutionFrom(a.toolRegistry)
	}
	child, err := NewAgentSession(a.adapterType, channelID, SessionParams{
		BaseDir:               a.baseDir,
		WorkspaceDir:          a.workspaceDir,
		Logger:                a.logger,
		Provider:              a.currentProvider(),
		ToolRegistry:          toolRegistry,
		SystemPrompt:          systemPrompt,
		ContextWindow:         a.ContextWindow(),
		ReservedTokens:        a.ReservedTokens(),
		KeepRecentTokens:      a.KeepRecentTokens(),
		SummaryTokens:         a.SummaryTokens(),
		InMemorySessionStore:  a.inMemorySessionStore,
		DisableContextManager: a.disableContextManager,
		DisableTasks:          a.disableTasks,
		PermissionTimeout:     a.permissionTimeout,
	})
	if err != nil {
		return nil, err
	}
	child.inheritAgents(a)
	child.SetAllowedAgents(a.AllowedAgents())
	child.SetSubagentModelResolver(a.subagentModelResolver)
	child.SetLoopGuard(a.LoopGuard())
	return child, nil
}

func (a *AgentLoop) run(ctx context.Context, turn AgentTurn) (*llm.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	originatorID := turn.OriginatorID
	parentOriginatorID := turn.ParentOriginatorID
	parentOriginatorVersion := turn.ParentOriginatorVersion
	newMessage := turn.Message
	summary := turn.Summary
	stop := turn.Stop
	provider := a.currentProvider()
	a.logModel(provider)
	// Schedules, subagents and the main session can run at once, so every
	// -v line from this loop names the model it belongs to.
	modelTag := "[" + provider.GetModel() + "]"
	requestKind := modelTag + " llm"
	if summary {
		requestKind = modelTag + " summary"
	}

	var cumulativePrompt, cumulativeCompletion, cumulativeTotal int64

	// we don't need to save this one since it's already saved pre calling
	var messages []llm.Message
	if summary {
		messages = []llm.Message{newMessage}
	} else {
		messages = a.activeMessages()
		messages = append(messages, newMessage)
		a.setActiveMessages(messages)
	}
	consecutiveEmptyResponses := 0
	contextSizeRecoveries := 0
	loopGuard := a.newTurnLoopGuard(newMessage.Content, summary)
	for iteration := 1; ; iteration++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if stopRequested(stop) {
			return nil, ErrGracefulStop
		}

		var toolDefs []llm.ToolDefinition
		var systemPrompt string
		if !summary {
			toolDefs = a.llmToolDefinitions
			systemPrompt = a.systemPrompt
			if iteration > 1 && a.contextManager != nil && a.contextManager.IsCompactionRequired(messages, toolDefs) {
				result, err := a.compactContext(ctx, originatorID, parentOriginatorID, parentOriginatorVersion, "mid-loop", newMessage.Content)
				if err != nil {
					return nil, err
				}
				if result != nil && result.Compacted {
					messages = a.activeMessages()
					a.setActiveMessages(messages)
					a.logger.Debug("Iteration %d: compacted context before LLM request: before=%d after=%d",
						iteration, result.BeforeTokens, result.AfterTokens)
				} else {
					estimatedTokens := a.contextManager.EstimateTokens(messages, toolDefs)
					threshold := a.contextManager.ContextWindow() - a.contextManager.ReservedTokens()
					if estimatedTokens > threshold {
						a.logger.Info("Iteration %d: compaction skipped but request remains above threshold: calibrated=%d threshold=%d; no older complete turns are available to summarize",
							iteration, estimatedTokens, threshold)
						activeResult, err := a.compactActiveToolChainContext(ctx, originatorID, parentOriginatorID, parentOriginatorVersion, "active-tool-chain-mid-loop", newMessage.Content)
						if err != nil {
							return nil, err
						}
						if activeResult != nil && activeResult.Compacted {
							messages = a.activeMessages()
							a.setActiveMessages(messages)
							a.logger.Debug("Iteration %d: active tool-chain compacted context before LLM request: before=%d after=%d",
								iteration, activeResult.BeforeTokens, activeResult.AfterTokens)
						}
					}
				}
			}
		}

		requestEstimate := 0
		if a.contextManager != nil && !summary {
			requestEstimate = a.contextManager.EstimateRawTokens(messages, toolDefs)
			a.logger.Info("%s #%d → ~%d tokens msgs=%d", requestKind, iteration, a.contextManager.EstimateTokens(messages, toolDefs), len(messages))
			a.logger.Debug("Iteration %d: raw request tokens=%d multiplier=%.2f",
				iteration, requestEstimate, a.contextManager.TokenEstimateMultiplier())
		} else {
			a.logger.Info("%s #%d → msgs=%d", requestKind, iteration, len(messages))
		}
		requestStart := time.Now()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		requestCtx := llm.WithRetryHandler(ctx, func(info llm.RetryInfo) {
			if !summary {
				a.publishAssistantDelta("", true)
			}
			a.publishLLMRetry(originatorID, parentOriginatorID, parentOriginatorVersion, info)
		})
		requestCtx = llm.WithReasoningHandler(requestCtx, func(update llm.ReasoningUpdate) {
			a.publishLLMReasoning(originatorID, parentOriginatorID, parentOriginatorVersion, update)
		})
		if !summary {
			a.publishLLMReasoningStarted(originatorID, parentOriginatorID, parentOriginatorVersion)
		}
		result, err := llm.ChatContext(requestCtx, provider, systemPrompt, messages, toolDefs, func(delta string) {
			if !summary {
				a.publishAssistantDelta(delta, false)
			}
		})
		if !summary {
			a.publishLLMReasoning(originatorID, parentOriginatorID, parentOriginatorVersion, llm.ReasoningUpdate{Done: true})
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			a.logger.Info("%s #%d ← %s error: %v", requestKind, iteration, formatDuration(time.Since(requestStart)), err)
			if !summary {
				a.publishAssistantDelta("", true)
			}
			if !summary && a.contextManager != nil && contextSizeRecoveries < maxContextSizeRecoveries {
				contextErr, ok := llm.ParseContextSizeError(err)
				if ok && contextErr.ContextSize > 0 {
					contextSizeRecoveries++
					if requestEstimate > 0 && contextErr.PromptTokens > 0 {
						a.contextManager.UpdateTokenCalibration(requestEstimate, int64(contextErr.PromptTokens))
					}
					a.logger.Info("Iteration %d: provider rejected request for context size; compacting and retrying once: prompt_tokens=%d provider_context=%d configured_context=%d",
						iteration, contextErr.PromptTokens, contextErr.ContextSize, a.contextManager.ContextWindow())
					result, compactErr := a.compactContext(ctx, originatorID, parentOriginatorID, parentOriginatorVersion, "context-size-retry", newMessage.Content)
					if compactErr != nil {
						return nil, compactErr
					}
					compacted := result != nil && result.Compacted
					if !compacted {
						activeResult, activeErr := a.compactActiveToolChainContext(ctx, originatorID, parentOriginatorID, parentOriginatorVersion, "active-tool-chain-context-size-retry", newMessage.Content)
						if activeErr != nil {
							return nil, activeErr
						}
						compacted = activeResult != nil && activeResult.Compacted
					}
					if compacted {
						a.removeActiveMessage(newMessage)
						messages = a.activeMessages()
						messages = append(messages, newMessage)
						a.setActiveMessages(messages)
					}

					trimPromptTokens := contextErr.PromptTokens
					if compacted {
						// The provider token count describes the rejected pre-compaction
						// request. After rebuilding the context, rely on the newly
						// calibrated estimate instead of forcing an unnecessary trim.
						trimPromptTokens = 0
					}
					trimmed, trimmedOK := a.trimActiveMessagesForContextSizeRetry(messages, toolDefs, contextErr.ContextSize, trimPromptTokens)
					if !trimmedOK {
						return nil, err
					}
					beforeTokens := a.contextManager.EstimateTokens(messages, toolDefs)
					afterTokens := a.contextManager.EstimateTokens(trimmed, toolDefs)
					if afterTokens < beforeTokens {
						a.logger.Info("Iteration %d: emergency-trimmed active context for context-size retry: before=%d after=%d provider_context=%d",
							iteration, beforeTokens, afterTokens, contextErr.ContextSize)
					}
					messages = trimmed
					a.setActiveMessages(messages)
					continue
				}
			}
			if !summary && a.contextManager != nil {
				if contextErr, ok := llm.ParseContextSizeError(err); ok && contextErr.ContextSize > 0 {
					a.logger.Info("Iteration %d: provider rejected request for context size but recovery limit is exhausted: prompt_tokens=%d provider_context=%d recoveries=%d",
						iteration, contextErr.PromptTokens, contextErr.ContextSize, contextSizeRecoveries)
				}
			}
			return nil, err
		}
		contextSizeRecoveries = 0
		result = normalizeLLMResponse(result)
		a.logger.Info("%s #%d ← %s prompt=%d completion=%d %s", requestKind, iteration, formatDuration(time.Since(requestStart)),
			result.Usage.PromptTokens, result.Usage.CompletionTokens, describeResponse(result))
		if a.contextManager != nil && !summary && requestEstimate > 0 && result.Usage.PromptTokens > 0 {
			beforeMultiplier := a.contextManager.TokenEstimateMultiplier()
			a.contextManager.UpdateTokenCalibration(requestEstimate, result.Usage.PromptTokens)
			afterMultiplier := a.contextManager.TokenEstimateMultiplier()
			a.logger.Debug("Iteration %d: actual prompt tokens=%d estimated=%d multiplier %.2f -> %.2f",
				iteration, result.Usage.PromptTokens, requestEstimate, beforeMultiplier, afterMultiplier)
		}

		cumulativePrompt += result.Usage.PromptTokens
		cumulativeCompletion += result.Usage.CompletionTokens
		cumulativeTotal += result.Usage.TotalTokens

		if isEmptyLLMResponse(result) {
			consecutiveEmptyResponses++
			if consecutiveEmptyResponses == 1 && a.contextManager != nil && !summary && a.contextManager.IsCompactionRequired(messages, toolDefs) {
				activeResult, activeErr := a.compactActiveToolChainContext(ctx, originatorID, parentOriginatorID, parentOriginatorVersion, "active-tool-chain-empty-response", newMessage.Content)
				if activeErr != nil {
					return nil, activeErr
				}
				if activeResult != nil && activeResult.Compacted {
					messages = a.activeMessages()
					a.setActiveMessages(messages)
					consecutiveEmptyResponses = 0
					a.logger.Debug("Iteration %d: active tool-chain compacted context after empty response: before=%d after=%d",
						iteration, activeResult.BeforeTokens, activeResult.AfterTokens)
					continue
				}
			}
			if consecutiveEmptyResponses >= maxConsecutiveEmptyLLMResponses {
				return nil, fmt.Errorf("LLM returned empty response %d consecutive times", consecutiveEmptyResponses)
			}
			a.logger.Warn("Iteration %d: LLM returned empty response (no content, no tool calls), retrying", iteration)
			continue
		}
		consecutiveEmptyResponses = 0

		if err := ctx.Err(); err != nil {
			return nil, err
		}
		messages = append(messages, result.Message)

		if len(result.ToolCalls) == 0 {
			// Update llmMessages BEFORE emitting the event so that TUI handlers
			// see the latest context when calling EstimatedContextTokens().
			if !summary {
				a.setActiveMessages(messages)
			}

			// save at this point the response to the context store
			var agentResponseEvent *types.Event
			if !summary {
				agentResponseEvent = eventing.NewLLMAgentResponseWithReasoning(originatorID, parentOriginatorID, parentOriginatorVersion, result.Message, result.Usage, cumulativeTotal, result.ReasoningSummary, a.tokenEstimateMultiplier())
			} else {
				agentResponseEvent = eventing.NewLLMAgentSummaryResponse(originatorID, parentOriginatorID, parentOriginatorVersion, result.Message, result.Usage, cumulativeTotal)
			}

			_, err = a.contextSessionStore.AppendAgentResponse(agentResponseEvent)
			if err != nil {
				a.logger.Warn("Failed to append agent response to context store: %v", err)
			}

			return &result, nil
		}

		// Persist the intermediate assistant message (with tool calls)
		if !summary {
			a.setActiveMessages(messages)
		}
		agentMessageEvent := eventing.NewLLMAgentMessageWithReasoning(originatorID, parentOriginatorID, parentOriginatorVersion, result.Message, result.Usage, cumulativeTotal, result.ReasoningSummary, a.tokenEstimateMultiplier())
		if _, err := a.contextSessionStore.AppendAgentMessage(agentMessageEvent); err != nil {
			a.logger.Warn("Failed to append agent message to context store: %v", err)
		}

		for i, tc := range result.ToolCalls {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// The loop guard judges once per batch, on its last result, so a
			// redirect lands where the model reads next.
			lastInBatch := i == len(result.ToolCalls)-1
			a.logger.Debug("Iteration %d: tool call %s (id: %s) arguments: %s", iteration, tc.Name, tc.ID, tc.Arguments)
			toolStart := time.Now()

			toolCtx := ctx
			grantedPermissions := map[string]bool{}
			var toolCallRes llm.Message
			var err error
			for {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if !summary && a.toolRegistry != nil && !a.toolRegistry.IsToolEnabled(tc.Name) {
					err = fmt.Errorf("tool %s is disabled", tc.Name)
				} else if !summary && tc.Name == updateTasksToolName {
					toolCallRes, err = a.handleUpdateTasksToolCall(originatorID, parentOriginatorID, parentOriginatorVersion, tc)
				} else if !summary && tc.Name == listTasksToolName {
					toolCallRes, err = a.handleListTasksToolCall(tc)
				} else {
					toolCallRes, err = a.toolRegistry.HandleToolCallContext(toolCtx, tc)
				}
				permissionErr, approvable := tools.AsPermissionError(err)
				if !approvable || summary {
					break
				}
				grantKey := permissionErr.Capability + "\x00" + permissionErr.Target
				if grantedPermissions[grantKey] {
					err = fmt.Errorf("approved permission did not satisfy tool request: %s", permissionErr.Target)
					break
				}
				allowed, requestErr := tools.RequestPermission(ctx, tc, permissionErr)
				// A request with a fallback leaves the tool's ordinary result
				// in place when the user declines or cannot be asked, so the
				// model never learns that an approval was possible.
				if permissionErr.Fallback != nil && (!allowed && requestErr == nil || errors.Is(requestErr, tools.ErrPermissionApprovalUnavailable)) {
					toolCallRes, err = *permissionErr.Fallback, nil
					break
				}
				if requestErr != nil {
					err = requestErr
					break
				}
				if !allowed {
					err = fmt.Errorf("permission denied by user: %s", permissionErr.Target)
					break
				}
				toolCtx = tools.WithPermissionGrant(toolCtx, permissionErr.Capability, permissionErr.Target, permissionErr.Recursive)
				grantedPermissions[grantKey] = true
			}
			if err != nil {
				if ctx.Err() != nil {
					if toolCallRes.Content != "" {
						partial := eventing.NewLLMToolCallRecoveryResultWithMessage(originatorID, parentOriginatorID, parentOriginatorVersion, eventing.NewRecoveryID(), classifyRecoveryReason(ctx.Err()), tc, toolCallRes.Content)
						if _, saveErr := a.contextSessionStore.AppendToolResult(partial); saveErr != nil {
							return nil, errors.Join(ctx.Err(), saveErr)
						}
						a.appendActiveMessage(llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Content: toolCallRes.Content})
					}
					return nil, errors.Join(ctx.Err(), err)
				}
				a.logger.Info("%s tool %s error %s %s: %v", modelTag, tc.Name, formatDuration(time.Since(toolStart)), toolArgSummary(tc.Arguments), err)
				contentErr := fmt.Sprintf("error: %v", err)
				toolCallMessage := llm.Message{
					Role:       llm.RoleTool,
					Content:    contentErr,
					ToolCallID: tc.ID,
				}
				toolCallMessage.Content += loopGuard.afterToolResult(ctx, a, tc, contentErr, true, lastInBatch)

				messages = append(messages, toolCallMessage)
				if !summary {
					a.setActiveMessages(messages)
				}
				toolCallResultErrEvent := eventing.NewLLMToolCallResult(originatorID, parentOriginatorID, parentOriginatorVersion, tc, contentErr, "", toolCallMessage)
				if _, err := a.contextSessionStore.AppendToolResult(toolCallResultErrEvent); err != nil {
					a.logger.Warn("Failed to append tool call result to context store: %v", err)
				}
				if stopRequested(stop) {
					return nil, ErrGracefulStop
				}
				continue
			}

			a.logger.Info("%s tool %s ok %s %dB %s", modelTag, tc.Name, formatDuration(time.Since(toolStart)), len(toolCallRes.Content), toolArgSummary(tc.Arguments))
			toolCallRes.Content += loopGuard.afterToolResult(ctx, a, tc, toolCallRes.Content, false, lastInBatch)
			messages = append(messages, toolCallRes)
			if !summary {
				a.setActiveMessages(messages)
			}
			// save point, save the success tool call, might need to also save into a file if output too big
			toolCallResultEvent := eventing.NewLLMToolCallResult(originatorID, parentOriginatorID, parentOriginatorVersion, tc, "", "", toolCallRes)
			if _, err := a.contextSessionStore.AppendToolResult(toolCallResultEvent); err != nil {
				a.logger.Warn("Failed to append tool call result to context store: %v", err)
			}
			if stopRequested(stop) {
				return nil, ErrGracefulStop
			}
		}

		if stopRequested(stop) {
			return nil, ErrGracefulStop
		}
	}
}

// logModel names the model at Info only when it changes mid-session; the
// startup line already named the first one.
func (a *AgentLoop) logModel(provider llm.Provider) {
	desc := provider.GetModel() + " endpoint=" + provider.GetBaseURL()
	if reasoningProvider, ok := provider.(llm.ReasoningProvider); ok {
		if reasoningEffort := strings.TrimSpace(reasoningProvider.ReasoningEffort()); reasoningEffort != "" {
			desc += " reasoning=" + reasoningEffort
		}
	}
	a.llmProviderMu.Lock()
	previous := a.loggedModel
	a.loggedModel = desc
	a.llmProviderMu.Unlock()
	switch {
	case previous == "":
		a.logger.Debug("model %s", desc)
	case previous != desc:
		a.logger.Info("model switched to %s", desc)
	}
}

func describeResponse(result llm.Response) string {
	if len(result.ToolCalls) == 0 {
		return "done"
	}
	names := make([]string, 0, len(result.ToolCalls))
	for _, tc := range result.ToolCalls {
		names = append(names, tc.Name)
	}
	return "tools=[" + strings.Join(names, ", ") + "]"
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}

// toolArgSummaryKeys are the argument fields that best identify a tool call,
// in the order they are tried.
var toolArgSummaryKeys = []string{"command", "file_path", "path", "pattern", "url", "query", "name", "description"}

// toolArgSummary picks one identifying argument for the -v tool line, cut to
// a single short line; full arguments are logged at -vv.
func toolArgSummary(arguments string) string {
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	for _, key := range toolArgSummaryKeys {
		value, ok := args[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		value = strings.Join(strings.Fields(value), " ")
		const maxLen = 80
		if runes := []rune(value); len(runes) > maxLen {
			value = string(runes[:maxLen-1]) + "…"
		}
		return value
	}
	return ""
}

func (a *AgentLoop) tokenEstimateMultiplier() float64 {
	if a.contextManager == nil {
		return 0
	}
	return a.contextManager.TokenEstimateMultiplier()
}

func (a *AgentLoop) publishLLMRetry(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, info llm.RetryInfo) {
	delay := info.Delay.Round(time.Second)
	if delay <= 0 {
		delay = info.Delay
	}
	message := fmt.Sprintf("LLM request failed; retrying in %s (attempt %d).", delay, info.Attempt+1)
	if info.RateLimited {
		message = fmt.Sprintf("Provider rate limit exceeded; retrying in %s (attempt %d).", delay, info.Attempt+1)
	}
	errStr := ""
	if info.Error != nil {
		errStr = info.Error.Error()
	}
	event := eventing.NewLLMRequestRetry(originatorID, parentOriginatorID, parentOriginatorVersion, info.Attempt, info.Delay, info.RateLimited, message, errStr)
	if err := a.contextSessionStore.PublishEvent(event); err != nil {
		a.logger.Warn("Failed to publish LLM retry event: %v", err)
	}
}

func (a *AgentSession) finalizeInterruptedTurn(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, inReplyTo, reason string) error {
	return a.finalizeInterruptedTurnWithNotes(originatorID, parentOriginatorID, parentOriginatorVersion, inReplyTo, reason, nil)
}

func (a *AgentSession) finalizeInterruptedTurnWithNotes(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, inReplyTo, reason string, notes []string) error {
	events, err := a.contextSessionStore.GetAllContextEvents(originatorID)
	if err != nil {
		return fmt.Errorf("read context events: %w", err)
	}

	pending := make(map[string]llm.ToolCall)
	pendingOrder := []string{}
	terminalExists := false
	for _, e := range events {
		switch e.EventType {
		case eventing.LLMUserMessageCreated:
			var payload eventing.LLMUserMessage
			if json.Unmarshal([]byte(e.Payload), &payload) != nil {
				continue
			}
			if payload.ParentOriginatorID != parentOriginatorID || payload.ParentOriginatorVersion != parentOriginatorVersion {
				continue
			}
			// A repeated user event starts a new attempt for the same audit
			// message. Earlier recovery events must not make this attempt look
			// terminal.
			pending = make(map[string]llm.ToolCall)
			pendingOrder = pendingOrder[:0]
			terminalExists = false
		case eventing.LLMAgentMessageCreated:
			var payload eventing.LLMAgentMessage
			if json.Unmarshal([]byte(e.Payload), &payload) != nil {
				continue
			}
			if payload.ParentOriginatorID != parentOriginatorID || payload.ParentOriginatorVersion != parentOriginatorVersion {
				continue
			}
			for _, tc := range payload.Message.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if _, exists := pending[tc.ID]; !exists {
					pendingOrder = append(pendingOrder, tc.ID)
				}
				pending[tc.ID] = tc
			}
		case eventing.LLMToolCallResultCreated:
			var payload eventing.LLMToolCallResult
			if json.Unmarshal([]byte(e.Payload), &payload) != nil {
				continue
			}
			if payload.ParentOriginatorID != parentOriginatorID || payload.ParentOriginatorVersion != parentOriginatorVersion {
				continue
			}
			delete(pending, payload.Message.ToolCallID)
		case eventing.LLMToolCallRecoveryResultCreated:
			payload, decodeErr := eventing.DecodeRecoveryPayload(e)
			if decodeErr == nil && payload.InterruptedParentOriginatorID == parentOriginatorID && payload.InterruptedParentOriginatorVersion == parentOriginatorVersion {
				delete(pending, payload.ToolCallID)
			}
		case eventing.LLMAgentResponseCreated:
			var payload eventing.LLMAgentResponse
			if json.Unmarshal([]byte(e.Payload), &payload) == nil && payload.ParentOriginatorID == parentOriginatorID && payload.ParentOriginatorVersion == parentOriginatorVersion {
				terminalExists = true
			}
		case eventing.LLMAgentInterruptedResponseCreated:
			payload, decodeErr := eventing.DecodeRecoveryPayload(e)
			if decodeErr == nil && payload.InterruptedParentOriginatorID == parentOriginatorID && payload.InterruptedParentOriginatorVersion == parentOriginatorVersion {
				terminalExists = true
			}
		}
	}
	if terminalExists {
		return nil
	}

	pendingIDs := make([]string, 0, len(pendingOrder))
	for _, id := range pendingOrder {
		if _, ok := pending[id]; ok {
			pendingIDs = append(pendingIDs, id)
		}
	}

	recoveryID := eventing.NewRecoveryID()
	for _, id := range pendingIDs {
		recoveryEvent := eventing.NewLLMToolCallRecoveryResult(originatorID, parentOriginatorID, parentOriginatorVersion, recoveryID, reason, pending[id])
		if _, err := a.contextSessionStore.AppendToolResult(recoveryEvent); err != nil {
			return fmt.Errorf("append synthetic tool result: %w", err)
		}
		a.appendActiveMessage(llm.Message{
			Role:       llm.RoleTool,
			Content:    eventing.InterruptedToolResultMessage,
			ToolCallID: id,
		})
	}

	message := eventing.InterruptedAfterToolsMessage
	if len(pendingIDs) > 0 {
		message = eventing.InterruptedDuringToolsMessage
	}
	if reason == "user_cancelled" {
		message = "[The previous assistant turn was stopped by the user. Operations may have partial effects.]"
	}
	if len(notes) > 0 {
		message += "\n" + strings.Join(notes, "\n")
	}
	responseEvent := eventing.NewLLMAgentInterruptedResponse(originatorID, parentOriginatorID, parentOriginatorVersion, recoveryID, reason, message, pendingIDs)
	if _, err := a.contextSessionStore.AppendAgentResponse(responseEvent); err != nil {
		return fmt.Errorf("append interrupted assistant response: %w", err)
	}
	a.appendActiveMessage(llm.Message{Role: llm.RoleAssistant, Content: message})

	auditEvent := eventing.NewAgentInterruptedLogMessage(originatorID, "", inReplyTo, recoveryID, reason, message, pendingIDs)
	if err := a.eventSessionStore.Append(auditEvent); err != nil {
		return fmt.Errorf("append interrupted audit event: %w", err)
	}
	return nil
}

func classifyRecoveryReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "user_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request_timeout"
	}
	if errors.Is(err, ErrGracefulStop) {
		return "graceful_stop"
	}
	return "provider_error"
}

func (a *AgentLoop) publishLLMReasoning(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, update llm.ReasoningUpdate) {
	if strings.TrimSpace(update.Delta) == "" && strings.TrimSpace(update.Text) == "" && !update.Done {
		return
	}
	event := eventing.NewLLMReasoningDelta(originatorID, parentOriginatorID, parentOriginatorVersion, update.Delta, update.Text, update.Done)
	if err := a.contextSessionStore.PublishEvent(event); err != nil {
		a.logger.Warn("Failed to publish LLM reasoning update: %v", err)
	}
}

func (a *AgentLoop) publishLLMReasoningStarted(originatorID, parentOriginatorID string, parentOriginatorVersion uint64) {
	event := eventing.NewLLMReasoningStarted(originatorID, parentOriginatorID, parentOriginatorVersion)
	if err := a.contextSessionStore.PublishEvent(event); err != nil {
		a.logger.Warn("Failed to publish LLM reasoning start: %v", err)
	}
}

func normalizeLLMResponse(resp llm.Response) llm.Response {
	if resp.Message.Role == "" {
		resp.Message.Role = llm.RoleAssistant
	}
	if resp.Message.Content == "" {
		resp.Message.Content = resp.Content
	}
	if len(resp.Message.ToolCalls) == 0 {
		resp.Message.ToolCalls = append([]llm.ToolCall{}, resp.ToolCalls...)
	}
	if resp.Content == "" {
		resp.Content = resp.Message.Content
	}
	if len(resp.ToolCalls) == 0 {
		resp.ToolCalls = append([]llm.ToolCall{}, resp.Message.ToolCalls...)
	}
	return resp
}

func isEmptyLLMResponse(resp llm.Response) bool {
	return strings.TrimSpace(resp.Content) == "" &&
		strings.TrimSpace(resp.Message.Content) == "" &&
		len(resp.ToolCalls) == 0 &&
		len(resp.Message.ToolCalls) == 0
}

func stopRequested(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func (a *AgentSession) convertToContextEvent(e *types.Event) (*types.Event, bool, error) {
	if e.EventType != eventing.LogMessageUserCreated {
		return nil, false, nil
	}

	logMessage := eventing.LogMessage{}

	if err := json.Unmarshal([]byte(e.Payload), &logMessage); err != nil {
		return nil, false, err
	}

	// A retried or restored turn must reuse the stored copy, mode notice
	// included, so context matching by content still finds it.
	variants := modeNoticeVariants(logMessage.Content)
	candidates := make([]*types.Event, len(variants))
	for i, content := range variants {
		candidates[i] = eventing.NewLLMUserMessage(a.channelId, e.Originator.ID, e.Originator.Version, llm.Message{Role: llm.RoleUser, Content: content})
	}
	if index, err := a.FirstUserEventInContext(candidates...); err == nil && index >= 0 {
		return candidates[index], true, nil
	}
	return eventing.NewLLMUserMessage(a.channelId, e.Originator.ID, e.Originator.Version, llm.Message{
		Role:    llm.RoleUser,
		Content: a.withModeNotice(logMessage.Content),
	}), true, nil
}

func (a *AgentSession) convertEventToLLMMessage(e *types.Event) (*llm.Message, error) {
	msg, err := eventing.EventToLLMMessage(e)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

func (a *AgentSession) convertLLMMessageToEventsResponse(originatorID, inReplyTo string, message llm.Message) (*types.Event, error) {
	if message.Role != llm.RoleAssistant {
		return nil, fmt.Errorf("message role is not assistant: %v", message)
	}

	if len(message.ToolCalls) != 0 {
		return nil, fmt.Errorf("still tool calls in final message : %+v", message.ToolCalls)
	}

	return eventing.NewAgentLogMessageInReplyToWithMetadata(originatorID, "", inReplyTo, message.Content, a.adapterType, a.channelId), nil
}

func (a *AgentLoop) rollbackActiveMessagesBefore(message llm.Message) {
	a.llmMessagesMu.Lock()
	defer a.llmMessagesMu.Unlock()
	for i := len(a.llmMessages) - 1; i >= 0; i-- {
		current := a.llmMessages[i]
		if current.Role == message.Role && current.Content == message.Content {
			a.llmMessages = cloneMessages(a.llmMessages[:i])
			return
		}
	}
}

func (a *AgentLoop) removeActiveMessage(message llm.Message) {
	a.llmMessagesMu.Lock()
	defer a.llmMessagesMu.Unlock()
	for i := len(a.llmMessages) - 1; i >= 0; i-- {
		current := a.llmMessages[i]
		if current.Role == message.Role && current.Content == message.Content {
			a.llmMessages = append(cloneMessages(a.llmMessages[:i]), cloneMessages(a.llmMessages[i+1:])...)
			return
		}
	}
}

func (a *AgentLoop) trimActiveMessagesForContextSizeRetry(messages []llm.Message, tools []llm.ToolDefinition, contextSize, promptTokens int) ([]llm.Message, bool) {
	if a.contextManager == nil || contextSize <= 0 {
		return nil, false
	}
	headroom := contextSizeRetryHeadroomTokens
	if headroom > contextSize/4 {
		headroom = contextSize / 4
	}
	if headroom < 1 {
		headroom = 1
	}
	targetTokens := contextSize - headroom
	if targetTokens <= 0 {
		return nil, false
	}

	trimmed := cloneMessages(messages)
	forceTrim := promptTokens > contextSize
	for forceTrim || a.contextManager.EstimateTokens(trimmed, tools) > targetTokens {
		start, end, ok := oldestDroppableActiveRange(trimmed)
		if !ok {
			return nil, false
		}
		next := make([]llm.Message, 0, len(trimmed)-(end-start))
		next = append(next, cloneMessages(trimmed[:start])...)
		next = append(next, cloneMessages(trimmed[end:])...)
		trimmed = next
		forceTrim = false
	}
	return trimmed, true
}

func oldestDroppableActiveRange(messages []llm.Message) (int, int, bool) {
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if i == 0 && msg.Role == llm.RoleUser {
			continue
		}
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			end := assistantToolChunkEnd(messages, i)
			if end > i {
				return i, end, true
			}
		}
		if msg.Role == llm.RoleAssistant || msg.Role == llm.RoleTool {
			return i, i + 1, true
		}
		if msg.Role == llm.RoleUser && i > 0 {
			end := i + 1
			for end < len(messages) && messages[end].Role != llm.RoleUser {
				end++
			}
			return i, end, true
		}
	}
	return 0, 0, false
}

func assistantToolChunkEnd(messages []llm.Message, assistantIndex int) int {
	if assistantIndex < 0 || assistantIndex >= len(messages) {
		return assistantIndex
	}
	pending := map[string]bool{}
	for _, tc := range messages[assistantIndex].ToolCalls {
		if tc.ID != "" {
			pending[tc.ID] = true
		}
	}
	end := assistantIndex + 1
	for end < len(messages) && messages[end].Role == llm.RoleTool {
		delete(pending, messages[end].ToolCallID)
		end++
		if len(pending) == 0 {
			break
		}
	}
	return end
}

func (a *AgentSession) PendingUserEvents() ([]*types.Event, error) {
	events, err := a.eventSessionStore.GetAllEvents(a.channelId)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		event   *types.Event
		handled bool
	}
	var candidates []candidate
	byMessageID := map[string]int{}
	legacyCursor := 0
	for _, event := range events {
		switch event.EventType {
		case eventing.LogMessageUserCreated:
			message, decodeErr := eventing.DecodeLogMessage(event)
			if decodeErr != nil {
				continue
			}
			byMessageID[message.MessageID] = len(candidates)
			candidates = append(candidates, candidate{event: event})
		case eventing.LogMessageAgentCreated:
			message, decodeErr := eventing.DecodeLogMessage(event)
			if decodeErr != nil {
				continue
			}
			if message.InReplyTo != "" {
				if index, ok := byMessageID[message.InReplyTo]; ok && !candidates[index].handled {
					candidates[index].handled = true
				}
				continue
			}
			// Before in_reply_to was persisted, responses were associated with
			// the earliest unmatched user message. Match while walking the event
			// stream so an unthreaded notification can never consume a future user.
			for legacyCursor < len(candidates) && candidates[legacyCursor].handled {
				legacyCursor++
			}
			if legacyCursor < len(candidates) {
				candidates[legacyCursor].handled = true
				legacyCursor++
			}
		}
	}
	pending := make([]*types.Event, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidate.handled {
			pending = append(pending, candidate.event)
		}
	}
	return pending, nil
}

// OnContextEvent registers a handler for events written to context.jsonpl.
func (a *AgentSession) OnContextEvent(eventType string, handler eventing.HandleFunc) error {
	return a.AgentLoop.OnContextEvent(eventType, handler)
}

// OnAuditEvent registers a handler for events written to events.jsonpl.
func (a *AgentSession) OnAuditEvent(eventType string, handler eventing.HandleFunc) error {
	return a.eventSessionStore.AddEventHandler(eventType, handler)
}

// OnEvent is kept as a compatibility wrapper for context events.
func (a *AgentSession) OnEvent(eventType string, handler eventing.HandleFunc) error {
	return a.OnContextEvent(eventType, handler)
}

func (a *AgentSession) AppendAuditEvent(e *types.Event) error {
	return a.eventSessionStore.Append(e)
}

// SetAssistantDeltaHandler installs a live, non-persistent assistant text
// callback. A clear event discards provisional text emitted for the current
// provider attempt. Persisted agent messages remain the source of truth.
func (a *AgentLoop) SetAssistantDeltaHandler(handler func(delta string, clear bool)) {
	a.assistantDeltaMu.Lock()
	a.assistantDeltaHandler = handler
	a.assistantDeltaMu.Unlock()
}

func (a *AgentLoop) publishAssistantDelta(delta string, clear bool) {
	a.assistantDeltaMu.RLock()
	handler := a.assistantDeltaHandler
	a.assistantDeltaMu.RUnlock()
	if handler != nil {
		handler(delta, clear)
	}
}

func (a *AgentSession) AuditEvents() ([]*types.Event, error) {
	return a.eventSessionStore.GetAllEvents(a.channelId)
}

func (a *AgentSession) PublishAuditEvent(e *types.Event) error {
	return a.eventSessionStore.PublishEvent(e)
}

// GetEvents returns all events from the event log (for testing/inspection).
func (a *AgentSession) GetEvents() ([]*types.Event, error) {
	return a.eventSessionStore.GetAllEvents(a.channelId)
}

// GetContextEvents returns all events from the context store (for testing/inspection).
func (a *AgentSession) GetContextEvents() ([]*types.Event, error) {
	return a.ContextEvents(a.channelId)
}

// RecordCommandOutcome publishes a command's visible result into the parent
// conversation. The key prevents a retry from publishing it twice.
func (a *AgentSession) RecordCommandOutcome(key, message string) (bool, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(message) == "" {
		return false, fmt.Errorf("command outcome key and message are required")
	}
	events, err := a.GetContextEvents()
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.EventType != eventing.LLMAgentResponseCreated {
			continue
		}
		var payload eventing.LLMAgentResponse
		if json.Unmarshal([]byte(event.Payload), &payload) == nil && payload.CommandOutcomeKey == key {
			return false, nil
		}
	}
	content := strings.TrimSpace(message)
	event := eventing.NewLLMCommandOutcome(a.channelId, key, content)
	if _, err := a.contextSessionStore.AppendAgentResponse(event); err != nil {
		return false, err
	}
	a.appendActiveMessage(llm.Message{Role: llm.RoleAssistant, Content: content})
	return true, nil
}

func (a *AgentLoop) activeMessages() []llm.Message {
	a.llmMessagesMu.RLock()
	defer a.llmMessagesMu.RUnlock()
	return cloneMessages(a.llmMessages)
}

func (a *AgentLoop) setActiveMessages(messages []llm.Message) {
	a.llmMessagesMu.Lock()
	defer a.llmMessagesMu.Unlock()
	a.llmMessages = cloneMessages(messages)
}

func (a *AgentLoop) appendActiveMessage(message llm.Message) {
	a.llmMessagesMu.Lock()
	defer a.llmMessagesMu.Unlock()
	a.llmMessages = append(a.llmMessages, cloneMessages([]llm.Message{message})[0])
}

func (a *AgentLoop) currentProvider() llm.Provider {
	a.llmProviderMu.RLock()
	defer a.llmProviderMu.RUnlock()
	return a.llmProvider
}

func cloneMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		out[i].ToolCalls = append([]llm.ToolCall{}, msg.ToolCalls...)
	}
	return out
}

// GetContextMessages returns the active LLM messages (compacted view) for replay.
func (a *AgentLoop) GetContextMessages() []llm.Message {
	return a.activeMessages()
}

// ActiveMessageCount returns the number of messages in the active LLM context.
func (a *AgentLoop) ActiveMessageCount() int {
	return len(a.activeMessages())
}

// EstimatedContextTokens returns the estimated token count of the current active context window.
func (a *AgentLoop) EstimatedContextTokens() int {
	return a.contextManager.EstimateTokens(a.activeMessages(), a.llmToolDefinitions)
}

// ContextWindow returns the configured context window ceiling.
func (a *AgentLoop) ContextWindow() int {
	return a.contextManager.ContextWindow()
}

// ReservedTokens returns the configured reserved tokens.
func (a *AgentLoop) ReservedTokens() int {
	return a.contextManager.ReservedTokens()
}

func (a *AgentLoop) KeepRecentTokens() int {
	return a.contextManager.KeepRecentTokens()
}

func (a *AgentLoop) SummaryTokens() int {
	return a.contextManager.SummaryTokens()
}

func (a *AgentLoop) Model() string {
	provider := a.currentProvider()
	if provider == nil {
		return ""
	}
	return provider.GetModel()
}

// ReasoningEffort returns the current provider's reasoning effort, or "" when
// none is set or the provider does not support one.
func (a *AgentLoop) ReasoningEffort() string {
	reasoningProvider, ok := a.currentProvider().(llm.ReasoningProvider)
	if !ok {
		return ""
	}
	return strings.TrimSpace(reasoningProvider.ReasoningEffort())
}

func (a *AgentSession) AdapterType() string {
	return a.adapterType
}

func (a *AgentSession) ChannelID() string {
	return a.channelId
}

// LLMSessionID is the opaque session ID providers such as OpenCode use for
// routing; model calls made outside the agent loop set it with
// llm.WithSessionID.
func (a *AgentSession) LLMSessionID() string {
	return a.llmSessionID()
}

func (a *AgentSession) llmSessionID() string {
	identity := a.adapterType + "\x00" + a.channelId
	return fmt.Sprintf("caretz_%x", sha256.Sum256([]byte(identity)))
}

func (a *AgentSession) BaseDir() string {
	return a.baseDir
}

// SessionDir is the directory containing this session's durable state.
// Incognito sessions have no durable directory.
func (a *AgentSession) SessionDir() string {
	if a == nil || a.inMemorySessionStore {
		return ""
	}
	return filepath.Join(a.baseDir, a.adapterType, a.channelId)
}

func (a *AgentSession) WorkspaceDir() string {
	return a.workspaceDir
}

// SetAgentsDirs records the subagents defined in dirs now. Only those can run
// in this session and its children, so an agent written during the session,
// for example by a Bash command, is ignored.
func (a *AgentSession) SetAgentsDirs(dirs []string) {
	a.agentsDirs = append([]string(nil), dirs...)
	if len(dirs) == 0 {
		dirs = []string{subagents.AgentsDir(a.WorkspaceDir())}
	}
	paths := subagents.Snapshot(dirs)
	a.agentsMu.Lock()
	a.agentPaths = paths
	a.agentsMu.Unlock()
}

func (a *AgentSession) inheritAgents(parent *AgentSession) {
	a.agentsDirs = parent.AgentsDirs()
	paths := parent.subagentPaths()
	a.agentsMu.Lock()
	a.agentPaths = paths
	a.agentsMu.Unlock()
}

// subagentPaths returns the subagents recorded when the session was created
// or its agent directories were set. The map is never modified.
func (a *AgentSession) subagentPaths() map[string]string {
	a.agentsMu.Lock()
	defer a.agentsMu.Unlock()
	return a.agentPaths
}

func (a *AgentSession) SetAllowedAgents(names map[string]bool) {
	if names == nil {
		a.allowedAgents = nil
		return
	}
	a.allowedAgents = make(map[string]bool, len(names))
	for name, allowed := range names {
		a.allowedAgents[name] = allowed
	}
}

func (a *AgentSession) AllowedAgents() map[string]bool {
	if a.allowedAgents == nil {
		return nil
	}
	out := make(map[string]bool, len(a.allowedAgents))
	for name, allowed := range a.allowedAgents {
		out[name] = allowed
	}
	return out
}

func (a *AgentSession) AgentsDirs() []string {
	return append([]string(nil), a.agentsDirs...)
}

func (a *AgentSession) SetSubagentModelResolver(resolver SubagentModelResolver) {
	a.subagentModelResolver = resolver
}

// resolveLocalImageModel resolves LocalImage's provider/model through the
// subagent resolver, so the same model rules decide where images may go.
func (a *AgentSession) resolveLocalImageModel(ctx context.Context, ref string) (tools.LocalImageModel, error) {
	resolution, err := a.subagentModelResolver.ResolveSubagentModel(ctx, SubagentModelRequest{
		Name:         "LocalImage",
		WorkspaceDir: a.WorkspaceDir(),
		Ref:          ref,
		ValidateOnly: true,
	})
	if err != nil {
		return tools.LocalImageModel{}, err
	}
	return tools.LocalImageModel{BaseURL: resolution.BaseURL, APIKey: resolution.APIKey, Model: resolution.Model}, nil
}

func (a *AgentLoop) ToolRegistry() *tools.Registry {
	a.toolRegistryMu.RLock()
	defer a.toolRegistryMu.RUnlock()
	return a.toolRegistry
}

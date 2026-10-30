package agent

import (
	"context"
	"fmt"
	"sync"

	ctxstore "github.com/makkalot/caretz/internal/context"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type AgentTurn struct {
	OriginatorID            string
	ParentOriginatorID      string
	ParentOriginatorVersion uint64
	Message                 llm.Message
	Summary                 bool
	Stop                    <-chan struct{}
}

// AgentLoop owns the executable agent loop state: model runtime, tools, active
// LLM messages, compaction policy, and task state. AgentSession owns identity,
// durable event logs, and session lifecycle around this loop.
type AgentLoop struct {
	systemPrompt       string
	llmMessagesMu      sync.RWMutex
	llmMessages        []llm.Message
	llmToolDefinitions []llm.ToolDefinition
	llmProviderMu      sync.RWMutex
	llmProvider        llm.Provider
	// loggedModel is the model+endpoint last named in the log, so -v shows a
	// switch once instead of repeating the model on every turn.
	loggedModel           string
	toolRegistryMu        sync.RWMutex
	toolRegistry          *tools.Registry
	ownsSessionExecution  bool
	logger                *log.Logger
	contextManager        ctxstore.Manager
	contextSessionStore   *ctxstore.ContextSessionStore
	assistantDeltaMu      sync.RWMutex
	assistantDeltaHandler func(delta string, clear bool)

	taskState                  *tasks.State
	taskMutationsSinceSnapshot int
	disableTasks               bool

	loopGuardMu sync.RWMutex
	loopGuard   *loopGuardHooks
}

type AgentLoopParams struct {
	Logger                     *log.Logger
	Provider                   llm.Provider
	ToolRegistry               *tools.Registry
	SystemPrompt               string
	ContextManager             ctxstore.Manager
	ContextSessionStore        *ctxstore.ContextSessionStore
	Messages                   []llm.Message
	ToolDefinitions            []llm.ToolDefinition
	TaskState                  *tasks.State
	TaskMutationsSinceSnapshot int
	DisableTasks               bool
}

func NewAgentLoop(params AgentLoopParams) *AgentLoop {
	return &AgentLoop{
		systemPrompt:               params.SystemPrompt,
		llmMessages:                cloneMessages(params.Messages),
		llmToolDefinitions:         append([]llm.ToolDefinition(nil), params.ToolDefinitions...),
		llmProvider:                params.Provider,
		toolRegistry:               params.ToolRegistry,
		logger:                     params.Logger,
		contextManager:             params.ContextManager,
		contextSessionStore:        params.ContextSessionStore,
		taskState:                  params.TaskState,
		taskMutationsSinceSnapshot: params.TaskMutationsSinceSnapshot,
		disableTasks:               params.DisableTasks,
	}
}

func (l *AgentLoop) Run(ctx context.Context, turn AgentTurn) (*llm.Response, error) {
	if l == nil {
		return nil, fmt.Errorf("agent loop is required")
	}
	return l.run(ctx, turn)
}

func (l *AgentLoop) IsEventProcessed(e *types.Event) (bool, error) {
	if l == nil || l.contextSessionStore == nil {
		return false, fmt.Errorf("agent loop context store is required")
	}
	return l.contextSessionStore.IsEventProcessed(e)
}

func (l *AgentLoop) IsUserEventInContext(e *types.Event) (bool, error) {
	if l == nil || l.contextSessionStore == nil {
		return false, fmt.Errorf("agent loop context store is required")
	}
	return l.contextSessionStore.IsUserEventInContext(e)
}

func (l *AgentLoop) FirstUserEventInContext(candidates ...*types.Event) (int, error) {
	if l == nil || l.contextSessionStore == nil {
		return -1, fmt.Errorf("agent loop context store is required")
	}
	return l.contextSessionStore.FirstUserEventInContext(candidates...)
}

func (l *AgentLoop) AppendUserMessage(e *types.Event) error {
	if l == nil || l.contextSessionStore == nil {
		return fmt.Errorf("agent loop context store is required")
	}
	return l.contextSessionStore.AppendUserMessage(e)
}

func (l *AgentLoop) OnContextEvent(eventType string, handler eventing.HandleFunc) error {
	if l == nil || l.contextSessionStore == nil {
		return fmt.Errorf("agent loop context store is required")
	}
	return l.contextSessionStore.AddEventHandler(eventType, handler)
}

func (l *AgentLoop) ContextEvents(channelID string) ([]*types.Event, error) {
	if l == nil || l.contextSessionStore == nil {
		return nil, fmt.Errorf("agent loop context store is required")
	}
	return l.contextSessionStore.GetAllContextEvents(channelID)
}

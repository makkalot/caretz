package context

import (
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

// Manager defines the session context-management boundary used by the agent.
// Implementations can compact, trim, retrieve, or simply pass through stored
// conversation history.
type Manager interface {
	RestoreContext(originatorID string) ([]llm.Message, error)
	IsCompactionRequired(messages []llm.Message, tools []llm.ToolDefinition) bool
	EstimateTokens(messages []llm.Message, tools []llm.ToolDefinition) int
	EstimateRawTokens(messages []llm.Message, tools []llm.ToolDefinition) int
	UpdateTokenCalibration(estimatedTokens int, actualPromptTokens int64)
	TokenEstimateMultiplier() float64
	SetLimits(contextWindow int, reservedTokens int, keepRecentTokens int, summaryTokens int)
	ContextWindow() int
	ReservedTokens() int
	KeepRecentTokens() int
	SummaryTokens() int
	EstimateSummaryTokens(summary string) int
	GetSummaryPromptEventWithActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string) (*types.Event, error)
	GetSummaryPromptEventWithContextLimitAndActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string, effectiveContextWindow int) (*types.Event, error)
	GetActiveToolChainSummaryPromptEventWithActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string) (*types.Event, error)
	GetActiveToolChainSummaryPromptEventWithContextLimitAndActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string, effectiveContextWindow int) (*types.Event, error)
	GetNewContext(originatorID string) ([]llm.Message, error)
}

type DisabledManager struct {
	contextStore            *ContextSessionStore
	contextWindow           int
	reservedTokens          int
	keepRecentTokens        int
	summaryTokens           int
	systemPromptTokens      int
	tokenEstimateMultiplier float64
}

func NewDisabledManager(contextStore *ContextSessionStore, contextWindow int, reservedTokens int, keepRecentTokens int, systemPrompt string, summaryTokens ...int) *DisabledManager {
	summaryTokenLimit := 0
	if len(summaryTokens) > 0 {
		summaryTokenLimit = summaryTokens[0]
	}
	if summaryTokenLimit <= 0 {
		summaryTokenLimit = DefaultSummaryTokens(keepRecentTokens)
	}
	return &DisabledManager{
		contextStore:            contextStore,
		contextWindow:           contextWindow,
		reservedTokens:          reservedTokens,
		keepRecentTokens:        keepRecentTokens,
		summaryTokens:           summaryTokenLimit,
		systemPromptTokens:      estimateSystemPromptTokens(systemPrompt),
		tokenEstimateMultiplier: tokenEstimateInitialCalibration,
	}
}

func (dm *DisabledManager) RestoreContext(originatorID string) ([]llm.Message, error) {
	events, err := dm.contextStore.GetAllContextEvents(originatorID)
	if err != nil {
		return nil, err
	}
	if multiplier, ok := latestTokenEstimateMultiplier(events); ok {
		dm.tokenEstimateMultiplier = multiplier
	}
	messages, err := conversationMessages(events)
	if err != nil {
		return nil, err
	}
	return trimIncompleteToolTail(messages), nil
}

func (dm *DisabledManager) IsCompactionRequired(messages []llm.Message, tools []llm.ToolDefinition) bool {
	return false
}

func (dm *DisabledManager) EstimateTokens(messages []llm.Message, tools []llm.ToolDefinition) int {
	return applyTokenCalibration(dm.EstimateRawTokens(messages, tools), dm.tokenEstimateMultiplier)
}

func (dm *DisabledManager) EstimateRawTokens(messages []llm.Message, tools []llm.ToolDefinition) int {
	return estimateTokens(messages, tools, dm.systemPromptTokens)
}

func (dm *DisabledManager) UpdateTokenCalibration(estimatedTokens int, actualPromptTokens int64) {
	if estimatedTokens <= 0 || actualPromptTokens <= 0 {
		return
	}
	ratio := float64(actualPromptTokens) / float64(estimatedTokens)
	if ratio < tokenEstimateMinCalibration {
		ratio = tokenEstimateMinCalibration
	}
	if ratio > tokenEstimateMaxCalibration {
		ratio = tokenEstimateMaxCalibration
	}
	dm.tokenEstimateMultiplier = ratio
}

func (dm *DisabledManager) TokenEstimateMultiplier() float64 {
	return dm.tokenEstimateMultiplier
}

func (dm *DisabledManager) SetLimits(contextWindow int, reservedTokens int, keepRecentTokens int, summaryTokens int) {
	if contextWindow > 0 {
		dm.contextWindow = contextWindow
	}
	if reservedTokens > 0 {
		dm.reservedTokens = reservedTokens
	}
	if keepRecentTokens > 0 {
		dm.keepRecentTokens = keepRecentTokens
	}
	if summaryTokens > 0 {
		dm.summaryTokens = summaryTokens
	} else {
		dm.summaryTokens = DefaultSummaryTokens(dm.keepRecentTokens)
	}
}

func (dm *DisabledManager) ContextWindow() int {
	return dm.contextWindow
}

func (dm *DisabledManager) ReservedTokens() int {
	return dm.reservedTokens
}

func (dm *DisabledManager) KeepRecentTokens() int {
	return dm.keepRecentTokens
}

func (dm *DisabledManager) SummaryTokens() int {
	return dm.summaryTokens
}

func (dm *DisabledManager) EstimateSummaryTokens(summary string) int {
	return estimateStringTokens(summary)
}

func (dm *DisabledManager) GetSummaryPromptEventWithActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string) (*types.Event, error) {
	return nil, nil
}

func (dm *DisabledManager) GetSummaryPromptEventWithContextLimitAndActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string, effectiveContextWindow int) (*types.Event, error) {
	return nil, nil
}

func (dm *DisabledManager) GetActiveToolChainSummaryPromptEventWithActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string) (*types.Event, error) {
	return nil, nil
}

func (dm *DisabledManager) GetActiveToolChainSummaryPromptEventWithContextLimitAndActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string, effectiveContextWindow int) (*types.Event, error) {
	return nil, nil
}

func (dm *DisabledManager) GetNewContext(originatorID string) ([]llm.Message, error) {
	return dm.RestoreContext(originatorID)
}

var _ Manager = (*CompactionManager)(nil)
var _ Manager = (*DisabledManager)(nil)

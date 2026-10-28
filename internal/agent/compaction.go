package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type CompactionResult struct {
	Compacted    bool
	Reason       string
	Summary      string
	BeforeTokens int
	AfterTokens  int
}

func (a *AgentSession) CompactNow(ctx context.Context) (*CompactionResult, error) {
	ctx = llm.WithSessionID(ctx, a.llmSessionID())
	return a.compactContext(ctx, a.channelId, "manual-command", 0, "manual-command", "")
}

func (a *AgentLoop) compactContext(ctx context.Context, originatorID, parentOriginatorID string, parentOriginatorVersion uint64, reason, activeUserRequest string) (*CompactionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.contextManager == nil {
		return &CompactionResult{Compacted: false, Reason: reason}, nil
	}

	beforeTokens := a.contextManager.EstimateTokens(a.activeMessages(), a.llmToolDefinitions)
	result := &CompactionResult{
		Compacted:    false,
		Reason:       reason,
		BeforeTokens: beforeTokens,
		AfterTokens:  beforeTokens,
	}
	a.logger.Debug("Starting compaction process (%s): before=%d threshold=%d multiplier=%.2f",
		reason, beforeTokens, a.contextManager.ContextWindow()-a.contextManager.ReservedTokens(), a.contextManager.TokenEstimateMultiplier())

	llmSummaryPromptEvent, err := a.contextManager.GetSummaryPromptEventWithActiveRequest(originatorID, parentOriginatorID, parentOriginatorVersion, a.renderedTaskState(), activeUserRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to get summary prompt event: %w", err)
	}

	if llmSummaryPromptEvent == nil {
		threshold := a.contextManager.ContextWindow() - a.contextManager.ReservedTokens()
		if beforeTokens > threshold {
			a.logger.Debug("Compaction skipped (%s): estimated context is still above threshold (%d > %d), but there are no older complete turns outside the recent-history window to summarize",
				reason, beforeTokens, threshold)
		} else {
			a.logger.Debug("Compaction skipped (%s): estimated context is below threshold (%d <= %d)",
				reason, beforeTokens, threshold)
		}
		return result, nil
	}

	summaryMessagePrompt, err := a.convertSummaryPromptEventToLLMMessage(llmSummaryPromptEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to convert summary prompt event: %w", err)
	}

	summaryResp, err := a.generateSummary(ctx, summaryMessagePrompt)
	if err != nil {
		contextErr, ok := llm.ParseContextSizeError(err)
		if !ok || contextErr.ContextSize <= 0 || contextErr.ContextSize >= a.contextManager.ContextWindow() {
			return nil, fmt.Errorf("failed to generate compaction summary: %w", err)
		}
		a.logger.Info("Compaction summary request exceeded provider context; rebuilding prompt with provider limit: prompt_tokens=%d provider_context=%d configured_context=%d", contextErr.PromptTokens, contextErr.ContextSize, a.contextManager.ContextWindow())
		llmSummaryPromptEvent, err = a.contextManager.GetSummaryPromptEventWithContextLimitAndActiveRequest(originatorID, parentOriginatorID, parentOriginatorVersion, a.renderedTaskState(), activeUserRequest, contextErr.ContextSize)
		if err != nil {
			return nil, fmt.Errorf("failed to rebuild summary prompt after context-size error: %w", err)
		}
		if llmSummaryPromptEvent == nil {
			return nil, fmt.Errorf("failed to rebuild summary prompt after context-size error: no summarizable context")
		}
		summaryMessagePrompt, err = a.convertSummaryPromptEventToLLMMessage(llmSummaryPromptEvent)
		if err != nil {
			return nil, fmt.Errorf("failed to convert rebuilt summary prompt event: %w", err)
		}
		summaryResp, err = a.generateSummary(ctx, summaryMessagePrompt)
		if err != nil {
			return nil, fmt.Errorf("failed to generate compaction summary after context-size recovery: %w", err)
		}
	}
	if err := a.contextSessionStore.AppendUserMessage(llmSummaryPromptEvent); err != nil {
		return nil, fmt.Errorf("failed to append user summary message to context session store: %w", err)
	}
	summaryEvent := newSummaryResponseEventFromPrompt(originatorID, parentOriginatorID, parentOriginatorVersion, llmSummaryPromptEvent, summaryResp)
	if _, err := a.contextSessionStore.AppendAgentResponse(summaryEvent); err != nil {
		return nil, fmt.Errorf("failed to append summary response to context store: %w", err)
	}

	newMessages, err := a.contextManager.GetNewContext(originatorID)
	if err != nil {
		return nil, fmt.Errorf("failed to get new context after compaction: %w", err)
	}

	// Keep the in-memory active context aligned with the compacted persisted view.
	a.setActiveMessages(newMessages)
	afterTokens := a.contextManager.EstimateTokens(a.activeMessages(), a.llmToolDefinitions)
	summaryTokens := a.contextManager.EstimateSummaryTokens(summaryResp.Message.Content)
	a.logger.Info("[%s] compact %s ~%d → ~%d tokens (summary ~%d)",
		a.currentProvider().GetModel(), reason, beforeTokens, afterTokens, summaryTokens)
	result.Compacted = true
	result.AfterTokens = afterTokens
	if summaryResp != nil {
		result.Summary = summaryResp.Message.Content
		event := eventing.NewCompactionCompleted(originatorID, reason, summaryResp.Message.Content, beforeTokens, afterTokens)
		if err := a.contextSessionStore.PublishEvent(event); err != nil {
			return nil, fmt.Errorf("failed to publish compaction completed event: %w", err)
		}
	}

	return result, nil
}

func (a *AgentLoop) compactActiveToolChainContext(ctx context.Context, originatorID, parentOriginatorID string, parentOriginatorVersion uint64, reason, activeUserRequest string) (*CompactionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.contextManager == nil {
		return &CompactionResult{Compacted: false, Reason: reason}, nil
	}

	beforeTokens := a.contextManager.EstimateTokens(a.activeMessages(), a.llmToolDefinitions)
	result := &CompactionResult{
		Compacted:    false,
		Reason:       reason,
		BeforeTokens: beforeTokens,
		AfterTokens:  beforeTokens,
	}
	a.logger.Debug("Starting active tool-chain compaction (%s): before=%d threshold=%d multiplier=%.2f",
		reason, beforeTokens, a.contextManager.ContextWindow()-a.contextManager.ReservedTokens(), a.contextManager.TokenEstimateMultiplier())

	llmSummaryPromptEvent, err := a.contextManager.GetActiveToolChainSummaryPromptEventWithActiveRequest(originatorID, parentOriginatorID, parentOriginatorVersion, a.renderedTaskState(), activeUserRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to get active tool-chain summary prompt event: %w", err)
	}
	if llmSummaryPromptEvent == nil {
		a.logger.Debug("Active tool-chain compaction skipped (%s): no completed tool-call boundary before recent-history window", reason)
		return result, nil
	}

	summaryMessagePrompt, err := a.convertSummaryPromptEventToLLMMessage(llmSummaryPromptEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to convert active tool-chain summary prompt event: %w", err)
	}

	summaryResp, err := a.generateSummary(ctx, summaryMessagePrompt)
	if err != nil {
		contextErr, ok := llm.ParseContextSizeError(err)
		if !ok || contextErr.ContextSize <= 0 || contextErr.ContextSize >= a.contextManager.ContextWindow() {
			return nil, fmt.Errorf("failed to generate active tool-chain summary: %w", err)
		}
		a.logger.Info("Active tool-chain summary request exceeded provider context; rebuilding prompt with provider limit: prompt_tokens=%d provider_context=%d configured_context=%d", contextErr.PromptTokens, contextErr.ContextSize, a.contextManager.ContextWindow())
		llmSummaryPromptEvent, err = a.contextManager.GetActiveToolChainSummaryPromptEventWithContextLimitAndActiveRequest(originatorID, parentOriginatorID, parentOriginatorVersion, a.renderedTaskState(), activeUserRequest, contextErr.ContextSize)
		if err != nil {
			return nil, fmt.Errorf("failed to rebuild active tool-chain summary prompt after context-size error: %w", err)
		}
		if llmSummaryPromptEvent == nil {
			return result, nil
		}
		summaryMessagePrompt, err = a.convertSummaryPromptEventToLLMMessage(llmSummaryPromptEvent)
		if err != nil {
			return nil, fmt.Errorf("failed to convert rebuilt active tool-chain summary prompt event: %w", err)
		}
		summaryResp, err = a.generateSummary(ctx, summaryMessagePrompt)
		if err != nil {
			return nil, fmt.Errorf("failed to generate active tool-chain summary after context-size recovery: %w", err)
		}
	}
	if err := a.contextSessionStore.AppendUserMessage(llmSummaryPromptEvent); err != nil {
		return nil, fmt.Errorf("failed to append active tool-chain summary prompt to context store: %w", err)
	}
	summaryEvent := newSummaryResponseEventFromPrompt(originatorID, parentOriginatorID, parentOriginatorVersion, llmSummaryPromptEvent, summaryResp)
	if _, err := a.contextSessionStore.AppendAgentResponse(summaryEvent); err != nil {
		return nil, fmt.Errorf("failed to append active tool-chain summary response to context store: %w", err)
	}

	newMessages, err := a.contextManager.GetNewContext(originatorID)
	if err != nil {
		return nil, fmt.Errorf("failed to get new context after active tool-chain compaction: %w", err)
	}
	a.setActiveMessages(newMessages)
	afterTokens := a.contextManager.EstimateTokens(a.activeMessages(), a.llmToolDefinitions)
	a.logger.Info("[%s] compact %s ~%d → ~%d tokens (summary ~%d)",
		a.currentProvider().GetModel(), reason, beforeTokens, afterTokens, a.contextManager.EstimateSummaryTokens(summaryResp.Message.Content))
	result.Compacted = true
	result.AfterTokens = afterTokens
	result.Summary = summaryResp.Message.Content
	event := eventing.NewCompactionCompleted(originatorID, reason, summaryResp.Message.Content, beforeTokens, afterTokens)
	if err := a.contextSessionStore.PublishEvent(event); err != nil {
		return nil, fmt.Errorf("failed to publish active tool-chain compaction completed event: %w", err)
	}
	return result, nil
}

func newSummaryResponseEventFromPrompt(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, promptEvent *types.Event, summaryResp *llm.Response) *types.Event {
	var prompt eventing.SummaryPrompt
	if promptEvent != nil && promptEvent.EventType == eventing.SummaryPromptCreated {
		_ = json.Unmarshal([]byte(promptEvent.Payload), &prompt)
	}
	if prompt.CompactionScope != "" || prompt.ActiveUserVersion != 0 || prompt.CoversThroughVersion != 0 {
		return eventing.NewLLMAgentSummaryResponseWithMetadata(originatorID, parentOriginatorID, parentOriginatorVersion, summaryResp.Message, summaryResp.Usage, summaryResp.Usage.TotalTokens, prompt.CompactionScope, prompt.ActiveUserVersion, prompt.CoversThroughVersion)
	}
	return eventing.NewLLMAgentSummaryResponse(originatorID, parentOriginatorID, parentOriginatorVersion, summaryResp.Message, summaryResp.Usage, summaryResp.Usage.TotalTokens)
}

func (a *AgentLoop) generateSummary(ctx context.Context, summaryMessagePrompt llm.Message) (*llm.Response, error) {
	resp, err := a.callSummaryModelWithRetries(ctx, summaryMessagePrompt)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (a *AgentLoop) callSummaryModelWithRetries(ctx context.Context, message llm.Message) (llm.Response, error) {
	var last llm.Response
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := a.callSummaryModel(ctx, message)
		if err != nil {
			return llm.Response{}, err
		}
		last = resp
		if strings.TrimSpace(resp.Message.Content) != "" && len(resp.Message.ToolCalls) == 0 {
			return resp, nil
		}
		a.logger.Warn("Summary model returned invalid response on attempt %d: content_bytes=%d tool_calls=%d", attempt, len(resp.Message.Content), len(resp.Message.ToolCalls))
	}
	return llm.Response{}, fmt.Errorf("summary model returned empty or tool-call response after retries: content_bytes=%d tool_calls=%d", len(last.Message.Content), len(last.Message.ToolCalls))
}

func (a *AgentLoop) callSummaryModel(ctx context.Context, message llm.Message) (llm.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	provider := a.currentProvider()
	a.logger.Debug("Sending compaction summary request to LLM")
	resp, err := llm.ChatContext(ctx, provider, "", []llm.Message{message}, nil, func(delta string) {})
	if err != nil {
		return llm.Response{}, err
	}
	if resp.Message.Role == "" {
		resp.Message = llm.Message{Role: llm.RoleAssistant, Content: resp.Content}
	}
	if resp.Message.Content == "" && resp.Content != "" {
		resp.Message.Content = resp.Content
	}
	return resp, nil
}

func (a *AgentLoop) convertSummaryPromptEventToLLMMessage(e *types.Event) (llm.Message, error) {
	msg, err := eventing.EventToLLMMessage(e)
	if err != nil {
		return llm.Message{}, fmt.Errorf("failed to convert summary prompt event to LLM message: %w", err)
	}
	return msg, nil
}

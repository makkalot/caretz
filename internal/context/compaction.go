package context

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

// --- Token estimation ---

const (
	tokenEstimateCharsPerToken      = 3
	tokenEstimateRequestOverhead    = 16
	tokenEstimateMessageOverhead    = 8
	tokenEstimateToolOverhead       = 12
	tokenEstimateToolCallOverhead   = 8
	tokenEstimateInitialCalibration = 1.0
	tokenEstimateMinCalibration     = 0.5
	tokenEstimateMaxCalibration     = 2.0
	tokenEstimateDownwardDecay      = 0.5
	obsoleteCheckpointEventType     = "Checkpoint.Created"
	summaryPromptBudgetNumerator    = 9
	summaryPromptBudgetDenominator  = 10
	summaryPromptMinReservedTokens  = 1024
	activeToolChainScope            = "active_tool_chain"
)

type versionedMessage struct {
	event *types.Event
	msg   llm.Message
}

type summaryMetadata struct {
	CompactionScope      string
	ActiveUserVersion    uint64
	CoversThroughVersion uint64
}

type ConversationValidationFinding struct {
	Kind                   string
	MessageIndex           int
	Role                   llm.Role
	ToolCallID             string
	PendingToolCallIDs     []string
	AwaitingFinalAssistant bool
	DroppedMessages        int
}

type ConversationValidationResult struct {
	Messages        []llm.Message
	Findings        []ConversationValidationFinding
	DroppedMessages int
}

func estimateTokens(messages []llm.Message, tools []llm.ToolDefinition, systemPromptTokens int) int {
	total := tokenEstimateRequestOverhead + systemPromptTokens
	for _, msg := range messages {
		total += estimateMessageTokens(msg)
	}
	total += estimateToolsOverhead(tools)
	return total
}

func estimateSystemPromptTokens(systemPrompt string) int {
	return estimateStringTokens(systemPrompt)
}

func estimateMessageTokensTotal(messages []llm.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateMessageTokens(msg)
	}
	return total
}

// estimateToolsOverhead estimates the token cost of tool definitions
// that are sent with every API request.
func estimateToolsOverhead(tools []llm.ToolDefinition) int {
	if len(tools) == 0 {
		return 0
	}
	total := 0
	for _, tool := range tools {
		data, err := json.Marshal(requestTool{
			Type: "function",
			Function: requestToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
		if err != nil {
			continue
		}
		total += estimateStringTokens(string(data)) + tokenEstimateToolOverhead
	}
	return total
}

func estimateMessageTokens(msg llm.Message) int {
	data, err := json.Marshal(toRequestMessage(msg))
	if err != nil {
		return tokenEstimateMessageOverhead + estimateStringTokens(extractMessageText(msg))
	}
	total := tokenEstimateMessageOverhead + estimateStringTokens(string(data))
	total += len(msg.ToolCalls) * tokenEstimateToolCallOverhead
	return total
}

func extractMessageText(msg llm.Message) string {
	var parts []string

	parts = append(parts, msg.Content)

	for _, tc := range msg.ToolCalls {
		parts = append(parts, tc.Name)
		parts = append(parts, tc.Arguments)
	}

	return strings.Join(parts, " ")
}

func estimateStringTokens(s string) int {
	if s == "" {
		return 0
	}
	return int(math.Ceil(float64(len(s)) / tokenEstimateCharsPerToken))
}

func applyTokenCalibration(tokens int, multiplier float64) int {
	if tokens <= 0 {
		return 0
	}
	if multiplier < tokenEstimateMinCalibration {
		multiplier = tokenEstimateMinCalibration
	}
	if multiplier > tokenEstimateMaxCalibration {
		multiplier = tokenEstimateMaxCalibration
	}
	return int(math.Ceil(float64(tokens) * multiplier))
}

type requestMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []requestToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type requestToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function requestToolFunction `json:"function"`
}

type requestToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Arguments   string         `json:"arguments,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type requestTool struct {
	Type     string              `json:"type"`
	Function requestToolFunction `json:"function"`
}

func toRequestMessage(msg llm.Message) requestMessage {
	result := requestMessage{
		Role:       string(msg.Role),
		Content:    msg.Content,
		ToolCallID: msg.ToolCallID,
	}
	for _, tc := range msg.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, requestToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: requestToolFunction{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		})
	}
	return result
}

// --- Prompt templates ---

const toolResultMaxLen = 2000
const staleToolResultDigestMaxLen = 240

var initialSummaryPromptText = `The messages above are a conversation to summarize. Create a structured context summary that another LLM will use to continue the work.

If an <active-user-request> block is present, treat it as the current top-priority user request. Preserve its intent in ## Goal, its explicit requirements in ## Constraints & Preferences, and any remaining work it implies in ## Next Steps.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Include all continuation-critical context. Prioritize the most recent state and actionable details needed to continue. Preserve exact file paths, function names, and error messages when they are still actionable. Do not shorten the summary to fit a fixed token or character budget.`

var updateSummaryPromptText = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE only actionable and currently relevant information from the previous summary
- ADD new progress, decisions, and context from the new messages
- If an <active-user-request> block is present, treat it as the current top-priority user request and preserve its intent in ## Goal, requirements in ## Constraints & Preferences, and remaining work in ## Next Steps
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRIORITIZE the most recent state and actionable details needed to continue
- PRESERVE exact file paths, function names, and error messages only when they are still actionable
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Include all continuation-critical context. Remove stale completed detail only when it would not change future work. Do not shorten the summary to fit a fixed token or character budget.`

// --- CompactionManager ---

type CompactionManager struct {
	contextStore *ContextSessionStore
	debugf       func(format string, args ...interface{})
	// total context window ceiling (e.g. 128000, 200000)
	contextWindow int
	// tokens reserved for next prompt + response; compaction triggers when remaining capacity drops below this
	reservedTokens int
	// how many tokens of recent messages to keep verbatim after compaction
	keepRecentTokens int
	// target token budget for the summary itself
	summaryTokens int
	// estimated tokens for the system prompt (constant per session)
	systemPromptTokens int
	// multiplier learned from provider-reported prompt_tokens.
	tokenEstimateMultiplier float64
}

func NewCompactionManager(contextStore *ContextSessionStore, contextWindow int, reservedTokens int, keepRecentTokens int, systemPrompt string, summaryTokens ...int) *CompactionManager {
	summaryTokenLimit := 0
	if len(summaryTokens) > 0 {
		summaryTokenLimit = summaryTokens[0]
	}
	if summaryTokenLimit <= 0 {
		summaryTokenLimit = DefaultSummaryTokens(keepRecentTokens)
	}
	return &CompactionManager{
		contextStore:            contextStore,
		contextWindow:           contextWindow,
		reservedTokens:          reservedTokens,
		keepRecentTokens:        keepRecentTokens,
		summaryTokens:           summaryTokenLimit,
		systemPromptTokens:      estimateSystemPromptTokens(systemPrompt),
		tokenEstimateMultiplier: tokenEstimateInitialCalibration,
	}
}

func (cm *CompactionManager) SetDebugLogger(debugf func(format string, args ...interface{})) {
	cm.debugf = debugf
}

func (cm *CompactionManager) logDebug(format string, args ...interface{}) {
	if cm.debugf != nil {
		cm.debugf(format, args...)
	}
}

func DefaultSummaryTokens(keepRecentTokens int) int {
	return keepRecentTokens / 2
}

func (cm *CompactionManager) IsCompactionRequired(messages []llm.Message, tools []llm.ToolDefinition) bool {
	estimatedTokens := cm.EstimateTokens(messages, tools)
	return estimatedTokens > cm.contextWindow-cm.reservedTokens
}

// EstimateTokens returns the estimated token count for the given messages + tools + system prompt.
func (cm *CompactionManager) EstimateTokens(messages []llm.Message, tools []llm.ToolDefinition) int {
	return applyTokenCalibration(cm.EstimateRawTokens(messages, tools), cm.tokenEstimateMultiplier)
}

// EstimateRawTokens returns the uncalibrated token estimate for a request.
func (cm *CompactionManager) EstimateRawTokens(messages []llm.Message, tools []llm.ToolDefinition) int {
	return estimateTokens(messages, tools, cm.systemPromptTokens)
}

// UpdateTokenCalibration adjusts future estimates using provider-reported prompt tokens.
func (cm *CompactionManager) UpdateTokenCalibration(estimatedTokens int, actualPromptTokens int64) {
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
	if ratio >= cm.tokenEstimateMultiplier {
		cm.tokenEstimateMultiplier = ratio
		return
	}
	cm.tokenEstimateMultiplier += tokenEstimateDownwardDecay * (ratio - cm.tokenEstimateMultiplier)
}

// TokenEstimateMultiplier returns the API-calibrated estimate multiplier.
func (cm *CompactionManager) TokenEstimateMultiplier() float64 {
	return cm.tokenEstimateMultiplier
}

func (cm *CompactionManager) SetLimits(contextWindow int, reservedTokens int, keepRecentTokens int, summaryTokens int) {
	if contextWindow > 0 {
		cm.contextWindow = contextWindow
	}
	if reservedTokens > 0 {
		cm.reservedTokens = reservedTokens
	}
	if keepRecentTokens > 0 {
		cm.keepRecentTokens = keepRecentTokens
	}
	if summaryTokens > 0 {
		cm.summaryTokens = summaryTokens
	} else {
		cm.summaryTokens = DefaultSummaryTokens(cm.keepRecentTokens)
	}
}

// ContextWindow returns the configured context window ceiling.
func (cm *CompactionManager) ContextWindow() int {
	return cm.contextWindow
}

// ReservedTokens returns the configured reserved tokens.
func (cm *CompactionManager) ReservedTokens() int {
	return cm.reservedTokens
}

// KeepRecentTokens returns how many recent tokens are kept verbatim after compaction.
func (cm *CompactionManager) KeepRecentTokens() int {
	return cm.keepRecentTokens
}

func (cm *CompactionManager) effectiveKeepRecentTokens() int {
	usable := cm.contextWindow - cm.reservedTokens
	if usable <= 0 {
		return cm.keepRecentTokens
	}
	maxKeep := usable / 2
	if maxKeep < summaryPromptMinReservedTokens {
		maxKeep = summaryPromptMinReservedTokens
	}
	if cm.keepRecentTokens > maxKeep {
		return maxKeep
	}
	return cm.keepRecentTokens
}

// SummaryTokens returns the target token budget for generated summaries.
func (cm *CompactionManager) SummaryTokens() int {
	return cm.summaryTokens
}

func (cm *CompactionManager) EstimateSummaryTokens(summary string) int {
	return estimateStringTokens(summary)
}

// getConversationMessages returns all context events excluding summary-related
// events, converted to llm.Message. This gives the flat conversation history
// regardless of where summary events sit in the append-only log.
func (cm *CompactionManager) getConversationMessages(originatorID string) ([]llm.Message, error) {
	allEvents, err := cm.contextStore.GetAllContextEvents(originatorID)
	if err != nil {
		return nil, err
	}

	return conversationMessages(allEvents)
}

func (cm *CompactionManager) GetSummaryPromptEvent(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState string) (*types.Event, error) {
	return cm.GetSummaryPromptEventWithActiveRequest(originatorID, parentOriginatorID, parentOriginatorVersion, renderedTaskState, "")
}

func (cm *CompactionManager) GetSummaryPromptEventWithActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string) (*types.Event, error) {
	return cm.GetSummaryPromptEventWithContextLimitAndActiveRequest(originatorID, parentOriginatorID, parentOriginatorVersion, renderedTaskState, activeUserRequest, cm.contextWindow)
}

func (cm *CompactionManager) GetSummaryPromptEventWithContextLimit(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState string, effectiveContextWindow int) (*types.Event, error) {
	return cm.GetSummaryPromptEventWithContextLimitAndActiveRequest(originatorID, parentOriginatorID, parentOriginatorVersion, renderedTaskState, "", effectiveContextWindow)
}

func (cm *CompactionManager) GetSummaryPromptEventWithContextLimitAndActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string, effectiveContextWindow int) (*types.Event, error) {
	latestSummaryEvent, previousSummary, _, err := cm.getLatestSummaryEvent(originatorID)
	if err != nil {
		return nil, err
	}
	if meta, ok := summaryEventMetadata(latestSummaryEvent); ok && meta.CompactionScope == activeToolChainScope {
		allEvents, err := cm.contextStore.GetAllContextEvents(originatorID)
		if err != nil {
			return nil, err
		}
		items, err := versionedConversationMessages(allEvents)
		if err != nil {
			return nil, err
		}
		latestUserIdx := latestUserMessageIndex(items)
		if latestUserIdx < 0 || items[latestUserIdx].event.Originator.Version <= meta.ActiveUserVersion {
			return nil, nil
		}
	}
	hasPreviousSummary := previousSummary != ""

	messages, err := cm.getMessagesToSummarize(originatorID, hasPreviousSummary)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}

	cutPoint := cm.findCuttingPoint(messages)
	if cutPoint <= 0 {
		if !hasPreviousSummary {
			// Nothing old enough to summarize
			return nil, nil
		}
		cutPoint = len(messages)
	} else {
		cutPoint = alignToUserTurnBoundary(messages, cutPoint)
		if cutPoint <= 0 {
			if !hasPreviousSummary {
				return nil, nil
			}
			cutPoint = len(messages)
		}
	}

	oldMessages := messages[:cutPoint]
	prompt, droppedMessages, droppedTurns, estimatedTokens, budget, err := cm.buildBoundedSummaryPrompt(oldMessages, previousSummary, renderedTaskState, activeUserRequest, effectiveContextWindow)
	if err != nil {
		return nil, err
	}
	if droppedMessages > 0 {
		cm.logDebug("Compaction summary prompt emergency-trimmed: dropped_messages=%d dropped_turns=%d estimated_tokens=%d budget=%d effective_context=%d", droppedMessages, droppedTurns, estimatedTokens, budget, effectiveContextWindow)
	}

	return eventing.NewSummaryPromptCreated(originatorID, parentOriginatorID, parentOriginatorVersion, prompt), nil
}

func (cm *CompactionManager) GetActiveToolChainSummaryPromptEventWithActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string) (*types.Event, error) {
	return cm.GetActiveToolChainSummaryPromptEventWithContextLimitAndActiveRequest(originatorID, parentOriginatorID, parentOriginatorVersion, renderedTaskState, activeUserRequest, cm.contextWindow)
}

func (cm *CompactionManager) GetActiveToolChainSummaryPromptEventWithContextLimitAndActiveRequest(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, renderedTaskState, activeUserRequest string, effectiveContextWindow int) (*types.Event, error) {
	allEvents, err := cm.contextStore.GetAllContextEvents(originatorID)
	if err != nil {
		return nil, err
	}
	items, err := versionedConversationMessages(allEvents)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}

	activeSummaryEvent, previousSummary, meta, err := cm.getLatestActiveToolChainSummary(originatorID)
	if err != nil {
		return nil, err
	}
	activeUserVersion := meta.ActiveUserVersion
	previousCovers := meta.CoversThroughVersion
	activeUserIdx := findVersionedMessageIndex(items, activeUserVersion)
	latestUserIdx := latestUserMessageIndex(items)
	if activeUserIdx < 0 || latestUserIdx > activeUserIdx {
		activeUserIdx = latestUserIdx
		previousSummary = ""
		previousCovers = 0
		activeSummaryEvent = nil
		if activeUserIdx >= 0 {
			activeUserVersion = items[activeUserIdx].event.Originator.Version
		}
	}
	if activeUserIdx < 0 || activeUserVersion == 0 {
		return nil, nil
	}

	startVersion := activeUserVersion
	if previousCovers > startVersion {
		startVersion = previousCovers
	}
	candidates := make([]versionedMessage, 0, len(items))
	for _, item := range items {
		if item.event.Originator.Version > startVersion {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	candidateMessages := versionedMessagesOnly(candidates)
	cutPoint := cm.findCuttingPoint(candidateMessages)
	if cutPoint <= 0 {
		return nil, nil
	}
	boundary := latestCompletedToolChunkEndAtOrBefore(candidateMessages, cutPoint)
	if boundary <= 0 {
		return nil, nil
	}

	coversThrough := candidates[boundary-1].event.Originator.Version
	if coversThrough <= previousCovers || coversThrough <= activeUserVersion {
		return nil, nil
	}
	oldMessages := candidateMessages[:boundary]
	activeRequest := activeUserRequest
	if strings.TrimSpace(activeRequest) == "" {
		activeRequest = items[activeUserIdx].msg.Content
	}
	prompt, droppedMessages, droppedTurns, estimatedTokens, budget, err := cm.buildBoundedSummaryPrompt(oldMessages, previousSummary, renderedTaskState, activeRequest, effectiveContextWindow)
	if err != nil {
		return nil, err
	}
	if droppedMessages > 0 {
		cm.logDebug("Active tool-chain summary prompt emergency-trimmed: dropped_messages=%d dropped_turns=%d estimated_tokens=%d budget=%d effective_context=%d", droppedMessages, droppedTurns, estimatedTokens, budget, effectiveContextWindow)
	}
	if activeSummaryEvent != nil {
		cm.logDebug("Active tool-chain summary update: previous=%s active_user_version=%d previous_covers=%d new_covers=%d", eventDebugRef(activeSummaryEvent), activeUserVersion, previousCovers, coversThrough)
	}

	return eventing.NewSummaryPromptCreatedWithMetadata(originatorID, parentOriginatorID, parentOriginatorVersion, prompt, activeToolChainScope, activeUserVersion, coversThrough), nil
}

func (cm *CompactionManager) buildBoundedSummaryPrompt(oldMessages []llm.Message, previousSummary, renderedTaskState, activeUserRequest string, effectiveContextWindow int) (string, int, int, int, int, error) {
	budget := summaryPromptTokenBudget(effectiveContextWindow, cm.summaryTokens)
	if budget <= 0 {
		return "", 0, 0, 0, budget, fmt.Errorf("summary prompt budget is not positive: effective_context=%d summary_tokens=%d", effectiveContextWindow, cm.summaryTokens)
	}

	start := 0
	droppedTurns := 0
	for {
		droppedMessages := start
		convoMessages := oldMessages[start:]
		omission := summaryOmissionNotice(droppedMessages, droppedTurns)
		convo := cm.convertMessagesToConvo(convoMessages)
		prompt := buildSummaryPrompt(convo, previousSummary, renderedTaskState, activeUserRequest, omission)
		estimatedTokens := estimateTokens([]llm.Message{{Role: llm.RoleUser, Content: prompt}}, nil, 0)
		if estimatedTokens <= budget {
			return prompt, droppedMessages, droppedTurns, estimatedTokens, budget, nil
		}
		if start >= len(oldMessages) {
			return "", droppedMessages, droppedTurns, estimatedTokens, budget, fmt.Errorf("summary prompt still exceeds budget after trimming all summarizable messages: estimated_tokens=%d budget=%d effective_context=%d", estimatedTokens, budget, effectiveContextWindow)
		}
		nextStart := nextUserTurnStart(oldMessages, start)
		if nextStart <= start {
			nextStart = start + 1
		}
		start = nextStart
		droppedTurns++
	}
}

func summaryPromptTokenBudget(effectiveContextWindow, summaryTokens int) int {
	reserved := summaryTokens
	if reserved < summaryPromptMinReservedTokens {
		reserved = summaryPromptMinReservedTokens
	}
	available := effectiveContextWindow - reserved
	if available <= 0 {
		return 0
	}
	return available * summaryPromptBudgetNumerator / summaryPromptBudgetDenominator
}

func alignToUserTurnBoundary(messages []llm.Message, cutPoint int) int {
	if cutPoint > len(messages) {
		cutPoint = len(messages)
	}
	for cutPoint > 0 && cutPoint < len(messages) && messages[cutPoint].Role != llm.RoleUser {
		cutPoint--
	}
	return cutPoint
}

func nextUserTurnStart(messages []llm.Message, start int) int {
	if start >= len(messages) {
		return len(messages)
	}
	for i := start + 1; i < len(messages); i++ {
		if messages[i].Role == llm.RoleUser {
			return i
		}
	}
	return len(messages)
}

func summaryOmissionNotice(droppedMessages, droppedTurns int) string {
	if droppedMessages <= 0 {
		return ""
	}
	return fmt.Sprintf("<omitted-context>\nEmergency compaction trimmed %d oldest message(s) from %d complete conversation turn(s) so the summary request fits the model context. The remaining conversation and previous summary are authoritative.\n</omitted-context>", droppedMessages, droppedTurns)
}

func (cm *CompactionManager) getMessagesToSummarize(originatorID string, hasPreviousSummary bool) ([]llm.Message, error) {
	if !hasPreviousSummary {
		return cm.getConversationMessages(originatorID)
	}

	events, err := cm.findEventsSinceLastSummary(originatorID)
	if err != nil {
		return nil, err
	}

	return conversationMessages(events)
}

func (cm *CompactionManager) GetNewContext(originatorID string) ([]llm.Message, error) {
	lastSummaryEvent, previousSummary, validSummaryEvents, err := cm.getLatestSummaryEvent(originatorID)
	if err != nil {
		return nil, err
	}
	cm.logDebug("Compaction GetNewContext: originator=%s latest_valid_summary=%s valid_summaries=%d summary_bytes=%d", originatorID, eventDebugRef(lastSummaryEvent), len(validSummaryEvents), len(previousSummary))

	if lastSummaryEvent == nil {
		// No compaction ever: standard cut of all messages.
		messages, err := cm.getConversationMessages(originatorID)
		if err != nil {
			return nil, err
		}
		cutPoint := cm.findCuttingPoint(messages)
		if cutPoint < 0 {
			cutPoint = 0
		}
		cm.logDebug("Compaction GetNewContext: no valid summary; conversation_messages=%d cut_point=%d returned_messages=%d", len(messages), cutPoint, len(messages[cutPoint:]))
		return messages[cutPoint:], nil
	}
	if meta, ok := summaryEventMetadata(lastSummaryEvent); ok && meta.CompactionScope == activeToolChainScope && meta.ActiveUserVersion > 0 && meta.CoversThroughVersion > 0 {
		return cm.getActiveToolChainContext(originatorID, previousSummary, meta)
	}

	// Compacted session. Build context in chronological order:
	//   [keepRecent from before last summary] [last summary] [msgs after last summary]

	// Collect the epoch events: from the previous summary (or from the start if this is
	// the first summary) through to the end of the context store.
	var epochEvents []*types.Event
	if len(validSummaryEvents) >= 2 {
		prevSummaryEvent := validSummaryEvents[len(validSummaryEvents)-2]
		epochEvents, err = cm.contextStore.GetEventsFrom(prevSummaryEvent)
	} else {
		epochEvents, err = cm.contextStore.GetAllContextEvents(originatorID)
	}
	if err != nil {
		return nil, err
	}
	cm.logDebug("Compaction GetNewContext: epoch_events=%d previous_valid_summary_count=%d from_previous_summary=%t", len(epochEvents), len(validSummaryEvents)-1, len(validSummaryEvents) >= 2)

	// Split epoch events into: before the last summary and after it.
	var beforeLast, afterLast []*types.Event
	pastLast := false
	for _, e := range epochEvents {
		if e.Originator.ID == lastSummaryEvent.Originator.ID &&
			e.Originator.Version == lastSummaryEvent.Originator.Version {
			pastLast = true
			continue
		}
		if !pastLast {
			beforeLast = append(beforeLast, e)
		} else {
			afterLast = append(afterLast, e)
		}
	}
	cm.logDebug("Compaction GetNewContext: split around summary=%s before_events=%d after_events=%d before_types=%s after_types=%s", eventDebugRef(lastSummaryEvent), len(beforeLast), len(afterLast), eventTypeCounts(beforeLast), eventTypeCounts(afterLast))

	beforeLastMsgs, err := conversationMessages(beforeLast)
	if err != nil {
		return nil, err
	}
	afterLastMsgs, err := conversationMessages(afterLast)
	if err != nil {
		return nil, err
	}
	cm.logDebug("Compaction GetNewContext: conversation_messages before=%d after=%d before_roles=%s after_roles=%s", len(beforeLastMsgs), len(afterLastMsgs), messageRoleCounts(beforeLastMsgs), messageRoleCounts(afterLastMsgs))

	cutPoint := cm.findCuttingPoint(beforeLastMsgs)
	if cutPoint < 0 {
		cutPoint = 0
	}
	// Retreat to the nearest preceding user message so keepRecent starts at a
	// clean turn boundary. Going backward (not forward) ensures we never lose
	// all context when the cut lands in a tail of tool-call sequences that has
	// no following user message.
	for cutPoint > 0 && beforeLastMsgs[cutPoint].Role != llm.RoleUser {
		cutPoint--
	}
	keepRecent := beforeLastMsgs[cutPoint:]
	cm.logDebug("Compaction GetNewContext: keep_recent cut_point=%d keep_messages=%d summary_bytes=%d", cutPoint, len(keepRecent), len(previousSummary))

	summaryUserMsg := llm.Message{
		Role:    llm.RoleUser,
		Content: fmt.Sprintf("The conversation history before this point was compacted into the following summary:\n\n<summary>\n%s\n</summary>", previousSummary),
	}

	// Chronological order: keepRecent → summary (compaction point) → new msgs
	result := make([]llm.Message, 0, len(keepRecent)+1+len(afterLastMsgs))
	result = append(result, keepRecent...)
	result = append(result, summaryUserMsg)
	result = append(result, afterLastMsgs...)
	cm.logDebug("Compaction GetNewContext: returned_messages=%d roles=%s", len(result), messageRoleCounts(result))
	return result, nil
}

// RestoreContext returns the active conversation context for session restore.
// Unlike GetNewContext it does not cut uncompacted sessions.
func (cm *CompactionManager) RestoreContext(originatorID string) ([]llm.Message, error) {
	_, previousSummary, _, err := cm.getLatestSummaryEvent(originatorID)
	if err != nil {
		return nil, err
	}
	allEvents, allEventsErr := cm.contextStore.GetAllContextEvents(originatorID)
	if allEventsErr != nil {
		return nil, allEventsErr
	}
	cm.restoreTokenCalibration(allEvents)

	cm.logDebug("RestoreContext: originator=%s raw_context_events=%d event_types=%s latest_summary_bytes=%d", originatorID, len(allEvents), eventTypeCounts(allEvents), len(previousSummary))

	if previousSummary == "" {
		// No compaction ever: return all events without cutting.
		messages, err := conversationMessages(allEvents)
		if err != nil {
			return nil, err
		}
		validation := validateConversationForRestore(messages)
		trimmed, reason := trimValidationResultWithReason(validation)
		cm.logDebug("RestoreContext: no valid summary; messages_before_trim=%d roles_before=%s messages_after_trim=%d roles_after=%s trim=%s", len(messages), messageRoleCounts(messages), len(trimmed), messageRoleCounts(trimmed), reason)
		return trimmed, nil
	}

	// Compacted session: same logic as GetNewContext.
	messages, err := cm.GetNewContext(originatorID)
	if err != nil {
		return nil, err
	}
	validation := validateConversationForRestore(messages)
	trimmed, reason := trimValidationResultWithReason(validation)
	cm.logDebug("RestoreContext: compacted; messages_before_trim=%d roles_before=%s messages_after_trim=%d roles_after=%s trim=%s", len(messages), messageRoleCounts(messages), len(trimmed), messageRoleCounts(trimmed), reason)
	return trimmed, nil
}

func (cm *CompactionManager) restoreTokenCalibration(events []*types.Event) {
	if multiplier, ok := latestTokenEstimateMultiplier(events); ok {
		cm.tokenEstimateMultiplier = multiplier
	}
}

func latestTokenEstimateMultiplier(events []*types.Event) (float64, bool) {
	latestVersion := uint64(0)
	latestMultiplier := 0.0
	found := false
	for _, event := range events {
		multiplier := 0.0
		switch event.EventType {
		case eventing.LLMAgentMessageCreated:
			var payload eventing.LLMAgentMessage
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				continue
			}
			multiplier = payload.TokenEstimateMultiplier
		case eventing.LLMAgentResponseCreated:
			var payload eventing.LLMAgentResponse
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				continue
			}
			multiplier = payload.TokenEstimateMultiplier
		default:
			continue
		}
		if multiplier < tokenEstimateMinCalibration || multiplier > tokenEstimateMaxCalibration {
			continue
		}
		if !found || event.Originator.Version >= latestVersion {
			latestVersion = event.Originator.Version
			latestMultiplier = multiplier
			found = true
		}
	}
	return latestMultiplier, found
}

func (cm *CompactionManager) findEventsSinceLastSummary(originatorID string) ([]*types.Event, error) {
	latestSummary, _, _, err := cm.getLatestSummaryEvent(originatorID)
	if err != nil {
		return nil, err
	}

	if latestSummary == nil {
		return cm.contextStore.GetAllContextEvents(originatorID)
	}

	return cm.contextStore.GetEventsFrom(latestSummary)
}

func (cm *CompactionManager) getLatestActiveToolChainSummary(originatorID string) (*types.Event, string, summaryMetadata, error) {
	summaryEvents, err := cm.contextStore.GetSummaryEvents(originatorID)
	if err != nil {
		return nil, "", summaryMetadata{}, err
	}
	for i := len(summaryEvents) - 1; i >= 0; i-- {
		meta, ok := summaryEventMetadata(summaryEvents[i])
		if !ok || meta.CompactionScope != activeToolChainScope {
			continue
		}
		msg, err := eventing.EventToLLMMessage(summaryEvents[i])
		if err != nil {
			return nil, "", summaryMetadata{}, err
		}
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		return summaryEvents[i], msg.Content, meta, nil
	}
	return nil, "", summaryMetadata{}, nil
}

func (cm *CompactionManager) getLatestSummaryEvent(originatorID string) (*types.Event, string, []*types.Event, error) {
	summaryEvents, err := cm.contextStore.GetSummaryEvents(originatorID)
	if err != nil {
		return nil, "", nil, err
	}
	validSummaryEvents := make([]*types.Event, 0, len(summaryEvents))
	validSummaryContents := make([]string, 0, len(summaryEvents))
	blankSkipped := 0
	for i := len(summaryEvents) - 1; i >= 0; i-- {
		msg, err := eventing.EventToLLMMessage(summaryEvents[i])
		if err != nil {
			return nil, "", nil, err
		}
		if strings.TrimSpace(msg.Content) != "" {
			validSummaryEvents = append(validSummaryEvents, summaryEvents[i])
			validSummaryContents = append(validSummaryContents, msg.Content)
		} else {
			blankSkipped++
		}
	}
	for i, j := 0, len(validSummaryEvents)-1; i < j; i, j = i+1, j-1 {
		validSummaryEvents[i], validSummaryEvents[j] = validSummaryEvents[j], validSummaryEvents[i]
		validSummaryContents[i], validSummaryContents[j] = validSummaryContents[j], validSummaryContents[i]
	}
	if len(validSummaryEvents) == 0 {
		cm.logDebug("Compaction summaries: total=%d valid=0 blank_skipped=%d", len(summaryEvents), blankSkipped)
		return nil, "", nil, nil
	}
	cm.logDebug("Compaction summaries: total=%d valid=%d blank_skipped=%d selected=%s selected_bytes=%d", len(summaryEvents), len(validSummaryEvents), blankSkipped, eventDebugRef(validSummaryEvents[len(validSummaryEvents)-1]), len(validSummaryContents[len(validSummaryContents)-1]))
	return validSummaryEvents[len(validSummaryEvents)-1], validSummaryContents[len(validSummaryContents)-1], validSummaryEvents, nil
}

func (cm *CompactionManager) getActiveToolChainContext(originatorID, previousSummary string, meta summaryMetadata) ([]llm.Message, error) {
	allEvents, err := cm.contextStore.GetAllContextEvents(originatorID)
	if err != nil {
		return nil, err
	}
	items, err := versionedConversationMessages(allEvents)
	if err != nil {
		return nil, err
	}
	activeUserIdx := findVersionedMessageIndex(items, meta.ActiveUserVersion)
	if activeUserIdx < 0 {
		return nil, nil
	}

	result := []llm.Message{items[activeUserIdx].msg}
	result = append(result, llm.Message{
		Role:    llm.RoleUser,
		Content: fmt.Sprintf("The active tool-call history before this point was compacted into the following summary:\n\n<summary>\n%s\n</summary>", previousSummary),
	})
	for _, item := range items {
		if item.event.Originator.Version > meta.CoversThroughVersion {
			result = append(result, item.msg)
		}
	}
	cm.logDebug("Compaction active tool-chain context: active_user_version=%d covers_through=%d returned_messages=%d roles=%s", meta.ActiveUserVersion, meta.CoversThroughVersion, len(result), messageRoleCounts(result))
	return result, nil
}

func summaryEventMetadata(e *types.Event) (summaryMetadata, bool) {
	if e == nil || e.EventType != eventing.LLMAgentSummaryResponseCreated {
		return summaryMetadata{}, false
	}
	var payload eventing.LLMAgentResponse
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return summaryMetadata{}, false
	}
	if payload.CompactionScope == "" && payload.ActiveUserVersion == 0 && payload.CoversThroughVersion == 0 {
		return summaryMetadata{}, false
	}
	return summaryMetadata{
		CompactionScope:      payload.CompactionScope,
		ActiveUserVersion:    payload.ActiveUserVersion,
		CoversThroughVersion: payload.CoversThroughVersion,
	}, true
}

func versionedConversationMessages(events []*types.Event) ([]versionedMessage, error) {
	out := make([]versionedMessage, 0, len(events))
	for _, e := range events {
		switch e.EventType {
		case eventing.SummaryPromptCreated, eventing.LLMAgentSummaryResponseCreated, eventing.TaskUpdatedCreated, eventing.TaskSnapshotCreated:
			continue
		case obsoleteCheckpointEventType:
			continue
		}
		msg, err := eventing.EventToLLMMessage(e)
		if err != nil {
			return nil, err
		}
		out = append(out, versionedMessage{event: e, msg: msg})
	}
	return out, nil
}

func versionedMessagesOnly(items []versionedMessage) []llm.Message {
	out := make([]llm.Message, len(items))
	for i, item := range items {
		out[i] = item.msg
	}
	return out
}

func findVersionedMessageIndex(items []versionedMessage, version uint64) int {
	if version == 0 {
		return -1
	}
	for i, item := range items {
		if item.event.Originator.Version == version {
			return i
		}
	}
	return -1
}

func latestUserMessageIndex(items []versionedMessage) int {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].msg.Role == llm.RoleUser {
			return i
		}
	}
	return -1
}

func latestCompletedToolChunkEndAtOrBefore(messages []llm.Message, limit int) int {
	if limit > len(messages) {
		limit = len(messages)
	}
	best := 0
	for i := 0; i < limit; i++ {
		end := completedToolChunkEnd(messages, i)
		if end > i && end <= limit {
			best = end
			i = end - 1
		}
	}
	return best
}

func completedToolChunkEnd(messages []llm.Message, assistantIdx int) int {
	if assistantIdx < 0 || assistantIdx >= len(messages) {
		return -1
	}
	msg := messages[assistantIdx]
	if msg.Role != llm.RoleAssistant || len(msg.ToolCalls) == 0 {
		return -1
	}
	pending := map[string]bool{}
	for _, tc := range msg.ToolCalls {
		if tc.ID == "" {
			return -1
		}
		pending[tc.ID] = true
	}
	idx := assistantIdx + 1
	for idx < len(messages) && messages[idx].Role == llm.RoleTool {
		if !pending[messages[idx].ToolCallID] {
			return -1
		}
		delete(pending, messages[idx].ToolCallID)
		idx++
		if len(pending) == 0 {
			return idx
		}
	}
	return -1
}

func conversationMessages(events []*types.Event) ([]llm.Message, error) {
	conversationEvents := make([]*types.Event, 0, len(events))
	for _, e := range events {
		switch e.EventType {
		case eventing.SummaryPromptCreated, eventing.LLMAgentSummaryResponseCreated, eventing.TaskUpdatedCreated, eventing.TaskSnapshotCreated:
			continue
		case obsoleteCheckpointEventType:
			continue
		}
		conversationEvents = append(conversationEvents, e)
	}
	return eventing.EventsToLLMMessages(conversationEvents)
}

func trimIncompleteToolTail(messages []llm.Message) []llm.Message {
	trimmed, _ := trimIncompleteToolTailWithReason(messages)
	return trimmed
}

func trimIncompleteToolTailWithReason(messages []llm.Message) ([]llm.Message, string) {
	validation := validateConversationForRestore(messages)
	return trimValidationResultWithReason(validation)
}

func trimValidationResultWithReason(validation ConversationValidationResult) ([]llm.Message, string) {
	if validation.DroppedMessages == 0 {
		return validation.Messages, "none"
	}
	reasons := make([]string, 0, len(validation.Findings))
	for _, finding := range validation.Findings {
		if finding.DroppedMessages == 0 {
			continue
		}
		switch finding.Kind {
		case "overlapping_assistant_tool_call":
			reasons = append(reasons, fmt.Sprintf("dropped turn before overlapping assistant tool-call index=%d dropped=%d", finding.MessageIndex, finding.DroppedMessages))
		case "orphan_tool_result":
			reasons = append(reasons, fmt.Sprintf("dropped orphan tool result index=%d tool_call_id=%q", finding.MessageIndex, finding.ToolCallID))
		case "incomplete_tool_turn_before_user_or_system":
			reasons = append(reasons, fmt.Sprintf("dropped incomplete tool turn before %s index=%d pending_tools=%d awaiting_final=%t dropped=%d", finding.Role, finding.MessageIndex, len(finding.PendingToolCallIDs), finding.AwaitingFinalAssistant, finding.DroppedMessages))
		case "incomplete_tool_turn_before_role":
			reasons = append(reasons, fmt.Sprintf("dropped incomplete tool turn before role=%s index=%d pending_tools=%d awaiting_final=%t dropped=%d", finding.Role, finding.MessageIndex, len(finding.PendingToolCallIDs), finding.AwaitingFinalAssistant, finding.DroppedMessages))
		case "incomplete_tool_tail":
			reasons = append(reasons, fmt.Sprintf("dropped tail pending_tools=%d awaiting_final=%t dropped=%d", len(finding.PendingToolCallIDs), finding.AwaitingFinalAssistant, finding.DroppedMessages))
		default:
			reasons = append(reasons, fmt.Sprintf("dropped messages kind=%s index=%d dropped=%d", finding.Kind, finding.MessageIndex, finding.DroppedMessages))
		}
	}
	return validation.Messages, fmt.Sprintf("%s; kept=%d dropped=%d", strings.Join(reasons, "; "), len(validation.Messages), validation.DroppedMessages)
}

func validateConversationForRestore(messages []llm.Message) ConversationValidationResult {
	out := make([]llm.Message, 0, len(messages))
	currentTurnStart := 0
	pendingTools := map[string]bool{}
	awaitingFinalAssistant := false
	droppedMessages := 0
	findings := []ConversationValidationFinding{}
	for i, msg := range messages {
		switch msg.Role {
		case llm.RoleAssistant:
			if len(msg.ToolCalls) == 0 {
				out = append(out, msg)
				pendingTools = map[string]bool{}
				awaitingFinalAssistant = false
				continue
			}
			if len(pendingTools) > 0 {
				dropped := len(out) - currentTurnStart
				out = out[:currentTurnStart]
				droppedMessages += dropped
				findings = append(findings, ConversationValidationFinding{
					Kind:                   "overlapping_assistant_tool_call",
					MessageIndex:           i,
					Role:                   msg.Role,
					PendingToolCallIDs:     pendingToolCallIDs(pendingTools),
					AwaitingFinalAssistant: awaitingFinalAssistant,
					DroppedMessages:        dropped,
				})
				pendingTools = map[string]bool{}
				awaitingFinalAssistant = false
				continue
			}
			out = append(out, msg)
			pendingTools = map[string]bool{}
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					pendingTools[tc.ID] = true
				}
			}
			awaitingFinalAssistant = true
		case llm.RoleTool:
			if !awaitingFinalAssistant || len(pendingTools) == 0 || msg.ToolCallID == "" || !pendingTools[msg.ToolCallID] {
				droppedMessages++
				findings = append(findings, ConversationValidationFinding{
					Kind:                   "orphan_tool_result",
					MessageIndex:           i,
					Role:                   msg.Role,
					ToolCallID:             msg.ToolCallID,
					PendingToolCallIDs:     pendingToolCallIDs(pendingTools),
					AwaitingFinalAssistant: awaitingFinalAssistant,
					DroppedMessages:        1,
				})
				continue
			}
			out = append(out, msg)
			delete(pendingTools, msg.ToolCallID)
		case llm.RoleUser, llm.RoleSystem:
			if awaitingFinalAssistant || len(pendingTools) > 0 {
				dropped := len(out) - currentTurnStart
				out = out[:currentTurnStart]
				droppedMessages += dropped
				findings = append(findings, ConversationValidationFinding{
					Kind:                   "incomplete_tool_turn_before_user_or_system",
					MessageIndex:           i,
					Role:                   msg.Role,
					PendingToolCallIDs:     pendingToolCallIDs(pendingTools),
					AwaitingFinalAssistant: awaitingFinalAssistant,
					DroppedMessages:        dropped,
				})
				pendingTools = map[string]bool{}
				awaitingFinalAssistant = false
			}
			out = append(out, msg)
			currentTurnStart = len(out) - 1
		default:
			if awaitingFinalAssistant || len(pendingTools) > 0 {
				dropped := len(out) - currentTurnStart
				out = out[:currentTurnStart]
				droppedMessages += dropped
				findings = append(findings, ConversationValidationFinding{
					Kind:                   "incomplete_tool_turn_before_role",
					MessageIndex:           i,
					Role:                   msg.Role,
					PendingToolCallIDs:     pendingToolCallIDs(pendingTools),
					AwaitingFinalAssistant: awaitingFinalAssistant,
					DroppedMessages:        dropped,
				})
				pendingTools = map[string]bool{}
				awaitingFinalAssistant = false
			}
			out = append(out, msg)
		}
	}
	if awaitingFinalAssistant || len(pendingTools) > 0 {
		dropped := len(out) - currentTurnStart
		out = out[:currentTurnStart]
		droppedMessages += dropped
		findings = append(findings, ConversationValidationFinding{
			Kind:                   "incomplete_tool_tail",
			MessageIndex:           len(messages),
			PendingToolCallIDs:     pendingToolCallIDs(pendingTools),
			AwaitingFinalAssistant: awaitingFinalAssistant,
			DroppedMessages:        dropped,
		})
	}
	return ConversationValidationResult{
		Messages:        cloneLLMMessages(out),
		Findings:        findings,
		DroppedMessages: droppedMessages,
	}
}

func pendingToolCallIDs(pendingTools map[string]bool) []string {
	ids := make([]string, 0, len(pendingTools))
	for id := range pendingTools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func cloneLLMMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		out[i].ToolCalls = append([]llm.ToolCall{}, msg.ToolCalls...)
	}
	return out
}

func eventDebugRef(e *types.Event) string {
	if e == nil {
		return "<none>"
	}
	return fmt.Sprintf("%s@%d/%s", e.Originator.ID, e.Originator.Version, e.EventType)
}

func eventTypeCounts(events []*types.Event) string {
	if len(events) == 0 {
		return "{}"
	}
	counts := map[string]int{}
	for _, e := range events {
		counts[e.EventType]++
	}
	parts := make([]string, 0, len(counts))
	for eventType, count := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", eventType, count))
	}
	return strings.Join(parts, ",")
}

func messageRoleCounts(messages []llm.Message) string {
	if len(messages) == 0 {
		return "{}"
	}
	counts := map[llm.Role]int{}
	toolCalls := 0
	contentBytes := 0
	for _, msg := range messages {
		counts[msg.Role]++
		toolCalls += len(msg.ToolCalls)
		contentBytes += len(msg.Content)
	}
	parts := make([]string, 0, len(counts)+2)
	for role, count := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", role, count))
	}
	parts = append(parts, fmt.Sprintf("tool_calls=%d", toolCalls))
	parts = append(parts, fmt.Sprintf("content_bytes=%d", contentBytes))
	return strings.Join(parts, ",")
}

func (cm *CompactionManager) findCuttingPoint(messages []llm.Message) int {
	if len(messages) == 0 {
		return -1
	}

	keepRecentTokens := cm.effectiveKeepRecentTokens()
	totalTokens := 0
	for i := len(messages) - 1; i >= 0; i-- {
		totalTokens += estimateMessageTokens(messages[i])
		if totalTokens < keepRecentTokens || messages[i].Role == llm.RoleTool {
			continue
		}

		return i
	}

	// all message tokens are within the keepRecentTokens limit, we can keep all messages
	return 0
}

func (cm *CompactionManager) convertMessagesToConvo(messages []llm.Message) string {
	var parts []string
	for _, msg := range messages {
		part := formatMessageForConvo(msg)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return "<conversation>\n" + strings.Join(parts, "\n\n") + "\n</conversation>"
}

func formatMessageForConvo(msg llm.Message) string {
	switch msg.Role {
	case llm.RoleUser:
		return "[User]: " + msg.Content
	case llm.RoleAssistant:
		var sections []string
		if msg.Content != "" {
			sections = append(sections, "[Assistant]: "+msg.Content)
		}
		for _, tc := range msg.ToolCalls {
			sections = append(sections, fmt.Sprintf("[Assistant tool calls]: %s(%s)", tc.Name, tc.Arguments))
		}
		return strings.Join(sections, "\n")
	case llm.RoleTool:
		content := msg.Content
		if len(content) > toolResultMaxLen {
			content = staleToolResultDigest(content)
		}
		return "[Tool result]: " + content
	default:
		return ""
	}
}

func staleToolResultDigest(content string) string {
	snippet := strings.Join(strings.Fields(content), " ")
	if len(snippet) > staleToolResultDigestMaxLen {
		snippet = snippet[:staleToolResultDigestMaxLen]
	}
	return fmt.Sprintf("[stale oversized tool result pruned before summarization: original_size=%d chars; prefix=%q]", len(content), snippet)
}

func buildInitialSummaryPrompt(convo, renderedTaskState, activeUserRequest string) string {
	return convo + activeUserRequestForPrompt(activeUserRequest) + renderedTaskStateForPrompt(renderedTaskState) + "\n\n" + initialSummaryPromptText
}

func buildUpdateSummaryPrompt(convo, previousSummary, renderedTaskState, activeUserRequest string) string {
	return convo + activeUserRequestForPrompt(activeUserRequest) + renderedTaskStateForPrompt(renderedTaskState) + "\n\n<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n" + updateSummaryPromptText
}

func buildSummaryPrompt(convo, previousSummary, renderedTaskState, activeUserRequest, omissionNotice string) string {
	if strings.TrimSpace(omissionNotice) != "" {
		convo += "\n\n" + omissionNotice
	}
	if previousSummary == "" {
		return buildInitialSummaryPrompt(convo, renderedTaskState, activeUserRequest)
	}
	return buildUpdateSummaryPrompt(convo, previousSummary, renderedTaskState, activeUserRequest)
}

func activeUserRequestForPrompt(activeUserRequest string) string {
	activeUserRequest = strings.TrimSpace(activeUserRequest)
	if activeUserRequest == "" {
		return ""
	}
	return "\n\n<active-user-request>\n" + activeUserRequest + "\n</active-user-request>\n\nPreserve <active-user-request> as the current user request being worked on, even if the raw user message is outside the recent context kept after compaction."
}

func renderedTaskStateForPrompt(renderedTaskState string) string {
	renderedTaskState = strings.TrimSpace(renderedTaskState)
	if renderedTaskState == "" {
		return ""
	}
	return "\n\n<current-task-state>\n" + renderedTaskState + "\n</current-task-state>\n\nUse <current-task-state> as the authoritative source for task progress, done work, active work, blocked work, and pending work when updating the summary."
}

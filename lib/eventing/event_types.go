package eventing

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

const maxPersistedToolResultChars = 24 * 1024

const (
	// those are events for events log
	LogMessageUserCreated       = "LogMessage.UserCreated"
	LogMessageAgentCreated      = "LogMessage.AgentCreated"
	LogMessageAgentInterrupted  = "LogMessage.AgentInterrupted"
	FileSent                    = "File.Sent"
	SubagentInvocationStarted   = "SubagentInvocation.Started"
	SubagentInvocationProgress  = "SubagentInvocation.Progress"
	SubagentInvocationCompleted = "SubagentInvocation.Completed"
	SubagentInvocationFailed    = "SubagentInvocation.Failed"
	// StatusUpdated reports progress of a long-running command, such as a
	// local model server restart, to the user. It never enters LLM context.
	StatusUpdated             = "Status.Updated"
	PermissionRequested       = "Permission.Requested"
	PermissionResolved        = "Permission.Resolved"
	GoalCreated               = "Goal.Created"
	GoalDiscoveryCompleted    = "Goal.DiscoveryCompleted"
	GoalPlanCreated           = "Goal.PlanCreated"
	GoalApproved              = "Goal.Approved"
	GoalRunStarted            = "Goal.RunStarted"
	GoalTaskStarted           = "Goal.TaskStarted"
	GoalTaskCompleted         = "Goal.TaskCompleted"
	GoalTaskChecked           = "Goal.TaskChecked"
	GoalVerificationStarted   = "Goal.VerificationStarted"
	GoalVerificationCompleted = "Goal.VerificationCompleted"
	GoalReviewed              = "Goal.Reviewed"
	GoalJudged                = "Goal.Judged"
	GoalEscalated             = "Goal.Escalated"
	GoalAnswered              = "Goal.Answered"
	GoalEditRequested         = "Goal.EditRequested"
	GoalRetryRequested        = "Goal.RetryRequested"
	GoalReplanCreated         = "Goal.ReplanCreated"
	GoalPaused                = "Goal.Paused"
	GoalInterrupted           = "Goal.Interrupted"
	GoalCompleted             = "Goal.Completed"
	GoalStopped               = "Goal.Stopped"
	GoalStalled               = "Goal.Stalled"
	GoalBudgetExceeded        = "Goal.BudgetExceeded"

	// those are events for events log, they all should share
	// the same parent id originator id coming from events log
	LLMUserMessageCreated              = "LLMUserMessage.Created"
	LLMAgentMessageCreated             = "LLMAgentMessage.Created"
	LLMReasoningDeltaCreated           = "LLMReasoningDelta.Created"
	LLMToolCallResultCreated           = "LLMToolCallResult.Created"
	LLMToolCallRecoveryResultCreated   = "LLMToolCallRecoveryResult.Created"
	LLMAgentResponseCreated            = "LLMAgentResponse.Created"
	LLMAgentInterruptedResponseCreated = "LLMAgentInterruptedResponse.Created"
	LLMAgentSummaryResponseCreated     = "LLMAgentSummaryResponse.Created"
	LLMRequestRetryScheduled           = "LLMRequest.RetryScheduled"
	SummaryPromptCreated               = "SummaryPrompt.Created"
	TaskUpdatedCreated                 = "TaskUpdated.Created"
	TaskSnapshotCreated                = "TaskSnapshot.Created"
	CompactionCompletedCreated         = "CompactionCompleted.Created"
)

var GoalEventTypes = []string{
	GoalCreated, GoalDiscoveryCompleted, GoalPlanCreated, GoalApproved, GoalRunStarted,
	GoalTaskStarted, GoalTaskCompleted, GoalTaskChecked, GoalVerificationStarted,
	GoalVerificationCompleted, GoalReviewed, GoalJudged, GoalEscalated, GoalReplanCreated,
	GoalPaused, GoalAnswered, GoalEditRequested, GoalRetryRequested, GoalInterrupted,
	GoalCompleted, GoalStopped, GoalStalled, GoalBudgetExceeded,
}

// GoalEvent is one fact in a goal's event log, the goal's source of truth.
// Data carries the typed payload the goal reducer folds; the other fields
// are a readable summary for timelines.
type GoalEvent struct {
	// Type is the envelope event type; it is not part of the payload.
	Type       string          `json:"-"`
	GoalID     string          `json:"goal_id"`
	Revision   int             `json:"revision"`
	At         time.Time       `json:"at"`
	Goal       string          `json:"goal,omitempty"`
	Stage      string          `json:"stage"`
	Status     string          `json:"status"`
	Summary    string          `json:"summary,omitempty"`
	NextAction string          `json:"next_action,omitempty"`
	TaskID     string          `json:"task_id,omitempty"`
	TaskTitle  string          `json:"task_title,omitempty"`
	TaskStatus string          `json:"task_status,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

func NewGoalEvent(originatorID, eventType string, payload GoalEvent) *types.Event {
	data, _ := json.Marshal(payload)
	return types.NewEvent(types.NewOriginator(originatorID, 0), eventType, string(data))
}

func IsGoalEvent(eventType string) bool {
	for _, candidate := range GoalEventTypes {
		if eventType == candidate {
			return true
		}
	}
	return false
}

const (
	PermissionDecisionAllow        = "allow"
	PermissionDecisionAllowSession = "allow_session"
	PermissionDecisionDeny         = "deny"
	PermissionDecisionCancelled    = "cancelled"
	PermissionDecisionExpired      = "expired"
)

// PermissionRequest is a durable, adapter-neutral request to relax one tool
// capability for one tool-call attempt.
type PermissionRequest struct {
	RequestID     string    `json:"request_id"`
	SessionID     string    `json:"session_id"`
	ToolCallID    string    `json:"tool_call_id"`
	ToolName      string    `json:"tool_name"`
	ToolArguments string    `json:"tool_arguments"`
	Capability    string    `json:"capability"`
	Target        string    `json:"target"`
	Command       string    `json:"command,omitempty"`
	Reason        string    `json:"reason"`
	MayHaveRun    bool      `json:"may_have_run,omitempty"`
	Recursive     bool      `json:"recursive,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
	// Details carries capability-specific values a client may render.
	Details map[string]string `json:"details,omitempty"`
	// Preview is the exact content an approval would release, such as a
	// payload sent to an external service.
	Preview string `json:"preview,omitempty"`
	// NoSessionGrant means approving this request never approves the next
	// one, for requests whose content differs every time.
	NoSessionGrant bool `json:"no_session_grant,omitempty"`
}

// PermissionMetadataPayload carries a user-edited replacement for
// PermissionRequest.Preview in PermissionResolution.Metadata.
const PermissionMetadataPayload = "payload"

type PermissionResolution struct {
	RequestID string            `json:"request_id"`
	Decision  string            `json:"decision"`
	Resolver  string            `json:"resolver,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func NewPermissionRequested(originatorID string, request PermissionRequest) *types.Event {
	payload, _ := json.Marshal(request)
	return types.NewEvent(types.NewOriginator(originatorID, 0), PermissionRequested, string(payload))
}

func NewPermissionResolved(originatorID string, resolution PermissionResolution) *types.Event {
	payload, _ := json.Marshal(resolution)
	return types.NewEvent(types.NewOriginator(originatorID, 0), PermissionResolved, string(payload))
}

func DecodePermissionRequest(event *types.Event) (PermissionRequest, error) {
	var request PermissionRequest
	if event == nil || event.EventType != PermissionRequested {
		return request, fmt.Errorf("expected event type %s", PermissionRequested)
	}
	if err := json.Unmarshal([]byte(event.Payload), &request); err != nil {
		return request, err
	}
	return request, nil
}

func DecodePermissionResolution(event *types.Event) (PermissionResolution, error) {
	var resolution PermissionResolution
	if event == nil || event.EventType != PermissionResolved {
		return resolution, fmt.Errorf("expected event type %s", PermissionResolved)
	}
	if err := json.Unmarshal([]byte(event.Payload), &resolution); err != nil {
		return resolution, err
	}
	return resolution, nil
}

type LogMessage struct {
	Content        string              `json:"content"`
	DisplayContent string              `json:"display_content,omitempty"`
	Attachments    []MessageAttachment `json:"attachments,omitempty"`
	InReplyTo      string              `json:"in_reply_to,omitempty"`
	MessageID      string              `json:"message_id,omitempty"`
	Adapter        string              `json:"adapter,omitempty"`
	ChannelID      string              `json:"channel_id,omitempty"`
	UserID         string              `json:"user_id,omitempty"`
}

// MessageAttachment is durable, client-safe metadata for an uploaded user
// attachment. Local filesystem paths belong only in LogMessage.Content.
type MessageAttachment struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// SentFile is durable, client-safe metadata for a file delivered by an agent.
// The file bytes live in session-owned storage and are never embedded in events.
type SentFile struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	MimeType  string `json:"mime_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Caption   string `json:"caption,omitempty"`
	InReplyTo string `json:"in_reply_to,omitempty"`
}

func NewSentFile(originatorID string, file SentFile) *types.Event {
	payload, _ := json.Marshal(file)
	return types.NewEvent(types.NewOriginator(originatorID, 0), FileSent, string(payload))
}

func DecodeSentFile(event *types.Event) (SentFile, error) {
	var file SentFile
	if event == nil || event.EventType != FileSent {
		return file, fmt.Errorf("expected event type %s", FileSent)
	}
	if err := json.Unmarshal([]byte(event.Payload), &file); err != nil {
		return file, err
	}
	return file, nil
}

func NewLogMessage(originatorID string, content string) *types.Event {
	return NewLogMessageWithID(originatorID, newMessageID(), content)
}

func NewLogMessageWithID(originatorID, messageID, content string) *types.Event {
	logMessage := LogMessage{
		MessageID: messageID,
		Content:   content,
	}

	logMessagePayload, _ := json.Marshal(logMessage)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LogMessageUserCreated, string(logMessagePayload))
}

func NewLogMessageWithMetadata(originatorID, messageID, content, adapter, channelID, userID string) *types.Event {
	logMessage := LogMessage{
		MessageID: messageID,
		Content:   content,
		Adapter:   adapter,
		ChannelID: channelID,
		UserID:    userID,
	}

	logMessagePayload, _ := json.Marshal(logMessage)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LogMessageUserCreated, string(logMessagePayload))
}

func NewLogMessageWithAttachments(originatorID, messageID, content, displayContent, adapter, channelID, userID string, attachments []MessageAttachment) *types.Event {
	if messageID == "" {
		messageID = newMessageID()
	}
	logMessage := LogMessage{
		MessageID:      messageID,
		Content:        content,
		DisplayContent: displayContent,
		Attachments:    append([]MessageAttachment(nil), attachments...),
		Adapter:        adapter,
		ChannelID:      channelID,
		UserID:         userID,
	}
	payload, _ := json.Marshal(logMessage)
	return types.NewEvent(types.NewOriginator(originatorID, 0), LogMessageUserCreated, string(payload))
}

func NewAgentLogMessage(originatorID string, content string) *types.Event {
	return NewAgentLogMessageInReplyTo(originatorID, newMessageID(), "", content)
}

func newMessageID() string {
	return uuid.NewV4().String()
}

func NewAgentLogMessageInReplyTo(originatorID, messageID, inReplyTo, content string) *types.Event {
	return NewAgentLogMessageInReplyToWithMetadata(originatorID, messageID, inReplyTo, content, "", "")
}

func NewAgentLogMessageInReplyToWithMetadata(originatorID, messageID, inReplyTo, content, adapter, channelID string) *types.Event {
	if messageID == "" {
		messageID = newMessageID()
	}
	logMessage := LogMessage{
		MessageID: messageID,
		InReplyTo: inReplyTo,
		Content:   content,
		Adapter:   adapter,
		ChannelID: channelID,
	}

	logMessagePayload, _ := json.Marshal(logMessage)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LogMessageAgentCreated, string(logMessagePayload))
}

func DecodeLogMessage(event *types.Event) (LogMessage, error) {
	var message LogMessage
	if err := json.Unmarshal([]byte(event.Payload), &message); err != nil {
		return message, err
	}
	if message.MessageID == "" {
		message.MessageID = fmt.Sprintf("%s:%d", event.Originator.ID, event.Originator.Version)
	}
	return message, nil
}

type RecoveryPayload struct {
	RecoveryID                         string   `json:"recovery_id"`
	Synthetic                          bool     `json:"synthetic"`
	Reason                             string   `json:"reason"`
	InterruptedOriginatorID            string   `json:"interrupted_originator_id"`
	InterruptedParentOriginatorID      string   `json:"interrupted_parent_originator_id,omitempty"`
	InterruptedParentOriginatorVersion uint64   `json:"interrupted_parent_originator_version,omitempty"`
	InterruptedParentVersion           uint64   `json:"interrupted_parent_version"`
	TargetBeforeVersion                uint64   `json:"target_before_version,omitempty"`
	Message                            string   `json:"message"`
	InReplyTo                          string   `json:"in_reply_to,omitempty"`
	PendingToolCallIDs                 []string `json:"pending_tool_call_ids,omitempty"`
	ToolCallID                         string   `json:"tool_call_id,omitempty"`
	ToolName                           string   `json:"tool_name,omitempty"`
	ToolArguments                      string   `json:"tool_arguments,omitempty"`
}

const (
	InterruptedAfterToolsMessage  = "[The previous assistant turn was interrupted after completing its tool calls. No final response was produced.]"
	InterruptedDuringToolsMessage = "[The previous assistant turn was interrupted. No final response was produced.]"
	InterruptedToolResultMessage  = "[Tool execution was interrupted before a result was produced.]"
)

func NewRecoveryID() string {
	return uuid.NewV4().String()
}

func NewLLMToolCallRecoveryResult(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, recoveryID, reason string, tool llm.ToolCall) *types.Event {
	return NewLLMToolCallRecoveryResultWithMessage(originatorID, parentOriginatorID, parentOriginatorVersion, recoveryID, reason, tool, InterruptedToolResultMessage)
}

func NewLLMToolCallRecoveryResultWithMessage(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, recoveryID, reason string, tool llm.ToolCall, message string) *types.Event {
	payload := RecoveryPayload{
		RecoveryID:                         recoveryID,
		Synthetic:                          true,
		Reason:                             reason,
		InterruptedOriginatorID:            originatorID,
		InterruptedParentOriginatorID:      parentOriginatorID,
		InterruptedParentOriginatorVersion: parentOriginatorVersion,
		InterruptedParentVersion:           parentOriginatorVersion,
		Message:                            message,
		ToolCallID:                         tool.ID,
		ToolName:                           tool.Name,
		ToolArguments:                      tool.Arguments,
	}
	data, _ := json.Marshal(payload)
	return types.NewEvent(types.NewOriginator(originatorID, 0), LLMToolCallRecoveryResultCreated, string(data))
}

func NewLLMAgentInterruptedResponse(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, recoveryID, reason, message string, pendingToolCallIDs []string) *types.Event {
	payload := RecoveryPayload{
		RecoveryID:                         recoveryID,
		Synthetic:                          true,
		Reason:                             reason,
		InterruptedOriginatorID:            originatorID,
		InterruptedParentOriginatorID:      parentOriginatorID,
		InterruptedParentOriginatorVersion: parentOriginatorVersion,
		InterruptedParentVersion:           parentOriginatorVersion,
		Message:                            message,
		PendingToolCallIDs:                 append([]string(nil), pendingToolCallIDs...),
	}
	data, _ := json.Marshal(payload)
	return types.NewEvent(types.NewOriginator(originatorID, 0), LLMAgentInterruptedResponseCreated, string(data))
}

func NewAgentInterruptedLogMessage(originatorID, messageID, inReplyTo, recoveryID, reason, message string, pendingToolCallIDs []string) *types.Event {
	if messageID == "" {
		messageID = newMessageID()
	}
	payload := RecoveryPayload{
		RecoveryID:              recoveryID,
		Synthetic:               true,
		Reason:                  reason,
		InterruptedOriginatorID: originatorID,
		Message:                 message,
		InReplyTo:               inReplyTo,
		PendingToolCallIDs:      append([]string(nil), pendingToolCallIDs...),
	}
	data, _ := json.Marshal(struct {
		MessageID string `json:"message_id,omitempty"`
		InReplyTo string `json:"in_reply_to,omitempty"`
		Content   string `json:"content,omitempty"`
		RecoveryPayload
	}{
		MessageID:       messageID,
		InReplyTo:       inReplyTo,
		Content:         message,
		RecoveryPayload: payload,
	})
	return types.NewEvent(types.NewOriginator(originatorID, 0), LogMessageAgentInterrupted, string(data))
}

func DecodeRecoveryPayload(event *types.Event) (RecoveryPayload, error) {
	var payload RecoveryPayload
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		return payload, err
	}
	return payload, nil
}

type SubagentInvocation struct {
	Name                    string         `json:"name"`
	ChildSessionID          string         `json:"child_session_id"`
	ParentSessionID         string         `json:"parent_session_id"`
	ParentOriginatorID      string         `json:"parent_originator_id,omitempty"`
	ParentOriginatorVersion uint64         `json:"parent_originator_version,omitempty"`
	Task                    string         `json:"task,omitempty"`
	Prompt                  string         `json:"prompt,omitempty"`
	Message                 string         `json:"message,omitempty"`
	Result                  string         `json:"result,omitempty"`
	Error                   string         `json:"error,omitempty"`
	Usage                   llm.Usage      `json:"usage,omitempty"`
	CumulativeTotal         int64          `json:"cumulative_total,omitempty"`
	ContextTokens           int            `json:"context_tokens,omitempty"`
	ContextWindow           int            `json:"context_window,omitempty"`
	Stats                   *SubagentStats `json:"stats,omitempty"`
}

type SubagentStats struct {
	PromptTokens       int64          `json:"prompt_tokens"`
	CachedPromptTokens int64          `json:"cached_prompt_tokens"`
	CompletionTokens   int64          `json:"completion_tokens"`
	TotalTokens        int64          `json:"total_tokens"`
	ToolCalls          map[string]int `json:"tool_calls,omitempty"`
}

func NewSubagentInvocationStarted(originatorID, name, childSessionID, parentSessionID, task string, contextWindow int) *types.Event {
	return NewSubagentInvocationStartedWithPrompt(originatorID, name, childSessionID, parentSessionID, task, task, contextWindow)
}

func NewSubagentInvocationStartedWithPrompt(originatorID, name, childSessionID, parentSessionID, task, prompt string, contextWindow int) *types.Event {
	return newSubagentInvocationEvent(originatorID, SubagentInvocationStarted, SubagentInvocation{
		Name:            name,
		ChildSessionID:  childSessionID,
		ParentSessionID: parentSessionID,
		Task:            task,
		Prompt:          prompt,
		ContextWindow:   contextWindow,
	})
}

func NewSubagentInvocationProgress(originatorID, name, childSessionID, parentSessionID, message string, usage llm.Usage, cumulativeTotal int64, contextTokens, contextWindow int) *types.Event {
	return newSubagentInvocationEvent(originatorID, SubagentInvocationProgress, SubagentInvocation{
		Name:            name,
		ChildSessionID:  childSessionID,
		ParentSessionID: parentSessionID,
		Message:         message,
		Usage:           usage,
		CumulativeTotal: cumulativeTotal,
		ContextTokens:   contextTokens,
		ContextWindow:   contextWindow,
	})
}

func NewSubagentInvocationCompleted(originatorID, name, childSessionID, parentSessionID, result string, contextTokens, contextWindow int) *types.Event {
	return newSubagentInvocationEvent(originatorID, SubagentInvocationCompleted, SubagentInvocation{
		Name:            name,
		ChildSessionID:  childSessionID,
		ParentSessionID: parentSessionID,
		Result:          result,
		ContextTokens:   contextTokens,
		ContextWindow:   contextWindow,
	})
}

func NewSubagentInvocationCompletedWithStats(originatorID, name, childSessionID, parentSessionID, result string, contextTokens, contextWindow int, stats *SubagentStats) *types.Event {
	return newSubagentInvocationEvent(originatorID, SubagentInvocationCompleted, SubagentInvocation{
		Name: name, ChildSessionID: childSessionID, ParentSessionID: parentSessionID,
		Result: result, ContextTokens: contextTokens, ContextWindow: contextWindow, Stats: stats,
	})
}

func NewSubagentInvocationFailed(originatorID, name, childSessionID, parentSessionID, errorMessage string, contextTokens, contextWindow int) *types.Event {
	return newSubagentInvocationEvent(originatorID, SubagentInvocationFailed, SubagentInvocation{
		Name:            name,
		ChildSessionID:  childSessionID,
		ParentSessionID: parentSessionID,
		Error:           errorMessage,
		ContextTokens:   contextTokens,
		ContextWindow:   contextWindow,
	})
}

func NewSubagentInvocationFailedWithStats(originatorID, name, childSessionID, parentSessionID, errorMessage string, contextTokens, contextWindow int, stats *SubagentStats) *types.Event {
	return newSubagentInvocationEvent(originatorID, SubagentInvocationFailed, SubagentInvocation{
		Name: name, ChildSessionID: childSessionID, ParentSessionID: parentSessionID,
		Error: errorMessage, ContextTokens: contextTokens, ContextWindow: contextWindow, Stats: stats,
	})
}

// Status is a progress update from a long-running command. Source names what
// is reporting, e.g. "local-model".
type Status struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

func NewStatusUpdated(originatorID, source, message string) *types.Event {
	data, _ := json.Marshal(Status{Source: source, Message: message})
	return types.NewEvent(types.NewOriginator(originatorID, 0), StatusUpdated, string(data))
}

func newSubagentInvocationEvent(originatorID, eventType string, payload SubagentInvocation) *types.Event {
	data, _ := json.Marshal(payload)
	return types.NewEvent(types.NewOriginator(originatorID, 0), eventType, string(data))
}

type LLMUserMessage struct {
	ParentOriginatorID      string      `json:"parent_originator_id"`
	ParentOriginatorVersion uint64      `json:"parent_originator_version"`
	Message                 llm.Message `json:"message"`
}

func NewLLMUserMessage(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, msg llm.Message) *types.Event {
	llmUserMessage := LLMUserMessage{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Message:                 msg,
	}

	llmUserMessagePayload, _ := json.Marshal(llmUserMessage)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMUserMessageCreated, string(llmUserMessagePayload))
}

type LLMAgentMessage struct {
	ParentOriginatorID      string      `json:"parent_originator_id"`
	ParentOriginatorVersion uint64      `json:"parent_originator_version"`
	Message                 llm.Message `json:"message"`
	Usage                   llm.Usage   `json:"usage"`
	CumulativeTotal         int64       `json:"cumulative_total"`
	ReasoningSummary        string      `json:"reasoning_summary,omitempty"`
	TokenEstimateMultiplier float64     `json:"token_estimate_multiplier,omitempty"`
}

func NewLLMAgentMessage(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, msg llm.Message, usage llm.Usage, cumulativeTotal int64) *types.Event {
	return NewLLMAgentMessageWithReasoning(originatorID, parentOriginatorID, parentOriginatorVersion, msg, usage, cumulativeTotal, "")
}

func NewLLMAgentMessageWithReasoning(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, msg llm.Message, usage llm.Usage, cumulativeTotal int64, reasoningSummary string, tokenEstimateMultiplier ...float64) *types.Event {
	multiplier := 0.0
	if len(tokenEstimateMultiplier) > 0 {
		multiplier = tokenEstimateMultiplier[0]
	}
	llmAgentMessage := LLMAgentMessage{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Message:                 msg,
		Usage:                   usage,
		CumulativeTotal:         cumulativeTotal,
		ReasoningSummary:        reasoningSummary,
		TokenEstimateMultiplier: multiplier,
	}

	llmAgentMessagePayload, _ := json.Marshal(llmAgentMessage)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMAgentMessageCreated, string(llmAgentMessagePayload))
}

type LLMReasoningDelta struct {
	ParentOriginatorID      string `json:"parent_originator_id"`
	ParentOriginatorVersion uint64 `json:"parent_originator_version"`
	Started                 bool   `json:"started,omitempty"`
	Delta                   string `json:"delta,omitempty"`
	Text                    string `json:"text,omitempty"`
	Done                    bool   `json:"done,omitempty"`
}

func NewLLMReasoningStarted(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64) *types.Event {
	payload := LLMReasoningDelta{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Started:                 true,
	}
	data, _ := json.Marshal(payload)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMReasoningDeltaCreated, string(data))
}

func NewLLMReasoningDelta(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, delta, text string, done bool) *types.Event {
	payload := LLMReasoningDelta{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Delta:                   delta,
		Text:                    text,
		Done:                    done,
	}
	data, _ := json.Marshal(payload)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMReasoningDeltaCreated, string(data))
}

type LLMToolCallResult struct {
	ParentOriginatorID      string       `json:"parent_originator_id"`
	ParentOriginatorVersion uint64       `json:"parent_originator_version"`
	Tool                    llm.ToolCall `json:"tool_call"`
	Error                   string       `json:"error"`
	// if it was a longer result it can be saved in file
	RawResultPath string      `json:"raw_result_path"`
	Message       llm.Message `json:"message"`
}

func NewLLMToolCallResult(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, tool llm.ToolCall, errStr string, rawResultPath string, msg llm.Message) *types.Event {
	msg.Content = SanitizeToolResultContent(msg.Content)
	llmToolCallResult := LLMToolCallResult{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Tool:                    tool,
		Error:                   errStr,
		RawResultPath:           rawResultPath,
		Message:                 msg,
	}

	llmToolCallResultPayload, _ := json.Marshal(llmToolCallResult)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMToolCallResultCreated, string(llmToolCallResultPayload))
}

func SanitizeToolResultContent(content string) string {
	if content == "" {
		return content
	}
	if !utf8.ValidString(content) || isControlHeavy(content) {
		return fmt.Sprintf("[Tool result omitted: output contains binary or invalid text; original size %d bytes]", len(content))
	}
	if len(content) <= maxPersistedToolResultChars {
		return content
	}
	cutoff := maxPersistedToolResultChars
	for cutoff > 0 && !utf8.RuneStart(content[cutoff]) {
		cutoff--
	}
	if cutoff == 0 {
		cutoff = maxPersistedToolResultChars
	}
	truncated := len(content) - cutoff
	return strings.TrimRight(content[:cutoff], "\n") + fmt.Sprintf("\n\n[Tool result truncated: %d bytes omitted]", truncated)
}

func isControlHeavy(content string) bool {
	if content == "" {
		return false
	}
	sample := content
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	control := 0
	total := 0
	for _, r := range sample {
		total++
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			control++
		}
	}
	return total > 0 && control > total/20
}

type LLMAgentResponse struct {
	ParentOriginatorID      string      `json:"parent_originator_id"`
	ParentOriginatorVersion uint64      `json:"parent_originator_version"`
	Message                 llm.Message `json:"message"`
	Usage                   llm.Usage   `json:"usage"`
	CumulativeTotal         int64       `json:"cumulative_total"`
	ReasoningSummary        string      `json:"reasoning_summary,omitempty"`
	CompactionScope         string      `json:"compaction_scope,omitempty"`
	ActiveUserVersion       uint64      `json:"active_user_version,omitempty"`
	CoversThroughVersion    uint64      `json:"covers_through_version,omitempty"`
	TokenEstimateMultiplier float64     `json:"token_estimate_multiplier,omitempty"`
	CommandOutcomeKey       string      `json:"command_outcome_key,omitempty"`
}

type LLMRequestRetry struct {
	ParentOriginatorID      string `json:"parent_originator_id"`
	ParentOriginatorVersion uint64 `json:"parent_originator_version"`
	Attempt                 int    `json:"attempt"`
	DelayMillis             int64  `json:"delay_millis"`
	RateLimited             bool   `json:"rate_limited"`
	Message                 string `json:"message"`
	Error                   string `json:"error,omitempty"`
}

func NewLLMRequestRetry(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, attempt int, delay time.Duration, rateLimited bool, message, errStr string) *types.Event {
	payload := LLMRequestRetry{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Attempt:                 attempt,
		DelayMillis:             delay.Milliseconds(),
		RateLimited:             rateLimited,
		Message:                 message,
		Error:                   errStr,
	}

	data, _ := json.Marshal(payload)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMRequestRetryScheduled, string(data))
}

func NewLLMAgentResponse(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, msg llm.Message, usage llm.Usage, cumulativeTotal int64) *types.Event {
	return NewLLMAgentResponseWithReasoning(originatorID, parentOriginatorID, parentOriginatorVersion, msg, usage, cumulativeTotal, "")
}

func NewLLMCommandOutcome(originatorID, key, content string) *types.Event {
	payload := LLMAgentResponse{
		Message:           llm.Message{Role: llm.RoleAssistant, Content: content},
		CommandOutcomeKey: key,
	}
	data, _ := json.Marshal(payload)
	return types.NewEvent(types.NewOriginator(originatorID, 0), LLMAgentResponseCreated, string(data))
}

func NewLLMAgentResponseWithReasoning(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, msg llm.Message, usage llm.Usage, cumulativeTotal int64, reasoningSummary string, tokenEstimateMultiplier ...float64) *types.Event {
	multiplier := 0.0
	if len(tokenEstimateMultiplier) > 0 {
		multiplier = tokenEstimateMultiplier[0]
	}
	llmAgentResponse := LLMAgentResponse{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Message:                 msg,
		Usage:                   usage,
		CumulativeTotal:         cumulativeTotal,
		ReasoningSummary:        reasoningSummary,
		TokenEstimateMultiplier: multiplier,
	}

	llmAgentResponsePayload, _ := json.Marshal(llmAgentResponse)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMAgentResponseCreated, string(llmAgentResponsePayload))
}

func NewLLMAgentSummaryResponse(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, msg llm.Message, usage llm.Usage, cumulativeTotal int64) *types.Event {
	llmAgentResponse := LLMAgentResponse{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Message:                 msg,
		Usage:                   usage,
		CumulativeTotal:         cumulativeTotal,
	}

	llmAgentResponsePayload, _ := json.Marshal(llmAgentResponse)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMAgentSummaryResponseCreated, string(llmAgentResponsePayload))
}

func NewLLMAgentSummaryResponseWithMetadata(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, msg llm.Message, usage llm.Usage, cumulativeTotal int64, compactionScope string, activeUserVersion, coversThroughVersion uint64) *types.Event {
	llmAgentResponse := LLMAgentResponse{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Message:                 msg,
		Usage:                   usage,
		CumulativeTotal:         cumulativeTotal,
		CompactionScope:         compactionScope,
		ActiveUserVersion:       activeUserVersion,
		CoversThroughVersion:    coversThroughVersion,
	}

	llmAgentResponsePayload, _ := json.Marshal(llmAgentResponse)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		LLMAgentSummaryResponseCreated, string(llmAgentResponsePayload))
}

type SummaryPrompt struct {
	ParentOriginatorID      string `json:"parent_originator_id"`
	ParentOriginatorVersion uint64 `json:"parent_originator_version"`
	Prompt                  string `json:"prompt"`
	CompactionScope         string `json:"compaction_scope,omitempty"`
	ActiveUserVersion       uint64 `json:"active_user_version,omitempty"`
	CoversThroughVersion    uint64 `json:"covers_through_version,omitempty"`
}

func NewSummaryPromptCreated(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, prompt string) *types.Event {
	summaryPrompt := SummaryPrompt{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Prompt:                  prompt,
	}

	summaryPromptPayload, _ := json.Marshal(summaryPrompt)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		SummaryPromptCreated, string(summaryPromptPayload))
}

func NewSummaryPromptCreatedWithMetadata(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, prompt, compactionScope string, activeUserVersion, coversThroughVersion uint64) *types.Event {
	summaryPrompt := SummaryPrompt{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Prompt:                  prompt,
		CompactionScope:         compactionScope,
		ActiveUserVersion:       activeUserVersion,
		CoversThroughVersion:    coversThroughVersion,
	}

	summaryPromptPayload, _ := json.Marshal(summaryPrompt)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		SummaryPromptCreated, string(summaryPromptPayload))
}

type CompactionCompleted struct {
	Reason       string `json:"reason"`
	Summary      string `json:"summary"`
	BeforeTokens int    `json:"before_tokens"`
	AfterTokens  int    `json:"after_tokens"`
}

type TaskUpdated struct {
	ParentOriginatorID      string       `json:"parent_originator_id"`
	ParentOriginatorVersion uint64       `json:"parent_originator_version"`
	Update                  tasks.Update `json:"update"`
	Stats                   *tasks.Stats `json:"stats,omitempty"`
}

func NewTaskUpdated(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, update tasks.Update) *types.Event {
	payload := TaskUpdated{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Update:                  update,
	}
	data, _ := json.Marshal(payload)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		TaskUpdatedCreated, string(data))
}

func NewTaskCompleted(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, update tasks.Update, stats *tasks.Stats) *types.Event {
	payload := TaskUpdated{ParentOriginatorID: parentOriginatorID, ParentOriginatorVersion: parentOriginatorVersion, Update: update, Stats: stats}
	data, _ := json.Marshal(payload)
	return types.NewEvent(types.NewOriginator(originatorID, 0), TaskUpdatedCreated, string(data))
}

type TaskSnapshot struct {
	ParentOriginatorID      string         `json:"parent_originator_id"`
	ParentOriginatorVersion uint64         `json:"parent_originator_version"`
	Snapshot                tasks.Snapshot `json:"snapshot"`
}

func NewTaskSnapshot(originatorID string, parentOriginatorID string, parentOriginatorVersion uint64, snapshot tasks.Snapshot) *types.Event {
	payload := TaskSnapshot{
		ParentOriginatorID:      parentOriginatorID,
		ParentOriginatorVersion: parentOriginatorVersion,
		Snapshot:                snapshot,
	}
	data, _ := json.Marshal(payload)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		TaskSnapshotCreated, string(data))
}

func NewCompactionCompleted(originatorID string, reason string, summary string, beforeTokens int, afterTokens int) *types.Event {
	compactionCompleted := CompactionCompleted{
		Reason:       reason,
		Summary:      summary,
		BeforeTokens: beforeTokens,
		AfterTokens:  afterTokens,
	}

	compactionCompletedPayload, _ := json.Marshal(compactionCompleted)
	return types.NewEvent(
		types.NewOriginator(originatorID, 0),
		CompactionCompletedCreated, string(compactionCompletedPayload))
}

func EventToLLMMessage(event *types.Event) (llm.Message, error) {
	switch event.EventType {
	case LLMUserMessageCreated:
		var payload LLMUserMessage
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return llm.Message{}, fmt.Errorf("failed to unmarshal LLMUserMessage: %w", err)
		}
		return payload.Message, nil
	case LLMAgentMessageCreated:
		var payload LLMAgentMessage
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return llm.Message{}, fmt.Errorf("failed to unmarshal LLMAgentMessage: %w", err)
		}
		return payload.Message, nil
	case LLMToolCallResultCreated:
		var payload LLMToolCallResult
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return llm.Message{}, fmt.Errorf("failed to unmarshal LLMToolCallResult: %w", err)
		}
		return payload.Message, nil
	case LLMToolCallRecoveryResultCreated:
		payload, err := DecodeRecoveryPayload(event)
		if err != nil {
			return llm.Message{}, fmt.Errorf("failed to unmarshal LLMToolCallRecoveryResult: %w", err)
		}
		return llm.Message{
			Role:       llm.RoleTool,
			Content:    payload.Message,
			ToolCallID: payload.ToolCallID,
		}, nil
	case LLMAgentResponseCreated:
		var payload LLMAgentResponse
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return llm.Message{}, fmt.Errorf("failed to unmarshal LLMAgentResponse: %w", err)
		}
		return payload.Message, nil
	case LLMAgentInterruptedResponseCreated:
		payload, err := DecodeRecoveryPayload(event)
		if err != nil {
			return llm.Message{}, fmt.Errorf("failed to unmarshal LLMAgentInterruptedResponse: %w", err)
		}
		return llm.Message{
			Role:    llm.RoleAssistant,
			Content: payload.Message,
		}, nil
	case LLMAgentSummaryResponseCreated:
		var payload LLMAgentResponse
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return llm.Message{}, fmt.Errorf("failed to unmarshal LLMAgentSummaryResponse: %w", err)
		}
		return payload.Message, nil
	case SummaryPromptCreated:
		var payload SummaryPrompt
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return llm.Message{}, fmt.Errorf("failed to unmarshal SummaryPrompt: %w", err)
		}
		return llm.Message{
			Role:    llm.RoleUser,
			Content: payload.Prompt,
		}, nil
	default:
		return llm.Message{}, fmt.Errorf("unknown event type: %s", event.EventType)
	}
}

func EventsToLLMMessages(events []*types.Event) ([]llm.Message, error) {
	var messages []llm.Message
	for _, event := range events {
		if event.EventType == TaskUpdatedCreated || event.EventType == TaskSnapshotCreated || event.EventType == "Checkpoint.Created" {
			continue
		}
		msg, err := EventToLLMMessage(event)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

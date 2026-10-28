package eventing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

func TestNewLogMessage(t *testing.T) {
	event := NewLogMessage("user-1", "hello")

	if event.EventType != LogMessageUserCreated {
		t.Errorf("expected event type %s, got %s", LogMessageUserCreated, event.EventType)
	}
	if event.Originator.ID != "user-1" {
		t.Errorf("expected originator ID user-1, got %s", event.Originator.ID)
	}
	if event.Originator.Version != 0 {
		t.Errorf("expected version 0, got %d", event.Originator.Version)
	}

	var payload LogMessage
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.Content != "hello" {
		t.Errorf("expected content hello, got %s", payload.Content)
	}
}

func TestNewAgentLogMessage(t *testing.T) {
	event := NewAgentLogMessage("user-1", "done")

	if event.EventType != LogMessageAgentCreated {
		t.Errorf("expected event type %s, got %s", LogMessageAgentCreated, event.EventType)
	}
	if event.Originator.ID != "user-1" {
		t.Errorf("expected originator ID user-1, got %s", event.Originator.ID)
	}

	var payload LogMessage
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.Content != "done" {
		t.Errorf("expected content done, got %s", payload.Content)
	}
}

func TestNewLLMUserMessage(t *testing.T) {
	msg := llm.Message{Role: llm.RoleUser, Content: "find TODOs"}
	event := NewLLMUserMessage("orig-1", "parent-1", 1, msg)

	if event.EventType != LLMUserMessageCreated {
		t.Errorf("expected event type %s, got %s", LLMUserMessageCreated, event.EventType)
	}
	if event.Originator.ID != "orig-1" {
		t.Errorf("expected originator ID orig-1, got %s", event.Originator.ID)
	}
	if event.Originator.Version != 0 {
		t.Errorf("expected version 0, got %d", event.Originator.Version)
	}

	var payload LLMUserMessage
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.ParentOriginatorID != "parent-1" {
		t.Errorf("expected parent originator ID parent-1, got %s", payload.ParentOriginatorID)
	}
	if payload.ParentOriginatorVersion != 1 {
		t.Errorf("expected parent originator version 1, got %d", payload.ParentOriginatorVersion)
	}
	if payload.Message.Role != llm.RoleUser {
		t.Errorf("expected message role %s, got %s", llm.RoleUser, payload.Message.Role)
	}
	if payload.Message.Content != "find TODOs" {
		t.Errorf("expected message content find TODOs, got %s", payload.Message.Content)
	}
}

func TestNewLLMAgentMessage(t *testing.T) {
	toolCalls := []llm.ToolCall{{ID: "tc-1", Name: "Grep", Arguments: `{"pattern":"TODO"}`}}
	msg := llm.Message{Role: llm.RoleAssistant, Content: "", ToolCalls: toolCalls}
	event := NewLLMAgentMessage("orig-1", "parent-1", 2, msg, llm.Usage{}, 0)

	if event.EventType != LLMAgentMessageCreated {
		t.Errorf("expected event type %s, got %s", LLMAgentMessageCreated, event.EventType)
	}
	if event.Originator.ID != "orig-1" {
		t.Errorf("expected originator ID orig-1, got %s", event.Originator.ID)
	}

	var payload LLMAgentMessage
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.ParentOriginatorID != "parent-1" {
		t.Errorf("expected parent originator ID parent-1, got %s", payload.ParentOriginatorID)
	}
	if payload.ParentOriginatorVersion != 2 {
		t.Errorf("expected parent originator version 2, got %d", payload.ParentOriginatorVersion)
	}
	if payload.Message.Role != llm.RoleAssistant {
		t.Errorf("expected message role %s, got %s", llm.RoleAssistant, payload.Message.Role)
	}
	if len(payload.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(payload.Message.ToolCalls))
	}
	if payload.Message.ToolCalls[0].ID != "tc-1" {
		t.Errorf("expected tool call ID tc-1, got %s", payload.Message.ToolCalls[0].ID)
	}
}

func TestNewLLMAgentMessagePersistsTokenEstimateMultiplier(t *testing.T) {
	event := NewLLMAgentMessageWithReasoning("orig-1", "parent-1", 2,
		llm.Message{Role: llm.RoleAssistant, Content: "calling tool"}, llm.Usage{}, 0, "", 0.74)

	var payload LLMAgentMessage
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if got, want := payload.TokenEstimateMultiplier, 0.74; got != want {
		t.Fatalf("token estimate multiplier = %.2f, want %.2f", got, want)
	}
}

func TestNewLLMToolCallResult_Success(t *testing.T) {
	tool := llm.ToolCall{ID: "tc-1", Name: "Grep", Arguments: `{"pattern":"TODO"}`}
	msg := llm.Message{Role: llm.RoleTool, Content: "file.go:12: TODO", ToolCallID: "tc-1"}
	event := NewLLMToolCallResult("orig-1", "parent-1", 3, tool, "", "", msg)

	if event.EventType != LLMToolCallResultCreated {
		t.Errorf("expected event type %s, got %s", LLMToolCallResultCreated, event.EventType)
	}

	var payload LLMToolCallResult
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.ParentOriginatorID != "parent-1" {
		t.Errorf("expected parent originator ID parent-1, got %s", payload.ParentOriginatorID)
	}
	if payload.Tool.ID != "tc-1" {
		t.Errorf("expected tool ID tc-1, got %s", payload.Tool.ID)
	}
	if payload.Error != "" {
		t.Errorf("expected empty error, got %s", payload.Error)
	}
	if payload.Message.Role != llm.RoleTool {
		t.Errorf("expected message role %s, got %s", llm.RoleTool, payload.Message.Role)
	}
	if payload.Message.Content != "file.go:12: TODO" {
		t.Errorf("expected message content file.go:12: TODO, got %s", payload.Message.Content)
	}
	if payload.Message.ToolCallID != "tc-1" {
		t.Errorf("expected tool call ID tc-1, got %s", payload.Message.ToolCallID)
	}
}

func TestNewLLMToolCallResult_Error(t *testing.T) {
	tool := llm.ToolCall{ID: "tc-1", Name: "Bash", Arguments: `{"command":"invalid"}`}
	msg := llm.Message{Role: llm.RoleTool, Content: "error: command failed", ToolCallID: "tc-1"}
	event := NewLLMToolCallResult("orig-1", "parent-1", 3, tool, "command failed", "", msg)

	var payload LLMToolCallResult
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.Error != "command failed" {
		t.Errorf("expected error command failed, got %s", payload.Error)
	}
	if payload.Message.Content != "error: command failed" {
		t.Errorf("expected message content error: command failed, got %s", payload.Message.Content)
	}
}

func TestNewLLMToolCallResult_SanitizesBinaryContent(t *testing.T) {
	tool := llm.ToolCall{ID: "tc-1", Name: "Read", Arguments: `{"file_path":"app.apk"}`}
	msg := llm.Message{Role: llm.RoleTool, Content: "ok\x00bad", ToolCallID: "tc-1"}
	event := NewLLMToolCallResult("orig-1", "parent-1", 3, tool, "", "", msg)

	var payload LLMToolCallResult
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.Message.Content == msg.Content {
		t.Fatalf("expected message content to be sanitized")
	}
	if !strings.Contains(payload.Message.Content, "omitted") {
		t.Fatalf("expected omitted marker, got %q", payload.Message.Content)
	}
}

func TestNewLLMAgentResponse(t *testing.T) {
	msg := llm.Message{Role: llm.RoleAssistant, Content: "Found 1 TODO"}
	usage := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	event := NewLLMAgentResponse("orig-1", "parent-1", 4, msg, usage, 0)

	if event.EventType != LLMAgentResponseCreated {
		t.Errorf("expected event type %s, got %s", LLMAgentResponseCreated, event.EventType)
	}

	var payload LLMAgentResponse
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.ParentOriginatorID != "parent-1" {
		t.Errorf("expected parent originator ID parent-1, got %s", payload.ParentOriginatorID)
	}
	if payload.ParentOriginatorVersion != 4 {
		t.Errorf("expected parent originator version 4, got %d", payload.ParentOriginatorVersion)
	}
	if payload.Message.Role != llm.RoleAssistant {
		t.Errorf("expected message role %s, got %s", llm.RoleAssistant, payload.Message.Role)
	}
	if payload.Message.Content != "Found 1 TODO" {
		t.Errorf("expected message content Found 1 TODO, got %s", payload.Message.Content)
	}
	if payload.Usage.TotalTokens != 150 {
		t.Errorf("expected total tokens 150, got %d", payload.Usage.TotalTokens)
	}
}

func TestNewLLMAgentResponsePersistsTokenEstimateMultiplier(t *testing.T) {
	event := NewLLMAgentResponseWithReasoning("orig-1", "parent-1", 4,
		llm.Message{Role: llm.RoleAssistant, Content: "done"}, llm.Usage{}, 0, "", 0.69)

	var payload LLMAgentResponse
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if got, want := payload.TokenEstimateMultiplier, 0.69; got != want {
		t.Fatalf("token estimate multiplier = %.2f, want %.2f", got, want)
	}
}

func TestAgentInterruptedLogMessageCanBeDeliveredAsLogMessage(t *testing.T) {
	event := NewAgentInterruptedLogMessage("ch-1", "interrupted-1", "user-1", "recovery-1", "provider_error", InterruptedAfterToolsMessage, nil)
	message, err := DecodeLogMessage(event)
	if err != nil {
		t.Fatal(err)
	}
	if message.MessageID != "interrupted-1" || message.InReplyTo != "user-1" || message.Content != InterruptedAfterToolsMessage {
		t.Fatalf("message = %+v", message)
	}
}

func TestNewLLMAgentResponseWithReasoningKeepsReasoningOutOfLLMMessage(t *testing.T) {
	msg := llm.Message{Role: llm.RoleAssistant, Content: "done"}
	event := NewLLMAgentResponseWithReasoning("orig-1", "parent-1", 4, msg, llm.Usage{}, 0, "thinking summary")

	var payload LLMAgentResponse
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.ReasoningSummary != "thinking summary" {
		t.Fatalf("reasoning summary = %q, want display metadata", payload.ReasoningSummary)
	}
	got, err := EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("EventToLLMMessage() error = %v", err)
	}
	if got.Content != "done" {
		t.Fatalf("message content = %q, want assistant content only", got.Content)
	}
	if strings.Contains(got.Content, "thinking summary") {
		t.Fatalf("reasoning leaked into LLM message: %q", got.Content)
	}
}

func TestNewLLMRequestRetry(t *testing.T) {
	event := NewLLMRequestRetry("orig-1", "parent-1", 7, 2, 1500*time.Millisecond, true, "rate limited; retrying", "429")

	if event.EventType != LLMRequestRetryScheduled {
		t.Errorf("expected event type %s, got %s", LLMRequestRetryScheduled, event.EventType)
	}
	if event.Originator.ID != "orig-1" {
		t.Errorf("expected originator ID orig-1, got %s", event.Originator.ID)
	}

	var payload LLMRequestRetry
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.ParentOriginatorID != "parent-1" {
		t.Errorf("expected parent originator ID parent-1, got %s", payload.ParentOriginatorID)
	}
	if payload.ParentOriginatorVersion != 7 {
		t.Errorf("expected parent originator version 7, got %d", payload.ParentOriginatorVersion)
	}
	if payload.Attempt != 2 {
		t.Errorf("expected attempt 2, got %d", payload.Attempt)
	}
	if payload.DelayMillis != 1500 {
		t.Errorf("expected delay 1500ms, got %d", payload.DelayMillis)
	}
	if !payload.RateLimited {
		t.Error("expected rate limited flag")
	}
	if payload.Message != "rate limited; retrying" {
		t.Errorf("expected retry message, got %q", payload.Message)
	}
	if payload.Error != "429" {
		t.Errorf("expected error 429, got %q", payload.Error)
	}
}

func TestNewCompactionCompleted(t *testing.T) {
	event := NewCompactionCompleted("orig-1", "preflight", "summary text", 57000, 12000)

	if event.EventType != CompactionCompletedCreated {
		t.Errorf("expected event type %s, got %s", CompactionCompletedCreated, event.EventType)
	}

	var payload CompactionCompleted
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.Reason != "preflight" {
		t.Errorf("expected reason preflight, got %s", payload.Reason)
	}
	if payload.Summary != "summary text" {
		t.Errorf("expected summary text, got %s", payload.Summary)
	}
	if payload.BeforeTokens != 57000 {
		t.Errorf("expected before tokens 57000, got %d", payload.BeforeTokens)
	}
	if payload.AfterTokens != 12000 {
		t.Errorf("expected after tokens 12000, got %d", payload.AfterTokens)
	}
}

func TestEventToLLMMessage_UserMessage(t *testing.T) {
	msg := llm.Message{Role: llm.RoleUser, Content: "hello"}
	event := NewLLMUserMessage("orig-1", "parent-1", 0, msg)

	result, err := EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Role != llm.RoleUser {
		t.Errorf("expected role %s, got %s", llm.RoleUser, result.Role)
	}
	if result.Content != "hello" {
		t.Errorf("expected content hello, got %s", result.Content)
	}
}

func TestEventToLLMMessage_AgentMessage(t *testing.T) {
	toolCalls := []llm.ToolCall{{ID: "tc-1", Name: "Read", Arguments: `{"file_path":"main.go"}`}}
	msg := llm.Message{Role: llm.RoleAssistant, Content: "", ToolCalls: toolCalls}
	event := NewLLMAgentMessage("orig-1", "parent-1", 0, msg, llm.Usage{}, 0)

	result, err := EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Role != llm.RoleAssistant {
		t.Errorf("expected role %s, got %s", llm.RoleAssistant, result.Role)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "Read" {
		t.Errorf("expected tool name Read, got %s", result.ToolCalls[0].Name)
	}
}

func TestEventToLLMMessage_ToolResult(t *testing.T) {
	tool := llm.ToolCall{ID: "tc-1", Name: "Grep", Arguments: `{"pattern":"TODO"}`}
	msg := llm.Message{Role: llm.RoleTool, Content: "result", ToolCallID: "tc-1"}
	event := NewLLMToolCallResult("orig-1", "parent-1", 0, tool, "", "", msg)

	result, err := EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Role != llm.RoleTool {
		t.Errorf("expected role %s, got %s", llm.RoleTool, result.Role)
	}
	if result.Content != "result" {
		t.Errorf("expected content result, got %s", result.Content)
	}
	if result.ToolCallID != "tc-1" {
		t.Errorf("expected tool call ID tc-1, got %s", result.ToolCallID)
	}
}

func TestEventToLLMMessage_AgentResponse(t *testing.T) {
	msg := llm.Message{Role: llm.RoleAssistant, Content: "all done"}
	usage := llm.Usage{TotalTokens: 42}
	event := NewLLMAgentResponse("orig-1", "parent-1", 0, msg, usage, 0)

	result, err := EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Role != llm.RoleAssistant {
		t.Errorf("expected role %s, got %s", llm.RoleAssistant, result.Role)
	}
	if result.Content != "all done" {
		t.Errorf("expected content all done, got %s", result.Content)
	}
}

func TestEventToLLMMessage_UnknownType(t *testing.T) {
	event := types.NewEvent(types.NewOriginator("x", 0), "Unknown.Event", `{}`)
	_, err := EventToLLMMessage(event)
	if err == nil {
		t.Fatal("expected error for unknown event type, got nil")
	}
}

func TestEventToLLMMessage_CorruptedPayload(t *testing.T) {
	event := types.NewEvent(types.NewOriginator("x", 0), LLMUserMessageCreated, `not json`)
	_, err := EventToLLMMessage(event)
	if err == nil {
		t.Fatal("expected error for corrupted payload, got nil")
	}
}

func TestEventsToLLMMessages_Empty(t *testing.T) {
	result, err := EventsToLLMMessages(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d messages", len(result))
	}

	result, err = EventsToLLMMessages([]*types.Event{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d messages", len(result))
	}
}

func TestEventsToLLMMessages_Single(t *testing.T) {
	msg := llm.Message{Role: llm.RoleUser, Content: "hello"}
	event := NewLLMUserMessage("orig-1", "parent-1", 0, msg)

	result, err := EventsToLLMMessages([]*types.Event{event})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Content != "hello" {
		t.Errorf("expected content hello, got %s", result[0].Content)
	}
}

func TestEventsToLLMMessages_PreservesOrder(t *testing.T) {
	userMsg := llm.Message{Role: llm.RoleUser, Content: "find TODOs"}
	toolCalls := []llm.ToolCall{{ID: "tc-1", Name: "Grep", Arguments: `{"pattern":"TODO"}`}}
	agentMsg := llm.Message{Role: llm.RoleAssistant, Content: "", ToolCalls: toolCalls}
	toolResult := llm.Message{Role: llm.RoleTool, Content: "file.go:12: TODO", ToolCallID: "tc-1"}
	finalResponse := llm.Message{Role: llm.RoleAssistant, Content: "Found 1 TODO"}

	events := []*types.Event{
		NewLLMUserMessage("orig-1", "parent-1", 0, userMsg),
		NewLLMAgentMessage("orig-1", "parent-1", 0, agentMsg, llm.Usage{}, 0),
		NewLLMToolCallResult("orig-1", "parent-1", 0, toolCalls[0], "", "", toolResult),
		NewLLMAgentResponse("orig-1", "parent-1", 0, finalResponse, llm.Usage{}, 0),
	}

	result, err := EventsToLLMMessages(events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
	}

	expected := []struct {
		role       llm.Role
		content    string
		toolCallID string
	}{
		{llm.RoleUser, "find TODOs", ""},
		{llm.RoleAssistant, "", ""},
		{llm.RoleTool, "file.go:12: TODO", "tc-1"},
		{llm.RoleAssistant, "Found 1 TODO", ""},
	}

	for i, exp := range expected {
		if result[i].Role != exp.role {
			t.Errorf("message %d: expected role %s, got %s", i, exp.role, result[i].Role)
		}
		if result[i].Content != exp.content {
			t.Errorf("message %d: expected content %q, got %q", i, exp.content, result[i].Content)
		}
		if result[i].ToolCallID != exp.toolCallID {
			t.Errorf("message %d: expected tool call ID %q, got %q", i, exp.toolCallID, result[i].ToolCallID)
		}
	}

	// Verify tool call chain: assistant's tool call ID matches tool result's ToolCallID
	if len(result[1].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call in assistant message, got %d", len(result[1].ToolCalls))
	}
	if result[1].ToolCalls[0].ID != result[2].ToolCallID {
		t.Errorf("tool call ID mismatch: assistant has %s, tool result has %s",
			result[1].ToolCalls[0].ID, result[2].ToolCallID)
	}
}

func TestEventsToLLMMessages_BadEventInMiddle(t *testing.T) {
	userMsg := llm.Message{Role: llm.RoleUser, Content: "hello"}
	badEvent := types.NewEvent(types.NewOriginator("x", 0), "Unknown.Event", `{}`)

	events := []*types.Event{
		NewLLMUserMessage("orig-1", "parent-1", 0, userMsg),
		badEvent,
	}

	_, err := EventsToLLMMessages(events)
	if err == nil {
		t.Fatal("expected error when bad event is in the middle, got nil")
	}
}

func TestFullConversationFlow(t *testing.T) {
	// Simulates one complete runLLMLoop turn:
	// 1. User sends prompt
	// 2. Assistant requests tool call
	// 3. Tool returns result
	// 4. Assistant returns final answer

	parentID := "parent-123"

	userEvent := NewLLMUserMessage("orig-1", parentID, 0, llm.Message{
		Role:    llm.RoleUser,
		Content: "Find all TODO comments",
	})

	toolCall := llm.ToolCall{
		ID:        "call_abc",
		Name:      "Grep",
		Arguments: `{"pattern":"TODO"}`,
	}
	agentWithTools := NewLLMAgentMessage("orig-1", parentID, 0, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   "",
		ToolCalls: []llm.ToolCall{toolCall},
	}, llm.Usage{}, 0)

	toolResult := NewLLMToolCallResult("orig-1", parentID, 0, toolCall, "", "", llm.Message{
		Role:       llm.RoleTool,
		Content:    "main.go:42: // TODO refactor this",
		ToolCallID: "call_abc",
	})

	finalResponse := NewLLMAgentResponse("orig-1", parentID, 0, llm.Message{
		Role:    llm.RoleAssistant,
		Content: "I found 1 TODO comment in main.go at line 42.",
	}, llm.Usage{
		PromptTokens:     150,
		CompletionTokens: 20,
		TotalTokens:      170,
	}, 0)

	// Verify we can reconstruct the conversation
	msgs, err := EventsToLLMMessages([]*types.Event{userEvent, agentWithTools, toolResult, finalResponse})
	if err != nil {
		t.Fatalf("failed to reconstruct messages: %v", err)
	}

	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}

	// Verify the flow
	if msgs[0].Role != llm.RoleUser {
		t.Errorf("message 0: expected role User, got %s", msgs[0].Role)
	}
	if msgs[1].Role != llm.RoleAssistant {
		t.Errorf("message 1: expected role Assistant, got %s", msgs[1].Role)
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "Grep" {
		t.Errorf("message 1: expected Grep tool call, got %+v", msgs[1].ToolCalls)
	}
	if msgs[2].Role != llm.RoleTool {
		t.Errorf("message 2: expected role Tool, got %s", msgs[2].Role)
	}
	if msgs[2].ToolCallID != "call_abc" {
		t.Errorf("message 2: expected ToolCallID call_abc, got %s", msgs[2].ToolCallID)
	}
	if msgs[3].Role != llm.RoleAssistant {
		t.Errorf("message 3: expected role Assistant, got %s", msgs[3].Role)
	}
	if msgs[3].Content != "I found 1 TODO comment in main.go at line 42." {
		t.Errorf("message 3: unexpected content: %s", msgs[3].Content)
	}

	// Verify parent originator consistency across all events
	events := []*types.Event{userEvent, agentWithTools, toolResult, finalResponse}
	for i, event := range events {
		var parentID string
		switch event.EventType {
		case LLMUserMessageCreated:
			var p LLMUserMessage
			if err := json.Unmarshal([]byte(event.Payload), &p); err != nil {
				t.Fatal(err)
			}
			parentID = p.ParentOriginatorID
		case LLMAgentMessageCreated:
			var p LLMAgentMessage
			if err := json.Unmarshal([]byte(event.Payload), &p); err != nil {
				t.Fatal(err)
			}
			parentID = p.ParentOriginatorID
		case LLMToolCallResultCreated:
			var p LLMToolCallResult
			if err := json.Unmarshal([]byte(event.Payload), &p); err != nil {
				t.Fatal(err)
			}
			parentID = p.ParentOriginatorID
		case LLMAgentResponseCreated:
			var p LLMAgentResponse
			if err := json.Unmarshal([]byte(event.Payload), &p); err != nil {
				t.Fatal(err)
			}
			parentID = p.ParentOriginatorID
		}
		if parentID != "parent-123" {
			t.Errorf("event %d (%s): expected parent ID parent-123, got %s", i, event.EventType, parentID)
		}
	}
}

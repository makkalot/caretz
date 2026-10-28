package output

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type mockProvider struct {
	responses []llm.Response
	callIndex int
}

func (m *mockProvider) Chat(systemPrompt string, messages []llm.Message, tools []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	if m.callIndex >= len(m.responses) {
		return llm.Response{}, nil
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

func (m *mockProvider) GetModel() string   { return "mock-model" }
func (m *mockProvider) GetBaseURL() string { return "http://mock" }

func setupAgentSession(t *testing.T) *agent.AgentSession {
	t.Helper()

	tmpDir := t.TempDir()
	logger := log.New()
	logger.SetVerbosity(0)

	provider := &mockProvider{}
	registry := &tools.Registry{
		Logger: logger,
		RunSandboxed: func(cwd, command string) (string, string, error) {
			return "mock-output", "", nil
		},
	}

	session, err := agent.NewAgentSession("test", "ch-1", agent.SessionParams{
		BaseDir:          tmpDir,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     registry,
		SystemPrompt:     "you are a test agent",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatalf("failed to create agent session: %v", err)
	}

	return session
}

func TestNewTUIOutput(t *testing.T) {
	var sent interface{}
	send := func(msg interface{}) {
		sent = msg
	}

	o := NewTUIOutput(send)
	if o == nil {
		t.Fatal("expected TUIOutput, got nil")
	}

	// Verify the send function is wired correctly
	o.send("test")
	if sent != "test" {
		t.Errorf("expected sent message to be 'test', got %v", sent)
	}
}

func TestTUIOutputReceivesStatusUpdates(t *testing.T) {
	session := setupAgentSession(t)
	got := make(chan interface{}, 1)
	o := NewTUIOutput(func(msg interface{}) { got <- msg })
	if err := o.RegisterHandlers(session); err != nil {
		t.Fatalf("RegisterHandlers: %v", err)
	}

	if err := session.PublishAuditEvent(eventing.NewStatusUpdated(session.ChannelID(), "local-model", "loading model 42%")); err != nil {
		t.Fatalf("PublishAuditEvent: %v", err)
	}
	select {
	case msg := <-got:
		want := TUIStatus{Payload: eventing.Status{Source: "local-model", Message: "loading model 42%"}}
		if !reflect.DeepEqual(msg, want) {
			t.Fatalf("sent %#v, want %#v", msg, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status update was not delivered")
	}
}

func TestTUIOutput_HandleLLMUserMessageCreated(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := eventing.NewLLMUserMessage("orig-1", "parent-1", 0, llm.Message{
		Role:    llm.RoleUser,
		Content: "hello",
	})

	err := o.HandleLLMUserMessageCreated(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent != nil {
		t.Error("expected no message to be sent for user message")
	}
}

func TestTUIOutput_HandleLLMAgentMessageCreated(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	toolCalls := []llm.ToolCall{
		{ID: "tc-1", Name: "Read", Arguments: `{"file_path":"main.go"}`},
	}
	msg := llm.Message{
		Role:      llm.RoleAssistant,
		Content:   "Let me read that file.",
		ToolCalls: toolCalls,
	}
	usage := llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	event := eventing.NewLLMAgentMessage("orig-1", "parent-1", 1, msg, usage, 25)

	err := o.HandleLLMAgentMessageCreated(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent == nil {
		t.Fatal("expected a message to be sent")
	}

	agentMsg, ok := sent.(TUIAgentMessage)
	if !ok {
		t.Fatalf("expected TUIAgentMessage, got %T", sent)
	}
	if agentMsg.Payload.Message.Content != "Let me read that file." {
		t.Errorf("expected content 'Let me read that file.', got %q", agentMsg.Payload.Message.Content)
	}
	if len(agentMsg.Payload.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(agentMsg.Payload.Message.ToolCalls))
	}
	if agentMsg.Payload.Message.ToolCalls[0].Name != "Read" {
		t.Errorf("expected tool name Read, got %s", agentMsg.Payload.Message.ToolCalls[0].Name)
	}
	if agentMsg.Payload.Usage.TotalTokens != 15 {
		t.Errorf("expected total tokens 15, got %d", agentMsg.Payload.Usage.TotalTokens)
	}
	if agentMsg.Payload.CumulativeTotal != 25 {
		t.Errorf("expected cumulative total 25, got %d", agentMsg.Payload.CumulativeTotal)
	}
}

func TestTUIOutput_HandleLLMReasoningDeltaCreated(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := eventing.NewLLMReasoningDelta("orig-1", "parent-1", 1, "still working", "", false)

	err := o.HandleLLMReasoningDeltaCreated(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reasoning, ok := sent.(TUIReasoningDelta)
	if !ok {
		t.Fatalf("expected TUIReasoningDelta, got %T", sent)
	}
	if reasoning.Payload.Delta != "still working" {
		t.Fatalf("delta = %q, want live reasoning delta", reasoning.Payload.Delta)
	}
}

func TestTUIOutput_HandleLLMAgentMessageCreated_BadJSON(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := types.NewEvent(types.NewOriginator("x", 0), eventing.LLMAgentMessageCreated, `not json`)

	err := o.HandleLLMAgentMessageCreated(event)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if sent != nil {
		t.Error("expected no message to be sent when unmarshaling fails")
	}
}

func TestTUIOutput_HandleLLMToolCallResultCreated(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	tool := llm.ToolCall{ID: "tc-1", Name: "Grep", Arguments: `{"pattern":"TODO"}`}
	msg := llm.Message{Role: llm.RoleTool, Content: "main.go:42: TODO", ToolCallID: "tc-1"}
	event := eventing.NewLLMToolCallResult("orig-1", "parent-1", 2, tool, "", "", msg)

	err := o.HandleLLMToolCallResultCreated(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent == nil {
		t.Fatal("expected a message to be sent")
	}

	toolResult, ok := sent.(TUIToolResult)
	if !ok {
		t.Fatalf("expected TUIToolResult, got %T", sent)
	}
	if toolResult.Payload.Message.Content != "main.go:42: TODO" {
		t.Errorf("expected content 'main.go:42: TODO', got %q", toolResult.Payload.Message.Content)
	}
	if toolResult.Payload.Tool.Name != "Grep" {
		t.Errorf("expected tool name Grep, got %s", toolResult.Payload.Tool.Name)
	}
	if toolResult.Payload.Error != "" {
		t.Errorf("expected empty error, got %q", toolResult.Payload.Error)
	}
}

func TestTUIOutput_HandleLLMToolCallResultCreated_WithError(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	tool := llm.ToolCall{ID: "tc-1", Name: "Bash", Arguments: `{"command":"invalid"}`}
	msg := llm.Message{Role: llm.RoleTool, Content: "error: command failed", ToolCallID: "tc-1"}
	event := eventing.NewLLMToolCallResult("orig-1", "parent-1", 2, tool, "command failed", "", msg)

	err := o.HandleLLMToolCallResultCreated(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent == nil {
		t.Fatal("expected a message to be sent")
	}

	toolResult, ok := sent.(TUIToolResult)
	if !ok {
		t.Fatalf("expected TUIToolResult, got %T", sent)
	}
	if toolResult.Payload.Error != "command failed" {
		t.Errorf("expected error 'command failed', got %q", toolResult.Payload.Error)
	}
	if toolResult.Payload.Message.Content != "error: command failed" {
		t.Errorf("expected message content 'error: command failed', got %q", toolResult.Payload.Message.Content)
	}
}

func TestTUIOutput_HandleLLMToolCallResultCreated_BadJSON(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := types.NewEvent(types.NewOriginator("x", 0), eventing.LLMToolCallResultCreated, `not json`)

	err := o.HandleLLMToolCallResultCreated(event)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if sent != nil {
		t.Error("expected no message to be sent when unmarshaling fails")
	}
}

func TestTUIOutput_HandleLLMAgentResponseCreated(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	msg := llm.Message{Role: llm.RoleAssistant, Content: "All done!"}
	usage := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	event := eventing.NewLLMAgentResponse("orig-1", "parent-1", 3, msg, usage, 150)

	err := o.HandleLLMAgentResponseCreated(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent == nil {
		t.Fatal("expected a message to be sent")
	}

	agentResp, ok := sent.(TUIAgentResponse)
	if !ok {
		t.Fatalf("expected TUIAgentResponse, got %T", sent)
	}
	if agentResp.Payload.Message.Content != "All done!" {
		t.Errorf("expected content 'All done!', got %q", agentResp.Payload.Message.Content)
	}
	if agentResp.Payload.Usage.TotalTokens != 150 {
		t.Errorf("expected total tokens 150, got %d", agentResp.Payload.Usage.TotalTokens)
	}
	if agentResp.Payload.CumulativeTotal != 150 {
		t.Errorf("expected cumulative total 150, got %d", agentResp.Payload.CumulativeTotal)
	}
}

func TestTUIOutput_HandleLLMAgentResponseCreated_BadJSON(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := types.NewEvent(types.NewOriginator("x", 0), eventing.LLMAgentResponseCreated, `not json`)

	err := o.HandleLLMAgentResponseCreated(event)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if sent != nil {
		t.Error("expected no message to be sent when unmarshaling fails")
	}
}

func TestTUIOutput_HandleLLMRequestRetryScheduled(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := eventing.NewLLMRequestRetry("orig-1", "parent-1", 3, 1, 2*time.Second, true, "Provider rate limit exceeded; retrying in 2s (attempt 2).", "429")

	err := o.HandleLLMRequestRetryScheduled(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent == nil {
		t.Fatal("expected a message to be sent")
	}

	retry, ok := sent.(TUIRequestRetry)
	if !ok {
		t.Fatalf("expected TUIRequestRetry, got %T", sent)
	}
	if retry.Payload.Message != "Provider rate limit exceeded; retrying in 2s (attempt 2)." {
		t.Errorf("expected retry message, got %q", retry.Payload.Message)
	}
	if !retry.Payload.RateLimited {
		t.Error("expected rate limited retry")
	}
}

func TestTUIOutput_HandleLLMRequestRetryScheduled_BadJSON(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := types.NewEvent(types.NewOriginator("x", 0), eventing.LLMRequestRetryScheduled, `not json`)

	err := o.HandleLLMRequestRetryScheduled(event)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if sent != nil {
		t.Error("expected no message to be sent when unmarshaling fails")
	}
}

func TestTUIOutput_HandleCompactionCompletedCreated(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := eventing.NewCompactionCompleted("orig-1", "preflight", "summary text", 57000, 12000)

	err := o.HandleCompactionCompletedCreated(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent == nil {
		t.Fatal("expected a message to be sent")
	}

	compactionMsg, ok := sent.(TUICompactionCompleted)
	if !ok {
		t.Fatalf("expected TUICompactionCompleted, got %T", sent)
	}
	if compactionMsg.Payload.Summary != "summary text" {
		t.Errorf("expected summary text, got %q", compactionMsg.Payload.Summary)
	}
	if compactionMsg.Payload.AfterTokens != 12000 {
		t.Errorf("expected after tokens 12000, got %d", compactionMsg.Payload.AfterTokens)
	}
}

func TestTUIOutput_HandleCompactionCompletedCreated_BadJSON(t *testing.T) {
	var sent interface{}
	o := NewTUIOutput(func(msg interface{}) { sent = msg })

	event := types.NewEvent(types.NewOriginator("x", 0), eventing.CompactionCompletedCreated, `not json`)

	err := o.HandleCompactionCompletedCreated(event)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if sent != nil {
		t.Error("expected no message to be sent when unmarshaling fails")
	}
}

func TestTUIOutput_RegisterHandlers(t *testing.T) {
	session := setupAgentSession(t)

	var sentCount int
	o := NewTUIOutput(func(msg interface{}) { sentCount++ })

	err := o.RegisterHandlers(session)
	if err != nil {
		t.Fatalf("unexpected error registering handlers: %v", err)
	}

	// Call the agent with a simple response to trigger all handlers
	responses := []llm.Response{
		{
			Content: "Hello!",
			Usage:   llm.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
			Message: llm.Message{Role: llm.RoleAssistant, Content: "Hello!"},
		},
	}

	// We need to inject the mock responses. Create a new session with responses.
	tmpDir := t.TempDir()
	logger := log.New()
	logger.SetVerbosity(0)
	provider := &mockProvider{responses: responses}
	registry := &tools.Registry{
		Logger: logger,
		RunSandboxed: func(cwd, command string) (string, string, error) {
			return "mock-output", "", nil
		},
	}

	session, err = agent.NewAgentSession("test", "ch-2", agent.SessionParams{
		BaseDir:          tmpDir,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     registry,
		SystemPrompt:     "you are a test agent",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatalf("failed to create agent session: %v", err)
	}

	err = o.RegisterHandlers(session)
	if err != nil {
		t.Fatalf("unexpected error registering handlers: %v", err)
	}

	_, err = session.Call(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error on call: %v", err)
	}

	// The request emits reasoning start/done lifecycle messages followed by the response.
	if sentCount != 3 {
		t.Errorf("expected reasoning lifecycle and agent response, got %d message(s)", sentCount)
	}
}

func TestTUIOutput_RegisterHandlers_WithToolCall(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New()
	logger.SetVerbosity(0)

	responses := []llm.Response{
		{
			Content: "",
			ToolCalls: []llm.ToolCall{
				{ID: "tc-1", Name: "Bash", Arguments: `{"command":"echo hello"}`},
			},
			Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			Message: llm.Message{
				Role:      llm.RoleAssistant,
				Content:   "",
				ToolCalls: []llm.ToolCall{{ID: "tc-1", Name: "Bash", Arguments: `{"command":"echo hello"}`}},
			},
		},
		{
			Content: "Done!",
			Usage:   llm.Usage{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25},
			Message: llm.Message{Role: llm.RoleAssistant, Content: "Done!"},
		},
	}

	provider := &mockProvider{responses: responses}
	registry := &tools.Registry{
		Logger: logger,
		RunSandboxed: func(cwd, command string) (string, string, error) {
			return "mock-output", "", nil
		},
	}

	session, err := agent.NewAgentSession("test", "ch-3", agent.SessionParams{
		BaseDir:          tmpDir,
		Logger:           logger,
		Provider:         provider,
		ToolRegistry:     registry,
		SystemPrompt:     "you are a test agent",
		ContextWindow:    128000,
		ReservedTokens:   16384,
		KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatalf("failed to create agent session: %v", err)
	}

	var messages []interface{}
	o := NewTUIOutput(func(msg interface{}) { messages = append(messages, msg) })

	err = o.RegisterHandlers(session)
	if err != nil {
		t.Fatalf("unexpected error registering handlers: %v", err)
	}

	_, err = session.Call(context.Background(), "run a command")
	if err != nil {
		t.Fatalf("unexpected error on call: %v", err)
	}

	if len(messages) != 7 {
		t.Fatalf("expected reasoning lifecycle around two model requests plus tool activity and response, got %d", len(messages))
	}
	wants := []interface{}{TUIReasoningDelta{}, TUIReasoningDelta{}, TUIAgentMessage{}, TUIToolResult{}, TUIReasoningDelta{}, TUIReasoningDelta{}, TUIAgentResponse{}}
	for i, want := range wants {
		if reflect.TypeOf(messages[i]) != reflect.TypeOf(want) {
			t.Errorf("message %d type = %T, want %T", i, messages[i], want)
		}
	}
}

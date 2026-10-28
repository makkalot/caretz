package output

import (
	"encoding/json"
	"fmt"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/types"
)

// TUI tea message types (sent from event handlers to bubbletea program)

type TUIAgentMessage struct {
	Payload eventing.LLMAgentMessage
}

type TUIReasoningDelta struct {
	Payload eventing.LLMReasoningDelta
}

type TUIToolResult struct {
	Payload eventing.LLMToolCallResult
}

type TUIAgentResponse struct {
	Payload eventing.LLMAgentResponse
}

type TUICompactionCompleted struct {
	Payload eventing.CompactionCompleted
}

type TUIRequestRetry struct {
	Payload eventing.LLMRequestRetry
}

type TUISubagentInvocation struct {
	EventType string
	Payload   eventing.SubagentInvocation
}

// TUIStatus is a progress update from a long-running command.
type TUIStatus struct {
	Payload eventing.Status
}

// TUIOutput renders agent events into bubbletea messages.
type TUIOutput struct {
	send func(interface{})
}

func NewTUIOutput(send func(interface{})) *TUIOutput {
	return &TUIOutput{send: send}
}

func (o *TUIOutput) DispatchEvent(e *types.Event) error {
	switch e.EventType {
	case eventing.LLMUserMessageCreated:
		return o.HandleLLMUserMessageCreated(e)
	case eventing.LLMAgentMessageCreated:
		return o.HandleLLMAgentMessageCreated(e)
	case eventing.LLMReasoningDeltaCreated:
		return o.HandleLLMReasoningDeltaCreated(e)
	case eventing.LLMToolCallResultCreated:
		return o.HandleLLMToolCallResultCreated(e)
	case eventing.LLMAgentResponseCreated:
		return o.HandleLLMAgentResponseCreated(e)
	case eventing.LLMRequestRetryScheduled:
		return o.HandleLLMRequestRetryScheduled(e)
	case eventing.CompactionCompletedCreated:
		return o.HandleCompactionCompletedCreated(e)
	case eventing.SubagentInvocationStarted, eventing.SubagentInvocationProgress, eventing.SubagentInvocationCompleted, eventing.SubagentInvocationFailed:
		return o.HandleSubagentInvocation(e)
	case eventing.StatusUpdated:
		var payload eventing.Status
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal Status: %w", err)
		}
		o.send(TUIStatus{Payload: payload})
		return nil
	default:
		return nil
	}
}

func (o *TUIOutput) RegisterHandlers(agentSession *agent.AgentSession) error {
	for _, et := range []string{
		eventing.LLMUserMessageCreated,
		eventing.LLMAgentMessageCreated,
		eventing.LLMReasoningDeltaCreated,
		eventing.LLMToolCallResultCreated,
		eventing.LLMAgentResponseCreated,
		eventing.LLMRequestRetryScheduled,
		eventing.CompactionCompletedCreated,
	} {
		if err := agentSession.OnContextEvent(et, o.DispatchEvent); err != nil {
			return err
		}
	}
	for _, et := range []string{
		eventing.SubagentInvocationStarted,
		eventing.SubagentInvocationProgress,
		eventing.SubagentInvocationCompleted,
		eventing.SubagentInvocationFailed,
		eventing.StatusUpdated,
	} {
		if err := agentSession.OnAuditEvent(et, o.DispatchEvent); err != nil {
			return err
		}
	}
	return nil
}

func (o *TUIOutput) HandleLLMUserMessageCreated(e *types.Event) error {
	return nil
}

func (o *TUIOutput) HandleLLMAgentMessageCreated(e *types.Event) error {
	var payload eventing.LLMAgentMessage
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal LLMAgentMessage: %w", err)
	}
	o.send(TUIAgentMessage{Payload: payload})
	return nil
}

func (o *TUIOutput) HandleLLMReasoningDeltaCreated(e *types.Event) error {
	var payload eventing.LLMReasoningDelta
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal LLMReasoningDelta: %w", err)
	}
	o.send(TUIReasoningDelta{Payload: payload})
	return nil
}

func (o *TUIOutput) HandleLLMToolCallResultCreated(e *types.Event) error {
	var payload eventing.LLMToolCallResult
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal LLMToolCallResult: %w", err)
	}
	o.send(TUIToolResult{Payload: payload})
	return nil
}

func (o *TUIOutput) HandleLLMAgentResponseCreated(e *types.Event) error {
	var payload eventing.LLMAgentResponse
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal LLMAgentResponse: %w", err)
	}
	o.send(TUIAgentResponse{Payload: payload})
	return nil
}

func (o *TUIOutput) HandleLLMRequestRetryScheduled(e *types.Event) error {
	var payload eventing.LLMRequestRetry
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal LLMRequestRetry: %w", err)
	}
	o.send(TUIRequestRetry{Payload: payload})
	return nil
}

func (o *TUIOutput) HandleCompactionCompletedCreated(e *types.Event) error {
	var payload eventing.CompactionCompleted
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal CompactionCompleted: %w", err)
	}
	o.send(TUICompactionCompleted{Payload: payload})
	return nil
}

func (o *TUIOutput) HandleSubagentInvocation(e *types.Event) error {
	var payload eventing.SubagentInvocation
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal SubagentInvocation: %w", err)
	}
	o.send(TUISubagentInvocation{EventType: e.EventType, Payload: payload})
	return nil
}

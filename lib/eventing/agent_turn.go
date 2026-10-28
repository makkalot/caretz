package eventing

import (
	"encoding/json"
	"fmt"

	"github.com/makkalot/caretz/lib/types"
)

const (
	AgentTurnStarted   = "Agent.TurnStarted"
	AgentTurnCompleted = "Agent.TurnCompleted"

	AgentTurnSucceeded = "succeeded"
	AgentTurnFailed    = "failed"
	AgentTurnCancelled = "cancelled"
	AgentTurnTimedOut  = "timed_out"
	AgentTurnPanicked  = "panicked"
)

type AgentTurnStartedPayload struct {
	TurnID    string `json:"turn_id"`
	MessageID string `json:"message_id"`
}

type AgentTurnCompletedPayload struct {
	TurnID    string `json:"turn_id"`
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

func NewAgentTurnStarted(originatorID, turnID, messageID string) *types.Event {
	payload, _ := json.Marshal(AgentTurnStartedPayload{TurnID: turnID, MessageID: messageID})
	return types.NewEvent(types.NewOriginator(originatorID, 0), AgentTurnStarted, string(payload))
}

func NewAgentTurnCompleted(originatorID, turnID, messageID, status, errText string) *types.Event {
	payload, _ := json.Marshal(AgentTurnCompletedPayload{TurnID: turnID, MessageID: messageID, Status: status, Error: errText})
	return types.NewEvent(types.NewOriginator(originatorID, 0), AgentTurnCompleted, string(payload))
}

func DecodeAgentTurnStarted(event *types.Event) (AgentTurnStartedPayload, error) {
	var payload AgentTurnStartedPayload
	if event == nil {
		return payload, fmt.Errorf("agent turn started event is nil")
	}
	if event.EventType != AgentTurnStarted {
		return payload, fmt.Errorf("expected event type %s, got %s", AgentTurnStarted, event.EventType)
	}
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		return payload, err
	}
	if payload.TurnID == "" || payload.MessageID == "" {
		return payload, fmt.Errorf("agent turn started event is incomplete")
	}
	return payload, nil
}

func DecodeAgentTurnCompleted(event *types.Event) (AgentTurnCompletedPayload, error) {
	var payload AgentTurnCompletedPayload
	if event == nil {
		return payload, fmt.Errorf("agent turn completed event is nil")
	}
	if event.EventType != AgentTurnCompleted {
		return payload, fmt.Errorf("expected event type %s, got %s", AgentTurnCompleted, event.EventType)
	}
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		return payload, err
	}
	if payload.TurnID == "" || payload.MessageID == "" || payload.Status == "" {
		return payload, fmt.Errorf("agent turn completed event is incomplete")
	}
	return payload, nil
}

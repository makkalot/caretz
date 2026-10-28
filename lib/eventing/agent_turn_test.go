package eventing

import "testing"

func TestAgentTurnEventsRoundTrip(t *testing.T) {
	started, err := DecodeAgentTurnStarted(NewAgentTurnStarted("session-1", "turn-1", "message-1"))
	if err != nil {
		t.Fatal(err)
	}
	if started.TurnID != "turn-1" || started.MessageID != "message-1" {
		t.Fatalf("started = %+v", started)
	}

	completed, err := DecodeAgentTurnCompleted(NewAgentTurnCompleted("session-1", "turn-1", "message-1", AgentTurnFailed, "boom"))
	if err != nil {
		t.Fatal(err)
	}
	if completed.TurnID != "turn-1" || completed.MessageID != "message-1" || completed.Status != AgentTurnFailed || completed.Error != "boom" {
		t.Fatalf("completed = %+v", completed)
	}
}

func TestDecodeAgentTurnRejectsIncompletePayload(t *testing.T) {
	if _, err := DecodeAgentTurnStarted(NewAgentTurnStarted("session-1", "", "message-1")); err == nil {
		t.Fatal("expected incomplete started event error")
	}
	if _, err := DecodeAgentTurnCompleted(NewAgentTurnCompleted("session-1", "turn-1", "message-1", "", "")); err == nil {
		t.Fatal("expected incomplete completed event error")
	}
}

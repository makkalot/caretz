package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/explorer"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type sessionRepairRequired struct {
	Reason             string
	TargetBeforeIndex  int
	PendingToolCallIDs []string
	DroppedMessages    int
	Preview            string
}

func (e *sessionRepairRequired) Error() string {
	return fmt.Sprintf("session repair required: %s before message index %d; run `caretz session repair <session-id>`", e.Reason, e.TargetBeforeIndex)
}

type sessionRepairAnalysis struct {
	Required                bool
	Reason                  string
	OriginatorID            string
	ParentOriginatorID      string
	ParentOriginatorVersion uint64
	TargetBeforeVersion     uint64
	TargetBeforeRole        llm.Role
	CompletedThroughVersion uint64
	InterruptedEventCount   int
	PendingToolCallIDs      []string
	PendingToolCalls        []llm.ToolCall
	Message                 string
	Preview                 string
}

func analyzeSessionRepair(events []*types.Event, originatorID string) sessionRepairAnalysis {
	pending := map[string]llm.ToolCall{}
	pendingOrder := []string{}
	awaitingFinalAssistant := false
	activeUserTurn := false
	parentOriginatorID := ""
	parentOriginatorVersion := uint64(0)
	completedThroughVersion := uint64(0)
	interruptedEventCount := 0

	for _, e := range events {
		switch e.EventType {
		case eventing.SummaryPromptCreated, eventing.LLMAgentSummaryResponseCreated, eventing.TaskUpdatedCreated, eventing.TaskSnapshotCreated:
			continue
		case eventing.LLMUserMessageCreated:
			if awaitingFinalAssistant || len(pending) > 0 {
				pendingIDs := outstandingPendingIDs(pendingOrder, pending)
				pendingCalls := outstandingPendingToolCalls(pendingOrder, pending)
				message := eventing.InterruptedAfterToolsMessage
				if len(pendingIDs) > 0 {
					message = eventing.InterruptedDuringToolsMessage
				}
				return sessionRepairAnalysis{
					Required:                true,
					Reason:                  "incomplete_turn_detected",
					OriginatorID:            originatorID,
					ParentOriginatorID:      parentOriginatorID,
					ParentOriginatorVersion: parentOriginatorVersion,
					TargetBeforeVersion:     e.Originator.Version,
					TargetBeforeRole:        llm.RoleUser,
					CompletedThroughVersion: completedThroughVersion,
					InterruptedEventCount:   interruptedEventCount,
					PendingToolCallIDs:      pendingIDs,
					PendingToolCalls:        pendingCalls,
					Message:                 message,
					Preview:                 "An unfinished assistant/tool turn appears before a later user message.",
				}
			}
			awaitingFinalAssistant = false
			pending = map[string]llm.ToolCall{}
			pendingOrder = nil
			activeUserTurn = true
			interruptedEventCount = 0
		case eventing.LLMAgentMessageCreated:
			var payload eventing.LLMAgentMessage
			if json.Unmarshal([]byte(e.Payload), &payload) != nil || len(payload.Message.ToolCalls) == 0 {
				continue
			}
			if !activeUserTurn {
				continue
			}
			if !awaitingFinalAssistant {
				interruptedEventCount = 0
			}
			parentOriginatorID = payload.ParentOriginatorID
			parentOriginatorVersion = payload.ParentOriginatorVersion
			interruptedEventCount++
			for _, tc := range payload.Message.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if _, exists := pending[tc.ID]; !exists {
					pendingOrder = append(pendingOrder, tc.ID)
				}
				pending[tc.ID] = tc
			}
			awaitingFinalAssistant = true
		case eventing.LLMToolCallResultCreated:
			var payload eventing.LLMToolCallResult
			if json.Unmarshal([]byte(e.Payload), &payload) != nil {
				continue
			}
			if !activeUserTurn {
				continue
			}
			delete(pending, payload.Message.ToolCallID)
			if awaitingFinalAssistant {
				interruptedEventCount++
			}
		case eventing.LLMToolCallRecoveryResultCreated:
			payload, err := eventing.DecodeRecoveryPayload(e)
			if err == nil {
				delete(pending, payload.ToolCallID)
			}
		case eventing.LLMAgentResponseCreated, eventing.LLMAgentInterruptedResponseCreated:
			if !activeUserTurn {
				continue
			}
			awaitingFinalAssistant = false
			pending = map[string]llm.ToolCall{}
			pendingOrder = nil
			activeUserTurn = false
			completedThroughVersion = e.Originator.Version
			interruptedEventCount = 0
		}
	}

	if activeUserTurn && (awaitingFinalAssistant || len(pending) > 0) {
		pendingIDs := outstandingPendingIDs(pendingOrder, pending)
		pendingCalls := outstandingPendingToolCalls(pendingOrder, pending)
		message := eventing.InterruptedAfterToolsMessage
		if len(pendingIDs) > 0 {
			message = eventing.InterruptedDuringToolsMessage
		}
		return sessionRepairAnalysis{
			Required:                true,
			Reason:                  "incomplete_tail_detected",
			OriginatorID:            originatorID,
			ParentOriginatorID:      parentOriginatorID,
			ParentOriginatorVersion: parentOriginatorVersion,
			CompletedThroughVersion: completedThroughVersion,
			InterruptedEventCount:   interruptedEventCount,
			PendingToolCallIDs:      pendingIDs,
			PendingToolCalls:        pendingCalls,
			Message:                 message,
			Preview:                 "The saved conversation ends inside an unfinished assistant/tool turn.",
		}
	}

	return sessionRepairAnalysis{OriginatorID: originatorID, CompletedThroughVersion: completedThroughVersion}
}

func outstandingPendingIDs(order []string, pending map[string]llm.ToolCall) []string {
	ids := make([]string, 0, len(order))
	for _, id := range order {
		if _, ok := pending[id]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func outstandingPendingToolCalls(order []string, pending map[string]llm.ToolCall) []llm.ToolCall {
	calls := make([]llm.ToolCall, 0, len(order))
	for _, id := range order {
		if call, ok := pending[id]; ok {
			calls = append(calls, call)
		}
	}
	return calls
}

func requireSessionRepairCheck(sessionsDir, sessionID string) error {
	sessionDir, _, err := explorer.FindSessionDir(sessionsDir, sessionID)
	if err != nil {
		return err
	}
	events, err := readContextEventsForSession(sessionDir, sessionID)
	if err != nil {
		return err
	}
	if analysis := analyzeSessionRepair(events, sessionID); analysis.Required {
		return &sessionRepairRequired{
			Reason:             analysis.Reason,
			PendingToolCallIDs: append([]string(nil), analysis.PendingToolCallIDs...),
			Preview:            analysis.Preview,
		}
	}
	return nil
}

func isSessionNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "session ") && strings.Contains(err.Error(), " not found")
}

func injectContextRecovery(events []*types.Event, analysis sessionRepairAnalysis, recoveryID string) ([]*types.Event, bool) {
	out := make([]*types.Event, 0, len(events)+len(analysis.PendingToolCalls)+1)
	inserted := false
	for _, e := range events {
		if !inserted && e.Originator != nil && e.Originator.ID == analysis.OriginatorID && e.Originator.Version == analysis.TargetBeforeVersion {
			for _, toolCall := range analysis.PendingToolCalls {
				out = append(out, eventing.NewLLMToolCallRecoveryResult(
					analysis.OriginatorID,
					analysis.ParentOriginatorID,
					analysis.ParentOriginatorVersion,
					recoveryID,
					analysis.Reason,
					toolCall,
				))
			}
			out = append(out, eventing.NewLLMAgentInterruptedResponse(
				analysis.OriginatorID,
				analysis.ParentOriginatorID,
				analysis.ParentOriginatorVersion,
				recoveryID,
				analysis.Reason,
				analysis.Message,
				analysis.PendingToolCallIDs,
			))
			inserted = true
		}
		out = append(out, e)
	}
	if !inserted && analysis.TargetBeforeVersion == 0 {
		for _, toolCall := range analysis.PendingToolCalls {
			out = append(out, eventing.NewLLMToolCallRecoveryResult(
				analysis.OriginatorID,
				analysis.ParentOriginatorID,
				analysis.ParentOriginatorVersion,
				recoveryID,
				analysis.Reason,
				toolCall,
			))
		}
		out = append(out, eventing.NewLLMAgentInterruptedResponse(
			analysis.OriginatorID,
			analysis.ParentOriginatorID,
			analysis.ParentOriginatorVersion,
			recoveryID,
			analysis.Reason,
			analysis.Message,
			analysis.PendingToolCallIDs,
		))
		inserted = true
	}
	return out, inserted
}

func renumberContextEvents(events []*types.Event, sessionID string) []*types.Event {
	versionMap := map[uint64]uint64{}
	nextVersion := uint64(1)
	for _, e := range events {
		if e.Originator == nil || e.Originator.ID != sessionID {
			continue
		}
		oldVersion := e.Originator.Version
		e.Originator.Version = nextVersion
		if oldVersion != 0 {
			versionMap[oldVersion] = nextVersion
		}
		nextVersion++
	}
	for _, e := range events {
		remapContextMetadataVersions(e, versionMap)
	}
	return events
}

func remapContextMetadataVersions(event *types.Event, versionMap map[uint64]uint64) {
	remap := func(v uint64) uint64 {
		if v == 0 {
			return 0
		}
		if mapped, ok := versionMap[v]; ok {
			return mapped
		}
		return v
	}
	switch event.EventType {
	case eventing.SummaryPromptCreated:
		var payload eventing.SummaryPrompt
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return
		}
		payload.ActiveUserVersion = remap(payload.ActiveUserVersion)
		payload.CoversThroughVersion = remap(payload.CoversThroughVersion)
		data, _ := json.Marshal(payload)
		event.Payload = string(data)
	case eventing.LLMAgentSummaryResponseCreated:
		var payload eventing.LLMAgentResponse
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return
		}
		payload.ActiveUserVersion = remap(payload.ActiveUserVersion)
		payload.CoversThroughVersion = remap(payload.CoversThroughVersion)
		data, _ := json.Marshal(payload)
		event.Payload = string(data)
	}
}

func filterModelContextEvents(events []*types.Event) []*types.Event {
	out := make([]*types.Event, 0, len(events))
	for _, e := range events {
		switch e.EventType {
		case eventing.SummaryPromptCreated,
			eventing.LLMAgentSummaryResponseCreated,
			eventing.TaskUpdatedCreated,
			eventing.TaskSnapshotCreated,
			eventing.LLMReasoningDeltaCreated,
			eventing.LLMRequestRetryScheduled,
			eventing.CompactionCompletedCreated,
			"Checkpoint.Created":
			continue
		}
		out = append(out, e)
	}
	return out
}

func writeContextJSONPL(path string, events []*types.Event) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	encoder := json.NewEncoder(out)
	for _, event := range events {
		createdAt := event.OccurredOn.Unix()
		if createdAt == 0 {
			createdAt = time.Now().UTC().Unix()
		}
		fileEvent := struct {
			OriginatorID      string      `json:"originator_id"`
			OriginatorVersion uint64      `json:"originator_version"`
			EventType         string      `json:"event_type"`
			Payload           interface{} `json:"payload"`
			CreatedAt         int64       `json:"created_at"`
		}{
			OriginatorID:      event.Originator.ID,
			OriginatorVersion: event.Originator.Version,
			EventType:         event.EventType,
			Payload:           jsonPayloadValue(event.Payload),
			CreatedAt:         createdAt,
		}
		if err := encoder.Encode(fileEvent); err != nil {
			return err
		}
	}
	return nil
}

func jsonPayloadValue(payload string) interface{} {
	if payload == "" {
		return nil
	}
	var raw interface{}
	if err := json.Unmarshal([]byte(payload), &raw); err == nil {
		return raw
	}
	return payload
}

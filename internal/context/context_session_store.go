package context

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/eventstore"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

const doneTaskRetention = 24 * time.Hour

var nowUTC = func() time.Time {
	return time.Now().UTC()
}

type ContextSessionStore struct {
	eventsLog     *EventSessionStore
	contextStore  eventstore.Store
	latestContext *types.Event
	eventLoop     *eventing.SyncEventLoop
}

func NewContextSessionStore(channelID string, eventsLog *EventSessionStore, contextStore eventstore.Store,
	eventLoop *eventing.SyncEventLoop) (*ContextSessionStore, error) {
	latestContext, err := latestEvent(contextStore, types.NewOriginator(channelID, 0))
	if err != nil {
		return nil, fmt.Errorf("failed to get latest context: %w", err)
	}

	return &ContextSessionStore{
		eventsLog:     eventsLog,
		contextStore:  contextStore,
		latestContext: latestContext,
		eventLoop:     eventLoop,
	}, nil
}

func (c *ContextSessionStore) assignVersion(event *types.Event) error {
	if c.latestContext != nil && c.latestContext.Originator.ID == event.Originator.ID {
		event.Originator.Version = c.latestContext.Originator.Version + 1
		return nil
	}
	latest, err := latestEvent(c.contextStore, types.NewOriginator(event.Originator.ID, 0))
	if err != nil {
		return err
	}
	if latest != nil {
		event.Originator.Version = latest.Originator.Version + 1
	} else {
		event.Originator.Version = 1
	}
	return nil
}

func (c *ContextSessionStore) AppendUserMessage(e *types.Event) error {
	if e.EventType != eventing.LLMUserMessageCreated && e.EventType != eventing.SummaryPromptCreated {
		return fmt.Errorf("expected event type %s or %s, got %s", eventing.LLMUserMessageCreated, eventing.SummaryPromptCreated, e.EventType)
	}
	if err := c.assignVersion(e); err != nil {
		return fmt.Errorf("failed to assign version: %w", err)
	}
	if err := c.contextStore.Append(e); err != nil {
		return fmt.Errorf("failed to append user message: %w", err)
	}
	c.latestContext = e
	return c.eventLoop.RunEvent(e)
}

func (c *ContextSessionStore) AppendAgentResponse(e *types.Event) (*types.Event, error) {
	if e.EventType != eventing.LLMAgentResponseCreated && e.EventType != eventing.LLMAgentSummaryResponseCreated && e.EventType != eventing.LLMAgentInterruptedResponseCreated {
		return nil, fmt.Errorf("expected event type %s, %s, or %s, got %s", eventing.LLMAgentResponseCreated, eventing.LLMAgentSummaryResponseCreated, eventing.LLMAgentInterruptedResponseCreated, e.EventType)
	}
	if err := c.assignVersion(e); err != nil {
		return nil, fmt.Errorf("failed to assign version: %w", err)
	}
	if err := c.contextStore.Append(e); err != nil {
		return nil, fmt.Errorf("failed to append agent response: %w", err)
	}
	c.latestContext = e
	err := c.eventLoop.RunEvent(e)

	return e, err
}

func (c *ContextSessionStore) AppendToolResult(e *types.Event) (*types.Event, error) {
	if e.EventType != eventing.LLMToolCallResultCreated && e.EventType != eventing.LLMToolCallRecoveryResultCreated {
		return nil, fmt.Errorf("expected event type %s or %s, got %s", eventing.LLMToolCallResultCreated, eventing.LLMToolCallRecoveryResultCreated, e.EventType)
	}
	if err := c.assignVersion(e); err != nil {
		return nil, fmt.Errorf("failed to assign version: %w", err)
	}
	if err := c.contextStore.Append(e); err != nil {
		return nil, fmt.Errorf("failed to append tool result: %w", err)
	}
	c.latestContext = e
	err := c.eventLoop.RunEvent(e)
	return e, err
}

func (c *ContextSessionStore) AppendTaskEvent(e *types.Event) (*types.Event, error) {
	if e.EventType != eventing.TaskUpdatedCreated && e.EventType != eventing.TaskSnapshotCreated {
		return nil, fmt.Errorf("expected event type %s or %s, got %s", eventing.TaskUpdatedCreated, eventing.TaskSnapshotCreated, e.EventType)
	}
	if err := c.assignVersion(e); err != nil {
		return nil, fmt.Errorf("failed to assign version: %w", err)
	}
	if err := c.contextStore.Append(e); err != nil {
		return nil, fmt.Errorf("failed to append task event: %w", err)
	}
	c.latestContext = e
	err := c.eventLoop.RunEvent(e)
	return e, err
}

func (c *ContextSessionStore) AppendAgentMessage(e *types.Event) (*types.Event, error) {
	if e.EventType != eventing.LLMAgentMessageCreated {
		return nil, fmt.Errorf("expected event type %s, got %s", eventing.LLMAgentMessageCreated, e.EventType)
	}
	if err := c.assignVersion(e); err != nil {
		return nil, fmt.Errorf("failed to assign version: %w", err)
	}
	if err := c.contextStore.Append(e); err != nil {
		return nil, fmt.Errorf("failed to append agent message: %w", err)
	}
	c.latestContext = e
	err := c.eventLoop.RunEvent(e)
	return e, err
}

func (c *ContextSessionStore) AddEventHandler(eventType string, handler eventing.HandleFunc) error {
	return c.eventLoop.AddHandler(eventType, handler)
}

func (c *ContextSessionStore) SubscribeEvents(eventTypes ...string) (eventing.Subscription, error) {
	return c.eventLoop.Subscribe(eventTypes...)
}

func (c *ContextSessionStore) PublishEvent(e *types.Event) error {
	return c.eventLoop.RunEvent(e)
}

func (c *ContextSessionStore) WhatsPending() ([]*types.Event, error) {
	events, err := c.eventsLog.GetAllEvents(c.eventsLog.channelId)
	if err != nil {
		return nil, fmt.Errorf("failed to get events log: %w", err)
	}

	var pending []*types.Event
	for _, event := range events {
		if event.EventType != eventing.LogMessageUserCreated {
			continue
		}
		pending = append(pending, event)
	}

	contextEvents, err := c.contextStore.Get(types.NewOriginator(c.eventsLog.channelId, 0), false)
	if err != nil {
		return nil, fmt.Errorf("failed to get context events: %w", err)
	}

	var agentResponses int
	for _, ce := range contextEvents {
		if isTerminalContextResponse(ce.EventType) {
			agentResponses++
		}
	}

	if agentResponses >= len(pending) {
		return nil, nil
	}

	return pending[agentResponses:], nil
}

func (c *ContextSessionStore) IsEventProcessed(event *types.Event) (bool, error) {
	contextEvents, err := c.contextStore.Get(types.NewOriginator(c.eventsLog.channelId, 0), false)
	if err != nil {
		return false, fmt.Errorf("failed to get context events: %w", err)
	}

	eventMsg, err := eventing.EventToLLMMessage(event)
	if err != nil {
		return false, fmt.Errorf("failed to extract message from event: %w", err)
	}

	eventParentVersion := extractParentOriginatorVersion(event)

	found := false
	for _, ce := range contextEvents {
		if !found {
			if ce.EventType != event.EventType {
				continue
			}
			ceMsg, err := eventing.EventToLLMMessage(ce)
			if err != nil {
				continue
			}
			if ceMsg.Content == eventMsg.Content && ceMsg.Role == eventMsg.Role {
				ceParentVersion := extractParentOriginatorVersion(ce)
				if ceParentVersion == eventParentVersion {
					found = true
				}
			}
			continue
		}
		if isTerminalContextResponse(ce.EventType) {
			return true, nil
		}
	}

	return false, nil
}

func isTerminalContextResponse(eventType string) bool {
	return eventType == eventing.LLMAgentResponseCreated || eventType == eventing.LLMAgentInterruptedResponseCreated
}

func (c *ContextSessionStore) IsUserEventInContext(event *types.Event) (bool, error) {
	index, err := c.FirstUserEventInContext(event)
	return index >= 0, err
}

// FirstUserEventInContext returns the index of the first candidate whose
// message is already in context, or -1, reading the context store once.
func (c *ContextSessionStore) FirstUserEventInContext(candidates ...*types.Event) (int, error) {
	for _, event := range candidates {
		if event.EventType != eventing.LLMUserMessageCreated {
			return -1, fmt.Errorf("expected event type %s, got %s", eventing.LLMUserMessageCreated, event.EventType)
		}
	}
	contextEvents, err := c.contextStore.Get(types.NewOriginator(c.eventsLog.channelId, 0), false)
	if err != nil {
		return -1, fmt.Errorf("failed to get context events: %w", err)
	}
	var contextMessages []llm.Message
	var contextParentVersions []uint64
	for _, ce := range contextEvents {
		if ce.EventType != eventing.LLMUserMessageCreated {
			continue
		}
		ceMsg, err := eventing.EventToLLMMessage(ce)
		if err != nil {
			continue
		}
		contextMessages = append(contextMessages, ceMsg)
		contextParentVersions = append(contextParentVersions, extractParentOriginatorVersion(ce))
	}
	for i, event := range candidates {
		eventMsg, err := eventing.EventToLLMMessage(event)
		if err != nil {
			return -1, fmt.Errorf("failed to extract message from event: %w", err)
		}
		eventParentVersion := extractParentOriginatorVersion(event)
		for j, ceMsg := range contextMessages {
			if ceMsg.Content == eventMsg.Content && ceMsg.Role == eventMsg.Role &&
				contextParentVersions[j] == eventParentVersion {
				return i, nil
			}
		}
	}
	return -1, nil
}

func extractParentOriginatorVersion(event *types.Event) uint64 {
	switch event.EventType {
	case eventing.LLMUserMessageCreated:
		var payload eventing.LLMUserMessage
		if err := json.Unmarshal([]byte(event.Payload), &payload); err == nil {
			return payload.ParentOriginatorVersion
		}
	case eventing.SummaryPromptCreated:
		var payload eventing.SummaryPrompt
		if err := json.Unmarshal([]byte(event.Payload), &payload); err == nil {
			return payload.ParentOriginatorVersion
		}
	}
	return 0
}

func (c *ContextSessionStore) GetAllContextEvents(channelID string) ([]*types.Event, error) {
	return c.contextStore.Get(types.NewOriginator(channelID, 0), false)
}

func (c *ContextSessionStore) GetSummaryEvents(channelID string) ([]*types.Event, error) {
	events, err := c.contextStore.Get(types.NewOriginator(channelID, 0), false)
	if err != nil {
		return nil, fmt.Errorf("failed to get context events: %w", err)
	}

	var summaryEvents []*types.Event
	for _, event := range events {
		if event.EventType == eventing.LLMAgentSummaryResponseCreated {
			summaryEvents = append(summaryEvents, event)
		}
	}

	return summaryEvents, nil
}

func (c *ContextSessionStore) RestoreTaskState(channelID string) (*tasks.State, int, error) {
	events, err := c.contextStore.Get(types.NewOriginator(channelID, 0), false)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get context events: %w", err)
	}

	start := 0
	state := tasks.NewState()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType != eventing.TaskSnapshotCreated {
			continue
		}
		var payload eventing.TaskSnapshot
		if err := json.Unmarshal([]byte(events[i].Payload), &payload); err != nil {
			return nil, 0, fmt.Errorf("failed to unmarshal task snapshot: %w", err)
		}
		state = tasks.FromSnapshot(payload.Snapshot)
		for _, task := range state.Tasks {
			if task.Status == tasks.StatusDone && task.CompletedAt == nil {
				state.MarkCompletedAt(task.ID, events[i].OccurredOn)
			}
		}
		start = i + 1
		break
	}

	mutationsSinceSnapshot := 0
	for _, e := range events[start:] {
		if e.EventType != eventing.TaskUpdatedCreated {
			continue
		}
		var payload eventing.TaskUpdated
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return nil, 0, fmt.Errorf("failed to unmarshal task update: %w", err)
		}
		if _, err := state.Apply(payload.Update); err != nil {
			return nil, 0, fmt.Errorf("failed to replay task update at version %d: %w", e.Originator.Version, err)
		}
		if payload.Update.Op == tasks.OpDone {
			state.MarkCompletedAt(payload.Update.ID, e.OccurredOn)
			state.SetStats(payload.Update.ID, payload.Stats)
		}
		mutationsSinceSnapshot++
	}

	expiredIDs := state.ExpiredDoneTaskIDs(nowUTC(), doneTaskRetention)
	if len(expiredIDs) > 0 {
		update := tasks.Update{Op: tasks.OpPrune, IDs: expiredIDs}
		pruneEvent := eventing.NewTaskUpdated(channelID, channelID, 0, update)
		if _, err := c.AppendTaskEvent(pruneEvent); err != nil {
			return nil, 0, fmt.Errorf("failed to append expired task prune: %w", err)
		}
		if _, err := state.Apply(update); err != nil {
			return nil, 0, fmt.Errorf("failed to apply expired task prune: %w", err)
		}
		mutationsSinceSnapshot++
	}

	return state, mutationsSinceSnapshot, nil
}

func (c *ContextSessionStore) GetEventsFrom(e *types.Event) ([]*types.Event, error) {
	events, err := c.contextStore.Get(types.NewOriginator(e.Originator.ID, 0), false)
	if err != nil {
		return nil, fmt.Errorf("failed to get events from context file: %w", err)
	}

	var result []*types.Event
	found := false
	for _, event := range events {
		if !found {
			if event.Originator.ID == e.Originator.ID &&
				event.Originator.Version == e.Originator.Version {
				found = true
			}
			continue
		}
		result = append(result, event)
	}

	return result, nil
}

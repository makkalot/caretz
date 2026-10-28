package context

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/eventstore"
	"github.com/makkalot/caretz/lib/types"
)

const dedupWindow = 2 * time.Second

type EventSessionStore struct {
	mu          sync.Mutex
	adapterType string
	channelId   string
	baseDir     string
	eventLog    eventstore.Store
	latestEvent *types.Event
	eventLoop   *eventing.SyncEventLoop
}

func NewEventSessionStore(adapterType, channelID, baseDir string, eventsLog eventstore.Store) (*EventSessionStore, error) {
	latestEvent, err := latestEvent(eventsLog, types.NewOriginator(channelID, 0))
	if err != nil {
		return nil, fmt.Errorf("failed to get latest event: %w", err)
	}

	return &EventSessionStore{
		adapterType: adapterType,
		channelId:   channelID,
		baseDir:     baseDir,
		eventLog:    eventsLog,
		latestEvent: latestEvent,
		eventLoop:   eventing.NewSyncEventLoop(context.Background()),
	}, nil
}

func latestEvent(store eventstore.Store, originator *types.Originator) (*types.Event, error) {
	if originator == nil {
		return nil, fmt.Errorf("originator cannot be nil")
	}
	events, err := store.Get(originator, false)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	if originator.Version == 0 {
		return events[len(events)-1], nil
	}
	last := events[len(events)-1]
	if last.Originator.Version != originator.Version {
		return nil, nil
	}
	return last, nil
}

func (e *EventSessionStore) assignVersion(event *types.Event) error {
	if e.latestEvent != nil && e.latestEvent.Originator.ID == event.Originator.ID {
		event.Originator.Version = e.latestEvent.Originator.Version + 1
		return nil
	}
	latest, err := latestEvent(e.eventLog, types.NewOriginator(event.Originator.ID, 0))
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

func (e *EventSessionStore) Append(event *types.Event) error {
	e.mu.Lock()
	if e.latestEvent != nil {
		samePayload := e.latestEvent.Payload == event.Payload
		if !samePayload && event.EventType == eventing.LogMessageUserCreated && e.latestEvent.EventType == event.EventType {
			oldMessage, oldErr := eventing.DecodeLogMessage(e.latestEvent)
			newMessage, newErr := eventing.DecodeLogMessage(event)
			samePayload = oldErr == nil && newErr == nil && oldMessage.Content == newMessage.Content
		}
		if e.latestEvent.Originator.ID == event.Originator.ID &&
			e.latestEvent.EventType == event.EventType &&
			samePayload &&
			event.OccurredOn.Sub(e.latestEvent.OccurredOn) < dedupWindow {
			e.mu.Unlock()
			return nil
		}
	}

	if err := e.assignVersion(event); err != nil {
		e.mu.Unlock()
		return fmt.Errorf("failed to assign version: %w", err)
	}

	if err := e.eventLog.Append(event); err != nil {
		e.mu.Unlock()
		return fmt.Errorf("failed to append event: %w", err)
	}

	e.latestEvent = event
	// Queue while the append lock still establishes commit order. Synchronous
	// handlers run after unlocking so they may safely append follow-up events.
	e.eventLoop.EnqueueEvent(event)
	e.mu.Unlock()
	return e.eventLoop.RunHandlers(event)
}

func (e *EventSessionStore) AddEventHandler(eventType string, handler eventing.HandleFunc) error {
	return e.eventLoop.AddHandler(eventType, handler)
}

func (e *EventSessionStore) SubscribeEvents(eventTypes ...string) (eventing.Subscription, error) {
	return e.eventLoop.Subscribe(eventTypes...)
}

func (e *EventSessionStore) PublishEvent(event *types.Event) error {
	return e.eventLoop.RunEvent(event)
}

func (e *EventSessionStore) GetLatest() (*types.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.latestEvent, nil
}

func (e *EventSessionStore) GetLatestUserMessage() (*types.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	events, err := e.eventLog.Get(types.NewOriginator(e.channelId, 0), false)
	if err != nil {
		return nil, fmt.Errorf("failed to get events: %w", err)
	}

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType == eventing.LogMessageUserCreated {
			return events[i], nil
		}
	}

	return nil, nil
}

func (e *EventSessionStore) GetLatestAgentResponse() (*types.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	events, err := e.eventLog.Get(types.NewOriginator(e.channelId, 0), false)
	if err != nil {
		return nil, fmt.Errorf("failed to get events: %w", err)
	}

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType == eventing.LogMessageAgentCreated {
			return events[i], nil
		}
	}

	return nil, nil
}

func (e *EventSessionStore) GetLatestAgentResponseForUserMessage(event *types.Event) (*types.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	user, err := eventing.DecodeLogMessage(event)
	if err != nil {
		return nil, err
	}
	events, err := e.eventLog.Get(types.NewOriginator(e.channelId, 0), false)
	if err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		candidate := events[i]
		if candidate.EventType != eventing.LogMessageAgentCreated && candidate.EventType != eventing.LogMessageAgentInterrupted {
			continue
		}
		if candidate.EventType == eventing.LogMessageAgentCreated {
			message, decodeErr := eventing.DecodeLogMessage(candidate)
			if decodeErr == nil && message.InReplyTo == user.MessageID {
				return candidate, nil
			}
			continue
		}
		recovery, decodeErr := eventing.DecodeRecoveryPayload(candidate)
		if decodeErr == nil && recovery.InReplyTo == user.MessageID {
			return candidate, nil
		}
	}
	return nil, nil
}

func (e *EventSessionStore) GetAllEvents(channelID string) ([]*types.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.eventLog.Get(types.NewOriginator(channelID, 0), false)
}

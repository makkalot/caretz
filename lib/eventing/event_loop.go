package eventing

import (
	"context"
	"fmt"
	"sync"

	"github.com/makkalot/caretz/lib/types"
)

type HandleFunc func(e *types.Event) error

// SyncEventLoop is a simple implementation of an event loop that processes events synchronously.
// you can add multipls of them for each eventtype and they will run in order of registration, you can also add multiple handlers for the same event type and they will run in order of registration
type SyncEventLoop struct {
	mu       sync.RWMutex
	handlers map[string][]HandleFunc
	subs     map[string]map[*asyncSubscription]struct{}
}

func NewSyncEventLoop(ctx context.Context) *SyncEventLoop {
	return &SyncEventLoop{
		handlers: make(map[string][]HandleFunc),
		subs:     make(map[string]map[*asyncSubscription]struct{}),
	}
}

// Subscription is a live, multi-event subscription. Each subscription owns a
// single merged channel; separate subscriptions never compete for events.
type Subscription interface {
	Events() <-chan *types.Event
	Close()
}

type asyncSubscription struct {
	loop  *SyncEventLoop
	types []string
	out   chan *types.Event
	wake  chan struct{}
	done  chan struct{}

	mu     sync.Mutex
	queue  []*types.Event
	closed bool
	once   sync.Once
}

func (l *SyncEventLoop) Subscribe(eventTypes ...string) (Subscription, error) {
	if len(eventTypes) == 0 {
		return nil, fmt.Errorf("at least one event type is required")
	}
	seen := make(map[string]struct{}, len(eventTypes))
	typesList := make([]string, 0, len(eventTypes))
	for _, eventType := range eventTypes {
		if eventType == "" {
			return nil, fmt.Errorf("event type must not be empty")
		}
		if _, ok := seen[eventType]; ok {
			continue
		}
		seen[eventType] = struct{}{}
		typesList = append(typesList, eventType)
	}
	s := &asyncSubscription{loop: l, types: typesList, out: make(chan *types.Event), wake: make(chan struct{}, 1), done: make(chan struct{})}
	l.mu.Lock()
	for _, eventType := range typesList {
		if l.subs[eventType] == nil {
			l.subs[eventType] = map[*asyncSubscription]struct{}{}
		}
		l.subs[eventType][s] = struct{}{}
	}
	l.mu.Unlock()
	go s.run()
	return s, nil
}

func (l *SyncEventLoop) AddHandler(eventType string, handler HandleFunc) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.handlers[eventType] = append(l.handlers[eventType], handler)
	return nil
}

func (l *SyncEventLoop) RunEvent(e *types.Event) error {
	handlerErr := l.RunHandlers(e)
	l.EnqueueEvent(e)
	return handlerErr
}

// RunHandlers dispatches only the synchronous side of the loop.
func (l *SyncEventLoop) RunHandlers(e *types.Event) error {
	l.mu.RLock()
	handlers := append([]HandleFunc(nil), l.handlers[e.EventType]...)
	l.mu.RUnlock()

	for _, h := range handlers {
		if err := h(e); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueEvent dispatches only to live asynchronous subscriptions.
func (l *SyncEventLoop) EnqueueEvent(e *types.Event) {
	l.mu.RLock()
	subs := make([]*asyncSubscription, 0, len(l.subs[e.EventType]))
	for sub := range l.subs[e.EventType] {
		subs = append(subs, sub)
	}
	l.mu.RUnlock()
	for _, sub := range subs {
		sub.enqueue(e)
	}
}

func (s *asyncSubscription) Events() <-chan *types.Event { return s.out }

func (s *asyncSubscription) Close() {
	s.once.Do(func() {
		if s.loop != nil {
			s.loop.mu.Lock()
			for _, eventType := range s.types {
				delete(s.loop.subs[eventType], s)
				if len(s.loop.subs[eventType]) == 0 {
					delete(s.loop.subs, eventType)
				}
			}
			s.loop.mu.Unlock()
		}
		s.mu.Lock()
		s.closed = true
		s.queue = nil
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *asyncSubscription) enqueue(event *types.Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, event)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *asyncSubscription) run() {
	defer close(s.out)
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			event := s.queue[0]
			s.queue[0] = nil
			s.queue = s.queue[1:]
			s.mu.Unlock()
			select {
			case s.out <- event:
			case <-s.done:
				return
			}
			continue
		}
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
	}
}

// Package turnscope tracks work that must stop with its originating turn.
package turnscope

import (
	"context"
	"errors"
	"sync"
)

type key struct{}

type Scope struct {
	mu        sync.Mutex
	callbacks []func() (string, error)
	stopped   bool
	notes     []string
	err       error
	done      chan struct{}
}

func With(ctx context.Context, scope *Scope) context.Context {
	return context.WithValue(ctx, key{}, scope)
}
func From(ctx context.Context) *Scope { scope, _ := ctx.Value(key{}).(*Scope); return scope }

// Register is serialized with Stop, including launches racing cancellation.
func (s *Scope) Register(fn func() (string, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		s.run(fn)
		return
	}
	s.callbacks = append(s.callbacks, fn)
	s.mu.Unlock()
}

func (s *Scope) run(fn func() (string, error)) {
	note, err := fn()
	s.mu.Lock()
	defer s.mu.Unlock()
	if note != "" {
		s.notes = append(s.notes, note)
	}
	s.err = errors.Join(s.err, err)
}

func (s *Scope) Stop() ([]string, error) {
	s.mu.Lock()
	if s.stopped {
		done := s.done
		s.mu.Unlock()
		<-done
		s.mu.Lock()
		defer s.mu.Unlock()
		return append([]string(nil), s.notes...), s.err
	}
	s.stopped = true
	s.done = make(chan struct{})
	callbacks := s.callbacks
	s.callbacks = nil
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, fn := range callbacks {
		wg.Add(1)
		go func() { defer wg.Done(); s.run(fn) }()
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.done)
	return append([]string(nil), s.notes...), s.err
}

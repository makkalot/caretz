package agent

import (
	"context"
	"sync"

	"github.com/makkalot/caretz/lib/progress"
)

// executionLock retains Lock for short internal operations and supports
// abandoning a foreground turn before it acquires session ownership.
type executionLock struct {
	once  sync.Once
	token chan struct{}
}

func (m *executionLock) init() { m.once.Do(func() { m.token = make(chan struct{}, 1) }) }
func (m *executionLock) Lock() { _ = m.LockContext(context.Background()) }
func (m *executionLock) LockContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.init()
	select {
	case m.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	default:
	}
	progress.Emit(ctx, "Waiting for background task to release the session; Esc cancels this prompt.")
	select {
	case m.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *executionLock) Unlock() { <-m.token }

package eventstore

import (
	"errors"

	"github.com/makkalot/caretz/lib/types"
)

var ErrDuplicate = errors.New("duplicate")

type Store interface {
	Append(event *types.Event) error
	Get(originator *types.Originator, fromVersion bool) ([]*types.Event, error)
	Logs(fromID uint64, size uint32, pipelineID string) ([]*types.AppLogEntry, error)
}

// StoreWithCleanup has the same methods as Store but also has Cleanup method
// it's useful when working in tests with the store
type StoreWithCleanup interface {
	Store
	Cleanup() error
}

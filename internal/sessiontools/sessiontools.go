package sessiontools

import (
	"context"

	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/types"
)

// SessionEventGateway is the session-scoped event surface available to
// lifecycle-aware tools.
type SessionEventGateway interface {
	PublishAsyncEvent(context.Context, *types.Event) error
	SubscribeAuditEvents(...string) (eventing.Subscription, error)
	SubscribeContextEvents(...string) (eventing.Subscription, error)
	AuditEvents() ([]*types.Event, error)
	ChannelID() string
}

type Integration interface {
	Name() string
	Tools() []tools.Tool
	Start(context.Context) error
	Close() error
}

type Factory func(SessionEventGateway) (Integration, error)

type asyncRequestKey struct{}

func WithAsyncRequest(ctx context.Context, request eventing.AsyncRunRequest) context.Context {
	return context.WithValue(ctx, asyncRequestKey{}, request)
}

func AsyncRequestFromContext(ctx context.Context) (eventing.AsyncRunRequest, bool) {
	if ctx == nil {
		return eventing.AsyncRunRequest{}, false
	}
	request, ok := ctx.Value(asyncRequestKey{}).(eventing.AsyncRunRequest)
	return request, ok
}

// Package sessiontools defines the public session-integration contract.
//
// Integrations use a Factory to attach lifecycle-aware tools and event
// subscribers to an agent session without depending on Caretz's internal
// packages.
package sessiontools

import (
	"context"

	"github.com/makkalot/caretz/internal/agent"
	internalsessiontools "github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/tools"
)

type SessionEventGateway = internalsessiontools.SessionEventGateway
type Integration = internalsessiontools.Integration
type Factory = internalsessiontools.Factory

var WithAsyncRequest = internalsessiontools.WithAsyncRequest
var AsyncRequestFromContext = internalsessiontools.AsyncRequestFromContext

// ApprovalAvailable reports whether someone can answer a permission request
// asked from ctx, during a model tool call or an interactive slash command.
// Without it, a request would wait for an answer that never comes.
func ApprovalAvailable(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return tools.PermissionRequesterAvailable(ctx) || agent.PermissionPromptsAvailable(ctx)
}

// WithApprovalPrompts marks ctx as run by a client that answers permission
// requests, for tests of an integration or a client of its own.
func WithApprovalPrompts(ctx context.Context) context.Context {
	return agent.WithPermissionPrompts(ctx)
}

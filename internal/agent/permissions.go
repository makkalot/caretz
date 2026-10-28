package agent

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

type pendingPermission struct {
	request eventing.PermissionRequest
	result  chan eventing.PermissionResolution
}

type sessionPermissionGrant struct {
	capability string
	target     string
}

func (a *AgentSession) restorePermissionResolutions() error {
	events, err := a.AuditEvents()
	if err != nil {
		return fmt.Errorf("restore permission events: %w", err)
	}
	requests := map[string]eventing.PermissionRequest{}
	for _, event := range events {
		switch event.EventType {
		case eventing.PermissionRequested:
			request, decodeErr := eventing.DecodePermissionRequest(event)
			if decodeErr != nil {
				return fmt.Errorf("decode permission request: %w", decodeErr)
			}
			requests[request.RequestID] = request
		case eventing.PermissionResolved:
			resolution, decodeErr := eventing.DecodePermissionResolution(event)
			if decodeErr != nil {
				return fmt.Errorf("decode permission resolution: %w", decodeErr)
			}
			a.permissionResolutions[resolution.RequestID] = resolution
			if resolution.Decision == eventing.PermissionDecisionAllowSession {
				if request, ok := requests[resolution.RequestID]; ok {
					a.addSessionPermissionGrant(request)
				}
			}
			delete(requests, resolution.RequestID)
		}
	}
	for requestID := range requests {
		resolution := eventing.PermissionResolution{RequestID: requestID, Decision: eventing.PermissionDecisionExpired, Resolver: "session-recovery"}
		a.permissionResolutions[requestID] = resolution
		if err := a.AppendAuditEvent(eventing.NewPermissionResolved(a.channelId, resolution)); err != nil {
			return fmt.Errorf("expire stale permission request: %w", err)
		}
	}
	return nil
}

// RequestPermission asks the session's user for a permission outside a model
// tool call, such as for a shell shortcut the user typed.
func (a *AgentSession) RequestPermission(ctx context.Context, call llm.ToolCall, denial *tools.PermissionError) (bool, error) {
	return a.requestPermission(ctx, call, denial)
}

type permissionPromptsKey struct{}

// WithPermissionPrompts marks ctx as run by a client that answers
// PermissionRequested events, such as the raw-terminal command loop.
func WithPermissionPrompts(ctx context.Context) context.Context {
	return context.WithValue(ctx, permissionPromptsKey{}, true)
}

// PermissionPromptsAvailable reports whether someone can answer AskPermission.
func PermissionPromptsAvailable(ctx context.Context) bool {
	available, _ := ctx.Value(permissionPromptsKey{}).(bool)
	return available
}

// AskPermission records a permission request outside a tool call and waits for
// the user's decision, including any client metadata. A session grant for the
// same capability and target answers immediately with
// PermissionDecisionAllowSession, unless the request sets NoSessionGrant.
func (a *AgentSession) AskPermission(ctx context.Context, request eventing.PermissionRequest) (eventing.PermissionResolution, error) {
	if !request.NoSessionGrant && a.hasSessionPermissionGrant(request.Capability, request.Target) {
		return eventing.PermissionResolution{Decision: eventing.PermissionDecisionAllowSession, Resolver: "session-grant"}, nil
	}
	request.RequestID = uuid.NewV4().String()
	request.SessionID = a.channelId
	return a.awaitPermission(ctx, request)
}

func (a *AgentSession) requestPermission(ctx context.Context, call llm.ToolCall, denial *tools.PermissionError) (bool, error) {
	if a.hasSessionPermissionGrant(denial.Capability, denial.Target) {
		return true, nil
	}
	request := eventing.PermissionRequest{
		RequestID:     uuid.NewV4().String(),
		SessionID:     a.channelId,
		ToolCallID:    call.ID,
		ToolName:      call.Name,
		ToolArguments: call.Arguments,
		Capability:    denial.Capability,
		Target:        denial.Target,
		Command:       denial.Command,
		Reason:        denial.Reason,
		MayHaveRun:    denial.MayHaveRun,
		Recursive:     denial.Recursive,
	}
	resolution, err := a.awaitPermission(ctx, request)
	return resolution.Decision == eventing.PermissionDecisionAllow || resolution.Decision == eventing.PermissionDecisionAllowSession, err
}

func (a *AgentSession) awaitPermission(ctx context.Context, request eventing.PermissionRequest) (eventing.PermissionResolution, error) {
	var expired <-chan time.Time
	if a.permissionTimeout > 0 {
		request.ExpiresAt = time.Now().Add(a.permissionTimeout)
		timer := time.NewTimer(a.permissionTimeout)
		defer timer.Stop()
		expired = timer.C
	}
	pending := &pendingPermission{request: request, result: make(chan eventing.PermissionResolution, 1)}
	a.permissionMu.Lock()
	a.pendingPermissions[request.RequestID] = pending
	a.permissionMu.Unlock()
	if err := a.AppendAuditEvent(eventing.NewPermissionRequested(a.channelId, request)); err != nil {
		a.permissionMu.Lock()
		delete(a.pendingPermissions, request.RequestID)
		a.permissionMu.Unlock()
		return eventing.PermissionResolution{}, err
	}

	// A nil expired channel never fires, so a zero timeout blocks until the
	// user answers or the turn is cancelled.
	select {
	case resolution := <-pending.result:
		return resolution, nil
	case <-expired:
		resolution := eventing.PermissionResolution{RequestID: request.RequestID, Decision: eventing.PermissionDecisionDeny, Resolver: "permission-timeout"}
		if err := a.finishPermission(request.RequestID, resolution); err != nil {
			return eventing.PermissionResolution{}, err
		}
		return resolution, nil
	case <-ctx.Done():
		resolution := eventing.PermissionResolution{RequestID: request.RequestID, Decision: eventing.PermissionDecisionCancelled}
		_ = a.finishPermission(request.RequestID, resolution)
		return resolution, ctx.Err()
	}
}

func (a *AgentSession) PendingPermissions() []eventing.PermissionRequest {
	a.permissionMu.Lock()
	defer a.permissionMu.Unlock()
	requests := make([]eventing.PermissionRequest, 0, len(a.pendingPermissions))
	for _, pending := range a.pendingPermissions {
		requests = append(requests, pending.request)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].RequestID < requests[j].RequestID })
	return requests
}

func (a *AgentSession) ResolvePermission(resolution eventing.PermissionResolution) error {
	switch resolution.Decision {
	case eventing.PermissionDecisionAllow, eventing.PermissionDecisionAllowSession, eventing.PermissionDecisionDeny:
	default:
		return fmt.Errorf("permission decision must be allow or deny")
	}
	return a.finishPermission(resolution.RequestID, resolution)
}

func (a *AgentSession) finishPermission(requestID string, resolution eventing.PermissionResolution) error {
	if requestID == "" {
		return fmt.Errorf("permission request ID is required")
	}
	a.permissionMu.Lock()
	if previous, ok := a.permissionResolutions[requestID]; ok {
		a.permissionMu.Unlock()
		if previous.Decision == resolution.Decision {
			return nil
		}
		return fmt.Errorf("permission request %s was already resolved as %s", requestID, previous.Decision)
	}
	pending := a.pendingPermissions[requestID]
	if pending == nil {
		a.permissionMu.Unlock()
		return fmt.Errorf("permission request %s is not pending", requestID)
	}
	resolution.RequestID = requestID
	if resolution.Decision == eventing.PermissionDecisionAllowSession {
		a.addSessionPermissionGrantLocked(pending.request)
	}
	delete(a.pendingPermissions, requestID)
	a.permissionResolutions[requestID] = resolution
	a.permissionMu.Unlock()
	if err := a.AppendAuditEvent(eventing.NewPermissionResolved(a.channelId, resolution)); err != nil {
		return err
	}
	pending.result <- resolution
	return nil
}

func (a *AgentSession) hasSessionPermissionGrant(capability, target string) bool {
	a.permissionMu.Lock()
	defer a.permissionMu.Unlock()
	for _, grant := range a.sessionPermissionGrants {
		if grant.capability == capability && grant.target == target {
			return true
		}
	}
	return false
}

func (a *AgentSession) addSessionPermissionGrant(request eventing.PermissionRequest) {
	a.permissionMu.Lock()
	defer a.permissionMu.Unlock()
	a.addSessionPermissionGrantLocked(request)
}

func (a *AgentSession) addSessionPermissionGrantLocked(request eventing.PermissionRequest) {
	if request.NoSessionGrant {
		return
	}
	for _, grant := range a.sessionPermissionGrants {
		if grant.capability == request.Capability && grant.target == request.Target {
			return
		}
	}
	a.sessionPermissionGrants = append(a.sessionPermissionGrants, sessionPermissionGrant{
		capability: request.Capability,
		target:     request.Target,
	})
}

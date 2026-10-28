package agent

import (
	"context"

	"github.com/makkalot/caretz/internal/tools"
)

// RunCommandWithPermissions runs a user-supplied foreground command without a
// model turn. Execution is serialized with agent turns; proxy permissions use
// the same pending-request lifecycle. No messages are added to model context.
func (a *AgentSession) RunCommandWithPermissions(ctx context.Context, command string) (tools.VerificationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.lifecycleCtx.Err(); err != nil {
		return tools.VerificationResult{Command: command, ExitCode: -1}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(a.lifecycleCtx, cancel)
	defer stop()
	ctx = tools.WithPermissionRequester(ctx, a.requestPermission)
	ctx = tools.WithSessionPermissions(ctx, a.hasSessionPermissionGrant)
	if err := a.executionMu.LockContext(ctx); err != nil {
		return tools.VerificationResult{Command: command, ExitCode: -1}, err
	}
	defer a.executionMu.Unlock()
	return a.ToolRegistry().RunCommandWithPermissions(ctx, command)
}

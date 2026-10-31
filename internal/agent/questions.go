package agent

import (
	"context"

	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

// askQuestions records the AskUser questions as a permission request, so
// clients that answer permissions see them, and waits for the answers.
func (a *AgentSession) askQuestions(ctx context.Context, call llm.ToolCall, questions []eventing.Question) (eventing.PermissionResolution, error) {
	request := eventing.PermissionRequest{
		RequestID:      uuid.NewV4().String(),
		SessionID:      a.channelId,
		ToolCallID:     call.ID,
		ToolName:       call.Name,
		ToolArguments:  call.Arguments,
		Capability:     eventing.PermissionCapabilityQuestion,
		Target:         questions[0].Header,
		Reason:         questions[0].Question,
		Details:        eventing.QuestionDetails(questions),
		NoSessionGrant: true,
	}
	return a.awaitPermission(ctx, request)
}

// withAskUser adds AskUser to a turn someone can answer. Prompt mode,
// scheduled runs and registries that disable it, such as subagents', never
// see it.
func withAskUser(ctx context.Context, registry *tools.Registry, defs []llm.ToolDefinition) []llm.ToolDefinition {
	if !tools.QuestionAskerAvailable(ctx) || registry == nil || !registry.IsToolEnabled(tools.AskUserToolName) {
		return defs
	}
	return append(defs[:len(defs):len(defs)], tools.AskUserToolDefinition())
}

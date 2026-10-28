package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/progress"
	"github.com/makkalot/caretz/lib/types"
)

func callSubagentTool(handler func(context.Context, llm.ToolCall) (llm.Message, error)) tools.Tool {
	return tools.Tool{
		Definition: callSubagentToolDefinition(),
		Handler: func(ctx context.Context, tc llm.ToolCall) (string, error) {
			msg, err := handler(ctx, tc)
			if err != nil {
				return "", err
			}
			return msg.Content, nil
		},
	}
}

func callSubagentToolDefinition() llm.ToolDefinition {
	for _, def := range tools.ToolDefinitions() {
		if def.Name == callSubagentToolName {
			return def
		}
	}
	return llm.ToolDefinition{Name: callSubagentToolName}
}

type SubagentRunOptions struct {
	Name             string
	Task             string
	ModelRefOverride string
	ReasoningEffort  string
}

type SubagentRunResult struct {
	Name           string
	Task           string
	Result         string
	ChildSessionID string
}

type callSubagentToolInput struct {
	Agent        string `json:"agent"`
	Task         string `json:"task"`
	Model        string `json:"model"`
	SubagentType string `json:"subagent_type"`
	Prompt       string `json:"prompt"`
	Description  string `json:"description"`
}

func (a *AgentSession) handleCallSubagentToolCall(ctx context.Context, tc llm.ToolCall) (llm.Message, error) {
	if a.toolRegistry != nil && !a.toolRegistry.IsToolEnabled(callSubagentToolName) {
		return llm.Message{}, fmt.Errorf("tool %s is disabled", callSubagentToolName)
	}
	var input callSubagentToolInput
	if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
		return llm.Message{}, fmt.Errorf("invalid CallSubagent arguments: %w", err)
	}
	input.Agent = firstNonEmpty(input.Agent, input.SubagentType)
	input.Task = firstNonEmpty(input.Task, input.Prompt)
	if input.Task == "" {
		input.Task = strings.TrimSpace(input.Description)
	}
	if input.Agent == "" {
		return llm.Message{}, fmt.Errorf("CallSubagent agent must not be empty")
	}
	if input.Task == "" {
		return llm.Message{}, fmt.Errorf("CallSubagent task must not be empty")
	}
	run, err := a.RunSubagent(ctx, SubagentRunOptions{
		Name:             input.Agent,
		Task:             input.Task,
		ModelRefOverride: strings.TrimSpace(input.Model),
	})
	if err != nil {
		return llm.Message{}, err
	}
	return llm.Message{
		Role:       llm.RoleTool,
		Content:    formatCallSubagentToolResult(run),
		ToolCallID: tc.ID,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func formatCallSubagentToolResult(run SubagentRunResult) string {
	return fmt.Sprintf(`Subagent %q completed.

Child session ID: %s

Subagent result:
%s`, run.Name, run.ChildSessionID, run.Result)
}

func (a *AgentSession) RunSubagent(ctx context.Context, opts SubagentRunOptions) (SubagentRunResult, error) {
	if a == nil {
		return SubagentRunResult{}, fmt.Errorf("agent session is required")
	}
	name := strings.TrimSpace(opts.Name)
	task := strings.TrimSpace(opts.Task)
	if name == "" {
		return SubagentRunResult{}, fmt.Errorf("subagent name must not be empty")
	}
	if task == "" {
		return SubagentRunResult{}, fmt.Errorf("subagent task must not be empty")
	}
	if a.allowedAgents != nil {
		allowed := a.allowedAgents[name]
		if !allowed {
			for configured, enabled := range a.allowedAgents {
				if enabled && strings.EqualFold(configured, name) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return SubagentRunResult{}, fmt.Errorf("subagent %q is not enabled by the active profile", name)
		}
	}
	def, err := subagents.LoadFromSnapshot(a.subagentPaths(), name)
	if err != nil {
		return SubagentRunResult{}, err
	}
	child, err := a.NewSubagentChildSession(ctx, def, SubagentChildOptions{
		Prefix:           "subagent-" + def.Name,
		ModelRefOverride: opts.ModelRefOverride,
		ReasoningEffort:  opts.ReasoningEffort,
	})
	if err != nil {
		return SubagentRunResult{}, err
	}
	defer func() { _ = child.Close() }()

	if err := a.AppendAuditEvent(eventing.NewSubagentInvocationStarted(a.ChannelID(), def.Name, child.ChannelID(), a.ChannelID(), task, child.ContextWindow())); err != nil {
		return SubagentRunResult{}, err
	}
	if err := RegisterSubagentProgressHandlers(a, child, def.Name); err != nil {
		return SubagentRunResult{}, err
	}

	resp, err := child.Call(ctx, BuildSubagentPrompt(def, task, a.ChannelID()))
	if err != nil {
		_ = a.AppendAuditEvent(eventing.NewSubagentInvocationFailed(a.ChannelID(), def.Name, child.ChannelID(), a.ChannelID(), err.Error(), child.EstimatedContextTokens(), child.ContextWindow()))
		return SubagentRunResult{}, err
	}
	result := responseText(resp)
	if err := a.AppendAuditEvent(eventing.NewSubagentInvocationCompleted(a.ChannelID(), def.Name, child.ChannelID(), a.ChannelID(), result, child.EstimatedContextTokens(), child.ContextWindow())); err != nil {
		return SubagentRunResult{}, err
	}

	return SubagentRunResult{
		Name:           def.Name,
		Task:           task,
		Result:         result,
		ChildSessionID: child.ChannelID(),
	}, nil
}

type SubagentChildOptions struct {
	Prefix           string
	ModelRefOverride string
	ReasoningEffort  string
	// Limits override the token limits of the child's model.
	Limits       *eventing.ContextLimits
	ToolRegistry *tools.Registry
	// IsolatedExecution gives the child its own session shell; see
	// ChildSessionOptions.IsolatedExecution.
	IsolatedExecution bool
}

func (a *AgentSession) NewSubagentChildSession(ctx context.Context, def subagents.Definition, opts SubagentChildOptions) (*AgentSession, error) {
	toolRegistry := opts.ToolRegistry
	if toolRegistry == nil {
		toolRegistry = SubagentToolRegistry(a, def)
	}
	prefix := opts.Prefix
	if prefix == "" {
		prefix = "subagent-" + def.Name
	}
	child, err := a.NewChildSessionWithOptions(ChildSessionOptions{
		Prefix:            prefix,
		SystemPrompt:      def.SystemPrompt,
		ToolRegistry:      toolRegistry,
		IsolatedExecution: opts.IsolatedExecution,
	})
	if err != nil {
		return nil, err
	}

	modelRef := strings.TrimSpace(opts.ModelRefOverride)
	if modelRef == "" {
		modelRef = strings.TrimSpace(def.Model)
	}
	reasoningEffort := strings.TrimSpace(opts.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = strings.TrimSpace(def.Reasoning)
	}
	if modelRef == "" && reasoningEffort != "" {
		_ = child.Close()
		return nil, fmt.Errorf("subagent reasoning requires a configured model")
	}
	if modelRef != "" {
		resolution, err := a.resolveSubagentModel(ctx, SubagentModelRequest{Name: def.Name, Ref: modelRef, ReasoningEffort: reasoningEffort})
		if err != nil {
			_ = child.Close()
			return nil, err
		}
		if err := child.SwitchModel(resolution.Provider, resolution.ContextWindow, resolution.ReservedTokens, resolution.KeepRecentTokens, resolution.SummaryTokens); err != nil {
			_ = child.Close()
			return nil, err
		}
		resolvedRef := resolution.Ref
		if resolvedRef == "" {
			resolvedRef = modelRef
		}
		progress.Emit(ctx, fmt.Sprintf("subagent %s using model %s → %s", def.Name, resolvedRef, resolution.Model))
	}
	if opts.Limits != nil {
		if err := child.applyContextLimits(*opts.Limits); err != nil {
			_ = child.Close()
			return nil, fmt.Errorf("subagent %s: %w", def.Name, err)
		}
		progress.Emit(ctx, fmt.Sprintf("subagent %s context limit %d tokens", def.Name, child.ContextWindow()))
	}
	return child, nil
}

// applyContextLimits lays limits over the current model's limits. A limit may
// shrink the context window but not grow it past what the model accepts.
func (a *AgentLoop) applyContextLimits(limits eventing.ContextLimits) error {
	if a.contextManager == nil {
		return nil
	}
	window, reserved, keepRecent, summary := a.ContextWindow(), a.ReservedTokens(), a.KeepRecentTokens(), a.SummaryTokens()
	if limits.Context > 0 {
		if window > 0 && limits.Context > window {
			return fmt.Errorf("context limit %d exceeds the model's context window %d", limits.Context, window)
		}
		window = limits.Context
	}
	if limits.ReservedTokens > 0 {
		reserved = limits.ReservedTokens
	}
	if limits.KeepRecentTokens > 0 {
		keepRecent = limits.KeepRecentTokens
	}
	if limits.SummaryTokens > 0 {
		summary = limits.SummaryTokens
	}
	if reserved >= window {
		return fmt.Errorf("reserved tokens %d must be below the context limit %d; set limit.reservedTokens", reserved, window)
	}
	a.contextManager.SetLimits(window, reserved, keepRecent, summary)
	return nil
}

// ModelProvider returns a provider for the provider/model ref, resolved like a
// subagent model, so a session integration can call another model. An empty
// ref returns the session's current provider. Calls made outside the agent
// loop should set llm.WithSessionID(ctx, LLMSessionID()).
func (a *AgentSession) ModelProvider(ctx context.Context, ref, reasoningEffort string) (llm.Provider, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return a.currentProvider(), nil
	}
	resolution, err := a.resolveSubagentModel(ctx, SubagentModelRequest{Name: "integration", Ref: ref, ReasoningEffort: reasoningEffort})
	if err != nil {
		return nil, err
	}
	return resolution.Provider, nil
}

// ValidateSubagentModel resolves ref without starting a child, so a command
// can reject an unknown model before it spends tokens.
func (a *AgentSession) ValidateSubagentModel(ctx context.Context, ref, reasoningEffort string) error {
	if strings.TrimSpace(ref) == "" {
		if strings.TrimSpace(reasoningEffort) != "" {
			return fmt.Errorf("reasoning %q requires a model", reasoningEffort)
		}
		return nil
	}
	_, err := a.resolveSubagentModel(ctx, SubagentModelRequest{Ref: strings.TrimSpace(ref), ReasoningEffort: reasoningEffort, ValidateOnly: true})
	return err
}

func (a *AgentSession) resolveSubagentModel(ctx context.Context, request SubagentModelRequest) (SubagentModelResolution, error) {
	ref := request.Ref
	resolver := a.subagentModelResolver
	if resolver == nil {
		return SubagentModelResolution{}, fmt.Errorf("subagent model %q requested but model resolver is not configured", ref)
	}
	request.WorkspaceDir = a.WorkspaceDir()
	request.ReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
	resolution, err := resolver.ResolveSubagentModel(ctx, request)
	if err != nil {
		return SubagentModelResolution{}, err
	}
	if resolution.Provider == nil {
		return SubagentModelResolution{}, fmt.Errorf("subagent model %q resolved without provider", ref)
	}
	if resolution.ContextWindow <= 0 || resolution.ReservedTokens <= 0 || resolution.KeepRecentTokens <= 0 || resolution.SummaryTokens <= 0 {
		return SubagentModelResolution{}, fmt.Errorf("subagent model %q resolved with invalid token limits", ref)
	}
	return resolution, nil
}

func SubagentToolRegistry(session *AgentSession, def subagents.Definition) *tools.Registry {
	disabled := subagents.DisabledTools(def.Permission)
	disabled[callSubagentToolName] = true
	return session.ToolRegistry().WithDisabledTools(disabled)
}

func RegisterSubagentProgressHandlers(parent, child *AgentSession, name string) error {
	emit := func(message string, usage llm.Usage, cumulativeTotal int64) error {
		return parent.PublishAuditEvent(eventing.NewSubagentInvocationProgress(parent.ChannelID(), name, child.ChannelID(), parent.ChannelID(), message, usage, cumulativeTotal, child.EstimatedContextTokens(), child.ContextWindow()))
	}
	if err := child.OnContextEvent(eventing.LLMAgentMessageCreated, func(e *types.Event) error {
		var payload eventing.LLMAgentMessage
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return err
		}
		if strings.TrimSpace(payload.ReasoningSummary) != "" {
			if err := emit(fmt.Sprintf("subagent %s reasoning: %s", name, previewLine(payload.ReasoningSummary, 180)), llm.Usage{}, 0); err != nil {
				return err
			}
		}
		for _, tc := range payload.Message.ToolCalls {
			if err := emit(fmt.Sprintf("subagent %s tool call %s(%s)", name, tc.Name, tc.Arguments), payload.Usage, payload.CumulativeTotal); err != nil {
				return err
			}
		}
		if payload.Message.Content != "" {
			return emit(fmt.Sprintf("subagent %s assistant: %s", name, previewLine(payload.Message.Content, 180)), payload.Usage, payload.CumulativeTotal)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := child.OnContextEvent(eventing.LLMReasoningDeltaCreated, func(e *types.Event) error {
		var payload eventing.LLMReasoningDelta
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return err
		}
		text := payload.Text
		if text == "" {
			text = payload.Delta
		}
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return emit(fmt.Sprintf("subagent %s reasoning: %s", name, previewLine(text, 180)), llm.Usage{}, 0)
	}); err != nil {
		return err
	}
	if err := child.OnContextEvent(eventing.LLMToolCallResultCreated, func(e *types.Event) error {
		var payload eventing.LLMToolCallResult
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return err
		}
		content := payload.Message.Content
		if payload.Error != "" {
			content = "error: " + payload.Error
		}
		return emit(fmt.Sprintf("subagent %s tool result %s: %s", name, payload.Tool.Name, previewLine(content, 140)), llm.Usage{}, 0)
	}); err != nil {
		return err
	}
	return child.OnContextEvent(eventing.CompactionCompletedCreated, func(e *types.Event) error {
		var payload eventing.CompactionCompleted
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return err
		}
		return emit(fmt.Sprintf("subagent %s context compacted: %d → %d tokens", name, payload.BeforeTokens, payload.AfterTokens), llm.Usage{}, 0)
	})
}

func BuildSubagentPrompt(def subagents.Definition, task, parentSessionID string) string {
	return fmt.Sprintf(`You are running as subagent %q for parent session %s.

Task:
%s

Work independently in your own context. Return a concise final result for the parent agent.`, def.Name, parentSessionID, task)
}

func responseText(resp *llm.Response) string {
	if resp == nil {
		return ""
	}
	if resp.Content != "" {
		return resp.Content
	}
	return resp.Message.Content
}

func previewLine(s string, maxLen int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return "(empty response)"
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

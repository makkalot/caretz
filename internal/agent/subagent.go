package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
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
	// ResumeChildID continues an earlier run of this session in its existing
	// child session, keeping its conversation and configuration. Task becomes
	// optional, and Name may be left empty.
	ResumeChildID string
}

type SubagentRunResult struct {
	Name           string
	Task           string
	Result         string
	ChildSessionID string
}

type callSubagentToolInput struct {
	Agent        string      `json:"agent"`
	Task         string      `json:"task"`
	Model        string      `json:"model"`
	SubagentType string      `json:"subagent_type"`
	Prompt       string      `json:"prompt"`
	Description  string      `json:"description"`
	Background   lenientBool `json:"background"`
	Resume       string      `json:"resume"`
}

// lenientBool accepts a JSON boolean or a string spelling of one. Models,
// local ones especially, often send "true" or "True" for a boolean, and
// refusing the whole call over that wastes a turn.
type lenientBool bool

func (b *lenientBool) UnmarshalJSON(data []byte) error {
	var value bool
	if err := json.Unmarshal(data, &value); err == nil {
		*b = lenientBool(value)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("expected true or false, got %s", data)
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "true", "1":
		*b = true
	case "false", "0", "":
		*b = false
	default:
		return fmt.Errorf("expected true or false, got %q", text)
	}
	return nil
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
	input.Resume = strings.TrimSpace(input.Resume)
	if input.Agent == "" && input.Resume == "" {
		return llm.Message{}, fmt.Errorf("CallSubagent agent must not be empty")
	}
	if input.Task == "" && input.Resume == "" {
		return llm.Message{}, fmt.Errorf("CallSubagent task must not be empty")
	}
	options := SubagentRunOptions{
		Name:             input.Agent,
		Task:             input.Task,
		ModelRefOverride: strings.TrimSpace(input.Model),
		ResumeChildID:    input.Resume,
	}
	if input.Background {
		run, err := a.startBackgroundSubagent(ctx, options)
		if err != nil {
			return llm.Message{}, err
		}
		return llm.Message{
			Role:       llm.RoleTool,
			Content:    formatBackgroundSubagentStart(run),
			ToolCallID: tc.ID,
		}, nil
	}
	run, err := a.RunSubagent(ctx, options)
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

func formatBackgroundSubagentStart(run SubagentRunResult) string {
	return fmt.Sprintf(`Subagent %q started in the background.

Child session ID: %s

Its result will arrive in a later message on its own, so continue with other work or end your turn.`, run.Name, run.ChildSessionID)
}

func (a *AgentSession) RunSubagent(ctx context.Context, opts SubagentRunOptions) (SubagentRunResult, error) {
	if a == nil {
		return SubagentRunResult{}, fmt.Errorf("agent session is required")
	}
	opts, resumed, err := a.resolveSubagentResume(opts)
	if err != nil {
		return SubagentRunResult{}, err
	}
	name := strings.TrimSpace(opts.Name)
	task := strings.TrimSpace(opts.Task)
	if name == "" {
		return SubagentRunResult{}, fmt.Errorf("subagent name must not be empty")
	}
	if task == "" {
		return SubagentRunResult{}, fmt.Errorf("subagent task must not be empty")
	}
	def, err := a.loadEnabledSubagent(name)
	if err != nil {
		return SubagentRunResult{}, err
	}
	child, err := a.NewSubagentChildSession(ctx, def, SubagentChildOptions{
		Prefix:           "subagent-" + def.Name,
		ModelRefOverride: opts.ModelRefOverride,
		ReasoningEffort:  opts.ReasoningEffort,
		ResumeChildID:    opts.ResumeChildID,
	})
	if err != nil {
		return SubagentRunResult{}, err
	}
	defer func() { _ = child.Close() }()

	model := firstNonEmpty(opts.ModelRefOverride, def.Model)
	reasoning := firstNonEmpty(opts.ReasoningEffort, def.Reasoning)
	if err := a.AppendAuditEvent(eventing.NewSubagentInvocationStartedWithConfig(a.ChannelID(), def.Name, child.ChannelID(), a.ChannelID(), task, model, reasoning, child.ContextWindow())); err != nil {
		return SubagentRunResult{}, err
	}
	if err := RegisterSubagentProgressHandlers(a, child, def.Name); err != nil {
		return SubagentRunResult{}, err
	}

	resp, err := child.Call(ctx, buildSubagentRunPrompt(def, task, a.ChannelID(), resumed))
	if err != nil {
		_ = a.AppendAuditEvent(eventing.NewSubagentInvocationStopped(a.ChannelID(), def.Name, child.ChannelID(), a.ChannelID(), err.Error(), a.subagentStopReason(ctx, err), child.EstimatedContextTokens(), child.ContextWindow()))
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

// loadEnabledSubagent loads the named agent if the active profile allows it.
// When the name is wrong, the error names the agents that can run, so the
// model can correct the call in one turn.
func (a *AgentSession) loadEnabledSubagent(name string) (subagents.Definition, error) {
	if !a.subagentAllowed(name) {
		return subagents.Definition{}, fmt.Errorf("subagent %q is not enabled by the active profile; %s", name, a.enabledSubagentsHint())
	}
	def, err := subagents.LoadFromSnapshot(a.subagentPaths(), name)
	if errors.Is(err, os.ErrNotExist) {
		return subagents.Definition{}, fmt.Errorf("%w; %s", err, a.enabledSubagentsHint())
	}
	return def, err
}

// subagentAllowed reports whether the active profile lets the named agent run.
// Profile names match case-insensitively.
func (a *AgentSession) subagentAllowed(name string) bool {
	if a.allowedAgents == nil || a.allowedAgents[name] {
		return true
	}
	for configured, enabled := range a.allowedAgents {
		if enabled && strings.EqualFold(configured, name) {
			return true
		}
	}
	return false
}

// enabledSubagentNames returns the session's agents the profile allows, sorted.
func (a *AgentSession) enabledSubagentNames() []string {
	var names []string
	for name := range a.subagentPaths() {
		if a.subagentAllowed(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// enabledSubagentsHint names the agents that can run, or points to
// ListSubagents when there are too many to name.
func (a *AgentSession) enabledSubagentsHint() string {
	names := a.enabledSubagentNames()
	switch {
	case len(names) == 0:
		return "no subagents are enabled in this session"
	case len(names) > subagentNameHintLimit:
		return fmt.Sprintf("%d subagents are enabled; call %s with available=true to list them", len(names), listSubagentsToolName)
	default:
		return "available: " + strings.Join(names, ", ")
	}
}

// subagentStopReason classifies why a run ended with err. ctx is the run's own
// context. A closed session wins, because closing it is what cancels the run.
func (a *AgentSession) subagentStopReason(ctx context.Context, err error) string {
	if a.lifecycleCtx != nil && a.lifecycleCtx.Err() != nil {
		return eventing.SubagentStopSessionClosed
	}
	var cancelled subagentCancel
	if errors.As(context.Cause(ctx), &cancelled) {
		return cancelled.reason
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return eventing.SubagentStopTimeout
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return eventing.SubagentStopUserCancelled
	default:
		return eventing.SubagentStopError
	}
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
	// ResumeChildID reopens that child; see ChildSessionOptions.ResumeChannelID.
	ResumeChildID string
	// NonInteractivePermissions; see ChildSessionOptions.NonInteractivePermissions.
	NonInteractivePermissions bool
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
		Prefix:                    prefix,
		SystemPrompt:              def.SystemPrompt,
		ToolRegistry:              toolRegistry,
		IsolatedExecution:         opts.IsolatedExecution,
		ResumeChannelID:           opts.ResumeChildID,
		NonInteractivePermissions: opts.NonInteractivePermissions,
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
	// Subagents report back to the parent model, which asks the user.
	disabled[tools.AskUserToolName] = true
	if !def.Plan {
		return session.ToolRegistry().WithDisabledTools(disabled)
	}
	// A plan agent never sees the mutating tools, and its Bash runs in a
	// read-only sandbox, whether it runs in the foreground or background.
	disabled["Edit"] = true
	disabled["Write"] = true
	disabled["SendFile"] = true
	return session.ToolRegistry().WithDisabledTools(disabled).WithReadOnlyWorkspace()
}

// subagentActivity is one line of a subagent's progress, as shown live for a
// foreground run and by /bg log for any run.
type subagentActivity struct {
	message         string
	usage           llm.Usage
	cumulativeTotal int64
}

// subagentActivityEvents are the child context events that produce activity.
var subagentActivityEvents = []string{
	eventing.LLMAgentMessageCreated,
	eventing.LLMReasoningDeltaCreated,
	eventing.LLMToolCallResultCreated,
	eventing.CompactionCompletedCreated,
}

// subagentActivityFor describes child context event e as activity lines,
// each starting "subagent <name> ". Other event types produce none.
func subagentActivityFor(name string, eventType string, raw []byte) ([]subagentActivity, error) {
	switch eventType {
	case eventing.LLMAgentMessageCreated:
		var payload eventing.LLMAgentMessage
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		var lines []subagentActivity
		if strings.TrimSpace(payload.ReasoningSummary) != "" {
			lines = append(lines, subagentActivity{message: fmt.Sprintf("subagent %s reasoning: %s", name, previewLine(payload.ReasoningSummary, 180))})
		}
		for _, tc := range payload.Message.ToolCalls {
			lines = append(lines, subagentActivity{message: fmt.Sprintf("subagent %s tool call %s(%s)", name, tc.Name, tc.Arguments), usage: payload.Usage, cumulativeTotal: payload.CumulativeTotal})
		}
		if payload.Message.Content != "" {
			lines = append(lines, subagentActivity{message: fmt.Sprintf("subagent %s assistant: %s", name, previewLine(payload.Message.Content, 180)), usage: payload.Usage, cumulativeTotal: payload.CumulativeTotal})
		}
		return lines, nil
	case eventing.LLMReasoningDeltaCreated:
		var payload eventing.LLMReasoningDelta
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		text := payload.Text
		if text == "" {
			text = payload.Delta
		}
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []subagentActivity{{message: fmt.Sprintf("subagent %s reasoning: %s", name, previewLine(text, 180))}}, nil
	case eventing.LLMToolCallResultCreated:
		var payload eventing.LLMToolCallResult
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		content := payload.Message.Content
		if payload.Error != "" {
			content = "error: " + payload.Error
		}
		return []subagentActivity{{message: fmt.Sprintf("subagent %s tool result %s: %s", name, payload.Tool.Name, previewLine(content, 140))}}, nil
	case eventing.CompactionCompletedCreated:
		var payload eventing.CompactionCompleted
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		return []subagentActivity{{message: fmt.Sprintf("subagent %s context compacted: %d → %d tokens", name, payload.BeforeTokens, payload.AfterTokens)}}, nil
	}
	return nil, nil
}

func RegisterSubagentProgressHandlers(parent, child *AgentSession, name string) error {
	for _, eventType := range subagentActivityEvents {
		if err := child.OnContextEvent(eventType, func(e *types.Event) error {
			lines, err := subagentActivityFor(name, e.EventType, []byte(e.Payload))
			if err != nil {
				return err
			}
			for _, line := range lines {
				if err := parent.PublishAuditEvent(eventing.NewSubagentInvocationProgress(parent.ChannelID(), name, child.ChannelID(), parent.ChannelID(), line.message, line.usage, line.cumulativeTotal, child.EstimatedContextTokens(), child.ContextWindow())); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func BuildSubagentPrompt(def subagents.Definition, task, parentSessionID string) string {
	return fmt.Sprintf(`You are running as subagent %q for parent session %s.

Task:
%s

Work independently in your own context. Return a concise final result for the parent agent.`, def.Name, parentSessionID, task)
}

// resolveSubagentResume fills opts from the earlier run opts.ResumeChildID
// names, and returns that run. Without a resume it returns opts unchanged and
// no run. Only a run in this session's own log can resume, so a guessed ID
// cannot open another session.
func (a *AgentSession) resolveSubagentResume(opts SubagentRunOptions) (SubagentRunOptions, *SubagentRun, error) {
	id := strings.TrimSpace(opts.ResumeChildID)
	if id == "" {
		return opts, nil, nil
	}
	runs, err := a.ListedSubagentRuns()
	if err != nil {
		return opts, nil, err
	}
	index := slices.IndexFunc(runs, func(run SubagentRun) bool { return run.ChildSessionID == id })
	if index < 0 {
		return opts, nil, fmt.Errorf("no subagent run of this session has child session ID %q", id)
	}
	run := runs[index]
	if run.Status == SubagentRunning {
		return opts, nil, fmt.Errorf("subagent %q is still running", run.Name)
	}
	if name := strings.TrimSpace(opts.Name); name != "" && !strings.EqualFold(name, run.Name) {
		return opts, nil, fmt.Errorf("child session %s belongs to subagent %q, not %q", id, run.Name, name)
	}
	opts.ResumeChildID = id
	opts.Name = run.Name
	opts.Task = firstNonEmpty(opts.Task, "Continue the original task.")
	opts.ModelRefOverride = firstNonEmpty(opts.ModelRefOverride, run.Model)
	opts.ReasoningEffort = firstNonEmpty(opts.ReasoningEffort, run.ReasoningEffort)
	return opts, &run, nil
}

func buildSubagentRunPrompt(def subagents.Definition, task, parentSessionID string, resumed *SubagentRun) string {
	if resumed == nil {
		return BuildSubagentPrompt(def, task, parentSessionID)
	}
	return fmt.Sprintf(`You are resuming as subagent %q for parent session %s. Your earlier run ended as: %s.

Original task:
%s

Now:
%s

Work may have stopped partway through, and the workspace may have changed since. Re-check the current state before relying on earlier results. Return a concise final result for the parent agent.`, def.Name, parentSessionID, resumed.Status, resumed.Task, task)
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

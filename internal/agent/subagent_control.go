package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

const (
	listSubagentsToolName  = "ListSubagents"
	cancelSubagentToolName = "CancelSubagent"
	// subagentListLimit keeps a long session's list from filling the context.
	subagentListLimit  = 20
	subagentPreviewLen = 300
)

// installSubagentControlTools gives a top-level session its subagent tools once
// it has an agent to run. Every tool definition is sent with every request, so
// a session with no agents should not pay for these. It runs during session
// setup, and again when the session's agents change.
func (a *AgentSession) installSubagentControlTools() {
	registry := a.ToolRegistry()
	if a.childSession || a.subagentToolsInstalled || len(a.subagentPaths()) == 0 || !registry.IsToolEnabled(callSubagentToolName) {
		return
	}
	a.subagentToolsInstalled = true
	for _, tool := range a.subagentControlTools() {
		// An embedder's custom tool of the same name stays in place.
		next, err := registry.WithSessionTool(tool)
		if err != nil {
			a.logger.Warn("Skipping subagent tool: %v", err)
			continue
		}
		registry = next
	}
	if err := a.SwitchRuntime(registry, a.systemPrompt); err != nil {
		a.logger.Warn("Failed to add subagent tools: %v", err)
	}
}

// subagentControlTools let a top-level session see and stop its subagents.
func (a *AgentSession) subagentControlTools() []tools.Tool {
	return []tools.Tool{
		{
			Definition: llm.ToolDefinition{
				Name:        listSubagentsToolName,
				Description: "List this session's subagent runs, newest first, including interrupted runs from before a resume. Use it when asked what happened to a subagent.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"child_session_id": map[string]any{
							"type":        "string",
							"description": "Show this run's full result.",
						},
					},
				},
			},
			Handler: a.handleListSubagents,
		},
		{
			Definition: llm.ToolDefinition{
				Name:        cancelSubagentToolName,
				Description: "Cancel a background subagent. Its work so far is kept.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"subagent": map[string]any{
							"type":        "string",
							"description": "Name or child session ID.",
						},
					},
					"required": []string{"subagent"},
				},
			},
			Handler: a.handleCancelSubagent,
		},
	}
}

func (a *AgentSession) handleListSubagents(_ context.Context, tc llm.ToolCall) (string, error) {
	var input struct {
		ChildSessionID string `json:"child_session_id"`
	}
	if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
		return "", fmt.Errorf("invalid %s arguments: %w", listSubagentsToolName, err)
	}
	runs, err := a.ListedSubagentRuns()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(input.ChildSessionID)
	if id == "" {
		return FormatSubagentRuns(runs), nil
	}
	for _, run := range runs {
		if run.ChildSessionID == id {
			return formatSubagentRunDetail(run), nil
		}
	}
	return "", fmt.Errorf("no subagent run has child session ID %q", id)
}

func (a *AgentSession) handleCancelSubagent(_ context.Context, tc llm.ToolCall) (string, error) {
	var input struct {
		Subagent string `json:"subagent"`
	}
	if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
		return "", fmt.Errorf("invalid %s arguments: %w", cancelSubagentToolName, err)
	}
	run, err := a.cancelBackgroundSubagent(input.Subagent, eventing.SubagentStopModelCancelled)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Cancelling subagent %q (%s).", run.Name, run.ChildSessionID), nil
}

// CancelBackgroundSubagentForUser stops a background run at the user's request.
// ref is a subagent name or child session ID.
func (a *AgentSession) CancelBackgroundSubagentForUser(ref string) (SubagentRun, error) {
	return a.cancelBackgroundSubagent(ref, eventing.SubagentStopUserCancelled)
}

func (a *AgentSession) cancelBackgroundSubagent(ref, reason string) (SubagentRun, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return SubagentRun{}, errors.New("subagent name or child session ID must not be empty")
	}
	runs, err := a.ListedSubagentRuns()
	if err != nil {
		return SubagentRun{}, err
	}
	var matches, running []SubagentRun
	for _, run := range runs {
		if run.ChildSessionID == ref || strings.EqualFold(run.Name, ref) {
			matches = append(matches, run)
			if run.Status == SubagentRunning {
				running = append(running, run)
			}
		}
	}
	switch {
	case len(matches) == 0:
		return SubagentRun{}, fmt.Errorf("no subagent run matches %q", ref)
	case len(running) == 0:
		return SubagentRun{}, fmt.Errorf("subagent %q is not running; its last run is %s", ref, matches[len(matches)-1].Status)
	case len(running) > 1:
		ids := make([]string, len(running))
		for i, run := range running {
			ids[i] = run.ChildSessionID
		}
		return SubagentRun{}, fmt.Errorf("several %q runs are running; pass one child session ID: %s", ref, strings.Join(ids, ", "))
	}
	a.asyncOwnedMu.Lock()
	background := a.backgroundRuns[running[0].ChildSessionID]
	a.asyncOwnedMu.Unlock()
	if background == nil {
		return SubagentRun{}, fmt.Errorf("subagent %q runs in the foreground; press Esc to stop it", ref)
	}
	background.requestCancel(subagentCancel{reason: reason})
	return running[0], nil
}

// subagentCancel is the cancellation cause of a run stopped by name, which
// records who stopped it.
type subagentCancel struct{ reason string }

func (c subagentCancel) Error() string { return "subagent cancelled: " + c.reason }
func (c subagentCancel) Unwrap() error { return context.Canceled }

// ListedSubagentRuns returns the runs started from agent definitions, leaving
// out goal and scheduled children, which have their own commands.
func (a *AgentSession) ListedSubagentRuns() ([]SubagentRun, error) {
	runs, err := a.SubagentRuns()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(runs, func(run SubagentRun) bool {
		return !a.ownsResumableChild(run.Name, run.ChildSessionID)
	}), nil
}

// FormatSubagentRuns renders runs newest first, with long text shortened.
func FormatSubagentRuns(runs []SubagentRun) string {
	if len(runs) == 0 {
		return "No subagent runs in this session."
	}
	var b strings.Builder
	omitted := 0
	for i := len(runs) - 1; i >= 0; i-- {
		if len(runs)-i > subagentListLimit {
			omitted = i + 1
			break
		}
		run := runs[i]
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		writeSubagentRunHeader(&b, run)
		if run.Result != "" {
			fmt.Fprintf(&b, "  result: %s\n", previewLine(run.Result, subagentPreviewLen))
		}
		if run.Error != "" {
			fmt.Fprintf(&b, "  error: %s\n", previewLine(run.Error, subagentPreviewLen))
		}
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n%d older runs not shown.\n", omitted)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatSubagentRunDetail(run SubagentRun) string {
	var b strings.Builder
	writeSubagentRunHeader(&b, run)
	if run.Result != "" {
		fmt.Fprintf(&b, "\nResult:\n%s\n", run.Result)
	}
	if run.Error != "" {
		fmt.Fprintf(&b, "\nError:\n%s\n", run.Error)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeSubagentRunHeader(b *strings.Builder, run SubagentRun) {
	fmt.Fprintf(b, "%s: %s", run.Name, run.Status)
	if run.StopReason != "" {
		fmt.Fprintf(b, " (%s)", run.StopReason)
	}
	fmt.Fprintf(b, "\n  child session ID: %s\n  task: %s\n", run.ChildSessionID, previewLine(run.Task, subagentPreviewLen))
	if run.Model != "" {
		fmt.Fprintf(b, "  model: %s\n", run.Model)
	}
	switch {
	case !run.Resumable:
	case run.StopReason == eventing.SubagentStopUserCancelled:
		b.WriteString("  resumable: only if the user asks, because they cancelled this run\n")
	default:
		b.WriteString("  resumable: yes\n")
	}
}

// InterruptedSubagentNotice names the runs a closed session or a dead process
// cut short, or returns "" if there are none. A user-cancelled run is left out
// because the user already knows about it.
func (a *AgentSession) InterruptedSubagentNotice() (string, error) {
	runs, err := a.ListedSubagentRuns()
	if err != nil {
		return "", err
	}
	var names []string
	for _, run := range runs {
		if run.Status == SubagentInterrupted && !slices.Contains(names, run.Name) {
			names = append(names, run.Name)
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	return fmt.Sprintf("Interrupted subagents: %s. Ask to resume them, or run /bg to see them.", strings.Join(names, ", ")), nil
}

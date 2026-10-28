package slashcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
)

// SubagentRunner runs one of the subagents the session recorded at startup.
type SubagentRunner struct{}

func (c SubagentRunner) Run(ctx context.Context, session *agent.AgentSession, name, task string) (Result, error) {
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}
	name = strings.TrimSpace(name)
	task = strings.TrimSpace(task)
	if name == "" {
		return Result{}, fmt.Errorf("subagent name must not be empty")
	}
	if task == "" {
		return Result{}, fmt.Errorf("subagent task must not be empty")
	}
	run, err := session.RunSubagent(ctx, agent.SubagentRunOptions{
		Name: name,
		Task: task,
	})
	if err != nil {
		return Result{}, err
	}

	parentPrompt := buildSubagentResultPrompt(run.Name, run.ChildSessionID, run.Task, run.Result)
	if _, err := session.Call(ctx, parentPrompt); err != nil {
		return Result{}, fmt.Errorf("parent follow-up after subagent failed: %w", err)
	}

	return Result{}, nil
}

func buildSubagentResultPrompt(name, childSessionID, task, result string) string {
	return fmt.Sprintf(`Subagent %q completed.

Child session ID: %s

Original task:
%s

Subagent result:
%s

Use this result in your own work. Do not repeat the entire transcript unless it is needed.`, name, childSessionID, task, result)
}

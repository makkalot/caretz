package slashcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/tasks"
)

type TasksCommand struct{}

func (TasksCommand) Name() string        { return "tasks" }
func (TasksCommand) Description() string { return "Show the current session's task list" }
func (TasksCommand) Usage() string       { return "/tasks [--detail]" }
func (TasksCommand) Aliases() []string   { return nil }

func (TasksCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	detail := false
	switch strings.TrimSpace(args) {
	case "":
	case "--detail":
		detail = true
	default:
		return Result{}, fmt.Errorf("usage: %s", (TasksCommand{}).Usage())
	}
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}
	if !session.TasksEnabled() {
		return Result{}, fmt.Errorf("/tasks requires internal tasks; restart caretz without --no-tasks")
	}
	state := tasks.FromSnapshot(session.TaskSnapshot())
	if detail {
		return Result{Message: tasks.RenderFull(state)}, nil
	}
	return Result{Message: tasks.RenderSummary(state)}, nil
}

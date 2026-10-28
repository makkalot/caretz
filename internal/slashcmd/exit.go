package slashcmd

import (
	"context"

	"github.com/makkalot/caretz/internal/agent"
)

type ExitCommand struct{}

func (ExitCommand) Name() string        { return "exit" }
func (ExitCommand) Description() string { return "Leave interactive mode" }
func (ExitCommand) Usage() string       { return "/exit" }
func (ExitCommand) Aliases() []string   { return []string{"quit"} }
func (ExitCommand) Run(_ context.Context, _ *agent.AgentSession, args string) (Result, error) {
	if err := rejectArguments("exit", args); err != nil {
		return Result{}, err
	}
	return Result{Exit: true}, nil
}

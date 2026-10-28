package slashcmd

import (
	"context"
	"fmt"

	"github.com/makkalot/caretz/internal/agent"
)

type ContextCommand struct {
	Profile string
}

func (ContextCommand) Name() string        { return "context" }
func (ContextCommand) Description() string { return "Show current context usage" }
func (ContextCommand) Usage() string       { return "/context" }
func (ContextCommand) Aliases() []string   { return nil }

func (c ContextCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	if err := rejectArguments("context", args); err != nil {
		return Result{}, err
	}
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}

	return Result{Message: fmt.Sprintf(
		"model: %s, profile: %s, context: %d/%d tokens, reserved: %d, messages: %d",
		session.Model(),
		c.Profile,
		session.EstimatedContextTokens(),
		session.ContextWindow(),
		session.ReservedTokens(),
		session.ActiveMessageCount(),
	)}, nil
}

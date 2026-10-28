package slashcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/uuid"
)

type ClearCommand struct{}

func (ClearCommand) Name() string        { return "clear" }
func (ClearCommand) Description() string { return "Start a fresh session" }
func (ClearCommand) Usage() string       { return "/clear" }
func (ClearCommand) Aliases() []string   { return nil }

func (ClearCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}
	if strings.TrimSpace(args) != "" {
		return Result{}, fmt.Errorf("/clear does not accept arguments")
	}

	newID := uuid.NewV4().String()[:8]
	newSession, err := session.NewSiblingSession(newID)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Message:    fmt.Sprintf("cleared session: %s → %s", session.ChannelID(), newID),
		NewSession: newSession,
	}, nil
}

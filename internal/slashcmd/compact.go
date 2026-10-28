package slashcmd

import (
	"context"
	"fmt"

	"github.com/makkalot/caretz/internal/agent"
)

type CompactCommand struct{}

func (CompactCommand) Name() string        { return "compact" }
func (CompactCommand) Description() string { return "Manually compact conversation context" }
func (CompactCommand) Usage() string       { return "/compact" }
func (CompactCommand) Aliases() []string   { return nil }

func (CompactCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	if err := rejectArguments("compact", args); err != nil {
		return Result{}, err
	}
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}

	result, err := session.CompactNow(ctx)
	if err != nil {
		return Result{}, err
	}
	if result == nil || !result.Compacted {
		beforeTokens := 0
		if result != nil {
			beforeTokens = result.BeforeTokens
		}
		return Result{Message: fmt.Sprintf(
			"context compaction skipped: current estimated context is %d tokens, but there are no older complete turns outside the recent-history window to summarize yet.",
			beforeTokens,
		)}, nil
	}

	return Result{Message: fmt.Sprintf("context compacted: %d → %d tokens", result.BeforeTokens, result.AfterTokens)}, nil
}

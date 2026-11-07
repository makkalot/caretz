package slashcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
)

// BackgroundCommand shows this session's subagent runs and cancels background
// ones, so the user need not go through the model to stop one.
type BackgroundCommand struct{}

func (BackgroundCommand) Name() string        { return "bg" }
func (BackgroundCommand) Description() string { return "Show subagent runs or cancel a background one" }
func (BackgroundCommand) Usage() string       { return "/bg [cancel <name or child session ID>]" }
func (BackgroundCommand) Aliases() []string   { return nil }

func (BackgroundCommand) Run(_ context.Context, session *agent.AgentSession, args string) (Result, error) {
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}
	fields := strings.Fields(args)
	switch {
	case len(fields) == 0:
		runs, err := session.ListedSubagentRuns()
		if err != nil {
			return Result{}, err
		}
		return Result{Message: agent.FormatSubagentRuns(runs)}, nil
	case len(fields) == 2 && fields[0] == "cancel":
		run, err := session.CancelBackgroundSubagentForUser(fields[1])
		if err != nil {
			return Result{}, err
		}
		return Result{Message: fmt.Sprintf("Cancelling subagent %s (%s).", run.Name, run.ChildSessionID)}, nil
	default:
		return Result{}, fmt.Errorf("usage: %s", (BackgroundCommand{}).Usage())
	}
}

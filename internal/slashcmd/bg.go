package slashcmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
)

// BackgroundCommand shows this session's subagent runs and cancels background
// ones, so the user need not go through the model to stop one.
type BackgroundCommand struct{}

func (BackgroundCommand) Name() string      { return "bg" }
func (BackgroundCommand) Aliases() []string { return nil }

func (BackgroundCommand) Description() string {
	return "Show subagent runs, a run's recent activity, or cancel a background one"
}

func (BackgroundCommand) Usage() string {
	return "/bg [log <name or child session ID> [entries] | cancel <name or child session ID>]"
}

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
	case (len(fields) == 2 || len(fields) == 3) && fields[0] == "log":
		n := agent.DefaultSubagentLogEntries
		if len(fields) == 3 {
			parsed, err := strconv.Atoi(fields[2])
			if err != nil || parsed <= 0 {
				return Result{}, fmt.Errorf("entries must be a positive number, got %q", fields[2])
			}
			n = parsed
		}
		log, err := session.SubagentRunLog(fields[1], n)
		if err != nil {
			return Result{}, err
		}
		return Result{Message: agent.FormatSubagentRunLog(log), SubagentLog: &log}, nil
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

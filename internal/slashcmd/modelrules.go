package slashcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
)

// ModelRulesCommand shows which model rules apply to the workspace.
type ModelRulesCommand struct {
	Report func() string
}

func (ModelRulesCommand) Name() string        { return "modelrules" }
func (ModelRulesCommand) Description() string { return "Show model rules and the one active here" }
func (ModelRulesCommand) Usage() string       { return "/modelrules" }
func (ModelRulesCommand) Aliases() []string   { return nil }
func (c ModelRulesCommand) Run(_ context.Context, _ *agent.AgentSession, args string) (Result, error) {
	if err := rejectArguments("modelrules", args); err != nil {
		return Result{}, err
	}
	return Result{Message: c.Report()}, nil
}

// AllowModelCommand allows a model in the workspace by saving a model rule
// to personal config. Only the user can run it: slash commands are never
// taken from model output.
type AllowModelCommand struct {
	Allow   func(ctx context.Context, session *agent.AgentSession, ref string) (string, error)
	Choices func() []config.ModelChoice
}

func (AllowModelCommand) Name() string { return "allow-model" }
func (AllowModelCommand) Description() string {
	return "Allow a model in this directory by saving a model rule"
}
func (AllowModelCommand) Usage() string     { return "/allow-model provider/model" }
func (AllowModelCommand) Aliases() []string { return nil }

func (c AllowModelCommand) Complete(args string, cursor int) (Completion, bool) {
	tokens, active, ok := completionTokens(args, cursor)
	if !ok || active > 0 || c.Choices == nil {
		return Completion{}, false
	}
	return tokenCompletion(tokens[active], tokens[active].value, modelRefs(c.Choices()))
}

func (c AllowModelCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	ref := strings.TrimSpace(args)
	if ref == "" || strings.ContainsAny(ref, " \t") {
		return Result{}, fmt.Errorf("usage: %s", c.Usage())
	}
	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}
	message, err := c.Allow(ctx, session, ref)
	if err != nil {
		return Result{}, err
	}
	return Result{Message: message}, nil
}

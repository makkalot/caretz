package slashcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
)

type HelpCommand struct {
	Registry *Registry
}

func (HelpCommand) Name() string        { return "help" }
func (HelpCommand) Description() string { return "Show available slash commands" }
func (HelpCommand) Usage() string       { return "/help" }
func (HelpCommand) Aliases() []string   { return []string{"?"} }

func (c HelpCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	if err := rejectArguments("help", args); err != nil {
		return Result{}, err
	}
	if c.Registry == nil {
		return Result{}, fmt.Errorf("help command has no registry")
	}

	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, cmd := range c.Registry.Commands() {
		usage := cmd.Usage()
		if aliases := cmd.Aliases(); len(aliases) > 0 {
			usage += " (aliases: /" + strings.Join(aliases, ", /") + ")"
		}
		_, _ = fmt.Fprintf(&b, "  %-12s %s\n", usage, cmd.Description())
	}
	return Result{Message: strings.TrimRight(b.String(), "\n")}, nil
}

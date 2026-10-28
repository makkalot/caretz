package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	libruntime "github.com/makkalot/caretz/lib/runtime"
)

func newSlashCommandRegistry(cwd string, agentsDirs, allowedAgents []string, profile string, modelsCommand slashcmd.ModelsCommand) (*slashcmd.Registry, error) {
	registry := slashcmd.NewRegistry()
	if err := registry.Register(slashcmd.ContextCommand{Profile: profile}); err != nil {
		return nil, err
	}
	if err := registry.Register(slashcmd.CompactCommand{}); err != nil {
		return nil, err
	}
	if err := registry.Register(slashcmd.ClearCommand{}); err != nil {
		return nil, err
	}
	if err := registry.Register(slashcmd.ExitCommand{}); err != nil {
		return nil, err
	}
	if err := registry.Register(slashcmd.AddPathCommand{}); err != nil {
		return nil, err
	}
	if err := registry.Register(slashcmd.AddNetCommand{}); err != nil {
		return nil, err
	}
	if err := registry.Register(slashcmd.LoopCommand{}); err != nil {
		return nil, err
	}
	if err := registry.Register(slashcmd.ScheduleListCommand{}); err != nil {
		return nil, err
	}
	for _, command := range []slashcmd.Command{
		slashcmd.GoalCommand{CurrentRef: modelsCommand.CurrentRef, Choices: modelsCommand.ChoiceList},
		slashcmd.GoalApproveCommand{CurrentRef: modelsCommand.CurrentRef, Choices: modelsCommand.ChoiceList},
		slashcmd.GoalAnswerCommand{},
		slashcmd.GoalEvidenceCommand{},
		slashcmd.GoalResumeCommand{},
		slashcmd.GoalRetryCommand{CurrentRef: modelsCommand.CurrentRef, Choices: modelsCommand.ChoiceList},
		slashcmd.GoalStatusCommand{},
		slashcmd.GoalStopCommand{},
	} {
		if err := registry.Register(command); err != nil {
			return nil, err
		}
	}
	if err := registry.Register(slashcmd.TasksCommand{}); err != nil {
		return nil, err
	}
	if err := registry.Register(slashcmd.ThemeCommand{}); err != nil {
		return nil, err
	}
	if err := registry.Register(modelsCommand); err != nil {
		return nil, err
	}
	if modelsCommand.RulesReport != nil {
		if err := registry.Register(slashcmd.ModelRulesCommand{Report: modelsCommand.RulesReport}); err != nil {
			return nil, err
		}
	}
	if modelsCommand.AllowModel != nil {
		allow := slashcmd.AllowModelCommand{Allow: modelsCommand.AllowModel, Choices: modelsCommand.ChoiceList}
		if err := registry.Register(allow); err != nil {
			return nil, err
		}
	}
	if err := registry.Register(slashcmd.HelpCommand{Registry: registry}); err != nil {
		return nil, err
	}
	registry.SubagentAvailable = func(name string) bool {
		if allowed := resourceNameMap(allowedAgents); allowed != nil && !allowed[name] {
			return false
		}
		_, err := subagents.LoadFromDirs(agentsDirs, name)
		return err == nil
	}
	registry.RunSubagent = (slashcmd.SubagentRunner{}).Run
	return registry, nil
}

// registerCustomSlashCommands adds an embedder's commands to registry. /help
// reads the registry when it runs, so it lists them too.
func registerCustomSlashCommands(registry *slashcmd.Registry, commands []libruntime.CustomSlashCommand) error {
	for _, command := range commands {
		if command == nil {
			return errors.New("custom slash command is nil")
		}
		if err := registry.Register(customSlashCommand{command}); err != nil {
			return err
		}
	}
	return nil
}

// customSlashCommand runs a public command against the agent session.
type customSlashCommand struct {
	libruntime.CustomSlashCommand
}

func (c customSlashCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (slashcmd.Result, error) {
	if session == nil {
		return slashcmd.Result{}, errors.New("agent session is required")
	}
	result, err := c.CustomSlashCommand.Run(ctx, commandSession{session}, args)
	if err != nil {
		return slashcmd.Result{}, err
	}
	return internalSlashCommandResult(result)
}

// commandSession adds the sandboxed command runner the public
// CommandSession promises to the agent session.
type commandSession struct {
	*agent.AgentSession
}

func (s commandSession) RunVerificationCommand(ctx context.Context, command string) (tools.VerificationResult, error) {
	return s.ToolRegistry().RunVerificationCommand(ctx, command)
}

func internalSlashCommandResult(result libruntime.SlashCommandResult) (slashcmd.Result, error) {
	var newSession *agent.AgentSession
	switch session := result.NewSession.(type) {
	case nil:
	case *agent.AgentSession:
		newSession = session
	case commandSession:
		newSession = session.AgentSession
	default:
		return slashcmd.Result{}, fmt.Errorf("unsupported session implementation %T", session)
	}
	return slashcmd.Result{
		Message:               result.Message,
		MessagePublished:      result.MessagePublished,
		ExitCode:              result.ExitCode,
		ActiveModel:           result.ActiveModel,
		ActiveModelRef:        result.ActiveModelRef,
		ActiveReasoningEffort: result.ActiveReasoningEffort,
		ActiveReasoningSet:    result.ActiveReasoningSet,
		NewSession:            newSession,
		Exit:                  result.Exit,
	}, nil
}

type publicSlashCommandRegistry struct {
	inner *slashcmd.Registry
}

func (r publicSlashCommandRegistry) Handles(input string) bool {
	return r.inner != nil && r.inner.Handles(input)
}

func (r publicSlashCommandRegistry) Execute(ctx context.Context, session libruntime.Session, input string) (libruntime.SlashCommandResult, bool, error) {
	if r.inner == nil {
		return libruntime.SlashCommandResult{}, false, nil
	}
	agentSession, ok := session.(*agent.AgentSession)
	if !ok {
		return libruntime.SlashCommandResult{}, true, fmt.Errorf("unsupported session implementation %T", session)
	}
	result, handled, err := r.inner.Execute(ctx, agentSession, input)
	return publicSlashCommandResult(result), handled, err
}

func publicSlashCommandResult(result slashcmd.Result) libruntime.SlashCommandResult {
	var newSession libruntime.Session
	if result.NewSession != nil {
		newSession = result.NewSession
	}
	return libruntime.SlashCommandResult{
		Message:               result.Message,
		MessagePublished:      result.MessagePublished,
		ExitCode:              result.ExitCode,
		ActiveModel:           result.ActiveModel,
		ActiveModelRef:        result.ActiveModelRef,
		ActiveReasoningEffort: result.ActiveReasoningEffort,
		ActiveReasoningSet:    result.ActiveReasoningSet,
		NewSession:            newSession,
		Exit:                  result.Exit,
	}
}

func (r publicSlashCommandRegistry) Commands() []libruntime.SlashCommand {
	if r.inner == nil {
		return nil
	}
	commands := r.inner.Commands()
	out := make([]libruntime.SlashCommand, 0, len(commands))
	for _, command := range commands {
		out = append(out, command)
	}
	return out
}

func publicModelChoices(choices []config.ModelChoice) []libruntime.ModelChoice {
	out := make([]libruntime.ModelChoice, 0, len(choices))
	for _, choice := range choices {
		out = append(out, libruntime.ModelChoice{
			Ref:             choice.Ref,
			Provider:        choice.ProviderKey,
			ProviderName:    choice.ProviderName,
			ModelKey:        choice.ModelKey,
			ModelName:       choice.ModelName,
			Model:           choice.Model,
			Reasoning:       choice.Reasoning,
			ReasoningValues: append([]string(nil), choice.ReasoningValues...),
		})
	}
	return out
}

package slashcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
)

type ModelSwitchResult struct {
	Ref             string
	Model           string
	ReasoningEffort string
}

type ModelSwitcher func(ctx context.Context, session *agent.AgentSession, ref, reasoningEffort string) (ModelSwitchResult, error)

type ModelsCommand struct {
	Choices []config.ModelChoice
	// ChoicesFunc, when set, replaces Choices so the list reflects switches
	// made during the session.
	ChoicesFunc func() []config.ModelChoice
	CurrentRef  func(*agent.AgentSession) string
	Switcher    ModelSwitcher
	// RulesReport, when set, registers /modelrules to show model rules.
	RulesReport func() string
	// AllowModel, when set, registers /allow-model to allow a model in the
	// workspace; it returns the message to show.
	AllowModel func(ctx context.Context, session *agent.AgentSession, ref string) (string, error)
}

// ChoiceList returns the current model choices, for commands that complete
// model refs the same way /models does.
func (c ModelsCommand) ChoiceList() []config.ModelChoice { return c.choices() }

func (c ModelsCommand) choices() []config.ModelChoice {
	if c.ChoicesFunc != nil {
		return c.ChoicesFunc()
	}
	return c.Choices
}

func (ModelsCommand) Name() string        { return "models" }
func (ModelsCommand) Description() string { return "List or switch available models" }
func (ModelsCommand) Usage() string       { return "/models [--reasoning VALUE] [provider/model]" }
func (ModelsCommand) Aliases() []string   { return nil }

func (c ModelsCommand) Complete(args string, cursor int) (Completion, bool) {
	tokens, active, ok := completionTokens(args, cursor)
	if !ok {
		return Completion{}, false
	}
	token := tokens[active]
	if positionalStarted(tokens, active, modelsOptionDefs) {
		return Completion{}, false
	}
	choices := c.choices()
	values := reasoningValues(choices, "")
	if strings.HasPrefix(token.value, "--reasoning=") {
		return optionValueCompletion(token, "--reasoning", values)
	}
	if active > 0 && tokens[active-1].value == "--reasoning" {
		return tokenCompletion(token, token.value, values)
	}

	if token.value == "" {
		return tokenCompletion(token, token.value, modelRefs(choices))
	}
	if strings.HasPrefix(token.value, "--") {
		return tokenCompletion(token, token.value, optionCandidates(modelsOptionDefs, completionUsedOptions(tokens, modelsOptionDefs)))
	}
	return tokenCompletion(token, token.value, modelRefs(choices))
}

func (c ModelsCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	parsed, err := parseModelsArgs(args)
	if err != nil {
		return Result{}, err
	}
	ref := parsed.Ref
	if ref == "" {
		currentRef := ""
		if c.CurrentRef != nil {
			currentRef = c.CurrentRef(session)
		}
		choices := c.choices()
		if len(choices) == 0 && currentRef == "" {
			return Result{Message: "no available models found"}, nil
		}
		var b strings.Builder
		b.WriteString("available models:\n")
		currentListed := false
		for _, choice := range choices {
			if currentRef != "" && choice.Ref == currentRef {
				writeModelChoice(&b, choice, true)
				currentListed = true
				break
			}
		}
		if currentRef != "" && !currentListed {
			writeModelChoice(&b, config.ModelChoice{Ref: currentRef}, true)
		}
		for _, choice := range choices {
			if choice.Ref != currentRef {
				writeModelChoice(&b, choice, false)
			}
		}
		return Result{Message: strings.TrimRight(b.String(), "\n")}, nil
	}

	if session == nil {
		return Result{}, fmt.Errorf("agent session is required")
	}
	if c.Switcher == nil {
		return Result{}, fmt.Errorf("model switcher is not configured")
	}
	result, err := c.Switcher(ctx, session, ref, parsed.ReasoningEffort)
	if err != nil {
		return Result{}, err
	}
	message := fmt.Sprintf("model switched: %s → %s", result.Ref, result.Model)
	if result.ReasoningEffort != "" {
		message += fmt.Sprintf(" (reasoning=%s)", result.ReasoningEffort)
	}
	return Result{
		Message:               message,
		ActiveModel:           result.Model,
		ActiveModelRef:        result.Ref,
		ActiveReasoningEffort: result.ReasoningEffort,
		ActiveReasoningSet:    true,
	}, nil
}

type modelsArgs struct {
	Ref             string
	ReasoningEffort string
}

func parseModelsArgs(input string) (modelsArgs, error) {
	args, err := parseCommandArgs(input, modelsOptionDefs)
	if err != nil {
		return modelsArgs{}, err
	}
	if len(args.Positionals) > 1 {
		return modelsArgs{}, fmt.Errorf("unexpected argument %q", args.Positionals[1])
	}
	var out modelsArgs
	if len(args.Positionals) == 1 {
		out.Ref = args.Positionals[0]
	}
	out.ReasoningEffort = args.Options["reasoning"]
	if out.ReasoningEffort != "" && out.Ref == "" {
		return modelsArgs{}, fmt.Errorf("--reasoning requires a model")
	}
	return out, nil
}

var modelsOptionDefs = []optionDef{{Name: "reasoning", TakesValue: true}}

func writeModelChoice(b *strings.Builder, choice config.ModelChoice, current bool) {
	if current {
		b.WriteString("* ")
	} else {
		b.WriteString("- ")
	}
	b.WriteString(choice.Ref)
	if label := modelChoiceLabel(choice); label != "" {
		b.WriteString("  ")
		b.WriteString(label)
	}
	b.WriteString("\n")
}

func modelChoiceLabel(choice config.ModelChoice) string {
	var parts []string
	if choice.ProviderName != "" {
		parts = append(parts, choice.ProviderName)
	}
	if choice.ModelName != "" {
		parts = append(parts, choice.ModelName)
	}
	if choice.Model != "" && choice.Model != choice.ModelKey && choice.Model != choice.ModelName {
		parts = append(parts, choice.Model)
	}
	return strings.Join(parts, " / ")
}

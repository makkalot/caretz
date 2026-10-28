package slashcmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/goal"
)

// goalDefaultModels are the profile's per-role models; flags override them.
var goalDefaultModels goal.Models

var goalModelOptionDefs = []optionDef{
	{Name: "model", TakesValue: true}, {Name: "reasoning", TakesValue: true},
	{Name: "planner-model", TakesValue: true}, {Name: "planner-reasoning", TakesValue: true},
	{Name: "worker-model", TakesValue: true}, {Name: "worker-reasoning", TakesValue: true},
	{Name: "verifier-model", TakesValue: true}, {Name: "verifier-reasoning", TakesValue: true},
}

var (
	goalStartOptionDefs   = append([]optionDef{{Name: "yes"}}, goalModelOptionDefs...)
	goalApproveOptionDefs = append(append([]optionDef(nil), goalModelOptionDefs...), optionDef{Name: "edit", TakesValue: true})
)

const goalModelUsage = "[--model REF] [--planner-model REF] [--worker-model REF] [--verifier-model REF] [--<role>-reasoning VALUE]"

func goalRoleModel(role *config.GoalRole) goal.RoleModel {
	if role == nil {
		return goal.RoleModel{}
	}
	return goal.RoleModel{Model: strings.TrimSpace(role.Model), Reasoning: strings.TrimSpace(role.Reasoning)}
}

// scanGoalOptions reads options at the start of input and returns the rest
// unparsed, so goal text keeps its quotes and apostrophes. --edit takes the
// rest of the line.
func scanGoalOptions(input string, defs []optionDef) (map[string]string, string, error) {
	byName := make(map[string]optionDef, len(defs))
	for _, def := range defs {
		byName[def.Name] = def
	}
	options := map[string]string{}
	rest := strings.TrimSpace(input)
	cut := func(value string) (string, string) {
		index := strings.IndexFunc(value, unicode.IsSpace)
		if index < 0 {
			return value, ""
		}
		return value[:index], strings.TrimSpace(value[index:])
	}
	for strings.HasPrefix(rest, "--") {
		var token string
		token, rest = cut(rest)
		name, inline, hasInline := strings.Cut(strings.TrimPrefix(token, "--"), "=")
		def, ok := byName[name]
		if !ok {
			return nil, "", fmt.Errorf("unknown option %q", "--"+name)
		}
		if _, dup := options[name]; dup {
			return nil, "", fmt.Errorf("duplicate option --%s", name)
		}
		switch {
		case !def.TakesValue:
			if hasInline {
				return nil, "", fmt.Errorf("--%s does not accept a value", name)
			}
			options[name] = "true"
			continue
		case name == "edit":
			value := rest
			if hasInline {
				value = strings.TrimSpace(inline + " " + rest)
			}
			options[name], rest = strings.Trim(strings.TrimSpace(value), `"`), ""
		case hasInline:
			options[name] = inline
		default:
			options[name], rest = cut(rest)
		}
		if strings.TrimSpace(options[name]) == "" || strings.HasPrefix(options[name], "--") {
			return nil, "", fmt.Errorf("--%s requires a value", name)
		}
	}
	return options, rest, nil
}

// goalModelsFromOptions builds the role models the flags set, or nil when
// none is set. --model and --reasoning set every role; a role flag wins. A
// reasoning value without a model applies to the current model.
func goalModelsFromOptions(options map[string]string, session *agent.AgentSession, currentRef func(*agent.AgentSession) string) (*goal.Models, error) {
	set := false
	role := func(name string) (goal.RoleModel, error) {
		model := goal.RoleModel{Model: options["model"], Reasoning: options["reasoning"]}
		if value := options[name+"-model"]; value != "" {
			model.Model = value
		}
		if value := options[name+"-reasoning"]; value != "" {
			model.Reasoning = value
		}
		if model.Reasoning != "" && model.Model == "" && currentRef != nil {
			model.Model = strings.TrimSpace(currentRef(session))
		}
		if model.Reasoning != "" && model.Model == "" {
			return model, fmt.Errorf("--%s-reasoning needs a model; add --%s-model", name, name)
		}
		if model.Model != "" {
			set = true
		}
		return model, nil
	}
	var models goal.Models
	var err error
	if models.Planner, err = role("planner"); err != nil {
		return nil, err
	}
	if models.Worker, err = role("worker"); err != nil {
		return nil, err
	}
	if models.Verifier, err = role("verifier"); err != nil {
		return nil, err
	}
	if !set {
		return nil, nil
	}
	return &models, nil
}

// validateGoalModels resolves every chosen model before any role runs, so an
// unknown model fails before planning spends tokens.
func validateGoalModels(ctx context.Context, session *agent.AgentSession, models goal.Models) error {
	roles := models.Roles()
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		role := roles[name]
		if err := session.ValidateSubagentModel(ctx, role.Model, role.Reasoning); err != nil {
			return fmt.Errorf("%s model %s: %w", name, role.Label(), err)
		}
	}
	return nil
}

// goalOptionCompletion completes goal options and model and reasoning values.
func goalOptionCompletion(args string, cursor int, defs []optionDef, choices []config.ModelChoice) (Completion, bool) {
	tokens, active, ok := completionTokens(args, cursor)
	if !ok {
		return Completion{}, false
	}
	token := tokens[active]
	if positionalStarted(tokens, active, defs) {
		return Completion{}, false
	}
	optionName := func(value string) string {
		name, _, _ := strings.Cut(strings.TrimPrefix(value, "--"), "=")
		return name
	}
	values := func(name string) []string {
		switch {
		case name == "model" || strings.HasSuffix(name, "-model"):
			return modelRefs(choices)
		case name == "reasoning" || strings.HasSuffix(name, "-reasoning"):
			return reasoningValues(choices, "")
		}
		return nil
	}
	if strings.HasPrefix(token.value, "--") && strings.Contains(token.value, "=") {
		name := optionName(token.value)
		if candidates := values(name); candidates != nil {
			return optionValueCompletion(token, "--"+name, candidates)
		}
		return Completion{}, false
	}
	if active > 0 && strings.HasPrefix(tokens[active-1].value, "--") && !strings.Contains(tokens[active-1].value, "=") {
		name := optionName(tokens[active-1].value)
		known, takesValue := false, false
		for _, def := range defs {
			if def.Name == name {
				known, takesValue = true, def.TakesValue
			}
		}
		// The value of an unknown or free-form option is left alone.
		if !known || takesValue {
			if candidates := values(name); candidates != nil {
				return tokenCompletion(token, token.value, candidates)
			}
			return Completion{}, false
		}
	}
	if strings.HasPrefix(token.value, "--") || token.value == "" {
		return tokenCompletion(token, token.value, optionCandidates(defs, completionUsedOptions(tokens, defs)))
	}
	return Completion{}, false
}

package slashcmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/goal"
)

type Result struct {
	Message string
	// MessagePublished means the message was already sent as a conversation event.
	MessagePublished      bool
	ExitCode              int
	ActiveModel           string
	ActiveModelRef        string
	ActiveReasoningEffort string
	ActiveReasoningSet    bool
	NewSession            *agent.AgentSession
	Exit                  bool
}

type Command interface {
	Name() string
	Description() string
	Usage() string
	Aliases() []string
	Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error)
}

type Registry struct {
	commands          map[string]Command
	SubagentAvailable func(name string) bool
	RunSubagent       func(ctx context.Context, session *agent.AgentSession, name, task string) (Result, error)
}

func NewRegistry() *Registry {
	return &Registry{commands: make(map[string]Command)}
}

func (r *Registry) Register(cmd Command) error {
	name := normalizeName(cmd.Name())
	if name == "" {
		return fmt.Errorf("command name must not be empty")
	}
	if _, exists := r.commands[name]; exists {
		return fmt.Errorf("command %q already registered", name)
	}
	r.commands[name] = cmd

	for _, alias := range cmd.Aliases() {
		alias = normalizeName(alias)
		if alias == "" {
			return fmt.Errorf("alias for command %q must not be empty", name)
		}
		if _, exists := r.commands[alias]; exists {
			return fmt.Errorf("command alias %q already registered", alias)
		}
		r.commands[alias] = cmd
	}

	return nil
}

func (r *Registry) Commands() []Command {
	seen := make(map[string]bool)
	var result []Command
	for _, cmd := range r.commands {
		name := normalizeName(cmd.Name())
		if seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, cmd)
	}
	sort.Slice(result, func(i, j int) bool {
		return normalizeName(result[i].Name()) < normalizeName(result[j].Name())
	})
	return result
}

func (r *Registry) Execute(ctx context.Context, session *agent.AgentSession, input string) (Result, bool, error) {
	name, args, ok := Parse(input)
	if !ok && session != nil && strings.TrimSpace(input) != "" {
		if state, found, err := loadGoalState(session); err == nil && found && state.Question != "" && (state.Stage == goal.AwaitingInput || state.Stage == goal.AwaitingApproval) {
			result, runErr := (GoalAnswerCommand{}).Run(ctx, session, strings.TrimSpace(input))
			return result, true, runErr
		}
	}
	if !ok && r.SubagentAvailable != nil {
		if subagentName, subagentArgs, shorthandOK := ParseSubagentShorthand(input, r.SubagentAvailable); shorthandOK {
			if r.RunSubagent == nil {
				return Result{}, true, fmt.Errorf("subagent runner is not configured")
			}
			result, err := r.RunSubagent(ctx, session, subagentName, subagentArgs)
			return result, true, err
		}
	}
	if !ok {
		return Result{}, false, nil
	}
	if name == "" {
		name = "help"
	}
	cmd, exists := r.commands[name]
	if !exists {
		return Result{}, true, fmt.Errorf("unknown command /%s", name)
	}
	result, err := cmd.Run(ctx, session, args)
	return result, true, err
}

func (r *Registry) Handles(input string) bool {
	if _, _, ok := Parse(input); ok {
		return true
	}
	if r.SubagentAvailable == nil {
		return false
	}
	_, _, ok := ParseSubagentShorthand(input, r.SubagentAvailable)
	return ok
}

// HandlesSessionInput includes plain-text answers to a pending goal question.
func (r *Registry) HandlesSessionInput(session *agent.AgentSession, input string) bool {
	if r.Handles(input) {
		return true
	}
	if session != nil && strings.TrimSpace(input) != "" {
		state, found, err := loadGoalState(session)
		return err == nil && found && state.Question != "" && (state.Stage == goal.AwaitingInput || state.Stage == goal.AwaitingApproval)
	}
	return false
}

func ParseSubagentShorthand(input string, available func(name string) bool) (name string, args string, ok bool) {
	if available == nil {
		return "", "", false
	}
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "@") {
		return "", "", false
	}
	first, rest, _ := strings.Cut(input, " ")
	name = strings.TrimPrefix(first, "@")
	if name == "" || strings.ContainsAny(name, `/\`) || !available(name) {
		return "", "", false
	}
	return name, strings.TrimSpace(rest), true
}

func Parse(input string) (name string, args string, ok bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return "", "", false
	}

	rest := strings.TrimSpace(strings.TrimPrefix(input, "/"))
	if rest == "" {
		return "", "", true
	}

	name, args, _ = strings.Cut(rest, " ")
	return normalizeName(name), strings.TrimSpace(args), true
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

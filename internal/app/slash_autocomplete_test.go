package app

import (
	"context"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/slashcmd"
)

func newSlashTestRegistry(t *testing.T) *slashcmd.Registry {
	t.Helper()
	choices := []config.ModelChoice{
		{Ref: "local/llama", Reasoning: "medium"},
		{Ref: "local/qwen", Reasoning: "medium", ReasoningValues: []string{"low", "medium", "high"}},
		{Ref: "openai/gpt", Reasoning: "high", ReasoningValues: []string{"low", "high"}},
	}
	registry := slashcmd.NewRegistry()
	for _, command := range []slashcmd.Command{
		slashcmd.ContextCommand{},
		slashcmd.ExitCommand{},
		slashcmd.GoalCommand{},
		slashcmd.GoalApproveCommand{},
		slashcmd.GoalAnswerCommand{},
		slashcmd.GoalEvidenceCommand{},
		slashcmd.GoalResumeCommand{},
		slashcmd.GoalRetryCommand{},
		slashcmd.GoalStatusCommand{},
		slashcmd.GoalStopCommand{},
		slashcmd.LoopCommand{},
		slashcmd.ModelsCommand{Choices: choices},
		slashcmd.ScheduleListCommand{},
	} {
		if err := registry.Register(command); err != nil {
			t.Fatalf("Register(%s): %v", command.Name(), err)
		}
	}
	return registry
}

func applySlashTestCompletion(t *testing.T, registry *slashcmd.Registry, input string) (string, bool) {
	t.Helper()
	start, end, replacement, ok := (slashAutocomplete{registry: registry}).complete(input, len([]rune(input)))
	if !ok {
		return input, false
	}
	runes := []rune(input)
	completed := append(append([]rune{}, runes[:start]...), append([]rune(replacement), runes[end:]...)...)
	return string(completed), true
}

func TestSlashAutocompleteCommandsModelsAndReasoning(t *testing.T) {
	registry := newSlashTestRegistry(t)
	tests := []struct {
		input string
		want  string
	}{
		{input: "/mod", want: "/models "},
		{input: "/ex", want: "/exit "},
		{input: "/go", want: "/goal "},
		{input: "/goal:sta", want: "/goal:status "},
		{input: "/goal:ret", want: "/goal:retry "},
		{input: "/lo", want: "/loop "},
		{input: "/schedule-l", want: "/schedule-list "},
		{input: "/models qwen", want: "/models local/qwen"},
		{input: "/models --rea", want: "/models --reasoning "},
		{input: "/models --reasoning h", want: "/models --reasoning high"},
		{input: "/models --reasoning=h", want: "/models --reasoning=high"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := applySlashTestCompletion(t, registry, tt.input)
			if !ok {
				t.Fatal("completion was not available")
			}
			if got != tt.want {
				t.Fatalf("completion = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlashAutocompleteLeavesOpenAndFreeFormValuesAlone(t *testing.T) {
	registry := newSlashTestRegistry(t)
	for _, input := range []string{
		`/goal --max-iter `,
		`/goal --done FIN`,
		`/goal make`,
		"/models local/qwen\nmore",
	} {
		if got, ok := applySlashTestCompletion(t, registry, input); ok || got != input {
			t.Fatalf("completion(%q) = %q, %v; want unchanged", input, got, ok)
		}
	}
}

// The TUI and ACP give /models a ChoicesFunc; goal model flags and
// /allow-model must complete from the same list.
func TestSlashAutocompleteGoalModelsFromChoicesFunc(t *testing.T) {
	models := []config.ModelChoice{{Ref: "local/qwen"}}
	registry, err := newSlashCommandRegistry(t.TempDir(), nil, nil, "general", slashcmd.ModelsCommand{
		ChoicesFunc: func() []config.ModelChoice { return models },
		AllowModel:  func(context.Context, *agent.AgentSession, string) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, want string }{
		{"/goal --worker-model loc", "/goal --worker-model local/qwen"},
		{"/goal:approve --model loc", "/goal:approve --model local/qwen"},
		{"/goal:retry --planner-model loc", "/goal:retry --planner-model local/qwen"},
		{"/allow-model loc", "/allow-model local/qwen"},
	} {
		if got, ok := applySlashTestCompletion(t, registry, tc.input); !ok || got != tc.want {
			t.Fatalf("completion(%q) = %q, %v; want %q", tc.input, got, ok, tc.want)
		}
	}
	// Models configured later in the session are offered too.
	models = append(models, config.ModelChoice{Ref: "openai/gpt"})
	if got, _ := applySlashTestCompletion(t, registry, "/goal --model ope"); got != "/goal --model openai/gpt" {
		t.Fatalf("late model completion = %q", got)
	}
}

func TestSlashAutocompleteDoesNotOfferRemovedSolveCommand(t *testing.T) {
	registry := newSlashTestRegistry(t)
	if got, ok := applySlashTestCompletion(t, registry, "/sol"); ok || got != "/sol" {
		t.Fatalf("completion = %q, %v; want removed command unchanged", got, ok)
	}
}

package slashcmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
)

func TestModelsCommandListsConfiguredModels(t *testing.T) {
	cmd := ModelsCommand{
		Choices: []config.ModelChoice{
			{Ref: "local/qwen", ProviderName: "Local", ModelName: "Qwen", Model: "qwen-code", ModelKey: "qwen"},
			{Ref: "openai/gpt", Model: "gpt-5", ModelKey: "gpt"},
		},
	}

	result, err := cmd.Run(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{"available models:", "local/qwen", "Local / Qwen / qwen-code", "openai/gpt", "gpt-5"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("result missing %q:\n%s", want, result.Message)
		}
	}
}

func TestModelsCommandChoicesFuncReflectsChanges(t *testing.T) {
	label := "running locally"
	cmd := ModelsCommand{
		Choices: []config.ModelChoice{{Ref: "stale/model"}},
		ChoicesFunc: func() []config.ModelChoice {
			return []config.ModelChoice{{Ref: "local/qwen", ModelName: label}}
		},
	}
	for _, want := range []string{"running locally", "restarts the local server"} {
		label = want
		result, err := cmd.Run(context.Background(), nil, "")
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if !strings.Contains(result.Message, want) || strings.Contains(result.Message, "stale/model") {
			t.Fatalf("Message = %q, want live choices with %q", result.Message, want)
		}
	}
}

func TestModelsCommandListsCurrentModelFirstAndMarksIt(t *testing.T) {
	currentRef := "openai/gpt"
	cmd := ModelsCommand{
		Choices: []config.ModelChoice{
			{Ref: "local/qwen"},
			{Ref: "openai/gpt", Model: "gpt-5", ModelKey: "gpt"},
			{Ref: "other/model"},
		},
		CurrentRef: func(*agent.AgentSession) string { return currentRef },
	}

	check := func(want string) {
		t.Helper()
		result, err := cmd.Run(context.Background(), &agent.AgentSession{}, "")
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if result.Message != want {
			t.Fatalf("Message = %q, want %q", result.Message, want)
		}
	}
	check("available models:\n* openai/gpt  gpt-5\n- local/qwen\n- other/model")
	currentRef = "local/qwen"
	check("available models:\n* local/qwen\n- openai/gpt  gpt-5\n- other/model")
	currentRef = "custom/model"
	check("available models:\n* custom/model\n- local/qwen\n- openai/gpt  gpt-5\n- other/model")
}

func TestModelsCommandListsCurrentModelWithoutConfiguredChoices(t *testing.T) {
	cmd := ModelsCommand{CurrentRef: func(*agent.AgentSession) string { return "custom/model" }}
	result, err := cmd.Run(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Message != "available models:\n* custom/model" {
		t.Fatalf("Message = %q", result.Message)
	}
}

func TestModelsCommandSwitchesModel(t *testing.T) {
	var gotRef string
	var gotReasoningEffort string
	cmd := ModelsCommand{
		Switcher: func(ctx context.Context, session *agent.AgentSession, ref, reasoningEffort string) (ModelSwitchResult, error) {
			gotRef = ref
			gotReasoningEffort = reasoningEffort
			return ModelSwitchResult{Ref: ref, Model: "qwen-code"}, nil
		},
	}

	result, err := cmd.Run(context.Background(), &agent.AgentSession{}, "local/qwen")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotRef != "local/qwen" {
		t.Fatalf("switch ref = %q, want local/qwen", gotRef)
	}
	if gotReasoningEffort != "" {
		t.Fatalf("reasoning effort = %q, want empty", gotReasoningEffort)
	}
	if result.ActiveModel != "qwen-code" {
		t.Fatalf("ActiveModel = %q, want qwen-code", result.ActiveModel)
	}
	if result.Message != "model switched: local/qwen → qwen-code" {
		t.Fatalf("Message = %q", result.Message)
	}
}

func TestModelsCommandSwitchesModelWithReasoningEffort(t *testing.T) {
	var gotReasoningEffort string
	cmd := ModelsCommand{
		Switcher: func(ctx context.Context, session *agent.AgentSession, ref, reasoningEffort string) (ModelSwitchResult, error) {
			gotReasoningEffort = reasoningEffort
			return ModelSwitchResult{Ref: ref, Model: "qwen-code", ReasoningEffort: reasoningEffort}, nil
		},
	}

	result, err := cmd.Run(context.Background(), &agent.AgentSession{}, "--reasoning xhigh local/qwen")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotReasoningEffort != "xhigh" {
		t.Fatalf("reasoning effort = %q, want xhigh", gotReasoningEffort)
	}
	if result.Message != "model switched: local/qwen → qwen-code (reasoning=xhigh)" {
		t.Fatalf("Message = %q", result.Message)
	}
}

func TestModelsCommandSwitchError(t *testing.T) {
	cmd := ModelsCommand{
		Switcher: func(ctx context.Context, session *agent.AgentSession, ref, reasoningEffort string) (ModelSwitchResult, error) {
			return ModelSwitchResult{}, fmt.Errorf("unknown configured model %q", ref)
		},
	}

	_, err := cmd.Run(context.Background(), &agent.AgentSession{}, "missing/model")
	if err == nil || !strings.Contains(err.Error(), `unknown configured model "missing/model"`) {
		t.Fatalf("Run() error = %v, want unknown model", err)
	}
}

func TestModelsCommandReasoningEffortRequiresValue(t *testing.T) {
	cmd := ModelsCommand{}

	_, err := cmd.Run(context.Background(), &agent.AgentSession{}, "--reasoning")
	if err == nil || !strings.Contains(err.Error(), "--reasoning requires a value") {
		t.Fatalf("Run() error = %v, want missing reasoning effort value", err)
	}
}

func TestModelsCommandCompletesModelRefsForEmptyArgument(t *testing.T) {
	cmd := ModelsCommand{Choices: []config.ModelChoice{
		{Ref: "openai/gpt"},
		{Ref: "local/qwen"},
	}}

	completion, ok := cmd.Complete("", 0)
	if !ok {
		t.Fatal("Complete() did not offer model refs")
	}
	want := []string{"local/qwen", "openai/gpt"}
	if fmt.Sprint(completion.Candidates) != fmt.Sprint(want) {
		t.Fatalf("Candidates = %v, want %v", completion.Candidates, want)
	}
}

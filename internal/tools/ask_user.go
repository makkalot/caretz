package tools

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

const (
	AskUserToolName = "AskUser"

	askUserMaxQuestions  = 4
	askUserMinOptions    = 2
	askUserMaxOptions    = 4
	askUserMaxHeaderRune = 16
)

// QuestionAsker shows questions to the session's user and waits for the
// resolution, which carries the answers when the user answered.
type QuestionAsker func(context.Context, llm.ToolCall, []eventing.Question) (eventing.PermissionResolution, error)

type questionAskerKey struct{}

func WithQuestionAsker(ctx context.Context, asker QuestionAsker) context.Context {
	return context.WithValue(ctx, questionAskerKey{}, asker)
}

// QuestionAskerAvailable reports whether someone can answer AskUser in ctx.
// AskUser is offered to the model only when this is true.
func QuestionAskerAvailable(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	asker, _ := ctx.Value(questionAskerKey{}).(QuestionAsker)
	return asker != nil
}

type AskUserToolArguments struct {
	Questions []eventing.Question `json:"questions"`
}

// AskUserToolDefinition is not part of ToolDefinitions: the agent loop adds
// it only to turns that have a QuestionAsker.
func AskUserToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "AskUser",
		Description: "Ask the user 1-4 multiple-choice questions and wait for the answers. Use it only when you are blocked on a decision that is genuinely the user's to make and cannot be settled from the request, the code or a sensible default; otherwise pick the conventional option and say so. " +
			"Put the option you recommend first and set recommended on it. Each question must stand on its own, since the user may see only the question picker. The user can always type their own answer, so never add an \"Other\" option. " +
			"Do not use it to ask whether to proceed or whether a plan is ready.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"description": "Questions to ask, 1-4.",
					"minItems":    1,
					"maxItems":    askUserMaxQuestions,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"header": map[string]any{
								"type":        "string",
								"description": fmt.Sprintf("Very short label shown as a tab, at most %d characters, for example \"Auth method\".", askUserMaxHeaderRune),
							},
							"question": map[string]any{
								"type":        "string",
								"description": "The complete question, ending with a question mark.",
							},
							"multi_select": map[string]any{
								"type":        "boolean",
								"description": "Let the user pick several options instead of one.",
							},
							"options": map[string]any{
								"type":        "array",
								"description": "2-4 distinct choices, recommended first.",
								"minItems":    askUserMinOptions,
								"maxItems":    askUserMaxOptions,
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label": map[string]any{
											"type":        "string",
											"description": "Concise choice text, 1-5 words.",
										},
										"description": map[string]any{
											"type":        "string",
											"description": "What choosing this means, including trade-offs.",
										},
										"recommended": map[string]any{
											"type":        "boolean",
											"description": "Mark the option you recommend; at most one per question.",
										},
									},
									"required": []string{"label"},
								},
							},
						},
						"required": []string{"header", "question", "options"},
					},
				},
			},
			"required": []string{"questions"},
		},
	}
}

func validateAskUserQuestions(questions []eventing.Question) error {
	if len(questions) == 0 || len(questions) > askUserMaxQuestions {
		return fmt.Errorf("questions must contain 1-%d questions", askUserMaxQuestions)
	}
	for i, q := range questions {
		n := i + 1
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("question %d: question is required", n)
		}
		if strings.TrimSpace(q.Header) == "" {
			return fmt.Errorf("question %d: header is required", n)
		}
		if utf8.RuneCountInString(q.Header) > askUserMaxHeaderRune {
			return fmt.Errorf("question %d: header must be at most %d characters", n, askUserMaxHeaderRune)
		}
		if len(q.Options) < askUserMinOptions || len(q.Options) > askUserMaxOptions {
			return fmt.Errorf("question %d: options must contain %d-%d choices", n, askUserMinOptions, askUserMaxOptions)
		}
		labels := map[string]bool{}
		recommended := 0
		for _, option := range q.Options {
			label := strings.TrimSpace(option.Label)
			if label == "" {
				return fmt.Errorf("question %d: option label is required", n)
			}
			if strings.EqualFold(label, "other") {
				return fmt.Errorf("question %d: do not add an \"Other\" option; the user can always type their own answer", n)
			}
			if labels[strings.ToLower(label)] {
				return fmt.Errorf("question %d: duplicate option %q", n, label)
			}
			labels[strings.ToLower(label)] = true
			if option.Recommended {
				recommended++
			}
		}
		if recommended > 1 {
			return fmt.Errorf("question %d: at most one option may be recommended", n)
		}
	}
	return nil
}

func (r *Registry) handleAskUserToolCall(ctx context.Context, call llm.ToolCall) (llm.Message, error) {
	asker, _ := ctx.Value(questionAskerKey{}).(QuestionAsker)
	if asker == nil {
		return llm.Message{}, fmt.Errorf("AskUser is unavailable: no one can answer questions in this session; decide yourself and state your assumption")
	}
	var args AskUserToolArguments
	if err := parseToolArgs(call, &args); err != nil {
		return llm.Message{}, fmt.Errorf("invalid arguments: %w", err)
	}
	for i := range args.Questions {
		args.Questions[i].Header = strings.TrimSpace(args.Questions[i].Header)
		for j := range args.Questions[i].Options {
			args.Questions[i].Options[j].Label = strings.TrimSpace(args.Questions[i].Options[j].Label)
		}
	}
	if err := validateAskUserQuestions(args.Questions); err != nil {
		return llm.Message{}, err
	}
	resolution, err := asker(ctx, call, args.Questions)
	if err != nil {
		return llm.Message{}, err
	}
	content, err := formatAskUserResult(args.Questions, resolution)
	if err != nil {
		return llm.Message{}, err
	}
	return llm.Message{Role: llm.RoleTool, Content: content, ToolCallID: call.ID}, nil
}

// formatAskUserResult tells the model what the user answered, or that they
// did not answer and it should continue on its own judgment.
func formatAskUserResult(questions []eventing.Question, resolution eventing.PermissionResolution) (string, error) {
	switch resolution.Decision {
	case eventing.PermissionDecisionAllow:
	case eventing.PermissionDecisionDeny:
		if resolution.Resolver == "permission-timeout" {
			return "The user did not answer in time. Continue with the recommended options and state that assumption.", nil
		}
		return "The user dismissed the questions without answering. Continue with your best judgment and state your assumptions; do not ask the same questions again.", nil
	default:
		return "", fmt.Errorf("questions were not answered: %s", resolution.Decision)
	}
	// A client that shows questions as a plain approval allows without
	// answers.
	if _, ok := resolution.Metadata[eventing.QuestionAnswersKey]; !ok {
		return "The user acknowledged the questions without choosing. Continue with the recommended options and state that assumption.", nil
	}
	answers, err := eventing.DecodeQuestionAnswers(resolution)
	if err != nil {
		return "", err
	}
	if len(answers) != len(questions) {
		return "", fmt.Errorf("got %d answers for %d questions", len(answers), len(questions))
	}
	var b strings.Builder
	b.WriteString("The user answered:\n")
	for i, q := range questions {
		parts := append([]string(nil), answers[i].Selected...)
		if other := strings.TrimSpace(answers[i].Other); other != "" {
			parts = append(parts, fmt.Sprintf("their own answer: %q", other))
		}
		answer := strings.Join(parts, ", ")
		if answer == "" {
			answer = "(no option selected)"
		}
		fmt.Fprintf(&b, "- %s: %s\n", q.Header, answer)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

func askUserCall(t *testing.T, questions []eventing.Question) llm.ToolCall {
	t.Helper()
	arguments, err := json.Marshal(AskUserToolArguments{Questions: questions})
	if err != nil {
		t.Fatal(err)
	}
	return llm.ToolCall{ID: "ask", Name: AskUserToolName, Arguments: string(arguments)}
}

func validQuestion() eventing.Question {
	return eventing.Question{
		Header:   "Scope",
		Question: "Which platforms?",
		Options:  []eventing.QuestionOption{{Label: "macOS", Recommended: true}, {Label: "Linux"}},
	}
}

func TestValidateAskUserQuestions(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*eventing.Question)
		want   string
	}{
		{name: "valid", mutate: func(*eventing.Question) {}},
		{name: "one option", mutate: func(q *eventing.Question) { q.Options = q.Options[:1] }, want: "options must contain"},
		{name: "other option", mutate: func(q *eventing.Question) { q.Options[1].Label = "Other" }, want: "Other"},
		{name: "duplicate", mutate: func(q *eventing.Question) { q.Options[1].Label = "MACOS" }, want: "duplicate"},
		{name: "two recommended", mutate: func(q *eventing.Question) { q.Options[1].Recommended = true }, want: "at most one"},
		{name: "long header", mutate: func(q *eventing.Question) { q.Header = strings.Repeat("h", 17) }, want: "header must be"},
		{name: "no question", mutate: func(q *eventing.Question) { q.Question = " " }, want: "question is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := validQuestion()
			test.mutate(&q)
			err := validateAskUserQuestions([]eventing.Question{q})
			if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
	if err := validateAskUserQuestions(nil); err == nil {
		t.Fatal("no questions accepted")
	}
}

func TestAskUserWithoutAskerFails(t *testing.T) {
	_, err := (&Registry{}).HandleToolCallContext(context.Background(), askUserCall(t, []eventing.Question{validQuestion()}))
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestAskUserResult(t *testing.T) {
	multi := validQuestion()
	multi.Header = "Targets"
	multi.MultiSelect = true
	questions := []eventing.Question{validQuestion(), multi}
	for _, test := range []struct {
		name       string
		resolution eventing.PermissionResolution
		want       []string
	}{
		{
			name: "answered",
			resolution: eventing.PermissionResolution{Decision: eventing.PermissionDecisionAllow, Metadata: eventing.QuestionAnswersMetadata([]eventing.QuestionAnswer{
				{Other: "FreeBSD only"},
				{Selected: []string{"macOS", "Linux"}},
			})},
			want: []string{`- Scope: their own answer: "FreeBSD only"`, "- Targets: macOS, Linux"},
		},
		{
			name:       "dismissed",
			resolution: eventing.PermissionResolution{Decision: eventing.PermissionDecisionDeny, Resolver: "tui"},
			want:       []string{"dismissed", "do not ask the same questions again"},
		},
		{
			name:       "allowed without answers",
			resolution: eventing.PermissionResolution{Decision: eventing.PermissionDecisionAllow},
			want:       []string{"without choosing", "recommended"},
		},
		{
			name:       "timed out",
			resolution: eventing.PermissionResolution{Decision: eventing.PermissionDecisionDeny, Resolver: "permission-timeout"},
			want:       []string{"did not answer in time", "recommended"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var asked []eventing.Question
			ctx := WithQuestionAsker(context.Background(), func(_ context.Context, _ llm.ToolCall, q []eventing.Question) (eventing.PermissionResolution, error) {
				asked = q
				return test.resolution, nil
			})
			message, err := (&Registry{}).HandleToolCallContext(ctx, askUserCall(t, questions))
			if err != nil {
				t.Fatal(err)
			}
			if len(asked) != 2 || message.ToolCallID != "ask" {
				t.Fatalf("asked = %+v, message = %+v", asked, message)
			}
			for _, want := range test.want {
				if !strings.Contains(message.Content, want) {
					t.Fatalf("content = %q, want %q", message.Content, want)
				}
			}
		})
	}
}

func TestAskUserCanBeDisabled(t *testing.T) {
	registry := &Registry{}
	if !registry.IsToolEnabled(AskUserToolName) {
		t.Fatal("AskUser disabled by default")
	}
	if registry.WithDisabledTools(map[string]bool{AskUserToolName: true}).IsToolEnabled(AskUserToolName) {
		t.Fatal("disabled AskUser still enabled")
	}
	if AskUserToolDefinition().Name != AskUserToolName {
		t.Fatal("definition name does not match AskUserToolName")
	}
}

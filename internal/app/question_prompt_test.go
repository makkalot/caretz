package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/lib/eventing"
)

func pickerQuestions() []eventing.Question {
	return []eventing.Question{
		{
			Header:   "Auth",
			Question: "Which auth flow?",
			Options: []eventing.QuestionOption{
				{Label: "Browser", Description: "Needs a local port"},
				{Label: "Device code", Description: "Works over SSH", Recommended: true},
			},
		},
		{
			Header:      "Targets",
			Question:    "Which platforms?",
			MultiSelect: true,
			Options:     []eventing.QuestionOption{{Label: "macOS"}, {Label: "Linux"}, {Label: "Windows"}},
		},
	}
}

const (
	keyDown  = "\x1b[B"
	keyUp    = "\x1b[A"
	keyLeft  = "\x1b[D"
	keyEnter = "\r"
)

func TestReadQuestionAnswers(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  []eventing.QuestionAnswer
	}{
		{
			// The cursor starts on the recommended option.
			name:  "enter takes recommended, space toggles",
			input: keyEnter + " " + keyDown + keyDown + " " + keyEnter,
			want:  []eventing.QuestionAnswer{{Selected: []string{"Device code"}}, {Selected: []string{"macOS", "Windows"}}},
		},
		{
			name:  "number keys pick and enter confirms multi",
			input: "1" + "2" + keyEnter,
			want:  []eventing.QuestionAnswer{{Selected: []string{"Browser"}}, {Selected: []string{"Linux"}}},
		},
		{
			name:  "other takes typed text",
			input: "3" + "only CLI" + keyEnter + "1" + keyEnter,
			want:  []eventing.QuestionAnswer{{Other: "only CLI"}, {Selected: []string{"macOS"}}},
		},
		{
			name:  "enter with nothing toggled takes the cursor row",
			input: keyEnter + keyUp + keyUp + keyEnter,
			want:  []eventing.QuestionAnswer{{Selected: []string{"Device code"}}, {Selected: []string{"Windows"}}},
		},
		{
			name:  "answering later question first",
			input: "\t" + "3" + keyEnter + keyEnter,
			want:  []eventing.QuestionAnswer{{Selected: []string{"Device code"}}, {Selected: []string{"Windows"}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			terminal := ansi.NewTerminal(strings.NewReader(test.input), &out)
			answers, outcome, err := readQuestionAnswers(terminal, pickerQuestions(), time.Time{})
			if err != nil {
				t.Fatalf("err = %v, output = %q", err, out.String())
			}
			if outcome != questionsAnswered || !reflect.DeepEqual(answers, test.want) {
				t.Fatalf("outcome = %v, answers = %+v, want %+v", outcome, answers, test.want)
			}
		})
	}
}

func TestQuestionResolutionEscapeSkipsAndCtrlCStops(t *testing.T) {
	request := eventing.PermissionRequest{RequestID: "q", Capability: eventing.PermissionCapabilityQuestion, Details: eventing.QuestionDetails(pickerQuestions())}

	terminal := ansi.NewTerminal(strings.NewReader("\x1b "), &bytes.Buffer{})
	resolution, err := questionResolution(terminal, request)
	if err != nil || resolution.Decision != eventing.PermissionDecisionDeny || resolution.RequestID != "q" {
		t.Fatalf("escape: resolution = %+v, err = %v", resolution, err)
	}

	terminal = ansi.NewTerminal(strings.NewReader("\x03"), &bytes.Buffer{})
	if _, err := questionResolution(terminal, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctrl-c: err = %v", err)
	}
}

func TestQuestionResolutionCarriesAnswers(t *testing.T) {
	request := eventing.PermissionRequest{RequestID: "q", Capability: eventing.PermissionCapabilityQuestion, Details: eventing.QuestionDetails(pickerQuestions()[:1])}
	var out bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader(keyLeft+keyEnter), &out)
	resolution, err := questionResolution(terminal, request)
	if err != nil {
		t.Fatal(err)
	}
	answers, err := eventing.DecodeQuestionAnswers(resolution)
	if err != nil || resolution.Decision != eventing.PermissionDecisionAllow || len(answers) != 1 || answers[0].Selected[0] != "Device code" {
		t.Fatalf("resolution = %+v, answers = %+v, err = %v", resolution, answers, err)
	}
	if !strings.Contains(out.String(), "Works over SSH") || !strings.Contains(out.String(), "Auth → Device code") {
		t.Fatalf("output = %q", out.String())
	}
}

// Model-written text cannot inject escape sequences into the picker.
func TestQuestionPickerStripsEscapes(t *testing.T) {
	questions := pickerQuestions()[:1]
	questions[0].Question = "Pick\x1b[2J one?"
	var out bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader(keyEnter), &out)
	if _, _, err := readQuestionAnswers(terminal, questions, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b[2J") {
		t.Fatalf("escape sequence reached the terminal: %q", out.String())
	}
}

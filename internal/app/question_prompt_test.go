package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/output"
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
			answers, outcome, err := readQuestionAnswers(terminal, pickerQuestions(), time.Time{}, nil)
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
	resolution, err := questionResolution(terminal, request, nil)
	if err != nil || resolution.Decision != eventing.PermissionDecisionDeny || resolution.RequestID != "q" {
		t.Fatalf("escape: resolution = %+v, err = %v", resolution, err)
	}

	terminal = ansi.NewTerminal(strings.NewReader("\x03"), &bytes.Buffer{})
	if _, err := questionResolution(terminal, request, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctrl-c: err = %v", err)
	}
}

// A question request can be withdrawn while the picker is up, such as when
// the subagent that asked is stopped; the picker is then dismissed instead of
// waiting for answers nobody reads.
func TestQuestionResolutionDismissesWithdrawnRequest(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	request := eventing.PermissionRequest{RequestID: "q", Capability: eventing.PermissionCapabilityQuestion, Details: eventing.QuestionDetails(pickerQuestions())}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	var out bytes.Buffer
	terminal := ansi.NewTerminal(r, &out)
	var checks atomic.Int32
	withdrawn := func() bool { return checks.Add(1) > 2 }

	done := make(chan error, 1)
	go func() {
		_, err := questionResolution(terminal, request, withdrawn)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errPermissionWithdrawn) {
			t.Fatalf("err = %v, want errPermissionWithdrawn", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("picker still waiting for input after the request was withdrawn")
	}
	if !strings.Contains(out.String(), "no longer needed") {
		t.Fatalf("withdrawal notice missing: %q", out.String())
	}
}

func TestQuestionResolutionCarriesAnswers(t *testing.T) {
	request := eventing.PermissionRequest{RequestID: "q", Capability: eventing.PermissionCapabilityQuestion, Details: eventing.QuestionDetails(pickerQuestions()[:1])}
	var out bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader(keyLeft+keyEnter), &out)
	resolution, err := questionResolution(terminal, request, nil)
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

// A long Other answer wraps under its label instead of being cut to one line.
func TestQuestionPickerWrapsLongOther(t *testing.T) {
	answer := strings.TrimSpace(strings.Repeat("the quick brown fox jumps ", 6))
	picker := newQuestionPicker(pickerQuestions()[:1])
	picker.states[0].cursor = picker.otherRow()
	picker.editing = true
	picker.states[0].other = answer
	lines := picker.lines(output.NewStyle(&bytes.Buffer{}), 40)
	var text strings.Builder
	for _, line := range lines {
		if ansi.VisibleWidth(line) > 40 {
			t.Fatalf("line wider than 40 cells: %q", line)
		}
		text.WriteString(strings.TrimSpace(ansi.Strip(line)) + " ")
	}
	if !strings.Contains(text.String(), "jumps the quick brown fox jumps▌") {
		t.Fatalf("answer end not visible: %q", lines)
	}

	got := wrapInput(strings.Repeat("ab ", 40), 10, 3, true)
	if len(got) != 3 || !strings.HasPrefix(got[0], "…") {
		t.Fatalf("tail wrap = %q", got)
	}
	got = wrapInput(strings.Repeat("ab ", 40), 10, 3, false)
	if len(got) != 3 || !strings.HasSuffix(got[2], "…") || got[0] != "ab ab ab " {
		t.Fatalf("head wrap = %q", got)
	}
}

// Model-written text cannot inject escape sequences into the picker.
func TestQuestionPickerStripsEscapes(t *testing.T) {
	questions := pickerQuestions()[:1]
	questions[0].Question = "Pick\x1b[2J one?"
	var out bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader(keyEnter), &out)
	if _, _, err := readQuestionAnswers(terminal, questions, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b[2J") {
		t.Fatalf("escape sequence reached the terminal: %q", out.String())
	}
}

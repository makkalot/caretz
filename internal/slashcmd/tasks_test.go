package slashcmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/tasks"
)

func TestTasksCommandShowsSummaryAndOptionalDetails(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	t.Cleanup(func() { _ = session.Close() })
	command := TasksCommand{}

	result, err := command.Run(context.Background(), session, "")
	if err != nil || result.Message != "## Tasks\n○ (none)" {
		t.Fatalf("empty /tasks = %q, %v", result.Message, err)
	}

	for i, title := range []string{"First", "Second", "Third", "Fourth", "Fifth", "Sixth", "Seventh", "Eighth"} {
		id := fmt.Sprintf("T%d", i+1)
		if _, err := session.ApplyTaskUpdate(tasks.Update{Op: tasks.OpAdd, ID: id, Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := session.ApplyTaskUpdate(tasks.Update{Op: tasks.OpStart, ID: "T1", NextAction: "Inspect parser"}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.ApplyTaskUpdate(tasks.Update{Op: tasks.OpDone, ID: "T2", Outcome: "Tests passed"}); err != nil {
		t.Fatal(err)
	}
	result, err = command.Run(context.Background(), session, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### In Progress", "▶ T1: First", "### Pending", "○ T8: Eighth", "### Done", "✓ T2: Second"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("/tasks missing %q:\n%s", want, result.Message)
		}
	}
	for _, unwanted := range []string{"Next:", "Outcome:", "Validate:", "tokens"} {
		if strings.Contains(result.Message, unwanted) {
			t.Errorf("/tasks unexpectedly includes detail %q:\n%s", unwanted, result.Message)
		}
	}
	if strings.Contains(result.Message, "more)") {
		t.Fatalf("/tasks truncated the list:\n%s", result.Message)
	}

	result, err = command.Run(context.Background(), session, "--detail")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"▶ T1: First\n  Next: Inspect parser", "✓ T2: Second\n  Outcome: Tests passed"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("/tasks --detail missing %q:\n%s", want, result.Message)
		}
	}
}

func TestTasksCommandRejectsArgumentsAndDisabledTasks(t *testing.T) {
	command := TasksCommand{}
	if _, err := command.Run(context.Background(), nil, "extra"); err == nil || !strings.Contains(err.Error(), "usage: /tasks [--detail]") {
		t.Fatalf("arguments error = %v", err)
	}
	if _, err := command.Run(context.Background(), nil, ""); err == nil || !strings.Contains(err.Error(), "agent session is required") {
		t.Fatalf("missing session error = %v", err)
	}
	session := newLoopTestSession(t, &loopMockProvider{}, true)
	t.Cleanup(func() { _ = session.Close() })
	if _, err := command.Run(context.Background(), session, ""); err == nil || !strings.Contains(err.Error(), "--no-tasks") {
		t.Fatalf("disabled tasks error = %v", err)
	}
}

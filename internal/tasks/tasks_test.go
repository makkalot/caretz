package tasks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStateApplyEnforcesSingleInProgress(t *testing.T) {
	state := NewState()
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T1", Title: "first"}); err != nil {
		t.Fatalf("add T1: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T2", Title: "second"}); err != nil {
		t.Fatalf("add T2: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpStart, ID: "T1", NextAction: "work on first"}); err != nil {
		t.Fatalf("start T1: %v", err)
	}
	_, err := state.Apply(Update{Op: OpStart, ID: "T2", NextAction: "work on second"})
	if err == nil || !strings.Contains(err.Error(), "finish or block") {
		t.Fatalf("start T2 error = %v, want single in-progress error", err)
	}
}

func TestStateValidationIsOptionalAndCanBeUpdated(t *testing.T) {
	state := NewState()
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T1", Title: "legacy task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T2", Title: "priced request", Validation: "  Run tests and lint  "}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Apply(Update{Op: OpStart, ID: "T2", NextAction: "add price"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Apply(Update{Op: OpUpdate, ID: "T2", Validation: "Check pricing response"}); err != nil {
		t.Fatal(err)
	}
	task := state.Tasks["T2"]
	if task.Validation != "Check pricing response" || task.NextAction != "add price" {
		t.Fatalf("updated task = %+v", task)
	}
	if !strings.Contains(Render(state), "validation: Check pricing response") || !strings.Contains(RenderFull(state), "Validate:\n    - Check pricing response") {
		t.Fatalf("rendered task missing validation: %s", RenderFull(state))
	}
	encoded, err := json.Marshal(state.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"validation":""`) {
		t.Fatalf("empty validation should be omitted: %s", encoded)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	if restored := FromSnapshot(snapshot); restored.Tasks["T2"].Validation != "Check pricing response" || restored.Tasks["T1"].Validation != "" {
		t.Fatalf("restored tasks = %+v", restored.Tasks)
	}
}

func TestStateApplyAllowsOneInProgressPerScope(t *testing.T) {
	state := NewState()
	for _, update := range []Update{
		{Op: OpAdd, ID: "T1", Title: "ordinary"},
		{Op: OpAdd, ID: "loop-a-T1", Scope: "loop-a", Title: "first loop"},
		{Op: OpAdd, ID: "loop-b-T1", Scope: "loop-b", Title: "second loop"},
	} {
		if _, err := state.Apply(update); err != nil {
			t.Fatal(err)
		}
	}
	for _, update := range []Update{
		{Op: OpStart, ID: "T1", NextAction: "ordinary work"},
		{Op: OpStart, ID: "loop-a-T1", NextAction: "loop a work"},
		{Op: OpStart, ID: "loop-b-T1", NextAction: "loop b work"},
	} {
		if _, err := state.Apply(update); err != nil {
			t.Fatalf("start %+v: %v", update, err)
		}
	}
}

func TestStateApplyValidatesRequiredFields(t *testing.T) {
	state := NewState()
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T1"}); err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Fatalf("add error = %v, want title validation", err)
	}
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T1", Title: "first"}); err != nil {
		t.Fatalf("add T1: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpDone, ID: "T1"}); err == nil || !strings.Contains(err.Error(), "outcome is required") {
		t.Fatalf("done error = %v, want outcome validation", err)
	}
}

func TestRenderCapsPendingAndCollapsesDone(t *testing.T) {
	state := NewState()
	for _, id := range []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"} {
		if _, err := state.Apply(Update{Op: OpAdd, ID: id, Title: "task " + id}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if _, err := state.Apply(Update{Op: OpDone, ID: "T1", Outcome: "finished one"}); err != nil {
		t.Fatalf("done T1: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpDone, ID: "T2", Outcome: "finished two"}); err != nil {
		t.Fatalf("done T2: %v", err)
	}
	rendered := Render(state)
	for _, want := range []string{"## Tasks", "○ T3", "○ (+1 more)", "✓ T2: task T2 — outcome: finished two", "✓ T1 (done)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered tasks missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "finished one") {
		t.Fatalf("older done outcome should be collapsed:\n%s", rendered)
	}
}

func TestRenderFullIncludesAllTasks(t *testing.T) {
	state := NewState()
	for _, id := range []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"} {
		if _, err := state.Apply(Update{Op: OpAdd, ID: id, Title: "task " + id}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if _, err := state.Apply(Update{Op: OpStart, ID: "T8", NextAction: "finish eight"}); err != nil {
		t.Fatalf("start T8: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpBlock, ID: "T7", NextAction: "wait on user"}); err != nil {
		t.Fatalf("block T7: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpDone, ID: "T1", Outcome: "finished one"}); err != nil {
		t.Fatalf("done T1: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpDone, ID: "T2", Outcome: "finished two"}); err != nil {
		t.Fatalf("done T2: %v", err)
	}

	rendered := RenderFull(state)
	for _, want := range []string{
		"### In Progress",
		"▶ T8: task T8\n  Next: finish eight",
		"### Blocked",
		"■ T7: task T7\n  Next: wait on user",
		"### Pending",
		"○ T3: task T3",
		"○ T6: task T6",
		"### Done",
		"✓ T1: task T1\n  Outcome: finished one",
		"✓ T2: task T2\n  Outcome: finished two",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("full rendered tasks missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "(+1 more)") || strings.Contains(rendered, "T1 (done)") {
		t.Fatalf("full render should not collapse tasks:\n%s", rendered)
	}
}

func TestRenderFullFormatsMultilineValidationAsChecklist(t *testing.T) {
	state := NewState()
	if _, err := state.Apply(Update{
		Op: OpAdd, ID: "T1", Title: "Build the CLI",
		Validation: "help lists every command\nintegration tests pass\nfull suite stays green",
		NextAction: "Implement argument parsing and HTTP helpers.",
	}); err != nil {
		t.Fatal(err)
	}

	rendered := RenderFull(state)
	for _, want := range []string{
		"○ T1: Build the CLI",
		"  Validate:\n    - help lists every command\n    - integration tests pass\n    - full suite stays green",
		"  Next: Implement argument parsing and HTTP helpers.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered tasks missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderSummaryOnlyShowsTaskDescriptions(t *testing.T) {
	state := NewState()
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T1", Title: "Build the CLI", Validation: "help works", NextAction: "write parser"}); err != nil {
		t.Fatal(err)
	}
	rendered := RenderSummary(state)
	if !strings.Contains(rendered, "○ T1: Build the CLI") {
		t.Fatalf("summary missing task description:\n%s", rendered)
	}
	for _, unwanted := range []string{"help works", "write parser", "Validate:", "Next:"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("summary includes detail %q:\n%s", unwanted, rendered)
		}
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	state := NewState()
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T1", Title: "first"}); err != nil {
		t.Fatalf("add T1: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpStart, ID: "T1", NextAction: "continue first"}); err != nil {
		t.Fatalf("start T1: %v", err)
	}
	completedAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	if _, err := state.Apply(Update{Op: OpDone, ID: "T1", Outcome: "finished first"}); err != nil {
		t.Fatalf("done T1: %v", err)
	}
	state.MarkCompletedAt("T1", completedAt)
	restored := FromSnapshot(state.Snapshot())
	task := restored.Tasks["T1"]
	if task.Status != StatusDone || task.Outcome != "finished first" || task.CompletedAt == nil || !task.CompletedAt.Equal(completedAt) {
		t.Fatalf("restored task = %+v", task)
	}
}

func TestStateApplyPruneExpiredOnlyRemovesDoneTasks(t *testing.T) {
	state := NewState()
	for _, id := range []string{"T1", "T2", "T3"} {
		if _, err := state.Apply(Update{Op: OpAdd, ID: id, Title: "task " + id}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if _, err := state.Apply(Update{Op: OpDone, ID: "T1", Outcome: "finished one"}); err != nil {
		t.Fatalf("done T1: %v", err)
	}
	if _, err := state.Apply(Update{Op: OpBlock, ID: "T2", NextAction: "wait"}); err != nil {
		t.Fatalf("block T2: %v", err)
	}

	if _, err := state.Apply(Update{Op: OpPrune, IDs: []string{"T1", "T2", "missing"}}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, ok := state.Tasks["T1"]; ok {
		t.Fatalf("done task T1 was not pruned: %#v", state.Tasks)
	}
	if _, ok := state.Tasks["T2"]; !ok {
		t.Fatalf("blocked task T2 should not be pruned: %#v", state.Tasks)
	}
	if _, ok := state.Tasks["T3"]; !ok {
		t.Fatalf("pending task T3 should not be pruned: %#v", state.Tasks)
	}
}

func TestExpiredDoneTaskIDsUsesCompletedAt(t *testing.T) {
	state := NewState()
	for _, id := range []string{"T1", "T2"} {
		if _, err := state.Apply(Update{Op: OpAdd, ID: id, Title: "task " + id}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
		if _, err := state.Apply(Update{Op: OpDone, ID: id, Outcome: "finished " + id}); err != nil {
			t.Fatalf("done %s: %v", id, err)
		}
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	state.MarkCompletedAt("T1", now.Add(-25*time.Hour))
	state.MarkCompletedAt("T2", now.Add(-2*time.Hour))

	got := state.ExpiredDoneTaskIDs(now, 24*time.Hour)
	if len(got) != 1 || got[0] != "T1" {
		t.Fatalf("ExpiredDoneTaskIDs() = %#v, want [T1]", got)
	}
}

// Small models drop next_action and skip add; a recoverable call must not turn
// into a retry loop.
func TestStateApplyStartAndDoneTolerateMissingFields(t *testing.T) {
	state := NewState()
	if _, err := state.Apply(Update{Op: OpStart, ID: "T1"}); err == nil || !strings.Contains(err.Error(), `{"op":"add","id":"T1","title":"..."}`) {
		t.Fatalf("start unknown without title error = %v, want add example", err)
	}
	if _, err := state.Apply(Update{Op: OpStart, ID: "T1", Title: "organize downloads", Validation: "files sorted"}); err != nil {
		t.Fatalf("start unknown with title: %v", err)
	}
	if task := state.Tasks["T1"]; task.Status != StatusInProgress || task.NextAction != "organize downloads" || task.Validation != "files sorted" {
		t.Fatalf("started task = %+v", task)
	}
	if _, err := state.Apply(Update{Op: OpAdd, ID: "T2", Title: "check result"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Apply(Update{Op: OpDone, ID: "T1", Outcome: "sorted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Apply(Update{Op: OpStart, ID: "T2"}); err != nil {
		t.Fatalf("start without next_action: %v", err)
	}
	if got := state.Tasks["T2"].NextAction; got != "check result" {
		t.Fatalf("next_action = %q, want title fallback", got)
	}
	if _, err := state.Apply(Update{Op: OpDone, ID: "T9", Outcome: "verified files"}); err != nil {
		t.Fatalf("done unknown: %v", err)
	}
	if task := state.Tasks["T9"]; task.Status != StatusDone || task.Title != "verified files" {
		t.Fatalf("done unknown task = %+v", task)
	}
}

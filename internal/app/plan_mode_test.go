package app

import (
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/ansi"
)

func TestEditorShiftTabTogglesPlanMode(t *testing.T) {
	e := newTestEditor(t, "", t.TempDir())
	planMode := false
	e.planMode = func() bool { return planMode }
	e.togglePlanMode = func() { planMode = !planMode }
	size := ansi.Size{Width: 80, Height: 10}

	if rows := e.View(size).Rows; len(rows) != 4 || !strings.HasPrefix(rows[3], "  build") {
		t.Fatalf("build mode rows = %q, want build footer", rows)
	}
	if result := e.Update(ansi.Event{Kind: ansi.EventShiftTab}); !result.Redraw || !planMode {
		t.Fatalf("Shift+Tab result = %#v planMode=%v, want redraw into plan mode", result, planMode)
	}
	rows := e.View(size).Rows
	if len(rows) != 4 || !strings.HasPrefix(rows[3], "  ⏸ plan") {
		t.Fatalf("plan mode rows = %q, want plan footer", rows)
	}
	e.Update(ansi.Event{Kind: ansi.EventShiftTab})
	if planMode {
		t.Fatal("second Shift+Tab should return to build mode")
	}
}

func TestStartTUIShiftTabTogglesSessionPlanMode(t *testing.T) {
	session := newTUISession(t, "tui-shift-tab")
	e := newTestEditor(t, "", t.TempDir())
	e.planMode = session.PlanMode
	e.togglePlanMode = func() { session.SetPlanMode(!session.PlanMode()) }

	e.Update(ansi.Event{Kind: ansi.EventShiftTab})
	if !session.PlanMode() {
		t.Fatal("Shift+Tab should put the session in plan mode")
	}
	if !session.ToolRegistry().IsToolEnabled("Edit") {
		t.Fatal("plan mode must not remove tools from the model's tool list")
	}
}

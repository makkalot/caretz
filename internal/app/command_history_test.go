package app

import (
	"testing"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/slashcmd"
)

func TestCommandHistoryNavigatesCommandsAndRestoresDraft(t *testing.T) {
	history := newCommandHistory()
	history.add("/context")
	history.add("/models local/qwen")

	value, ok := history.previous("/con")
	assertHistoryValue(t, value, ok, "/models local/qwen", true)
	value, ok = history.previous("ignored")
	assertHistoryValue(t, value, ok, "/context", true)
	value, ok = history.previous("ignored")
	assertHistoryValue(t, value, ok, "", false)
	value, ok = history.next()
	assertHistoryValue(t, value, ok, "/models local/qwen", true)
	value, ok = history.next()
	assertHistoryValue(t, value, ok, "/con", true)
	value, ok = history.next()
	assertHistoryValue(t, value, ok, "", false)
}

func TestInputEditorHistoryStoresOnlyRegisteredSlashCommands(t *testing.T) {
	registry := slashcmd.NewRegistry()
	for _, command := range []slashcmd.Command{slashcmd.ContextCommand{}, slashcmd.HelpCommand{Registry: registry}} {
		if err := registry.Register(command); err != nil {
			t.Fatal(err)
		}
	}
	editor := newTestEditor(t, "", t.TempDir())
	editor.slashCommandRegistry = registry

	for _, input := range []string{"/context", "explain this", "@reviewer check", "/missing", "/?"} {
		editor.buffer = []rune(input)
		editor.cursor = len(editor.buffer)
		editor.Update(ansi.Event{Kind: ansi.EventEnter})
	}

	editor.Update(ansi.Event{Kind: ansi.EventUp})
	if got := string(editor.buffer); got != "/?" {
		t.Fatalf("first recall = %q", got)
	}
	editor.Update(ansi.Event{Kind: ansi.EventUp})
	if got := string(editor.buffer); got != "/context" {
		t.Fatalf("second recall = %q", got)
	}
	if editor.cursor != len(editor.buffer) {
		t.Fatalf("cursor = %d, want %d", editor.cursor, len(editor.buffer))
	}
}

func TestCommandHistoryEditDetachesNavigation(t *testing.T) {
	history := newCommandHistory()
	history.add("/context")
	value, _ := history.previous("draft")
	history.edited(value + " now")
	got, ok := history.next()
	assertHistoryValue(t, got, ok, "", false)
	got, ok = history.previous(value + " now")
	assertHistoryValue(t, got, ok, "/context", true)
	got, ok = history.next()
	assertHistoryValue(t, got, ok, value+" now", true)
}

func assertHistoryValue(t *testing.T, value string, ok bool, want string, wantOK bool) {
	t.Helper()
	if value != want || ok != wantOK {
		t.Fatalf("history result = %q, %v; want %q, %v", value, ok, want, wantOK)
	}
}

package tmux

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/makkalot/caretz/internal/herdr"
)

func insideTmux(key string) string {
	if key == "TMUX" {
		return "/tmp/tmux-501/default,123,0"
	}
	return ""
}

func foundTmux(string) (string, error) { return "/opt/bin/tmux", nil }

func TestSpawnTabOpensWindowAndTilesPanes(t *testing.T) {
	var calls [][]string
	spawner := NewSpawnerFromEnvironment(SpawnerOptions{
		Getenv:   insideTmux,
		LookPath: foundTmux,
		Run: func(_ context.Context, path string, args []string) ([]byte, error) {
			if path != "/opt/bin/tmux" {
				t.Fatalf("path = %q", path)
			}
			calls = append(calls, append([]string(nil), args...))
			switch args[0] {
			case "new-window":
				return []byte("@4 %7\n"), nil
			case "split-window":
				return []byte("%8\n"), nil
			}
			return nil, nil
		},
	})
	panes, err := spawner.SpawnTab(context.Background(), "bots", "/work/my repo", herdr.LayoutDefault, []herdr.PaneCommand{
		{Label: "reviewer", Command: []string{"/bin/caretz", "peer", "reviewer"}},
		{Label: "planner", Command: []string{"/bin/caretz", "peer", "planner", "--config-root", "/work/it's"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(panes, []string{"%7", "%8"}) {
		t.Fatalf("panes = %#v", panes)
	}
	want := [][]string{
		{"new-window", "-d", "-P", "-F", "#{window_id} #{pane_id}", "-n", "bots", "-c", "/work/my repo"},
		{"select-pane", "-t", "%7", "-T", "reviewer"},
		{"send-keys", "-t", "%7", "-l", "/bin/caretz peer reviewer"},
		{"send-keys", "-t", "%7", "Enter"},
		{"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "@4", "-c", "/work/my repo"},
		{"select-layout", "-t", "@4", "tiled"},
		{"select-pane", "-t", "%8", "-T", "planner"},
		{"send-keys", "-t", "%8", "-l", `/bin/caretz peer planner --config-root '/work/it'\''s'`},
		{"send-keys", "-t", "%8", "Enter"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls =\n%#v\nwant\n%#v", calls, want)
	}
}

func TestSpawnTabAppliesLayout(t *testing.T) {
	for layout, want := range map[herdr.Layout]string{herdr.LayoutRows: "even-vertical", herdr.LayoutColumns: "even-horizontal"} {
		var applied []string
		spawner := NewSpawnerFromEnvironment(SpawnerOptions{
			Getenv:   insideTmux,
			LookPath: foundTmux,
			Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				switch args[0] {
				case "new-window":
					return []byte("@4 %7\n"), nil
				case "split-window":
					return []byte("%8\n"), nil
				case "select-layout":
					applied = append(applied, args[len(args)-1])
				}
				return nil, nil
			},
		})
		_, err := spawner.SpawnTab(context.Background(), "impl", "/work", layout, []herdr.PaneCommand{
			{Label: "a", Command: []string{"caretz", "peer", "a"}},
			{Label: "b", Command: []string{"caretz", "peer", "b"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(applied, []string{want}) {
			t.Fatalf("%s: layouts = %v, want [%s]", layout, applied, want)
		}
	}
}

func TestSpawnerRequiresRunningInsideTmux(t *testing.T) {
	outside := func(string) string { return "" }
	if NewSpawnerFromEnvironment(SpawnerOptions{Getenv: outside, LookPath: foundTmux}) != nil {
		t.Fatal("spawner enabled outside tmux")
	}
	missing := func(string) (string, error) { return "", errors.New("not found") }
	if NewSpawnerFromEnvironment(SpawnerOptions{Getenv: insideTmux, LookPath: missing}) != nil {
		t.Fatal("spawner enabled without a tmux binary")
	}
}

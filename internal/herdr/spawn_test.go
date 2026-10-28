package herdr

import (
	"context"
	"reflect"
	"testing"
)

func TestSpawnTabCreatesTabAndStacksPanes(t *testing.T) {
	var calls [][]string
	responses := map[string]string{
		"tab":   `{"result":{"tab":{"tab_id":"w1:t3"},"root_pane":{"pane_id":"w1:p7"}}}`,
		"split": `{"result":{"pane":{"pane_id":"w1:p8"}}}`,
	}
	spawner := NewSpawnerFromEnvironment(SpawnerOptions{
		Getenv: func(key string) string {
			if key == "HERDR_WORKSPACE_ID" {
				return "w1"
			}
			return validEnvironment(key)
		},
		Run: func(_ context.Context, path string, args []string) ([]byte, error) {
			if path != "/usr/local/bin/herdr" {
				t.Fatalf("path = %q", path)
			}
			calls = append(calls, append([]string(nil), args...))
			switch args[0] + " " + args[1] {
			case "tab create":
				return []byte(responses["tab"]), nil
			case "pane split":
				return []byte(responses["split"]), nil
			}
			return []byte(`{"result":{}}`), nil
		},
	})
	panes, err := spawner.SpawnTab(context.Background(), "bots", "/work/my repo", LayoutDefault, []PaneCommand{
		{Label: "reviewer", Command: []string{"/bin/caretz", "peer", "reviewer"}},
		{Label: "planner", Command: []string{"/bin/caretz", "peer", "planner", "--config-root", "/work/it's"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(panes, []string{"w1:p7", "w1:p8"}) {
		t.Fatalf("panes = %#v", panes)
	}
	want := [][]string{
		{"tab", "create", "--label", "bots", "--cwd", "/work/my repo", "--no-focus", "--workspace", "w1"},
		{"pane", "rename", "w1:p7", "reviewer"},
		{"pane", "run", "w1:p7", "/bin/caretz peer reviewer"},
		{"pane", "split", "w1:p7", "--direction", "down", "--cwd", "/work/my repo", "--no-focus"},
		{"pane", "rename", "w1:p8", "planner"},
		{"pane", "run", "w1:p8", `/bin/caretz peer planner --config-root '/work/it'\''s'`},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls =\n%#v\nwant\n%#v", calls, want)
	}
}

func TestSpawnTabColumnsSplitsRight(t *testing.T) {
	var splits [][]string
	spawner := NewSpawnerFromEnvironment(SpawnerOptions{
		Getenv: validEnvironment,
		Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			switch args[0] + " " + args[1] {
			case "tab create":
				return []byte(`{"result":{"root_pane":{"pane_id":"p1"}}}`), nil
			case "pane split":
				splits = append(splits, append([]string(nil), args...))
				return []byte(`{"result":{"pane":{"pane_id":"p2"}}}`), nil
			}
			return []byte(`{"result":{}}`), nil
		},
	})
	_, err := spawner.SpawnTab(context.Background(), "impl", "/work", LayoutColumns, []PaneCommand{
		{Label: "a", Command: []string{"caretz", "peer", "a"}},
		{Label: "b", Command: []string{"caretz", "peer", "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"pane", "split", "p1", "--direction", "right", "--cwd", "/work", "--no-focus"}}
	if !reflect.DeepEqual(splits, want) {
		t.Fatalf("splits = %#v, want %#v", splits, want)
	}
}

func TestSpawnerRequiresHerdr(t *testing.T) {
	if NewSpawnerFromEnvironment(SpawnerOptions{Getenv: func(string) string { return "" }}) != nil {
		t.Fatal("spawner enabled outside Herdr")
	}
}

func TestReporterUsesResumeCommandOverride(t *testing.T) {
	reporter := &Reporter{resumable: true, adapter: "peer", resumeCommand: []string{"caretz", "peer", "reviewer"}}
	if got, want := reporter.resumeArgs("reviewer"), []string{"caretz", "peer", "reviewer"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resume args = %#v, want %#v", got, want)
	}
}

func TestReporterDropsPathResumeCommand(t *testing.T) {
	// Herdr rejects a report whose resume command is a path, which would
	// hide the agent entirely; reporting without a resume command is better.
	reporter := &Reporter{resumable: true, adapter: "peer", resumeCommand: []string{"/usr/local/bin/caretz", "peer", "reviewer"}}
	if got := reporter.resumeArgs("reviewer"); got != nil {
		t.Fatalf("resume args = %#v, want none", got)
	}
}

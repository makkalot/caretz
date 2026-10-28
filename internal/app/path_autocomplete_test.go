package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPathAutocompleteLoadsCandidatesInBackground(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "docs", "findme.md"), "x")
	agentsDir := filepath.Join(t.TempDir(), "agents")
	for _, name := range []string{"allowed", "blocked"} {
		writeTestFile(t, filepath.Join(agentsDir, name+".md"), "---\ndescription: test agent\nmode: subagent\n---\nbody")
	}

	completion := newPathAutocomplete(root, []string{agentsDir})
	completion.filterAgents(map[string]bool{"allowed": true})

	deadline := time.Now().Add(5 * time.Second)
	for completion.indexing() {
		if time.Now().After(deadline) {
			t.Fatal("path candidates were not loaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	completion.update("@findme", 0, len("@findme"))
	if len(completion.matches) != 1 || completion.matches[0].path != "docs/findme.md" {
		t.Fatalf("matches = %+v, want docs/findme.md", completion.matches)
	}
	var agents []string
	for _, candidate := range completion.candidates {
		if candidate.isAgent {
			agents = append(agents, candidate.path)
		}
	}
	if !slices.Equal(agents, []string{"allowed"}) {
		t.Fatalf("agents = %v, want [allowed]", agents)
	}
}

func TestWalkProjectFilesSkipsHiddenAndDependencyDirs(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{".cache/x", "node_modules/pkg/index.js", "src/.git/HEAD", "src/a.go", ".env"} {
		writeTestFile(t, filepath.Join(root, path), "x")
	}
	got := walkProjectFiles(root)
	slices.Sort(got)
	want := []string{".env", filepath.Join("src", "a.go")}
	if !slices.Equal(got, want) {
		t.Fatalf("walkProjectFiles = %v, want %v", got, want)
	}
}

func TestWalkProjectFilesStopsAtLimit(t *testing.T) {
	root := t.TempDir()
	for i := range 10 {
		writeTestFile(t, filepath.Join(root, fmt.Sprintf("f%d.txt", i)), "x")
	}
	previous := maxWalkedProjectFiles
	maxWalkedProjectFiles = 3
	t.Cleanup(func() { maxWalkedProjectFiles = previous })
	if got := walkProjectFiles(root); len(got) != 3 {
		t.Fatalf("walkProjectFiles returned %d files, want 3", len(got))
	}
}

func TestRankPathCandidatesShortQueryRequiresContiguousBasenameMatch(t *testing.T) {
	candidates := []pathCandidate{
		newPathCandidate("cpu.py", false),
		newPathCandidate("cpuio.py", false),
		newPathCandidate("venv/bin/Activate.ps1", false),
		newPathCandidate("venv/lib/python3.14t/site-packages/pip/", true),
		newPathCandidate("venv/lib/python3.14t/site-packages/pip/__init__.py", false),
	}

	matches := rankPathCandidates(candidates, "cp", 8)
	want := []string{"cpu.py", "cpuio.py"}
	if len(matches) != len(want) {
		t.Fatalf("matches = %v, want %v", matchPaths(matches), want)
	}
	for i := range want {
		if matches[i].path != want[i] {
			t.Fatalf("matches = %v, want %v", matchPaths(matches), want)
		}
	}
}

func TestRankPathCandidatesUsesBasenameUnlessQueryContainsPath(t *testing.T) {
	candidates := []pathCandidate{
		newPathCandidate("internal/app/completion.go", false),
		newPathCandidate("vendor/internalized.txt", false),
	}

	if matches := rankPathCandidates(candidates, "internal/app", 8); len(matches) != 1 || matches[0].path != "internal/app/completion.go" {
		t.Fatalf("path matches = %v", matchPaths(matches))
	}
	if matches := rankPathCandidates(candidates, "int", 8); len(matches) != 1 || matches[0].path != "vendor/internalized.txt" {
		t.Fatalf("basename matches = %v", matchPaths(matches))
	}
}

func TestRankLargePathCandidatesKeepsShortMatchesRelevantAndUnique(t *testing.T) {
	candidates := []pathCandidate{
		newPathCandidate("cpu.py", false),
		newPathCandidate("cpuio.py", false),
		newPathCandidate("venv/lib/site-packages/pip/__init__.py", false),
	}
	for i := 0; i <= largePathAutocompleteSet; i++ {
		candidates = append(candidates, newPathCandidate(fmt.Sprintf("vendor/file-%05d.txt", i), false))
	}

	matches := rankPathCandidates(candidates, "cp", 8)
	want := []string{"cpu.py", "cpuio.py"}
	if fmt.Sprint(matchPaths(matches)) != fmt.Sprint(want) {
		t.Fatalf("matches = %v, want %v", matchPaths(matches), want)
	}
}

func matchPaths(matches []pathMatch) []string {
	paths := make([]string, len(matches))
	for i, match := range matches {
		paths[i] = match.path
	}
	return paths
}

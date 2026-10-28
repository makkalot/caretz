package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSessionAccessFollowsResolvedLocation(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	local := filepath.Join(workspace, "state", "sessions")
	private := filepath.Join(external, "sessions")
	c := Config{SessionPaths: []string{local, private}, HiddenPaths: []string{local}, ReadOnlyPaths: []string{filepath.Join(workspace, "other")}}
	p := c.ForWorkspace(workspace)
	if !slices.Contains(p.ReadOnlyPaths, local) || !slices.Contains(p.ReadOnlyPaths, filepath.Join(workspace, ".caretz", "sessions")) || !slices.Contains(p.ReadOnlyPaths, filepath.Join(workspace, ".caretz", "agents")) || !slices.Contains(p.HiddenPaths, private) || !slices.Contains(p.HiddenPaths, local) {
		t.Fatalf("policy=%#v", p)
	}
	if len(c.HiddenPaths) != 1 {
		t.Fatal("base policy mutated")
	}
	alias := filepath.Join(workspace, "external-state")
	if err := os.Symlink(external, alias); err != nil {
		t.Fatal(err)
	}
	aliased := filepath.Join(alias, "sessions")
	p = (Config{SessionPaths: []string{aliased}}).ForWorkspace(workspace)
	if !slices.Contains(p.HiddenPaths, aliased) || slices.Contains(p.ReadOnlyPaths, aliased) {
		t.Fatalf("external alias became public: %#v", p)
	}
	p = c.ForWorkspace(external)
	if !slices.Contains(p.HiddenPaths, local) || !slices.Contains(p.ReadOnlyPaths, private) {
		t.Fatalf("workspace switch=%#v", p)
	}
}

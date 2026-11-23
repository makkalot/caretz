package subagents_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/makkalot/caretz/lib/subagents"
)

func TestParseAndLoadFromDirs(t *testing.T) {
	content := []byte("---\ndescription: Reviews diffs\nmode: peer\nmodel: test-model\npermission:\n  bash: deny\n---\nReview carefully.")
	fm, body, err := subagents.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Mode != subagents.ModePeer || fm.Model != "test-model" || fm.Permission["bash"] != "deny" || body != "Review carefully." {
		t.Errorf("Parse() = %+v, %q", fm, body)
	}
	if _, _, err := subagents.Parse([]byte("---\nmode: peer\n---\nNo description.")); err == nil {
		t.Error("Parse() accepted a definition without a description")
	}

	// A later directory overrides an earlier one.
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "reviewer.md"), []byte("---\ndescription: Home\nmode: subagent\n---\nHome prompt."), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "reviewer.md")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	def, err := subagents.LoadFromDirs([]string{home, workspace}, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if def.Path != path || !def.IsPeer() || def.Description != "Reviews diffs" {
		t.Errorf("LoadFromDirs() = %+v, want the workspace peer definition", def)
	}
}

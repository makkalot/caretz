package subagents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSubagent(t *testing.T) {
	fm, body, err := Parse([]byte(`---
description: Reviews code
mode: subagent
model: anthropic/claude-sonnet-4-20250514
reasoning: high
temperature: 0.1
permission:
  edit: deny
  bash: deny
---

You are in review mode.`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if fm.Description != "Reviews code" {
		t.Fatalf("Description = %q", fm.Description)
	}
	if fm.Model != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("Model = %q", fm.Model)
	}
	if fm.Reasoning != "high" {
		t.Fatalf("Reasoning = %q", fm.Reasoning)
	}
	if fm.Temperature == nil || *fm.Temperature != 0.1 {
		t.Fatalf("Temperature = %v", fm.Temperature)
	}
	if body != "You are in review mode." {
		t.Fatalf("body = %q", body)
	}
	disabled := DisabledTools(fm.Permission)
	for _, name := range []string{"Edit", "Write", "Bash"} {
		if !disabled[name] {
			t.Fatalf("expected %s to be disabled", name)
		}
	}
}

func TestParseSubagentValidation(t *testing.T) {
	tests := []string{
		`description: nope`,
		`---
mode: subagent
---
body`,
		`---
description: nope
mode: skill
---
body`,
		`---
description: nope
mode: subagent
---`,
		`---
description: nope
mode: subagent
temperature: warm
---
body`,
		`---
description: nope
mode: subagent
permission:
  - edit
---
body`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, _, err := Parse([]byte(input)); err == nil {
				t.Fatal("Parse() succeeded, want error")
			}
		})
	}
}

func TestParseSubagentQuotedScalarsAndPermissionMap(t *testing.T) {
	fm, body, err := Parse([]byte(`---
description: "Reviews code"
mode: 'subagent'
permission:
  edit: 'deny'
  bash: allow
---
Body.`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if fm.Description != "Reviews code" || fm.Mode != "subagent" {
		t.Fatalf("frontmatter = %+v", fm)
	}
	if fm.Permission["edit"] != "deny" || fm.Permission["bash"] != "allow" {
		t.Fatalf("permission map = %#v", fm.Permission)
	}
	if body != "Body." {
		t.Fatalf("body = %q", body)
	}
}

func TestDiscoverSubagents(t *testing.T) {
	cwd := t.TempDir()
	dir := AgentsDir(cwd)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(`---
description: Reviews code
mode: subagent
---
Review things.`), 0644); err != nil {
		t.Fatal(err)
	}
	defs, err := Discover(cwd)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "review" {
		t.Fatalf("Discover() = %+v", defs)
	}
}

func TestLoadFromDirsUsesLaterDirectoryAsOverride(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "root-agents")
	workspaceDir := filepath.Join(t.TempDir(), "workspace-agents")
	writeSubagent(t, rootDir, "review", "Root review prompt.")
	writeSubagent(t, workspaceDir, "review", "Workspace review prompt.")

	def, err := LoadFromDirs([]string{rootDir, workspaceDir}, "review")
	if err != nil {
		t.Fatalf("LoadFromDirs() error = %v", err)
	}
	if def.SystemPrompt != "Workspace review prompt." {
		t.Fatalf("SystemPrompt = %q, want workspace override", def.SystemPrompt)
	}
}

func TestDiscoverFromDirsMergesAndOverridesByName(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "root-agents")
	workspaceDir := filepath.Join(t.TempDir(), "workspace-agents")
	writeSubagent(t, rootDir, "root-only", "Root only prompt.")
	writeSubagent(t, rootDir, "review", "Root review prompt.")
	writeSubagent(t, workspaceDir, "review", "Workspace review prompt.")

	defs, err := DiscoverFromDirs([]string{rootDir, workspaceDir})
	if err != nil {
		t.Fatalf("DiscoverFromDirs() error = %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("DiscoverFromDirs() len = %d, want 2: %+v", len(defs), defs)
	}
	byName := map[string]Definition{}
	for _, def := range defs {
		byName[def.Name] = def
	}
	if byName["root-only"].SystemPrompt != "Root only prompt." {
		t.Fatalf("root-only prompt = %q", byName["root-only"].SystemPrompt)
	}
	if byName["review"].SystemPrompt != "Workspace review prompt." {
		t.Fatalf("review prompt = %q, want workspace override", byName["review"].SystemPrompt)
	}
}

func writeSubagent(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`---
description: Test subagent
mode: subagent
---
` + body)
	if err := os.WriteFile(filepath.Join(dir, name+".md"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

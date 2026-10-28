package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/lib/llm"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    SkillFrontmatter
		wantErr bool
	}{
		{
			name:    "valid frontmatter",
			input:   "---\nname: my-skill\ndescription: Does something useful\n---\n\n# My Skill\n...",
			want:    SkillFrontmatter{Name: "my-skill", Description: "Does something useful"},
			wantErr: false,
		},
		{
			name:    "missing name",
			input:   "---\ndescription: Does something\n---\n",
			want:    SkillFrontmatter{},
			wantErr: true,
		},
		{
			name:    "missing description",
			input:   "---\nname: my-skill\n---\n",
			want:    SkillFrontmatter{},
			wantErr: true,
		},
		{
			name:    "no frontmatter delimiters",
			input:   "# Just a markdown file\n\nNo frontmatter here.",
			want:    SkillFrontmatter{},
			wantErr: true,
		},
		{
			name:    "no closing delimiter",
			input:   "---\nname: my-skill\ndescription: test\n\n# Content without closing",
			want:    SkillFrontmatter{},
			wantErr: true,
		},
		{
			name:    "empty content",
			input:   "",
			want:    SkillFrontmatter{},
			wantErr: true,
		},
		{
			name:    "frontmatter with extra fields",
			input:   "---\nname: my-skill\ndescription: Does stuff\nextra: value\n---\n",
			want:    SkillFrontmatter{Name: "my-skill", Description: "Does stuff"},
			wantErr: false,
		},
		{
			name:    "quoted scalar values",
			input:   "---\nname: \"my-skill\"\ndescription: 'Does stuff'\n---\n",
			want:    SkillFrontmatter{Name: "my-skill", Description: "Does stuff"},
			wantErr: false,
		},
		{
			name:    "unsupported sequence",
			input:   "---\nname: my-skill\ndescription:\n  - nope\n---\n",
			want:    SkillFrontmatter{},
			wantErr: true,
		},
		{
			name:    "malformed frontmatter line",
			input:   "---\nname my-skill\ndescription: Does stuff\n---\n",
			want:    SkillFrontmatter{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFrontmatter([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFrontmatter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseFrontmatter() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDiscoverSkills(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, ".caretz", "skills")

	validSkillDir := filepath.Join(skillsDir, "valid-skill")
	if err := os.MkdirAll(validSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	err := os.WriteFile(filepath.Join(validSkillDir, "SKILL.md"), []byte("---\nname: valid-skill\ndescription: A valid skill\n---\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	anotherSkillDir := filepath.Join(skillsDir, "another-skill")
	if err := os.MkdirAll(anotherSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(anotherSkillDir, "SKILL.md"), []byte("---\nname: another-skill\ndescription: Another one\n---\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	badDir := filepath.Join(skillsDir, "bad-frontmatter")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("not yaml at all"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	noSkillDir := filepath.Join(skillsDir, "no-skillmd")
	if err := os.MkdirAll(noSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(noSkillDir, "README.md"), []byte("hello"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	mismatchDir := filepath.Join(skillsDir, "name-mismatch")
	if err := os.MkdirAll(mismatchDir, 0755); err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(mismatchDir, "SKILL.md"), []byte("---\nname: wrong-name\ndescription: Mismatched name\n---\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(skillsDir, "not-a-dir.txt"), []byte("I am a file"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	skills, err := DiscoverSkills([]string{skillsDir}, log.New())
	if err != nil {
		t.Fatalf("DiscoverSkills() error = %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("DiscoverSkills() returned %d skills, want 2", len(skills))
	}

	wantNames := map[string]string{
		"valid-skill":   "A valid skill",
		"another-skill": "Another one",
	}
	for _, s := range skills {
		desc, ok := wantNames[s.Name]
		if !ok {
			t.Errorf("unexpected skill name: %s", s.Name)
			continue
		}
		if s.Description != desc {
			t.Errorf("skill %s description = %q, want %q", s.Name, s.Description, desc)
		}
		expectedPath := filepath.Join(skillsDir, s.Name, "SKILL.md")
		if s.Location != expectedPath && !filepath.IsAbs(s.Location) {
			t.Errorf("skill %s location = %q, want %q", s.Name, s.Location, expectedPath)
		}
		delete(wantNames, s.Name)
	}
}

func TestDiscoverSkills_NoDir(t *testing.T) {
	tmpDir := t.TempDir()
	skills, err := DiscoverSkills([]string{filepath.Join(tmpDir, ".caretz", "skills")}, log.New())
	if err != nil {
		t.Fatalf("DiscoverSkills() error = %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("DiscoverSkills() returned %d skills, want 0", len(skills))
	}
}

func TestDiscoverSkills_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, ".caretz", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	skills, err := DiscoverSkills([]string{skillsDir}, log.New())
	if err != nil {
		t.Fatalf("DiscoverSkills() error = %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("DiscoverSkills() returned %d skills, want 0", len(skills))
	}
}

func TestDiscoverSkills_MergesDirsWithLaterOverride(t *testing.T) {
	tmpDir := t.TempDir()
	globalDir := filepath.Join(tmpDir, "home", ".config", "caretz", "skills")
	projectDir := filepath.Join(tmpDir, "project", ".caretz", "skills")

	writeSkill(t, globalDir, "global-only", "Global only")
	writeSkill(t, globalDir, "shared", "Global shared")
	writeSkill(t, projectDir, "project-only", "Project only")
	writeSkill(t, projectDir, "shared", "Project shared")

	skills, err := DiscoverSkills([]string{globalDir, projectDir}, log.New())
	if err != nil {
		t.Fatalf("DiscoverSkills() error = %v", err)
	}
	if len(skills) != 3 {
		t.Fatalf("DiscoverSkills() returned %d skills, want 3: %+v", len(skills), skills)
	}

	want := map[string]string{
		"global-only":  "Global only",
		"project-only": "Project only",
		"shared":       "Project shared",
	}
	var names []string
	for _, skill := range skills {
		names = append(names, skill.Name)
		if skill.Description != want[skill.Name] {
			t.Fatalf("skill %s description = %q, want %q", skill.Name, skill.Description, want[skill.Name])
		}
		delete(want, skill.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing skills: %v", want)
	}
	if got := strings.Join(names, ","); got != "global-only,project-only,shared" {
		t.Fatalf("skill order = %q, want sorted by name", got)
	}
	if !strings.HasPrefix(skills[2].Location, filepath.Join(projectDir, "shared")) {
		t.Fatalf("shared skill location = %q, want project override", skills[2].Location)
	}
}

func writeSkill(t *testing.T, skillsDir, name, description string) {
	t.Helper()
	skillDir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("---\nname: " + name + "\ndescription: " + description + "\n---\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAgentInstructions(t *testing.T) {
	t.Run("missing file returns empty content", func(t *testing.T) {
		got, err := LoadAgentInstructions(t.TempDir())
		if err != nil {
			t.Fatalf("LoadAgentInstructions() error = %v", err)
		}
		if got != "" {
			t.Fatalf("LoadAgentInstructions() = %q, want empty string", got)
		}
	})

	t.Run("existing file returns trimmed content", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("\n# Instructions\n\nFollow project style.\n\n"), 0644)
		if err != nil {
			t.Fatal(err)
		}

		got, err := LoadAgentInstructions(tmpDir)
		if err != nil {
			t.Fatalf("LoadAgentInstructions() error = %v", err)
		}
		want := "# Instructions\n\nFollow project style."
		if got != want {
			t.Fatalf("LoadAgentInstructions() = %q, want %q", got, want)
		}
	})

	t.Run("whitespace-only file returns empty content", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("\n\t \n"), 0644)
		if err != nil {
			t.Fatal(err)
		}

		got, err := LoadAgentInstructions(tmpDir)
		if err != nil {
			t.Fatalf("LoadAgentInstructions() error = %v", err)
		}
		if got != "" {
			t.Fatalf("LoadAgentInstructions() = %q, want empty string", got)
		}
	})
}

func TestBuildSystemPrompt(t *testing.T) {
	fileTools := []llm.ToolDefinition{
		{Name: "Read", Description: "Read file contents"},
		{Name: "Write", Description: "Create or overwrite files"},
		{Name: "Grep", Description: "Search file contents for patterns"},
		{Name: "Ls", Description: "List directory contents"},
	}
	fullTools := append([]llm.ToolDefinition{}, fileTools...)
	fullTools = append(fullTools,
		llm.ToolDefinition{Name: "Edit", Description: "Make precise file edits"},
		llm.ToolDefinition{Name: "Bash", Description: "Execute shell commands in a sandbox"},
		llm.ToolDefinition{Name: "Find", Description: "Find files by glob pattern"},
		llm.ToolDefinition{Name: "CallSubagent", Description: "Run a configured subagent"},
	)

	t.Run("custom prompt with no dynamic inputs stays small", func(t *testing.T) {
		result := BuildSystemPromptWithOptions(SystemPromptOptions{
			BasePrompt: "you're useful small agent",
		})
		if result != "you're useful small agent" {
			t.Fatalf("BuildSystemPromptWithOptions() = %q, want only custom prompt", result)
		}
		for _, sub := range []string{"Available Tools", "<available_skills>", "<project_instructions>", "Working directory:"} {
			if strings.Contains(result, sub) {
				t.Fatalf("minimal prompt should not contain %q:\n%s", sub, result)
			}
		}
	})

	t.Run("with skills and read tool", func(t *testing.T) {
		availableSkills := []Skill{
			{Name: "gmail", Description: "Read and send email", Location: "/path/to/gmail/SKILL.md"},
			{Name: "deploy", Description: "Deploy services", Location: "/path/to/deploy/SKILL.md"},
		}
		result := BuildSystemPromptWithOptions(SystemPromptOptions{
			Skills: availableSkills,
			CWD:    "/home/user/project",
			Tools:  fileTools,
		})
		if result == "" {
			t.Fatal("BuildSystemPrompt() returned empty string for non-empty skills")
		}
		mustContain := []string{
			BaseSystemPrompt,
			"Available Tools",
			"Read:",
			"Write:",
			"Grep:",
			"Ls:",
			"Guidelines",
			"Tool Constraints",
			"<available_skills>",
			"Use the Read tool to load a skill's SKILL.md file",
			"<name>gmail</name>",
			"<description>Read and send email</description>",
			"<location>/path/to/gmail/SKILL.md</location>",
			"<name>deploy</name>",
			"<description>Deploy services</description>",
			"<location>/path/to/deploy/SKILL.md</location>",
			"</available_skills>",
			"Working directory: /home/user/project",
		}
		for _, sub := range mustContain {
			if !strings.Contains(result, sub) {
				t.Errorf("BuildSystemPrompt() missing %q in output:\n%s", sub, result)
			}
		}
		for _, sub := range []string{"Edit:", "Bash:", "Find:", "CallSubagent:"} {
			if strings.Contains(result, sub) {
				t.Errorf("BuildSystemPrompt() should not mention unavailable tool %q", sub)
			}
		}
	})

	t.Run("empty skills and no tools returns base prompt without dynamic sections", func(t *testing.T) {
		result := BuildSystemPrompt([]Skill{}, "/home/user/project", "")
		if result == "" {
			t.Fatal("BuildSystemPrompt() returned empty string, want base prompt")
		}
		if strings.Contains(result, "<available_skills>") {
			t.Error("BuildSystemPrompt() should not contain skills catalog when no skills")
		}
		if !strings.Contains(result, BaseSystemPrompt) {
			t.Error("BuildSystemPrompt() should contain base prompt")
		}
		if strings.Contains(result, "Available Tools") || strings.Contains(result, "Tool Constraints") {
			t.Fatalf("BuildSystemPrompt() should not contain tool sections without tools:\n%s", result)
		}
		if !strings.Contains(result, "Working directory: /home/user/project") {
			t.Error("missing working directory")
		}
	})

	t.Run("nil skills returns base prompt without skills section", func(t *testing.T) {
		result := BuildSystemPrompt(nil, "/home/user/project", "")
		if result == "" {
			t.Fatal("BuildSystemPrompt() returned empty string, want base prompt")
		}
		if strings.Contains(result, "<available_skills>") {
			t.Error("BuildSystemPrompt() should not contain skills catalog when no skills")
		}
		if !strings.Contains(result, BaseSystemPrompt) {
			t.Error("BuildSystemPrompt() should contain base prompt")
		}
	})

	t.Run("cache-friendly ordering: dynamic content is last", func(t *testing.T) {
		availableSkills := []Skill{
			{Name: "test-skill", Description: "Test", Location: "/path/SKILL.md"},
		}
		result := BuildSystemPromptWithOptions(SystemPromptOptions{
			Skills:            availableSkills,
			CWD:               "/home/user/project",
			AgentInstructions: "Follow local rules.",
			Tools:             fullTools,
		})

		cwdIdx := strings.Index(result, "Working directory:")
		guidelinesIdx := strings.Index(result, "## Guidelines")
		toolsIdx := strings.Index(result, "## Available Tools")
		constraintsIdx := strings.Index(result, "## Tool Constraints")
		skillsIdx := strings.Index(result, "<available_skills>")
		agentsIdx := strings.Index(result, "<project_instructions>")

		if cwdIdx < 0 {
			t.Fatal("missing 'Working directory:' in prompt")
		}
		if guidelinesIdx < 0 || toolsIdx < 0 || constraintsIdx < 0 || skillsIdx < 0 || agentsIdx < 0 {
			t.Fatal("missing expected sections")
		}

		if cwdIdx < guidelinesIdx {
			t.Error("Working directory should appear after Guidelines")
		}
		if cwdIdx < toolsIdx {
			t.Error("Working directory should appear after Available Tools")
		}
		if cwdIdx < constraintsIdx {
			t.Error("Working directory should appear after Tool Constraints")
		}
		if cwdIdx < skillsIdx {
			t.Error("Working directory should appear after skills")
		}
		if agentsIdx < skillsIdx {
			t.Error("AGENTS.md instructions should appear after skills")
		}
		if cwdIdx < agentsIdx {
			t.Error("Working directory should appear after AGENTS.md instructions")
		}
	})

	t.Run("with agent instructions", func(t *testing.T) {
		result := BuildSystemPromptWithOptions(SystemPromptOptions{
			CWD:               "/home/user/project",
			AgentInstructions: "\nUse tabs for indentation.\n",
		})
		wantBlock := "<project_instructions>\nUse tabs for indentation.\n</project_instructions>"
		mustContain := []string{
			"Project instructions from AGENTS.md:",
			wantBlock,
			"Working directory: /home/user/project",
		}
		for _, sub := range mustContain {
			if !strings.Contains(result, sub) {
				t.Errorf("BuildSystemPrompt() missing %q in output:\n%s", sub, result)
			}
		}
	})

	t.Run("tool constraints mention limits", func(t *testing.T) {
		result := BuildSystemPromptWithOptions(SystemPromptOptions{
			CWD:   "/home/user/project",
			Tools: fullTools,
		})
		mustContain := []string{
			"200 lines",
			"20KB",
			"120 seconds",
			"100 matches",
			"1000",
			"500",
		}
		for _, sub := range mustContain {
			if !strings.Contains(result, sub) {
				t.Errorf("tool constraints missing %q", sub)
			}
		}
	})
}

func TestDefaultSystemPromptRequiresTLDRForQuestions(t *testing.T) {
	result := BuildSystemPromptWithOptions(SystemPromptOptions{})
	for _, want := range []string{
		"When the user asks a question",
		"end your response",
		"## TL;DR",
		"can be understood on its own",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("default system prompt missing %q:\n%s", want, result)
		}
	}

	custom := BuildSystemPromptWithOptions(SystemPromptOptions{BasePrompt: "custom prompt"})
	if strings.Contains(custom, "## TL;DR") {
		t.Fatalf("custom system prompt should not inherit default TL;DR guidance:\n%s", custom)
	}
}

func TestSystemPromptEfficiencyGuidelines(t *testing.T) {
	result := BuildSystemPromptWithOptions(SystemPromptOptions{
		CWD: "/home/user/project",
		Tools: []llm.ToolDefinition{
			{Name: "Read", Description: "Read file contents"},
			{Name: "Edit", Description: "Make precise file edits"},
			{Name: "Write", Description: "Create or overwrite files"},
			{Name: "Grep", Description: "Search file contents for patterns"},
			{Name: "Find", Description: "Find files by glob pattern"},
			{Name: "Ls", Description: "List directory contents"},
		},
	})
	mustContain := []string{
		"Guidelines",
		"Edit for precise changes",
		"old_text as small as possible",
		"Write only for new files",
		"Prefer Grep/Find/Ls",
		"parallel tool",
		"offset",
		"limit",
	}
	mustNotContain := []string{
		"start_line",
		"end_line",
	}
	for _, sub := range mustContain {
		if !strings.Contains(result, sub) {
			t.Errorf("BuildSystemPrompt() missing %q", sub)
		}
	}
	for _, sub := range mustNotContain {
		if strings.Contains(result, sub) {
			t.Errorf("BuildSystemPrompt() should not contain %q", sub)
		}
	}
}

func TestSystemPromptUnsandboxedBashConstraint(t *testing.T) {
	result := BuildSystemPromptWithOptions(SystemPromptOptions{
		CWD: "/home/user/project",
		Tools: []llm.ToolDefinition{{
			Name:        "Bash",
			Description: "Execute a shell command without CARETZ's internal sandbox.",
		}},
	})
	if !strings.Contains(result, "**Bash** runs without CARETZ's internal sandbox") {
		t.Fatalf("system prompt missing unsandboxed Bash constraint:\n%s", result)
	}
	if strings.Contains(result, "**Bash** runs in a sandbox") {
		t.Fatalf("system prompt still says Bash is sandboxed:\n%s", result)
	}
}

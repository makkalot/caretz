package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/internal/skills"
	"github.com/makkalot/caretz/internal/subagents"
)

// Parse the skill/subagent/AGENTS.md examples from the docs with the real
// parsers, so a doc example that the runtime would reject is caught here.
func main() {
	dir := os.Args[1]
	bad := 0

	// Optional subagent frontmatter block: wrap it into a real agent file and
	// confirm the runtime accepts every documented field.
	yamls, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	for _, f := range yamls {
		data, _ := os.ReadFile(f)
		content := "---\n" + strings.TrimSpace(string(data)) + "\nmode: subagent\ndescription: doc example\n---\n\nBody text for the agent.\n"
		fm, body, err := subagents.Parse([]byte(content))
		if err != nil {
			fmt.Printf("  FAIL  %-40s optional subagent frontmatter: %v\n", filepath.Base(f), err)
			bad++
			continue
		}
		denied := keys(subagents.DisabledTools(fm.Permission))
		need := map[string]bool{}
		for _, d := range denied {
			need[d] = true
		}
		missing := []string{}
		for _, want := range []string{"Edit", "Write", "Bash"} {
			if !need[want] {
				missing = append(missing, want)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("  FAIL  %-40s permission deny map did not disable %v (got %v)\n", filepath.Base(f), missing, denied)
			bad++
			continue
		}
		if fm.Model != "local/qwen" || fm.Reasoning != "high" || fm.Temperature == nil || *fm.Temperature != 0.2 {
			fmt.Printf("  FAIL  %-40s parsed model=%q reasoning=%q temperature=%v\n", filepath.Base(f), fm.Model, fm.Reasoning, fm.Temperature)
			bad++
			continue
		}
		fmt.Printf("  PASS  %-40s model/reasoning/temperature parsed; permission denies %v\n", filepath.Base(f), denied)
		_ = body
	}

	skillFiles, _ := filepath.Glob(filepath.Join(dir, "*.markdown"))
	for _, f := range skillFiles {
		base := filepath.Base(f)
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("  ERROR %-40s %v\n", base, err)
			bad++
			continue
		}
		switch {
		case strings.Contains(base, "customization_skills__"):
			fm, err := skills.ParseFrontmatter(data)
			if err != nil {
				fmt.Printf("  FAIL  %-40s skill frontmatter: %v\n", base, err)
				bad++
				continue
			}
			fmt.Printf("  PASS  %-40s skill name=%q desc=%q\n", base, fm.Name, truncate(fm.Description))
		case strings.Contains(base, "customization_subagents__"):
			fm, body, err := subagents.Parse(data)
			if err != nil {
				fmt.Printf("  FAIL  %-40s subagent frontmatter: %v\n", base, err)
				bad++
				continue
			}
			if strings.TrimSpace(body) == "" {
				fmt.Printf("  FAIL  %-40s subagent body is empty\n", base)
				bad++
				continue
			}
			if fm.Mode != "subagent" {
				fmt.Printf("  FAIL  %-40s subagent mode=%q\n", base, fm.Mode)
				bad++
				continue
			}
			fmt.Printf("  PASS  %-40s subagent desc=%q mode=%q denied=%v\n", base,
				truncate(fm.Description), fm.Mode, keys(subagents.DisabledTools(fm.Permission)))
		default:
			// AGENTS.md-style project instructions: must load into the prompt.
			tmp, _ := os.MkdirTemp("", "doccheck-agents")
			if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), data, 0o600); err != nil {
				fmt.Printf("  ERROR %-40s %v\n", base, err)
				bad++
				continue
			}
			got, err := skills.LoadAgentInstructions(tmp)
			if err != nil {
				fmt.Printf("  FAIL  %-40s AGENTS.md load: %v\n", base, err)
				bad++
				continue
			}
			if !strings.Contains(got, "public wire format") {
				fmt.Printf("  FAIL  %-40s AGENTS.md content not surfaced (%q)\n", base, truncate(got))
				bad++
				continue
			}
			prompt := skills.BuildSystemPromptWithOptions(skills.SystemPromptOptions{
				Skills:            nil,
				CWD:               tmp,
				AgentInstructions: got,
			})
			if !strings.Contains(prompt, "public wire format") {
				fmt.Printf("  FAIL  %-40s AGENTS.md text missing from built system prompt\n", base)
				bad++
				continue
			}
			fmt.Printf("  PASS  %-40s project instructions reach the system prompt\n", base)
		}
	}

	// Skill layout requirement: name must match the containing directory.
	for _, f := range skillFiles {
		if !strings.Contains(filepath.Base(f), "customization_skills__") {
			continue
		}
		data, _ := os.ReadFile(f)
		fm, err := skills.ParseFrontmatter(data)
		if err != nil {
			continue
		}
		root, _ := os.MkdirTemp("", "doccheck-skill")
		dirPath := filepath.Join(root, "skills", fm.Name)
		_ = os.MkdirAll(dirPath, 0o755)
		_ = os.WriteFile(filepath.Join(dirPath, "SKILL.md"), data, 0o600)
		_ = os.WriteFile(filepath.Join(dirPath, "checklist.md"), []byte("- [ ] smoke test\n"), 0o600)
		got, err := skills.DiscoverSkillsFromDir(filepath.Join(root, "skills"), nil)
		if err != nil || len(got) != 1 {
			fmt.Printf("  FAIL  %-40s documented skill layout not discovered: n=%d err=%v\n",
				filepath.Base(f), len(got), err)
			bad++
			continue
		}
		// Docs claim the catalog exposes name, description, and SKILL.md path.
		prompt := skills.BuildSystemPromptWithOptions(skills.SystemPromptOptions{
			Skills: got, CWD: root,
		})
		missing := []string{}
		for _, want := range []string{got[0].Name, got[0].Description} {
			if !strings.Contains(prompt, want) {
				missing = append(missing, want)
			}
		}
		if !strings.Contains(prompt, "SKILL.md") {
			missing = append(missing, "SKILL.md")
		}
		if len(missing) > 0 {
			fmt.Printf("  FAIL  %-40s skill catalog missing %v\n", filepath.Base(f), missing)
			bad++
			continue
		}
		fmt.Printf("  PASS  %-40s discovers as skill %q; catalog exposes name+description+SKILL.md path\n",
			filepath.Base(f), got[0].Name)
	}

	fmt.Printf("\n%d failing markdown resource example(s)\n", bad)
	if bad > 0 {
		os.Exit(1)
	}
}

func truncate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

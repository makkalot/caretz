package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/makkalot/caretz/internal/frontmatter"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/lib/llm"
)

type SkillFrontmatter struct {
	Name        string
	Description string
}

type Skill struct {
	Name        string
	Description string
	Location    string
}

const BaseSystemPrompt = `You are a helpful coding assistant. You are eager to help humans with their
tasks — especially the boring, repetitive ones that nobody wants to do.
Every task is an opportunity, and you approach each one with enthusiasm.

## How You Work

- Be concise. Short, correct answers over long explanations.
- When the user asks a question, end your response with a concise ## TL;DR
  section that summarizes the answer and can be understood on its own.
- Follow existing project conventions and style when writing code.
- Read files before editing them to understand context.
- When unsure, say so. Do not guess.
- Before running destructive actions (deleting files, dropping databases,
  stopping services), confirm with the user.
- When given a tedious task, dive right in. That's what you're here for.`

const GeneralSystemPrompt = `You are a helpful assistant. Work carefully, communicate clearly, and use the available tools when they help complete the user's request.

Follow applicable instructions, inspect relevant context before acting, and be honest about uncertainty. Confirm before destructive actions.

When the user asks a question, end your response with a concise ## TL;DR section that summarizes the answer and can be understood on its own.`

const CompactSystemPrompt = `You are a helpful assistant. Work carefully and use the available tools when they help complete the user's request.

Follow applicable instructions, inspect relevant context before acting, and be honest about uncertainty. Confirm before destructive actions.

Omit unnecessary details from your responses. When the user asks a question, make a concise ## TL;DR section the main answer. Include additional detail only when needed to answer accurately or complete the task.`

const OpsSystemPrompt = `You are an experienced operations assistant helping maintain this machine: installing and removing software, cleaning up packages and disk space, and diagnosing broken services. Work carefully, communicate clearly, and use the available tools when they help.

- Establish the current state before changing anything: versions, service status, logs, and disk usage.
- Prefer the smallest reversible change that addresses the root cause, and prefer the package manager that already owns the software.
- Before destructive, irreversible, security-sensitive, or availability-impacting actions, state the exact target and the risk, and confirm with the user unless they already authorized it.
- Commands run in a sandbox. When one is blocked, report what was blocked; do not try to work around the sandbox, and do not retry the same blocked command repeatedly.
- For root-owned files and services, use Bash with sudo (for example "sudo tee FILE" or "sudo systemctl restart NAME"). The Read, Write, and Edit tools run as the user and cannot change root-owned files.
- Do not hide a command's exit status or its errors; report failures as they happened.
- On Linux, a host socket the sandbox hides, such as Docker's or D-Bus's, shows up as a character device owned by nobody. That is the sandbox, not a change on the host. Report it as blocked rather than proposing to fix its ownership.
- A result that starts with "[The user approved running this command outside the sandbox" ran on the host. Later commands run sandboxed again, so the same access may be blocked.
- Never expose credentials or secrets.
- Verify the result, then report what changed, what was verified, and how to roll it back.

When the user asks a question, end your response with a concise ## TL;DR section that summarizes the answer and can be understood on its own.`

type SystemPromptOptions struct {
	BasePrompt        string
	Skills            []Skill
	CWD               string
	AgentInstructions string
	Tools             []llm.ToolDefinition
}

func ParseFrontmatter(content []byte) (SkillFrontmatter, error) {
	parsed, err := frontmatter.Parse(content)
	if err != nil {
		return SkillFrontmatter{}, err
	}
	fm := SkillFrontmatter{
		Name:        strings.TrimSpace(parsed.Fields["name"]),
		Description: strings.TrimSpace(parsed.Fields["description"]),
	}
	if fm.Name == "" {
		return SkillFrontmatter{}, fmt.Errorf("frontmatter missing required field: name")
	}
	if fm.Description == "" {
		return SkillFrontmatter{}, fmt.Errorf("frontmatter missing required field: description")
	}
	return fm, nil
}

func DiscoverSkills(skillDirs []string, logger *log.Logger) ([]Skill, error) {
	merged := map[string]Skill{}
	for _, dir := range skillDirs {
		discovered, err := DiscoverSkillsFromDir(dir, logger)
		if err != nil {
			return nil, err
		}
		for _, skill := range discovered {
			merged[skill.Name] = skill
		}
	}

	skills := make([]Skill, 0, len(merged))
	for _, skill := range merged {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

func DiscoverSkillsFromDir(skillsDir string, logger *log.Logger) ([]Skill, error) {
	if strings.TrimSpace(skillsDir) == "" {
		return nil, nil
	}
	info, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading skills directory: %w", err)
	}
	if !info.IsDir() {
		return nil, nil
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("reading skills directory: %w", err)
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		fm, err := ParseFrontmatter(data)
		if err != nil {
			logger.Info("Skipping skill %s: %v", entry.Name(), err)
			continue
		}
		if fm.Name != entry.Name() {
			logger.Info("Skipping skill %s: frontmatter name %q does not match directory name", entry.Name(), fm.Name)
			continue
		}
		absPath, _ := filepath.Abs(skillPath)
		skills = append(skills, Skill{
			Name:        fm.Name,
			Description: fm.Description,
			Location:    absPath,
		})
	}
	return skills, nil
}

func LoadAgentInstructions(cwd string) (string, error) {
	data, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading AGENTS.md: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func BuildSystemPrompt(skills []Skill, cwd string, agentInstructions string) string {
	return BuildSystemPromptWithOptions(SystemPromptOptions{
		BasePrompt:        BaseSystemPrompt,
		Skills:            skills,
		CWD:               cwd,
		AgentInstructions: agentInstructions,
	})
}

func BuildSystemPromptWithOptions(opts SystemPromptOptions) string {
	var b strings.Builder
	basePrompt := strings.TrimSpace(opts.BasePrompt)
	if basePrompt == "" {
		basePrompt = BaseSystemPrompt
	}
	b.WriteString(basePrompt)

	if len(opts.Skills) > 0 {
		b.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\n")
		if hasPromptTool(opts.Tools, "Read") {
			b.WriteString("Use the Read tool to load a skill's SKILL.md file when the task matches its description.\n\n")
		} else {
			b.WriteString("Use this catalog for routing context; this session cannot load skill files unless Read is available.\n\n")
		}
		b.WriteString("<available_skills>\n")
		for _, s := range opts.Skills {
			b.WriteString("  <skill>\n")
			_, _ = fmt.Fprintf(&b, "    <name>%s</name>\n", s.Name)
			_, _ = fmt.Fprintf(&b, "    <description>%s</description>\n", s.Description)
			_, _ = fmt.Fprintf(&b, "    <location>%s</location>\n", s.Location)
			b.WriteString("  </skill>\n")
		}
		b.WriteString("</available_skills>\n")
	}

	agentInstructions := strings.TrimSpace(opts.AgentInstructions)
	if agentInstructions != "" {
		b.WriteString("\n\nProject instructions from AGENTS.md:\n\n")
		b.WriteString("<project_instructions>\n")
		b.WriteString(agentInstructions)
		b.WriteString("\n</project_instructions>\n")
	}

	appendToolPromptSections(&b, opts.Tools)

	if strings.TrimSpace(opts.CWD) != "" {
		_, _ = fmt.Fprintf(&b, "\n\nWorking directory: %s", opts.CWD)
	}

	return b.String()
}

func appendToolPromptSections(b *strings.Builder, defs []llm.ToolDefinition) {
	if len(defs) == 0 {
		return
	}

	b.WriteString("\n\n## Available Tools\n\n")
	for _, def := range defs {
		description := strings.TrimSpace(def.Description)
		if description == "" {
			_, _ = fmt.Fprintf(b, "- %s\n", def.Name)
			continue
		}
		_, _ = fmt.Fprintf(b, "- %s: %s\n", def.Name, description)
	}

	guidelines := toolGuidelines(defs)
	if len(guidelines) > 0 {
		b.WriteString("\n## Guidelines\n\n")
		for _, guideline := range guidelines {
			_, _ = fmt.Fprintf(b, "- %s\n", guideline)
		}
	}

	constraints := toolConstraints(defs)
	if len(constraints) > 0 {
		b.WriteString("\n## Tool Constraints\n\n")
		for _, constraint := range constraints {
			b.WriteString(constraint)
			b.WriteString("\n\n")
		}
	}
}

func toolGuidelines(defs []llm.ToolDefinition) []string {
	var guidelines []string
	if hasPromptTool(defs, "Edit") {
		guidelines = append(guidelines,
			"Use Edit for precise changes (edits[].old_text must match exactly).",
			"When changing multiple separate locations in one file, use one Edit call with multiple entries in edits[] instead of multiple Edit calls.",
			"Each edits[].old_text is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits.",
			"Keep edits[].old_text as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
		)
	}
	if hasPromptTool(defs, "Write") {
		guidelines = append(guidelines, "Use Write only for new files or complete rewrites.")
	}
	if hasPromptTool(defs, "Grep") || hasPromptTool(defs, "Find") || hasPromptTool(defs, "Ls") {
		guidelines = append(guidelines, "Prefer Grep/Find/Ls tools over Bash for file exploration when those tools are available.")
	}
	if hasPromptTool(defs, "Read") {
		guidelines = append(guidelines, "Use Read to examine files instead of shelling out to cat or sed.")
	}
	if hasPromptTool(defs, "CallSubagent") {
		guidelines = append(guidelines, "If a loaded skill says to call @name or use @name, treat name as a configured subagent name and use CallSubagent with the complete delegated task.")
	}
	if len(defs) > 1 {
		guidelines = append(guidelines, "You can make multiple parallel tool calls in a single response when the calls are independent.")
	}
	return guidelines
}

func toolConstraints(defs []llm.ToolDefinition) []string {
	constraintByName := map[string]string{
		"Read":       "**Read** returns at most 200 lines and 20KB at a time. Use offset/limit for large files.",
		"Edit":       "**Edit** rejects paths outside the current working directory and paths containing symlinks.",
		"Write":      "**Write** rejects paths outside the current working directory and paths containing symlinks. Creates parent directories automatically.",
		"Bash":       "**Bash** runs in a sandbox. Commands time out after 120 seconds by default; set timeout_seconds to request more time, capped at 600 seconds. Output is capped at 1MB. File writes are restricted to the current working directory. On Linux, network access is denied by default; configured HTTP/HTTPS hosts may be reached through the sandbox proxy.",
		"Grep":       "**Grep** returns at most 100 matches and 50KB. Each line is truncated to 500 chars.",
		"Find":       "**Find** returns at most 1000 results and 50KB.",
		"Ls":         "**Ls** returns at most 500 entries and 50KB.",
		"SendFile":   "**SendFile** can only send files from readable workspace roots.",
		"LocalImage": "**LocalImage** analyzes local image files from readable workspace roots.",
	}
	var constraints []string
	for _, def := range defs {
		if constraint, ok := constraintByName[def.Name]; ok {
			if def.Name == "Bash" && strings.Contains(def.Description, "without CARETZ's internal sandbox") {
				constraint = "**Bash** runs without CARETZ's internal sandbox. Commands time out after 120 seconds by default; set timeout_seconds to request more time, capped at 600 seconds. Output is capped at 1MB. Commands run with the process permissions available inside this environment."
			}
			constraints = append(constraints, constraint)
		}
	}
	return constraints
}

func hasPromptTool(defs []llm.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

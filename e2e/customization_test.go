//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var customization = story.NewFeature("Teaching caretz my project",
	"As a developer\n"+
		"I want to give caretz my project's rules, playbooks, and specialist agents\n"+
		"So that it works the way my team does without me repeating myself")

const releaseSkill = "---\n" +
	"name: release-check\n" +
	"description: Verify a release candidate before tagging it.\n" +
	"---\n" +
	"# Release check\n" +
	"1. Run the tests.\n" +
	"2. Report failures with the command and output.\n"

func TestCustomizationProjectRules(t *testing.T) {
	s := customization.Scenario(t, "caretz follows the rules in my project's AGENTS.md")
	s.Given().
		File("AGENTS.md", "- Run `go test ./...` after Go changes.\n").
		Model(story.Replies("Understood"))
	s.When().Ask("fix the bug")
	s.Then().
		Succeeds().
		Instructions(story.Contains("Run `go test ./...` after Go changes."))
}

func TestCustomizationOwnSystemPromptKeepsRules(t *testing.T) {
	s := customization.Scenario(t, "my own system prompt still keeps the project's rules")
	s.Given().
		File("AGENTS.md", "- Ask before changing a public API.\n").
		Model(story.Replies("Noted"))
	s.When().Ask("fix the bug", "--system-prompt", "You are a terse senior reviewer.")
	s.Then().
		Succeeds().
		Instructions(story.Contains("You are a terse senior reviewer.")).
		Instructions(story.Contains("Ask before changing a public API."))
}

func TestCustomizationSkillOffered(t *testing.T) {
	s := customization.Scenario(t, "caretz knows about the skills in my project")
	s.Given().
		File(".caretz/skills/release-check/SKILL.md", releaseSkill).
		Model(story.Replies("I can run a release check"))
	s.When().Ask("what can you do?")
	s.Then().
		Succeeds().
		Instructions(story.Contains("release-check")).
		Instructions(story.Contains("Verify a release candidate before tagging it."))
}

func TestCustomizationSkillLoaded(t *testing.T) {
	s := customization.Scenario(t, "caretz follows a skill when the task needs it")
	s.Given().
		File(".caretz/skills/release-check/SKILL.md", releaseSkill).
		Model(story.Reads(".caretz/skills/release-check/SKILL.md")).
		Model(story.Replies("Running the release check"))
	s.When().Ask("check the release candidate")
	s.Then().
		Succeeds().
		Saw(story.Contains("1. Run the tests."))
}

func TestCustomizationPersonalSkill(t *testing.T) {
	s := customization.Scenario(t, "my personal skills are available in every project")
	s.Given().
		HomeFile(".config/caretz/skills/release-check/SKILL.md",
			"---\nname: release-check\ndescription: My personal release routine.\n---\nPersonal steps.\n").
		Model(story.Replies("I can run your release routine"))
	s.When().Ask("what can you do?")
	s.Then().
		Succeeds().
		Instructions(story.Contains("My personal release routine."))
}

func TestCustomizationProjectSkillOverridesPersonal(t *testing.T) {
	s := customization.Scenario(t, "a project skill replaces my personal skill with the same name")
	s.Given().
		HomeFile(".config/caretz/skills/release-check/SKILL.md",
			"---\nname: release-check\ndescription: My personal release routine.\n---\nPersonal steps.\n").
		File(".caretz/skills/release-check/SKILL.md", releaseSkill).
		Model(story.Replies("I will use the project's release check"))
	s.When().Ask("what can you do?")
	s.Then().
		Succeeds().
		Instructions(story.Contains("Verify a release candidate before tagging it.")).
		Instructions(story.Not(story.Contains("My personal release routine.")))
}

func TestCustomizationClaudeSkills(t *testing.T) {
	s := customization.Scenario(t, "skills I already keep for Claude work without copying")
	s.Given().
		File(".claude/skills/release-check/SKILL.md", releaseSkill).
		Model(story.Replies("I can run a release check"))
	s.When().Ask("what can you do?")
	s.Then().
		Succeeds().
		Instructions(story.Contains("Verify a release candidate before tagging it."))
}

func TestCustomizationSubagent(t *testing.T) {
	s := customization.Scenario(t, "caretz hands a focused job to a specialist agent")
	s.Given().
		File(".caretz/agents/reviewer.md",
			"---\ndescription: Reviews changes for bugs.\nmode: subagent\n---\nReview the change. Do not edit files.\n").
		Model(story.Delegates("reviewer", "review my latest change")).
		Model(story.Replies("No bugs found").As("reviewer")).
		Model(story.Replies("The reviewer found no bugs"))
	s.When().Ask("get my change reviewed")
	s.Then().
		Succeeds().
		Stdout(story.Contains("The reviewer found no bugs")).
		Saw(story.Contains("No bugs found"))
}

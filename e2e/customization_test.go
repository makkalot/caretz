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

const readOnlySearcher = "---\n" +
	"description: Searches the code.\n" +
	"mode: subagent\n" +
	"permission:\n" +
	"  edit: deny\n" +
	"  bash: deny\n" +
	"---\n" +
	"Search the code and report what you find.\n"

func TestCustomizationBackgroundSubagentFinishesBeforeExit(t *testing.T) {
	s := customization.Scenario(t, "a one-shot run waits for a specialist agent working in the background")
	s.Given().
		File(".caretz/agents/searcher.md", readOnlySearcher).
		Model(story.Calls("CallSubagent", `{"agent":"searcher","task":"find the callers","background":true}`)).
		// The parent and the child now run at once, so their replies can come in
		// either order; they are the same text for that reason.
		Model(story.Replies("Working on it")).
		Model(story.Replies("Working on it").As("searcher")).
		Model(story.Replies("The searcher found the callers"))
	s.When().Ask("find the callers")
	s.Then().
		Succeeds().
		RequestCount(4).
		Stdout(story.Contains("The searcher found the callers")).
		Stderr(story.Contains("waiting for 1 background subagent(s) to finish"))
	s.Then().LastRequest().User(story.Contains(`Background subagent "searcher" completed.`))
}

func TestCustomizationSubagentResumesAfterCaretzIsKilled(t *testing.T) {
	s := customization.Scenario(t, "a specialist agent cut short by a crash picks up where it stopped")
	s.Given().
		File(".caretz/agents/searcher.md", readOnlySearcher).
		Model(story.Delegates("searcher", "find the callers")).
		Model(story.StillWorking().As("searcher")).
		Model(story.Calls("ListSubagents", `{}`).ID("list")).
		Model(story.ResumesRun("searcher")).
		Model(story.Replies("Found 3 callers").As("searcher")).
		Model(story.Replies("The searcher found 3 callers"))
	s.When().
		AskAndKill("find the callers").
		Repair().
		Continue("finish finding the callers")
	s.Then().
		Succeeds().
		RequestCount(6).
		Stdout(story.Contains("The searcher found 3 callers")).
		Saw(story.Contains("Found 3 callers"))
	s.Then().LastRequest().
		ToolResult("list", story.Contains("searcher: interrupted (process_exit)")).
		ToolResult("list", story.Contains("resumable: yes"))
	s.Then().Request(5).
		User(story.Contains("Your earlier run ended as: interrupted")).
		User(story.Contains("Original task:\nfind the callers"))
}

func TestCustomizationBackgroundSubagentCanChangeFiles(t *testing.T) {
	s := customization.Scenario(t, "a specialist agent that can change files does so in the background")
	s.Given().
		File(".caretz/agents/fixer.md",
			"---\ndescription: Fixes bugs.\nmode: subagent\n---\nFix the bug.\n").
		Model(story.Calls("CallSubagent", `{"agent":"fixer","task":"fix it","background":true}`)).
		// The parent and the child run at once, so their first replies can come
		// in either order; they are the same text for that reason.
		Model(story.Replies("Working on it")).
		Model(story.Writes("fixed.txt", "fixed").As("fixer")).
		Model(story.Replies("Working on it").As("fixer")).
		Model(story.Replies("The fixer fixed it"))
	s.When().Ask("fix it in the background")
	s.Then().
		Succeeds().
		File("fixed.txt", story.Contains("fixed")).
		Stdout(story.Contains("The fixer fixed it"))
	s.Then().LastRequest().User(story.Contains(`Background subagent "fixer" completed.`))
}

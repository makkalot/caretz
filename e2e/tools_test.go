//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var tools = story.NewFeature("Choosing caretz's tools",
	"As a developer\n"+
		"I want to choose which tools caretz may use for a task\n"+
		"So that a review or an explanation cannot change or run anything")

func TestToolsOnlyListed(t *testing.T) {
	s := tools.Scenario(t, "I can limit caretz to the tools I list")
	s.Given().Model(story.Replies("I will only read"))
	s.When().Ask("explain the request flow", "--tools", "Read,Grep")
	s.Then().
		Succeeds().
		CanUse("Read").
		CanUse("Grep").
		CannotUse("Bash").
		CannotUse("Edit")
}

func TestToolsDisableOne(t *testing.T) {
	s := tools.Scenario(t, "I can take one tool away")
	s.Given().Model(story.Replies("I will review without running anything"))
	s.When().Ask("review this diff", "--disable-tool", "Bash")
	s.Then().
		Succeeds().
		CannotUse("Bash").
		CanUse("Read")
}

func TestToolsCompactProfile(t *testing.T) {
	s := tools.Scenario(t, "a small model can work with just the shell")
	s.Describe("The compact profile keeps the tool list short for tiny models.")
	s.Given().Model(story.Replies("I have the shell"))
	s.When().Ask("list the files", "--profile", "compact")
	s.Then().
		Succeeds().
		CanUse("Bash").
		CannotUse("Read").
		CannotUse("CallSubagent")
}

//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var network = story.NewFeature("Network access",
	"As a developer\n"+
		"I want caretz's commands to reach only the websites I allow\n"+
		"So that a task cannot send my code or data anywhere I did not expect")

func TestNetworkBlockedByDefault(t *testing.T) {
	s := network.Scenario(t, "commands cannot reach the internet unless I allow it")
	s.Given().
		Website("docs.test", "the docs").
		Model(story.RunsCommand("curl -s http://docs.test/")).
		Model(story.Replies("I could not reach the docs"))
	s.When().Ask("read the docs")
	s.Then().
		Succeeds().
		Saw(story.Not(story.Contains("the docs"))).
		NotVisited("docs.test")
}

func TestNetworkAssistantToldWhatWasBlocked(t *testing.T) {
	s := network.Scenario(t, "the assistant is told which website was blocked")
	s.Describe("In a run that cannot ask me, the assistant still sees what the\n" +
		"command printed, so it can explain what it needs.")
	s.Given().
		Website("docs.test", "the docs").
		Website("tracker.test", "tracked you").
		Model(story.RunsCommand("curl -s http://docs.test/; curl -s http://tracker.test/")).
		Model(story.Replies("I read the docs; tracker.test is not allowed"))
	s.When().Ask("read the docs", "--network-allow", "docs.test")
	s.Then().
		Succeeds().
		Saw(story.Contains("the docs")).
		Saw(story.Contains("blocked network access to tracker.test")).
		Saw(story.Not(story.Contains("tracked you"))).
		NotVisited("tracker.test")
}

func TestNetworkAllowWebsite(t *testing.T) {
	s := network.Scenario(t, "I can allow a website a task needs")
	s.Given().
		Website("docs.test", "the docs").
		Model(story.RunsCommand("curl -s http://docs.test/")).
		Model(story.Replies("I read the docs"))
	s.When().Ask("read the docs", "--network-allow", "docs.test")
	s.Then().
		Succeeds().
		Saw(story.Contains("the docs")).
		Visited("docs.test")
}

func TestNetworkAllowOnlyListedWebsites(t *testing.T) {
	s := network.Scenario(t, "allowing one website does not open up the others")
	s.Given().
		Website("docs.test", "the docs").
		Website("tracker.test", "tracked you").
		Model(story.RunsCommand("curl -s http://docs.test/")).
		Model(story.RunsCommand("curl -s http://tracker.test/")).
		Model(story.Replies("I read the docs"))
	s.When().Ask("read the docs", "--network-allow", "docs.test")
	s.Then().
		Succeeds().
		Saw(story.Not(story.Contains("tracked you"))).
		Visited("docs.test").
		NotVisited("tracker.test")
}

func TestNetworkAllowInConfig(t *testing.T) {
	s := network.Scenario(t, "I can allow a website permanently in my config")
	s.Given().
		Website("docs.test", "the docs").
		Config(`{"profiles": {"general": {"sandbox": {"networkAllowlist": ["docs.test"]}}}}`).
		Model(story.RunsCommand("curl -s http://docs.test/")).
		Model(story.Replies("I read the docs"))
	s.When().Ask("read the docs")
	s.Then().
		Succeeds().
		Saw(story.Contains("the docs")).
		Visited("docs.test")
}

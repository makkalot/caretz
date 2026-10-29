//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var asking = story.NewFeature("Asking caretz",
	"As a developer\n"+
		"I want to ask caretz for help from my terminal\n"+
		"So that it answers me and does the work in my project")

func TestAskingAnswersAQuestion(t *testing.T) {
	s := asking.Scenario(t, "caretz answers a question")
	s.Given().Model(story.Replies("Hello! How can I help?"))
	s.When().Ask("say hello")
	s.Then().
		Succeeds().
		Stdout(story.Contains("Hello! How can I help?")).
		Prompt(story.Contains("say hello"))
}

func TestAskingRunsACommand(t *testing.T) {
	s := asking.Scenario(t, "caretz runs a command to get the work done")
	s.Given().
		Model(story.RunsCommand("echo hello > greeting.txt && cat greeting.txt")).
		Model(story.Replies("I wrote greeting.txt"))
	s.When().Ask("create a greeting file")
	s.Then().
		Succeeds().
		Stdout(story.Contains("I wrote greeting.txt")).
		File("greeting.txt", story.Equals("hello\n")).
		Saw(story.Contains("hello"))
}

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

func TestAskingRunsMyOwnCommand(t *testing.T) {
	s := asking.Scenario(t, "I can run a command myself without asking the assistant")
	s.Describe("A prompt that starts with ! runs in the sandbox as a shell command.")
	s.When().Ask("!echo hi from my shell")
	s.Then().
		Succeeds().
		Stdout(story.Contains("hi from my shell")).
		RequestCount(0)
}

func TestAskingTaskFromFile(t *testing.T) {
	s := asking.Scenario(t, "I can give caretz a task written in a file")
	s.Given().
		File("task.md", "Summarize the open TODOs in this project.").
		Model(story.Replies("There are no open TODOs"))
	s.When().Run("--prompt-file", "task.md")
	s.Then().
		Succeeds().
		Stdout(story.Contains("There are no open TODOs")).
		Prompt(story.Contains("Summarize the open TODOs in this project."))
}

func TestAskingAboutAnotherProject(t *testing.T) {
	s := asking.Scenario(t, "I can point caretz at another project without changing directory")
	s.Given().
		File("../other-project/README.md", "The other project").
		Model(story.RunsCommand("cat README.md")).
		Model(story.Replies("This is the other project"))
	s.When().Ask("what is this project?", "--workspace", "../other-project")
	s.Then().
		Succeeds().
		Saw(story.Contains("The other project"))
}

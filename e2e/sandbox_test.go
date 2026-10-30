//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var sandbox = story.NewFeature("Working safely",
	"As a developer\n"+
		"I want the commands caretz runs to stay inside my project\n"+
		"So that I can let it work unattended without risking the rest of my machine")

func TestSandboxCannotChangeFilesOutsideProject(t *testing.T) {
	s := sandbox.Scenario(t, "caretz cannot change files outside my project")
	s.Describe("On macOS the write is refused; on Linux it lands in a throwaway copy.")
	s.Given().
		Model(story.RunsCommand("echo oops > ../outside.txt")).
		Model(story.Replies("I tried to write outside the project"))
	s.When().Ask("write a file next to the project")
	s.Then().
		Succeeds().
		Missing("../outside.txt")
}

func TestSandboxCannotReadFilesElsewhere(t *testing.T) {
	s := sandbox.Scenario(t, "caretz cannot read files elsewhere on my machine")
	s.Given().
		File("../private/notes.txt", "my bank PIN is 1234").
		Model(story.RunsCommand("cat ../private/notes.txt")).
		Model(story.Replies("I could not read your notes"))
	s.When().Ask("read my private notes")
	s.Then().
		Succeeds().
		Saw(story.Not(story.Contains("1234")))
}

func TestSandboxWritableFolder(t *testing.T) {
	s := sandbox.Scenario(t, "I can let caretz write to a folder outside my project")
	s.Given().
		Folder("../output").
		Model(story.RunsCommand("echo summary > ../output/report.txt")).
		Model(story.Replies("The report is in ../output"))
	s.When().Ask("write the report", "--writable-root", "../output")
	s.Then().
		Succeeds().
		File("../output/report.txt", story.Equals("summary\n"))
}

func TestSandboxReadOnlyFolder(t *testing.T) {
	s := sandbox.Scenario(t, "I can share a folder caretz may read but not change")
	s.Given().
		File("../docs/guide.md", "Use tabs.").
		Model(story.RunsCommand("cat ../docs/guide.md; echo rewritten > ../docs/guide.md")).
		Model(story.Replies("The guide says to use tabs"))
	s.When().Ask("follow the style guide", "--read-root", "../docs")
	s.Then().
		Succeeds().
		Saw(story.Contains("Use tabs.")).
		File("../docs/guide.md", story.Equals("Use tabs."))
}

func TestSandboxPassTokenByName(t *testing.T) {
	s := sandbox.Scenario(t, "I can pass a token from my shell to caretz's commands")
	s.Given().
		Env("DEPLOY_TOKEN", "s3cret").
		Model(story.RunsCommand(`echo "token=$DEPLOY_TOKEN"`)).
		Model(story.Replies("The deploy token is available"))
	s.When().Ask("check the deploy token", "--sandbox-env", "DEPLOY_TOKEN")
	s.Then().
		Succeeds().
		Saw(story.Contains("token=s3cret"))
}

func TestSandboxPassSettingsFromFile(t *testing.T) {
	s := sandbox.Scenario(t, "I can pass several settings from an env file")
	s.Given().
		Env("DEPLOY_TOKEN", "s3cret").
		File(".caretz/sandbox.env", "# copied from my shell\nDEPLOY_TOKEN\nCI=true\n").
		Model(story.RunsCommand(`echo "token=$DEPLOY_TOKEN ci=$CI"`)).
		Model(story.Replies("Both settings are available"))
	s.When().Ask("check the settings", "--sandbox-env-file", ".caretz/sandbox.env")
	s.Then().
		Succeeds().
		Saw(story.Contains("token=s3cret ci=true"))
}

func TestSandboxBackgroundJobKeepsRunning(t *testing.T) {
	s := sandbox.Scenario(t, "a job started in the background keeps running for my next command")
	s.Describe("Like a dev server that the assistant starts and then exercises.")
	s.Given().
		Model(story.RunsInBackground("sleep 1; echo ready > status.txt")).
		Model(story.RunsCommand("sleep 3; cat status.txt")).
		Model(story.Replies("The background job finished"))
	s.When().Ask("start the job and check on it")
	s.Then().
		Succeeds().
		Saw(story.Contains("ready"))
}

func TestSandboxPlanModeChangesNothing(t *testing.T) {
	s := sandbox.Scenario(t, "in plan mode caretz looks but does not change anything")
	s.Given().
		File("notes.txt", "draft").
		Model(story.RunsCommand("cat notes.txt; echo rewritten > notes.txt")).
		Model(story.Replies("Here is my plan for your notes"))
	s.When().Ask("tidy up my notes", "--plan")
	s.Then().
		Succeeds().
		Stdout(story.Contains("Here is my plan")).
		Saw(story.Contains("draft")).
		File("notes.txt", story.Equals("draft"))
}

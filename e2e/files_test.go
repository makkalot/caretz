//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var files = story.NewFeature("Working with files",
	"As a developer\n"+
		"I want caretz to read and change the files in my project\n"+
		"So that it can make the changes I ask for")

func TestFilesReads(t *testing.T) {
	s := files.Scenario(t, "caretz reads a file to answer my question")
	s.Given().
		File("main.go", "package main\n\nfunc main() {}\n").
		Model(story.Reads("main.go")).
		Model(story.Replies("main.go has an empty main function"))
	s.When().Ask("what does main.go do?")
	s.Then().
		Succeeds().
		Stdout(story.Contains("empty main function")).
		Saw(story.Contains("func main() {}"))
}

func TestFilesWrites(t *testing.T) {
	s := files.Scenario(t, "caretz creates a new file")
	s.Given().
		Model(story.Writes("docs/notes.md", "# Notes\n")).
		Model(story.Replies("I created docs/notes.md"))
	s.When().Ask("start a notes file in docs")
	s.Then().
		Succeeds().
		File("docs/notes.md", story.Equals("# Notes\n"))
}

func TestFilesEdits(t *testing.T) {
	s := files.Scenario(t, "caretz changes part of a file and leaves the rest alone")
	s.Given().
		File("config.yaml", "port: 8080\nhost: localhost\n").
		Model(story.Edits("config.yaml", "port: 8080", "port: 9090")).
		Model(story.Replies("The port is now 9090"))
	s.When().Ask("change the port to 9090")
	s.Then().
		Succeeds().
		File("config.yaml", story.Equals("port: 9090\nhost: localhost\n"))
}

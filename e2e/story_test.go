//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var (
	echo    = story.NewFeature("Echo", "The real caretz binary talks to a scripted model and prints its reply.")
	sandbox = story.NewFeature("Sandbox", "Bash calls from the model run inside the OS sandbox, not a plain shell.")
)

func TestStoryEchoText(t *testing.T) {
	s := echo.Scenario(t, "model replies with text")
	s.Given().Model(story.Replies("hello from the fake model"))
	s.When().RunPrompt("say hello")
	s.Then().
		Succeeds().
		Stdout(story.Contains("hello from the fake model")).
		Request(1).User(story.Contains("say hello")).OffersTool("Bash")
}

func TestStoryEchoViaBash(t *testing.T) {
	s := echo.Scenario(t, "model runs echo through Bash")
	s.Given().
		Model(story.Calls("Bash", `{"command":"echo sandbox-echo > out.txt && cat out.txt"}`).ID("call_echo")).
		Model(story.Replies("all done"))
	s.When().RunPrompt("write a file")
	s.Then().
		Succeeds().
		Stdout(story.Contains("all done")).
		File("out.txt", story.Equals("sandbox-echo\n")).
		Request(2).ToolResult("call_echo", story.Contains("sandbox-echo"))
}

func TestStorySandboxWriteOutsideWorkspaceStaysContained(t *testing.T) {
	s := sandbox.Scenario(t, "write outside the workspace stays contained")
	s.Describe("macOS denies the write; Linux lets it land on the sandbox's throwaway\n" +
		"tmpfs root. Either way it never reaches the host.")
	s.Given().
		Model(story.Calls("Bash", `{"command":"echo escaped > ../escaped.txt"}`).ID("call_escape")).
		Model(story.Replies("tried"))
	s.When().RunPrompt("escape the workspace")
	s.Then().
		Succeeds().
		Stdout(story.Contains("tried")).
		Missing("../escaped.txt").
		Request(2).HasToolResult("call_escape")
}

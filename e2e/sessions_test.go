//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var sessions = story.NewFeature("Continuing earlier work",
	"As a developer\n"+
		"I want to come back to an earlier conversation\n"+
		"So that I don't have to explain the project again")

func TestSessionsContinue(t *testing.T) {
	s := sessions.Scenario(t, "I can pick up where I left off")
	s.Given().
		Model(story.Replies("The flow starts in auth.go")).
		Model(story.Replies("I'll start with the token check in auth.go"))
	s.When().
		Ask("map the authentication flow").
		Continue("now fix the riskiest part")
	s.Then().
		Succeeds().
		Stdout(story.Contains("I'll start with the token check")).
		Conversation(story.Contains("map the authentication flow")).
		Conversation(story.Contains("The flow starts in auth.go"))
}

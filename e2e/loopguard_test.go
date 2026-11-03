//go:build e2e

package e2e

import (
	"testing"

	"github.com/makkalot/caretz/e2e/story"
)

var loopGuard = story.NewFeature("Catching a stuck turn",
	"As a developer\n"+
		"I want caretz to notice when the assistant keeps hitting the same wall\n"+
		"So that I don't pay for a turn that goes in circles")

// failingTest fails the same way every time, like a test that cannot compile.
const failingTest = "echo 'db_test.go:12: undefined: testDB' >&2; exit 1"

// failsThreeTimes scripts the assistant running failingTest three times, which
// is the loop guard's default repeat threshold.
func failsThreeTimes(g *story.Given) *story.Given {
	return g.
		Model(story.RunsCommand(failingTest)).
		Model(story.RunsCommand(failingTest)).
		Model(story.RunsCommand(failingTest))
}

func TestLoopGuardRedirectsALoopingTurn(t *testing.T) {
	s := loopGuard.Scenario(t, "caretz points a looping assistant in a new direction")
	s.Describe("Without a judge model configured, the session's own model judges.")
	g := failsThreeTimes(s.Given())
	g.
		Model(story.JudgeFindsStuck(0.9, "testDB is declared behind a build tag.", "run the tests with -tags integration")).
		Model(story.RunsCommand("echo 'ok with -tags integration'")).
		Model(story.Replies("The tests pass with the integration tag"))
	s.When().Ask("make the tests pass", "--loop-guard", "active", "-v")
	s.Then().
		Succeeds().
		Stdout(story.Contains("The tests pass with the integration tag")).
		JudgeAsked(1).
		JudgeSaw(story.Contains("Task: make the tests pass")).
		JudgeSaw(story.Contains("repeat: 3×")).
		Note(story.Contains("[loop guard] Automated message from caretz, not from the user")).
		Note(story.Contains("The same failure has occurred 3 times")).
		Note(story.Contains("Try instead: run the tests with -tags integration")).
		Saw(story.Not(story.Contains("[loop guard]"))).
		Stderr(story.Contains("[loop-guard] stuck (0.90) → redirected the agent"))
}

func TestLoopGuardEndsATurnThatStaysStuck(t *testing.T) {
	s := loopGuard.Scenario(t, "caretz ends a turn that stays stuck after being redirected")
	s.Describe("The assistant answers without tools, saying what is done and what is left.")
	g := failsThreeTimes(s.Given())
	g.Model(story.JudgeFindsStuck(0.9, "testDB is declared behind a build tag.", "run the tests with -tags integration"))
	g = failsThreeTimes(g)
	g.
		Model(story.JudgeFindsStuck(0.95, "The same error is still coming back.", "ask the user how the tests are meant to run")).
		Model(story.Replies("I could not fix testDB; tell me how you run the integration tests"))
	s.When().Ask("make the tests pass", "--loop-guard", "active", "-v")
	s.Then().
		Succeeds().
		Stdout(story.Contains("tell me how you run the integration tests")).
		JudgeAsked(2).
		Note(story.Contains("This turn is ending: tools are no longer available.")).
		LastRequest().OffersNoTools().
		Stderr(story.Contains("still stuck after redirect, ending the turn"))
}

func TestLoopGuardShadowModeOnlyReports(t *testing.T) {
	s := loopGuard.Scenario(t, "in shadow mode caretz only reports what the judge thinks")
	s.Describe("Useful for checking the judge's calls before letting it steer.")
	g := failsThreeTimes(s.Given())
	g.
		Model(story.JudgeFindsStuck(0.9, "testDB is declared behind a build tag.", "run the tests with -tags integration")).
		Model(story.Replies("I am still looking into testDB"))
	s.When().Ask("make the tests pass", "--loop-guard", "shadow", "-v")
	s.Then().
		Succeeds().
		JudgeAsked(1).
		NoNote().
		Stderr(story.Contains("[loop-guard] stuck (0.90) → shadow mode, no redirect")).
		Stderr(story.Contains("try instead: run the tests with -tags integration")).
		RecordedEvent("LoopGuard.Judged")
}

func TestLoopGuardOffByDefault(t *testing.T) {
	s := loopGuard.Scenario(t, "without --loop-guard caretz never asks a judge")
	g := failsThreeTimes(s.Given())
	g.Model(story.Replies("I am still looking into testDB"))
	s.When().Ask("make the tests pass", "-v")
	s.Then().
		Succeeds().
		RequestCount(4).
		JudgeAsked(0).
		NoNote().
		Stderr(story.Not(story.Contains("[loop-guard]")))
}

func TestLoopGuardLeavesProgressAlone(t *testing.T) {
	s := loopGuard.Scenario(t, "a turn the judge finds making progress is left alone")
	g := failsThreeTimes(s.Given())
	g.
		Model(story.JudgeFindsNotStuck(0.8, "Each attempt narrows the problem down.")).
		Model(story.Replies("testDB needs the integration tag"))
	s.When().Ask("make the tests pass", "--loop-guard", "active", "-v")
	s.Then().
		Succeeds().
		JudgeAsked(1).
		NoNote().
		Stderr(story.Contains("[loop-guard] not stuck (0.80)"))
}

func TestLoopGuardIgnoresABrokenJudge(t *testing.T) {
	s := loopGuard.Scenario(t, "a judge that does not answer properly does not interrupt my work")
	s.Describe("A reply caretz cannot read as a verdict counts as not stuck.")
	g := failsThreeTimes(s.Given())
	g.
		Model(story.JudgeReplies("I think maybe it is stuck?")).
		Model(story.Replies("testDB needs the integration tag"))
	s.When().Ask("make the tests pass", "--loop-guard", "active", "-v")
	s.Then().
		Succeeds().
		Stdout(story.Contains("testDB needs the integration tag")).
		JudgeAsked(1).
		NoNote().
		Stderr(story.Contains("[loop-guard] judge failed, ignored"))
}

func TestLoopGuardChecksALongTurnWithoutFailures(t *testing.T) {
	s := loopGuard.Scenario(t, "caretz checks on a long turn even when nothing fails")
	s.Describe("This catches an assistant drifting away from the task.")
	s.Given().
		Model(story.RunsCommand("echo reading the changelog")).
		Model(story.RunsCommand("echo reformatting the README")).
		Model(story.RunsCommand("echo renaming unrelated variables")).
		Model(story.JudgeFindsStuck(0.85, "None of this touches the failing test.", "go back to db_test.go")).
		Model(story.Replies("Back to db_test.go"))
	s.When().Ask("make the tests pass", "--loop-guard", "active", "--loop-guard-check-every", "3")
	s.Then().
		Succeeds().
		JudgeAsked(1).
		JudgeSaw(story.Contains("checkpoint: 3 tool calls")).
		Note(story.Contains("Try instead: go back to db_test.go"))
}

func TestLoopGuardSkipsModelsNotListed(t *testing.T) {
	s := loopGuard.Scenario(t, "caretz only guards the models I list")
	s.Given().Config(`{
  "profiles": {
    "general": {
      "loopGuard": {"mode": "active", "models": ["other-provider/*"]}
    }
  }
}`)
	g := failsThreeTimes(s.Given())
	g.Model(story.Replies("I am still looking into testDB"))
	s.When().Ask("make the tests pass")
	s.Then().
		Succeeds().
		RequestCount(4).
		JudgeAsked(0)
}

func TestLoopGuardGuardsAListedModel(t *testing.T) {
	s := loopGuard.Scenario(t, "caretz guards a model matching my list")
	s.Describe("A pattern also matches the bare model ID of an ad hoc endpoint.")
	s.Given().Config(`{
  "profiles": {
    "general": {
      "loopGuard": {"mode": "active", "models": ["fake-*"]}
    }
  }
}`)
	g := failsThreeTimes(s.Given())
	g.
		Model(story.JudgeFindsNotStuck(0.8, "Each attempt narrows the problem down.")).
		Model(story.Replies("testDB needs the integration tag"))
	s.When().Ask("make the tests pass")
	s.Then().
		Succeeds().
		JudgeAsked(1)
}

func TestLoopGuardRejectsAnUnknownJudgeModel(t *testing.T) {
	s := loopGuard.Scenario(t, "caretz refuses to start with a judge model it cannot find")
	s.When().Ask("make the tests pass", "--loop-guard", "active", "--loop-guard-model", "nope/missing")
	s.Then().
		ExitCode(1).
		Stderr(story.Contains("nope/missing")).
		RequestCount(0)
}

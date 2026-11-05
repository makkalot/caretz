# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Catching a stuck turn
  As a developer
  I want caretz to notice when the assistant keeps hitting the same wall
  So that I don't pay for a turn that goes in circles

  # Test: TestLoopGuardRedirectsALoopingTurn (e2e/loopguard_test.go)
  Scenario: caretz points a looping assistant in a new direction
    Without a judge model configured, the session's own model judges.
    Given the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the loop guard's judge finds the turn stuck (confidence 0.90):
      """
      reason: testDB is declared behind a build tag.
      try instead: run the tests with -tags integration
      """
    And the assistant runs:
      """
      echo 'ok with -tags integration'
      """
    And the assistant replies "The tests pass with the integration tag"
    When I ask caretz "make the tests pass" with --loop-guard active -v
    Then caretz finishes successfully
    And caretz's answer contains "The tests pass with the integration tag"
    And caretz asked the loop guard's judge once
    And what the loop guard's judge was shown contains "Task: make the tests pass"
    And what the loop guard's judge was shown contains "repeat: 3×"
    And the note caretz added for the assistant contains "[loop guard] Automated message from caretz, not from the user"
    And the note caretz added for the assistant contains "The same failure has occurred 3 times"
    And the note caretz added for the assistant contains "Try instead: run the tests with -tags integration"
    And what the assistant saw from the command does not contain "[loop guard]"
    And caretz's error output contains "[loop-guard] stuck (0.90) → redirected the agent"

  # Test: TestLoopGuardEndsATurnThatStaysStuck (e2e/loopguard_test.go)
  Scenario: caretz ends a turn that stays stuck after being redirected
    The assistant answers without tools, saying what is done and what is left.
    Given the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the loop guard's judge finds the turn stuck (confidence 0.90):
      """
      reason: testDB is declared behind a build tag.
      try instead: run the tests with -tags integration
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the loop guard's judge finds the turn stuck (confidence 0.95):
      """
      reason: The same error is still coming back.
      try instead: ask the user how the tests are meant to run
      """
    And the assistant replies "I could not fix testDB; tell me how you run the integration tests"
    When I ask caretz "make the tests pass" with --loop-guard active -v
    Then caretz finishes successfully
    And caretz's answer contains "tell me how you run the integration tests"
    And caretz asked the loop guard's judge 2 times
    And the note caretz added for the assistant contains "This turn is ending: tools are no longer available."
    And the last request offers the assistant no tools
    And caretz's error output contains "still stuck after redirect, ending the turn"

  # Test: TestLoopGuardShadowModeOnlyReports (e2e/loopguard_test.go)
  Scenario: in shadow mode caretz only reports what the judge thinks
    Useful for checking the judge's calls before letting it steer.
    Given the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the loop guard's judge finds the turn stuck (confidence 0.90):
      """
      reason: testDB is declared behind a build tag.
      try instead: run the tests with -tags integration
      """
    And the assistant replies "I am still looking into testDB"
    When I ask caretz "make the tests pass" with --loop-guard shadow -v
    Then caretz finishes successfully
    And caretz asked the loop guard's judge once
    And caretz added no note for the assistant
    And caretz's error output contains "[loop-guard] stuck (0.90) → shadow mode, no redirect"
    And caretz's error output contains "try instead: run the tests with -tags integration"
    And the session recorded a "LoopGuard.Judged" event

  # Test: TestLoopGuardOffByDefault (e2e/loopguard_test.go)
  Scenario: without --loop-guard caretz never asks a judge
    Given the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant replies "I am still looking into testDB"
    When I ask caretz "make the tests pass" with -v
    Then caretz finishes successfully
    And caretz contacted the assistant 4 times
    And caretz never asked the loop guard's judge
    And caretz added no note for the assistant
    And caretz's error output does not contain "[loop-guard]"

  # Test: TestLoopGuardLeavesProgressAlone (e2e/loopguard_test.go)
  Scenario: a turn the judge finds making progress is left alone
    Given the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the loop guard's judge finds the turn making progress (confidence 0.80): "Each attempt narrows the problem down."
    And the assistant replies "testDB needs the integration tag"
    When I ask caretz "make the tests pass" with --loop-guard active -v
    Then caretz finishes successfully
    And caretz asked the loop guard's judge once
    And caretz added no note for the assistant
    And caretz's error output contains "[loop-guard] not stuck (0.80)"

  # Test: TestLoopGuardIgnoresABrokenJudge (e2e/loopguard_test.go)
  Scenario: a judge that does not answer properly does not interrupt my work
    A reply caretz cannot read as a verdict counts as not stuck.
    Given the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the loop guard's judge replies "I think maybe it is stuck?"
    And the assistant replies "testDB needs the integration tag"
    When I ask caretz "make the tests pass" with --loop-guard active -v
    Then caretz finishes successfully
    And caretz's answer contains "testDB needs the integration tag"
    And caretz asked the loop guard's judge once
    And caretz added no note for the assistant
    And caretz's error output contains "[loop-guard] judge failed, ignored"

  # Test: TestLoopGuardChecksALongTurnWithoutFailures (e2e/loopguard_test.go)
  Scenario: caretz checks on a long turn even when nothing fails
    This catches an assistant drifting away from the task.
    Given the assistant runs:
      """
      echo reading the changelog
      """
    And the assistant runs:
      """
      echo reformatting the README
      """
    And the assistant runs:
      """
      echo renaming unrelated variables
      """
    And the loop guard's judge finds the turn stuck (confidence 0.85):
      """
      reason: None of this touches the failing test.
      try instead: go back to db_test.go
      """
    And the assistant replies "Back to db_test.go"
    When I ask caretz "make the tests pass" with --loop-guard active --loop-guard-check-every 3
    Then caretz finishes successfully
    And caretz asked the loop guard's judge once
    And what the loop guard's judge was shown contains "checkpoint: 3 tool calls"
    And the note caretz added for the assistant contains "Try instead: go back to db_test.go"

  # Test: TestLoopGuardSkipsModelsNotListed (e2e/loopguard_test.go)
  Scenario: caretz only guards the models I list
    Given my caretz config is:
      """
      {
        "profiles": {
          "general": {
            "loopGuard": {"mode": "active", "models": ["other-provider/*"]}
          }
        }
      }
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant replies "I am still looking into testDB"
    When I ask caretz "make the tests pass"
    Then caretz finishes successfully
    And caretz contacted the assistant 4 times
    And caretz never asked the loop guard's judge

  # Test: TestLoopGuardGuardsAListedModel (e2e/loopguard_test.go)
  Scenario: caretz guards a model matching my list
    A pattern also matches the bare model ID of an ad hoc endpoint.
    Given my caretz config is:
      """
      {
        "profiles": {
          "general": {
            "loopGuard": {"mode": "active", "models": ["fake-*"]}
          }
        }
      }
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the assistant runs:
      """
      echo 'db_test.go:12: undefined: testDB' >&2; exit 1
      """
    And the loop guard's judge finds the turn making progress (confidence 0.80): "Each attempt narrows the problem down."
    And the assistant replies "testDB needs the integration tag"
    When I ask caretz "make the tests pass"
    Then caretz finishes successfully
    And caretz asked the loop guard's judge once

  # Test: TestLoopGuardRejectsAnUnknownJudgeModel (e2e/loopguard_test.go)
  Scenario: caretz refuses to start with a judge model it cannot find
    When I ask caretz "make the tests pass" with --loop-guard active --loop-guard-model nope/missing
    Then caretz exits with code 1
    And caretz's error output contains "nope/missing"
    And caretz did not contact the assistant

# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Working safely
  As a developer
  I want the commands caretz runs to stay inside my project
  So that I can let it work unattended without risking the rest of my machine

  # Test: TestSandboxCannotChangeFilesOutsideProject (e2e/sandbox_test.go)
  Scenario: caretz cannot change files outside my project
    On macOS the write is refused; on Linux it lands in a throwaway copy.
    Given the assistant runs:
      """
      echo oops > ../outside.txt
      """
    And the assistant replies "I tried to write outside the project"
    When I ask caretz "write a file next to the project"
    Then caretz finishes successfully
    And the file "../outside.txt" does not exist

  # Test: TestSandboxCannotReadFilesElsewhere (e2e/sandbox_test.go)
  Scenario: caretz cannot read files elsewhere on my machine
    Given a file "../private/notes.txt" containing:
      """
      my bank PIN is 1234
      """
    And the assistant runs:
      """
      cat ../private/notes.txt
      """
    And the assistant replies "I could not read your notes"
    When I ask caretz "read my private notes"
    Then caretz finishes successfully
    And what the assistant saw from the command does not contain "1234"

  # Test: TestSandboxWritableFolder (e2e/sandbox_test.go)
  Scenario: I can let caretz write to a folder outside my project
    Given a folder "../output"
    And the assistant runs:
      """
      echo summary > ../output/report.txt
      """
    And the assistant replies "The report is in ../output"
    When I ask caretz "write the report" with --writable-root ../output
    Then caretz finishes successfully
    And the file "../output/report.txt" is "summary\n"

  # Test: TestSandboxReadOnlyFolder (e2e/sandbox_test.go)
  Scenario: I can share a folder caretz may read but not change
    Given a file "../docs/guide.md" containing:
      """
      Use tabs.
      """
    And the assistant runs:
      """
      cat ../docs/guide.md; echo rewritten > ../docs/guide.md
      """
    And the assistant replies "The guide says to use tabs"
    When I ask caretz "follow the style guide" with --read-root ../docs
    Then caretz finishes successfully
    And what the assistant saw from the command contains "Use tabs."
    And the file "../docs/guide.md" is "Use tabs."

  # Test: TestSandboxPassTokenByName (e2e/sandbox_test.go)
  Scenario: I can pass a token from my shell to caretz's commands
    Given my shell has DEPLOY_TOKEN set to "s3cret"
    And the assistant runs:
      """
      echo "token=$DEPLOY_TOKEN"
      """
    And the assistant replies "The deploy token is available"
    When I ask caretz "check the deploy token" with --sandbox-env DEPLOY_TOKEN
    Then caretz finishes successfully
    And what the assistant saw from the command contains "token=s3cret"

  # Test: TestSandboxPassSettingsFromFile (e2e/sandbox_test.go)
  Scenario: I can pass several settings from an env file
    Given my shell has DEPLOY_TOKEN set to "s3cret"
    And a file ".caretz/sandbox.env" containing:
      """
      # copied from my shell
      DEPLOY_TOKEN
      CI=true
      """
    And the assistant runs:
      """
      echo "token=$DEPLOY_TOKEN ci=$CI"
      """
    And the assistant replies "Both settings are available"
    When I ask caretz "check the settings" with --sandbox-env-file .caretz/sandbox.env
    Then caretz finishes successfully
    And what the assistant saw from the command contains "token=s3cret ci=true"

  # Test: TestSandboxBackgroundJobKeepsRunning (e2e/sandbox_test.go)
  Scenario: a job started in the background keeps running for my next command
    Like a dev server that the assistant starts and then exercises.
    Given the assistant starts in the background:
      """
      sleep 1; echo ready > status.txt
      """
    And the assistant runs:
      """
      sleep 3; cat status.txt
      """
    And the assistant replies "The background job finished"
    And the assistant replies "The job is done"
    When I ask caretz "start the job and check on it"
    Then caretz finishes successfully
    And what the assistant saw from the command contains "ready"

  # Test: TestSandboxBackgroundJobReportsWhenItEnds (e2e/sandbox_test.go)
  Scenario: caretz tells the assistant when a background job ends
    The assistant does not have to poll: the exit status and the end of the log arrive as a new message.
    Given the assistant starts in the background:
      """
      echo compiling; echo 'main.go:3: undefined: Foo' >&2; exit 2
      """
    And the assistant runs:
      """
      sleep 1; echo other work done
      """
    And the assistant replies "The build is running in the background"
    And the assistant replies "The build failed: Foo is undefined in main.go"
    When I ask caretz "build in the background while you do other work"
    Then caretz finishes successfully
    And caretz contacted the assistant 4 times
    And caretz's answer contains "The build failed: Foo is undefined in main.go"
    And the tool result for "build" in request 2 contains "You will get a message when it exits"
    And the user message in the last request contains "Background command finished."
    And the user message in the last request contains "Exit code: 2"
    And the user message in the last request contains "main.go:3: undefined: Foo"

  # Test: TestSandboxPlanModeChangesNothing (e2e/sandbox_test.go)
  Scenario: in plan mode caretz looks but does not change anything
    Given a file "notes.txt" containing:
      """
      draft
      """
    And the assistant runs:
      """
      cat notes.txt; echo rewritten > notes.txt
      """
    And the assistant replies "Here is my plan for your notes"
    When I ask caretz "tidy up my notes" with --plan
    Then caretz finishes successfully
    And caretz's answer contains "Here is my plan"
    And what the assistant saw from the command contains "draft"
    And the file "notes.txt" is "draft"

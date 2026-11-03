# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Asking caretz
  As a developer
  I want to ask caretz for help from my terminal
  So that it answers me and does the work in my project

  # Test: TestAskingAnswersAQuestion (e2e/asking_test.go)
  Scenario: caretz answers a question
    Given the assistant replies "Hello! How can I help?"
    When I ask caretz "say hello"
    Then caretz finishes successfully
    And caretz's answer contains "Hello! How can I help?"
    And the prompt the assistant received contains "say hello"

  # Test: TestAskingRunsACommand (e2e/asking_test.go)
  Scenario: caretz runs a command to get the work done
    Given the assistant runs:
      """
      echo hello > greeting.txt && cat greeting.txt
      """
    And the assistant replies "I wrote greeting.txt"
    When I ask caretz "create a greeting file"
    Then caretz finishes successfully
    And caretz's answer contains "I wrote greeting.txt"
    And the file "greeting.txt" is "hello\n"
    And what the assistant saw from the command contains "hello"

  # Test: TestAskingRunsMyOwnCommand (e2e/asking_test.go)
  Scenario: I can run a command myself without asking the assistant
    A prompt that starts with ! runs in the sandbox as a shell command.
    When I ask caretz "!echo hi from my shell"
    Then caretz finishes successfully
    And caretz's answer contains "hi from my shell"
    And caretz did not contact the assistant

  # Test: TestAskingTaskFromFile (e2e/asking_test.go)
  Scenario: I can give caretz a task written in a file
    Given a file "task.md" containing:
      """
      Summarize the open TODOs in this project.
      """
    And the assistant replies "There are no open TODOs"
    When I run `caretz --prompt-file task.md`
    Then caretz finishes successfully
    And caretz's answer contains "There are no open TODOs"
    And the prompt the assistant received contains "Summarize the open TODOs in this project."

  # Test: TestAskingAboutAnotherProject (e2e/asking_test.go)
  Scenario: I can point caretz at another project without changing directory
    Given a file "../other-project/README.md" containing:
      """
      The other project
      """
    And the assistant runs:
      """
      cat README.md
      """
    And the assistant replies "This is the other project"
    When I ask caretz "what is this project?" with --workspace ../other-project
    Then caretz finishes successfully
    And what the assistant saw from the command contains "The other project"

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

# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Choosing caretz's tools
  As a developer
  I want to choose which tools caretz may use for a task
  So that a review or an explanation cannot change or run anything

  # Test: TestToolsOnlyListed (e2e/tools_test.go)
  Scenario: I can limit caretz to the tools I list
    Given the assistant replies "I will only read"
    When I ask caretz "explain the request flow" with --tools Read,Grep
    Then caretz finishes successfully
    And the assistant can use the "Read" tool
    And the assistant can use the "Grep" tool
    And the assistant cannot use the "Bash" tool
    And the assistant cannot use the "Edit" tool

  # Test: TestToolsDisableOne (e2e/tools_test.go)
  Scenario: I can take one tool away
    Given the assistant replies "I will review without running anything"
    When I ask caretz "review this diff" with --disable-tool Bash
    Then caretz finishes successfully
    And the assistant cannot use the "Bash" tool
    And the assistant can use the "Read" tool

  # Test: TestToolsCompactProfile (e2e/tools_test.go)
  Scenario: a small model can work with just the shell
    The compact profile keeps the tool list short for tiny models.
    Given the assistant replies "I have the shell"
    When I ask caretz "list the files" with --profile compact
    Then caretz finishes successfully
    And the assistant can use the "Bash" tool
    And the assistant cannot use the "Read" tool
    And the assistant cannot use the "CallSubagent" tool

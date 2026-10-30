# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Continuing earlier work
  As a developer
  I want to come back to an earlier conversation
  So that I don't have to explain the project again

  # Test: TestSessionsContinue (e2e/sessions_test.go)
  Scenario: I can pick up where I left off
    Given the assistant replies "The flow starts in auth.go"
    And the assistant replies "I'll start with the token check in auth.go"
    When I ask caretz "map the authentication flow"
    And I continue that session with "now fix the riskiest part"
    Then caretz finishes successfully
    And caretz's answer contains "I'll start with the token check"
    And the conversation the assistant received contains "map the authentication flow"
    And the conversation the assistant received contains "The flow starts in auth.go"

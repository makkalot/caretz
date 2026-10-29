# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Echo
  The real caretz binary talks to a scripted model and prints its reply.

  # Test: TestStoryEchoText (e2e/story_test.go)
  Scenario: model replies with text
    Given the model replies "hello from the fake model"
    When I run caretz with prompt "say hello"
    Then it succeeds
    And stdout contains "hello from the fake model"
    And the user message in request 1 contains "say hello"
    And request 1 offers the "Bash" tool

  # Test: TestStoryEchoViaBash (e2e/story_test.go)
  Scenario: model runs echo through Bash
    Given the model calls "Bash" as "call_echo" with:
      """
      {"command":"echo sandbox-echo > out.txt && cat out.txt"}
      """
    And the model replies "all done"
    When I run caretz with prompt "write a file"
    Then it succeeds
    And stdout contains "all done"
    And workspace file "out.txt" is "sandbox-echo\n"
    And the tool result for "call_echo" in request 2 contains "sandbox-echo"

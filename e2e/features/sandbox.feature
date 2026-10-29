# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Sandbox
  Bash calls from the model run inside the OS sandbox, not a plain shell.

  # Test: TestStorySandboxWriteOutsideWorkspaceStaysContained (e2e/story_test.go)
  Scenario: write outside the workspace stays contained
    macOS denies the write; Linux lets it land on the sandbox's throwaway
    tmpfs root. Either way it never reaches the host.
    Given the model calls "Bash" as "call_escape" with:
      """
      {"command":"echo escaped > ../escaped.txt"}
      """
    And the model replies "tried"
    When I run caretz with prompt "escape the workspace"
    Then it succeeds
    And stdout contains "tried"
    And path "../escaped.txt" does not exist
    And request 2 has a tool result for "call_escape"

# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Teaching caretz my project
  As a developer
  I want to give caretz my project's rules, playbooks, and specialist agents
  So that it works the way my team does without me repeating myself

  # Test: TestCustomizationProjectRules (e2e/customization_test.go)
  Scenario: caretz follows the rules in my project's AGENTS.md
    Given a file "AGENTS.md" containing:
      """
      - Run `go test ./...` after Go changes.
      """
    And the assistant replies "Understood"
    When I ask caretz "fix the bug"
    Then caretz finishes successfully
    And the instructions the assistant received contains "Run `go test ./...` after Go changes."

  # Test: TestCustomizationOwnSystemPromptKeepsRules (e2e/customization_test.go)
  Scenario: my own system prompt still keeps the project's rules
    Given a file "AGENTS.md" containing:
      """
      - Ask before changing a public API.
      """
    And the assistant replies "Noted"
    When I ask caretz "fix the bug" with --system-prompt "You are a terse senior reviewer."
    Then caretz finishes successfully
    And the instructions the assistant received contains "You are a terse senior reviewer."
    And the instructions the assistant received contains "Ask before changing a public API."

  # Test: TestCustomizationSkillOffered (e2e/customization_test.go)
  Scenario: caretz knows about the skills in my project
    Given a file ".caretz/skills/release-check/SKILL.md" containing:
      """
      ---
      name: release-check
      description: Verify a release candidate before tagging it.
      ---
      # Release check
      1. Run the tests.
      2. Report failures with the command and output.
      """
    And the assistant replies "I can run a release check"
    When I ask caretz "what can you do?"
    Then caretz finishes successfully
    And the instructions the assistant received contains "release-check"
    And the instructions the assistant received contains "Verify a release candidate before tagging it."

  # Test: TestCustomizationSkillLoaded (e2e/customization_test.go)
  Scenario: caretz follows a skill when the task needs it
    Given a file ".caretz/skills/release-check/SKILL.md" containing:
      """
      ---
      name: release-check
      description: Verify a release candidate before tagging it.
      ---
      # Release check
      1. Run the tests.
      2. Report failures with the command and output.
      """
    And the assistant reads ".caretz/skills/release-check/SKILL.md"
    And the assistant replies "Running the release check"
    When I ask caretz "check the release candidate"
    Then caretz finishes successfully
    And what the assistant saw from reading ".caretz/skills/release-check/SKILL.md" contains "1. Run the tests."

  # Test: TestCustomizationPersonalSkill (e2e/customization_test.go)
  Scenario: my personal skills are available in every project
    Given a file "~/.config/caretz/skills/release-check/SKILL.md" containing:
      """
      ---
      name: release-check
      description: My personal release routine.
      ---
      Personal steps.
      """
    And the assistant replies "I can run your release routine"
    When I ask caretz "what can you do?"
    Then caretz finishes successfully
    And the instructions the assistant received contains "My personal release routine."

  # Test: TestCustomizationProjectSkillOverridesPersonal (e2e/customization_test.go)
  Scenario: a project skill replaces my personal skill with the same name
    Given a file "~/.config/caretz/skills/release-check/SKILL.md" containing:
      """
      ---
      name: release-check
      description: My personal release routine.
      ---
      Personal steps.
      """
    And a file ".caretz/skills/release-check/SKILL.md" containing:
      """
      ---
      name: release-check
      description: Verify a release candidate before tagging it.
      ---
      # Release check
      1. Run the tests.
      2. Report failures with the command and output.
      """
    And the assistant replies "I will use the project's release check"
    When I ask caretz "what can you do?"
    Then caretz finishes successfully
    And the instructions the assistant received contains "Verify a release candidate before tagging it."
    And the instructions the assistant received does not contain "My personal release routine."

  # Test: TestCustomizationClaudeSkills (e2e/customization_test.go)
  Scenario: skills I already keep for Claude work without copying
    Given a file ".claude/skills/release-check/SKILL.md" containing:
      """
      ---
      name: release-check
      description: Verify a release candidate before tagging it.
      ---
      # Release check
      1. Run the tests.
      2. Report failures with the command and output.
      """
    And the assistant replies "I can run a release check"
    When I ask caretz "what can you do?"
    Then caretz finishes successfully
    And the instructions the assistant received contains "Verify a release candidate before tagging it."

  # Test: TestCustomizationSubagent (e2e/customization_test.go)
  Scenario: caretz hands a focused job to a specialist agent
    Given a file ".caretz/agents/reviewer.md" containing:
      """
      ---
      description: Reviews changes for bugs.
      mode: subagent
      ---
      Review the change. Do not edit files.
      """
    And the assistant hands "review my latest change" to the "reviewer" agent
    And the "reviewer" agent replies "No bugs found"
    And the assistant replies "The reviewer found no bugs"
    When I ask caretz "get my change reviewed"
    Then caretz finishes successfully
    And caretz's answer contains "The reviewer found no bugs"
    And what the assistant saw from the "reviewer" agent contains "No bugs found"

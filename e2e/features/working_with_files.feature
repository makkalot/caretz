# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Working with files
  As a developer
  I want caretz to read and change the files in my project
  So that it can make the changes I ask for

  # Test: TestFilesReads (e2e/files_test.go)
  Scenario: caretz reads a file to answer my question
    Given a file "main.go" containing:
      """
      package main

      func main() {}
      """
    And the assistant reads "main.go"
    And the assistant replies "main.go has an empty main function"
    When I ask caretz "what does main.go do?"
    Then caretz finishes successfully
    And caretz's answer contains "empty main function"
    And what the assistant saw from reading "main.go" contains "func main() {}"

  # Test: TestFilesWrites (e2e/files_test.go)
  Scenario: caretz creates a new file
    Given the assistant writes "docs/notes.md":
      """
      # Notes
      """
    And the assistant replies "I created docs/notes.md"
    When I ask caretz "start a notes file in docs"
    Then caretz finishes successfully
    And the file "docs/notes.md" is "# Notes\n"

  # Test: TestFilesEdits (e2e/files_test.go)
  Scenario: caretz changes part of a file and leaves the rest alone
    Given a file "config.yaml" containing:
      """
      port: 8080
      host: localhost
      """
    And the assistant changes "port: 8080" to "port: 9090" in "config.yaml"
    And the assistant replies "The port is now 9090"
    When I ask caretz "change the port to 9090"
    Then caretz finishes successfully
    And the file "config.yaml" is "port: 9090\nhost: localhost\n"

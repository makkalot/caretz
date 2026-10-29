# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit.
Feature: Network access
  As a developer
  I want caretz's commands to reach only the websites I allow
  So that a task cannot send my code or data anywhere I did not expect

  # Test: TestNetworkBlockedByDefault (e2e/network_test.go)
  Scenario: commands cannot reach the internet unless I allow it
    Given a website "docs.test" serving:
      """
      the docs
      """
    And the assistant runs:
      """
      curl -s http://docs.test/
      """
    And the assistant replies "I could not reach the docs"
    When I ask caretz "read the docs"
    Then caretz finishes successfully
    And what the assistant saw from the command does not contain "the docs"
    And the website "docs.test" was not visited

  # Test: TestNetworkAssistantToldWhatWasBlocked (e2e/network_test.go)
  Scenario: the assistant is told which website was blocked
    In a run that cannot ask me, the assistant still sees what the
    command printed, so it can explain what it needs.
    Given a website "docs.test" serving:
      """
      the docs
      """
    And a website "tracker.test" serving:
      """
      tracked you
      """
    And the assistant runs:
      """
      curl -s http://docs.test/; curl -s http://tracker.test/
      """
    And the assistant replies "I read the docs; tracker.test is not allowed"
    When I ask caretz "read the docs" with --network-allow docs.test
    Then caretz finishes successfully
    And what the assistant saw from the command contains "the docs"
    And what the assistant saw from the command contains "blocked network access to tracker.test"
    And what the assistant saw from the command does not contain "tracked you"
    And the website "tracker.test" was not visited

  # Test: TestNetworkAllowWebsite (e2e/network_test.go)
  Scenario: I can allow a website a task needs
    Given a website "docs.test" serving:
      """
      the docs
      """
    And the assistant runs:
      """
      curl -s http://docs.test/
      """
    And the assistant replies "I read the docs"
    When I ask caretz "read the docs" with --network-allow docs.test
    Then caretz finishes successfully
    And what the assistant saw from the command contains "the docs"
    And the website "docs.test" was visited

  # Test: TestNetworkAllowOnlyListedWebsites (e2e/network_test.go)
  Scenario: allowing one website does not open up the others
    Given a website "docs.test" serving:
      """
      the docs
      """
    And a website "tracker.test" serving:
      """
      tracked you
      """
    And the assistant runs:
      """
      curl -s http://docs.test/
      """
    And the assistant runs:
      """
      curl -s http://tracker.test/
      """
    And the assistant replies "I read the docs"
    When I ask caretz "read the docs" with --network-allow docs.test
    Then caretz finishes successfully
    And what the assistant saw from the command does not contain "tracked you"
    And the website "docs.test" was visited
    And the website "tracker.test" was not visited

  # Test: TestNetworkAllowInConfig (e2e/network_test.go)
  Scenario: I can allow a website permanently in my config
    Given a website "docs.test" serving:
      """
      the docs
      """
    And my caretz config is:
      """
      {"profiles": {"general": {"sandbox": {"networkAllowlist": ["docs.test"]}}}}
      """
    And the assistant runs:
      """
      curl -s http://docs.test/
      """
    And the assistant replies "I read the docs"
    When I ask caretz "read the docs"
    Then caretz finishes successfully
    And what the assistant saw from the command contains "the docs"
    And the website "docs.test" was visited

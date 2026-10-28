package goal

import (
	"fmt"
	"strings"
)

const MaxCheckTimeoutSeconds = 1800

// Outcomes of a deterministic check. No model ever grades a check.
const (
	CheckPassed     = "passed"
	CheckFailed     = "failed"
	CheckInvalid    = "check_invalid"
	CheckEnvBlocked = "env_blocked"
	CheckTailSize   = 1500
)

// Check is a deterministic command the harness runs to grade a task or the
// goal: an argv, or a shell command as an explicit escape hatch. It passes
// when it exits 0.
type Check struct {
	Argv  []string `json:"argv,omitempty"`
	Shell string   `json:"shell,omitempty"`
	// WrittenBy names the check task that writes what this check runs.
	WrittenBy      string `json:"written_by,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func ShellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }

// Empty reports a check with nothing to run.
func (c Check) Empty() bool { return len(c.Argv) == 0 && strings.TrimSpace(c.Shell) == "" }

// Command compiles the check to the shell command the harness runs.
func (c Check) Command() (string, error) {
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > MaxCheckTimeoutSeconds {
		return "", fmt.Errorf("timeout_seconds must be between 0 and %d", MaxCheckTimeoutSeconds)
	}
	switch {
	case len(c.Argv) > 0 && strings.TrimSpace(c.Shell) != "":
		return "", fmt.Errorf("a check sets argv or shell, not both")
	case strings.TrimSpace(c.Shell) != "":
		return c.Shell, nil
	case len(c.Argv) == 0:
		return "", fmt.Errorf("a check needs argv, for example {\"argv\":[\"go\",\"test\",\"./...\"]}")
	case strings.TrimSpace(c.Argv[0]) == "" || strings.ContainsAny(c.Argv[0], "\t\r\n") || (strings.Contains(c.Argv[0], " ") && !strings.Contains(c.Argv[0], "/")):
		return "", fmt.Errorf("argv[0] must be one executable, got %q; put subcommands and options in later argv entries", c.Argv[0])
	}
	parts := make([]string, len(c.Argv))
	for i, arg := range c.Argv {
		parts[i] = ShellQuote(arg)
	}
	return strings.Join(parts, " "), nil
}

// Label is the check as a person would type it.
func (c Check) Label() string {
	if strings.TrimSpace(c.Shell) != "" {
		return "shell: " + c.Shell
	}
	return strings.Join(c.Argv, " ")
}

// Executable is argv[0], or empty for a shell check.
func (c Check) Executable() string {
	if len(c.Argv) == 0 || strings.TrimSpace(c.Shell) != "" {
		return ""
	}
	return c.Argv[0]
}

// Key identifies a check by what it runs, independent of written_by.
func (c Check) Key() string {
	command, err := c.Command()
	if err != nil {
		return "invalid:" + c.Label()
	}
	return command
}

// CheckOutcome is the graded result of running a check.
type CheckOutcome struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Tail   string `json:"tail,omitempty"`
	// Suspect marks a passing check whose output reports a failure; the
	// reviewer and judge weigh it.
	Suspect  bool   `json:"suspect,omitempty"`
	Command  string `json:"-"`
	ExitCode int    `json:"-"`
}

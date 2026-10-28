package output

import (
	"fmt"
	"strings"
)

// Mode selects how agent activity is presented without changing the events
// produced by the agent. Keep parsing here so future presentation modes remain
// an output concern.
type Mode string

const (
	ModeCompact  Mode = "compact"
	ModeDetailed Mode = "detailed"
)

func ParseMode(value string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(value))) {
	case "", ModeCompact:
		return ModeCompact, nil
	case ModeDetailed:
		return ModeDetailed, nil
	default:
		return "", fmt.Errorf("output mode must be compact or detailed, got %q", value)
	}
}

package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalSpinnerRendersAndClears(t *testing.T) {
	var output bytes.Buffer
	spinner := startTerminalSpinner(&output, NewStyleEnabled(false))
	spinner.Stop(&output)

	got := output.String()
	if !strings.Contains(got, "Thinking ") {
		t.Fatalf("spinner output missing activity label: %q", got)
	}
	if !strings.HasSuffix(got, "\r\x1b[2K") {
		t.Fatalf("spinner output was not cleared: %q", got)
	}
}

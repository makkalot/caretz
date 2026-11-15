package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/lib/types"
)

// HandleLoopGuardJudged shows each loop-guard verdict, since compact mode
// hides the tool result that carries a redirect.
func (o *CLIOutput) HandleLoopGuardJudged(e *types.Event) error {
	var record agent.LoopGuardJudged
	if err := json.Unmarshal([]byte(e.Payload), &record); err != nil {
		return err
	}
	writeLoopGuardJudged(os.Stderr, o.stderrStyle, o.mode, record)
	return nil
}

// writeLoopGuardJudged renders one verdict: the outcome on the first line,
// the reason and suggested alternative below, and cost details in detailed
// mode.
func writeLoopGuardJudged(w io.Writer, style Style, mode Mode, record agent.LoopGuardJudged) {
	prefix := style.Agent("[loop-guard]")
	v := record.Verdict
	outcome := truncateLine(record.Outcome(), 170)
	var action string
	switch {
	case record.Escalated:
		action = style.Error(outcome)
	case record.Error != "", record.Redirected, v.Stuck && record.Mode == "shadow":
		action = style.Warning(outcome)
	default:
		action = style.Muted(outcome)
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", prefix, action)
	if record.Error != "" {
		return
	}
	if v.Reason != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", style.Muted("reason: "+truncateLine(v.Reason, 200)))
	}
	if v.Stuck && v.Alternative != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", style.Muted("try instead: "+truncateLine(v.Alternative, 200)))
	}
	if mode == ModeDetailed {
		details := []string{fmt.Sprintf("%dms", record.LatencyMS), fmt.Sprintf("%d tokens", record.Usage.TotalTokens)}
		if record.Model != "" {
			details = append([]string{record.Model}, details...)
		}
		_, _ = fmt.Fprintf(w, "  %s\n", style.Muted("judge: "+strings.Join(details, " · ")))
	}
}

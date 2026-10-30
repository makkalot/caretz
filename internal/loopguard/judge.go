package loopguard

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// JudgeSystemPrompt asks for a strict verdict. The key distinction is spelled
// out because false positives (interrupting real progress) cost more than a
// missed loop.
const JudgeSystemPrompt = `You review a coding agent's recent tool activity and decide whether the current turn is stuck in a failure loop.

Moving through different errors is progress. The same error returning after attempted fixes is stuck. Slowly converging (fewer failures, new files, new errors) is not stuck. When unsure, answer not stuck.

If stuck, name the likely root cause and one concrete, different approach (for example a missing dependency, a wrong file, a wrong command, or a wrong assumption to check), not "try another approach".

Reply with only a JSON object:
{"stuck": true|false, "confidence": 0.0-1.0, "reason": "one sentence", "alternative": "one concrete different approach, or empty"}`

// Verdict is the judge's answer. Anything that does not parse is not stuck.
type Verdict struct {
	Stuck       bool    `json:"stuck"`
	Confidence  float64 `json:"confidence"`
	Reason      string  `json:"reason"`
	Alternative string  `json:"alternative"`
}

// ParseVerdict reads the first JSON object in a reply, tolerating code
// fences or prose around it.
func ParseVerdict(reply string) (Verdict, error) {
	start := strings.Index(reply, "{")
	end := strings.LastIndex(reply, "}")
	if start < 0 || end < start {
		return Verdict{}, fmt.Errorf("no JSON object in judge reply")
	}
	var v Verdict
	if err := json.Unmarshal([]byte(reply[start:end+1]), &v); err != nil {
		return Verdict{}, fmt.Errorf("malformed judge verdict: %w", err)
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return Verdict{}, fmt.Errorf("judge confidence %v out of range", v.Confidence)
	}
	v.Reason = strings.TrimSpace(v.Reason)
	v.Alternative = strings.TrimSpace(v.Alternative)
	return v, nil
}

// Digest is the judge's input: a compact view of the turn rather than the
// transcript, so the judge sees the pattern instead of re-living the work.
func (g *Guard) Digest() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s\n\nTrigger: %s\n\n", g.task, g.trigger)

	calls := g.calls
	if len(calls) > recentCallsInDigest {
		fmt.Fprintf(&b, "Recent tool calls (last %d of %d):\n", recentCallsInDigest, len(calls))
		calls = calls[len(calls)-recentCallsInDigest:]
	} else {
		b.WriteString("Tool calls:\n")
	}
	for i, c := range calls {
		status := "ok"
		if c.failed {
			status = "FAILED: " + c.fingerprint
		}
		fmt.Fprintf(&b, "%d. %s %s → %s\n", i+1, c.tool, c.args, status)
	}

	if fps := g.topFailures(topFailuresInDigest); len(fps) > 0 {
		b.WriteString("\nMost repeated failures:\n")
		for _, fp := range fps {
			fmt.Fprintf(&b, "- %d× %s\n  example: %s\n", g.counts[fp], fp, strings.ReplaceAll(g.examples[fp], "\n", "\n  "))
		}
	}

	if len(g.edits) > 0 {
		paths := make([]string, 0, len(g.edits))
		for path := range g.edits {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		b.WriteString("\nFiles edited:\n")
		for _, path := range paths {
			fmt.Fprintf(&b, "- %s (%d edits)\n", path, g.edits[path])
		}
	}
	return b.String()
}

package loopguard

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// JudgeSystemPrompt asks for a strict verdict. The key distinction is spelled
// out because false positives (interrupting real progress) cost more than a
// missed loop.
const JudgeSystemPrompt = `You review a coding agent's recent tool activity and decide whether the current turn is stuck or has gone off the rails.

Stuck means one of:
- a failure loop: the same error returning after attempted fixes;
- going in circles: re-reading the same files or re-running similar commands without learning anything new;
- drifting: doing work the task does not need, or wandering far from it.

Use what you are given: compare what the agent says it is doing with the task and with its actual calls; weigh the effort so far (calls, time, tokens) against the size of the task; and if earlier checks this turn named a concern, consider whether the agent is still on it.

Moving through different errors is progress. Slowly converging (fewer failures, new files, new errors) is not stuck. A long turn that keeps doing distinct, relevant work is not stuck. A tool call marked "ok" only means it did not error; it may not have succeeded at what the agent wanted. When unsure, answer not stuck.

If stuck, name the likely root cause and one concrete, different approach (for example a missing dependency, a wrong file, a wrong command, a wrong assumption to check, or the part of the task to return to), not "try another approach".

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
	fmt.Fprintf(&b, "Task: %s\n", g.task)
	if len(g.task) < followUpChars && (g.previousPrompt != "" || g.previousAnswer != "") {
		b.WriteString("This message follows up on the previous exchange:\n")
		if g.previousPrompt != "" {
			fmt.Fprintf(&b, "- previous user message: %s\n", g.previousPrompt)
		}
		if g.previousAnswer != "" {
			fmt.Fprintf(&b, "- previous agent answer (start): %s\n", g.previousAnswer)
		}
	}
	fmt.Fprintf(&b, "\nTrigger: %s\n\n", g.trigger)
	b.WriteString(g.turnSoFar())

	if len(g.notes) > 0 {
		b.WriteString("\nWhat the agent said it is doing (latest last):\n")
		for _, note := range g.notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}

	if len(g.verdicts) > 0 {
		b.WriteString("\nEarlier checks this turn:\n")
		verdicts := g.verdicts
		if len(verdicts) > maxVerdictsInDigest {
			verdicts = verdicts[len(verdicts)-maxVerdictsInDigest:]
		}
		for _, p := range verdicts {
			outcome := "not stuck"
			if p.verdict.Stuck {
				outcome = "stuck"
			}
			fmt.Fprintf(&b, "- after call %d (%s): %s (%.2f): %s\n", p.atCall, p.trigger, outcome, p.verdict.Confidence, p.verdict.Reason)
		}
	}
	b.WriteString("\n")

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

	if repeated := g.repeatedCalls(topFailuresInDigest + 2); len(repeated) > 0 {
		b.WriteString("\nRepeated identical calls:\n")
		for _, key := range repeated {
			fmt.Fprintf(&b, "- %d× %s\n", g.callCounts[key], key)
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

// turnSoFar summarizes the whole turn, not just the recent calls: its size,
// time and tokens, calls per tool, and the files it touched.
func (g *Guard) turnSoFar() string {
	var b strings.Builder
	failed := 0
	perTool := map[string]int{}
	failedPerTool := map[string]int{}
	var tools []string
	for _, c := range g.calls {
		if perTool[c.tool] == 0 {
			tools = append(tools, c.tool)
		}
		perTool[c.tool]++
		if c.failed {
			failed++
			failedPerTool[c.tool]++
		}
	}
	fmt.Fprintf(&b, "Turn so far: %d tool calls (%d failed) over %s", len(g.calls), failed, time.Since(g.started).Round(time.Second))
	if g.tokens > 0 {
		fmt.Fprintf(&b, ", %d tokens used", g.tokens)
	}
	b.WriteString("\n")
	if len(tools) > 0 {
		sort.Slice(tools, func(i, j int) bool {
			if perTool[tools[i]] != perTool[tools[j]] {
				return perTool[tools[i]] > perTool[tools[j]]
			}
			return tools[i] < tools[j]
		})
		parts := make([]string, 0, len(tools))
		for _, tool := range tools {
			part := fmt.Sprintf("%s %d", tool, perTool[tool])
			if n := failedPerTool[tool]; n > 0 {
				part += fmt.Sprintf(" (%d failed)", n)
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(&b, "Calls per tool: %s\n", strings.Join(parts, ", "))
	}
	if len(g.files) > 0 {
		files := g.files
		more := ""
		if len(files) > maxFilesInDigest {
			more = fmt.Sprintf(" (+%d more)", len(files)-maxFilesInDigest)
			files = files[:maxFilesInDigest]
		}
		fmt.Fprintf(&b, "Files touched (%d): %s%s\n", len(g.files), strings.Join(files, ", "), more)
	}
	return b.String()
}

// repeatedCalls returns up to n tool calls made more than once with the same
// arguments, most repeated first.
func (g *Guard) repeatedCalls(n int) []string {
	var keys []string
	for key, count := range g.callCounts {
		if count > 1 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if g.callCounts[keys[i]] != g.callCounts[keys[j]] {
			return g.callCounts[keys[i]] > g.callCounts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

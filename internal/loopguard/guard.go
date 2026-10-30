// Package loopguard notices when an agent turn keeps hitting the same
// failure, asks a judge model whether the turn is stuck, and produces one
// redirect note for the agent. It holds no LLM client: the agent loop makes
// the judge call, so this package stays pure and cheap on every tool result.
package loopguard

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Mode string

const (
	ModeOff    Mode = "off"
	ModeShadow Mode = "shadow"
	ModeActive Mode = "active"
)

// ParseMode accepts the config and flag spelling of a mode; empty is off.
func ParseMode(value string) (Mode, error) {
	switch mode := Mode(strings.ToLower(strings.TrimSpace(value))); mode {
	case "", ModeOff:
		return ModeOff, nil
	case ModeShadow, ModeActive:
		return mode, nil
	default:
		return ModeOff, fmt.Errorf("invalid loop guard mode %q (want off, shadow or active)", value)
	}
}

const (
	defaultRepeatThreshold = 3
	defaultErrorWindow     = 12
	defaultErrorRate       = 0.65
	defaultConfidence      = 0.7
	defaultMaxRedirects    = 1
	defaultJudgeTimeout    = 15 * time.Second
	recentCallsInDigest    = 12
	topFailuresInDigest    = 3
	maxTaskChars           = 600
	maxArgsChars           = 120
)

// Config is the guard's policy. Zero values take the defaults above, so only
// Mode must be set to turn the guard on.
type Config struct {
	Mode            Mode
	RepeatThreshold int
	JudgeTimeout    time.Duration
	// JudgeModel and JudgeReasoning pick the judge; an empty model uses the
	// session's current model.
	JudgeModel     string
	JudgeReasoning string
}

func (c Config) Enabled() bool { return c.Mode == ModeShadow || c.Mode == ModeActive }

func (c Config) WithDefaults() Config {
	if c.RepeatThreshold <= 1 {
		c.RepeatThreshold = defaultRepeatThreshold
	}
	if c.JudgeTimeout <= 0 {
		c.JudgeTimeout = defaultJudgeTimeout
	}
	return c
}

type call struct {
	tool, args, fingerprint string
	failed                  bool
}

// Guard is one turn's state. The agent loop runs tool calls sequentially, so
// it is used from one goroutine and needs no locking.
type Guard struct {
	cfg      Config
	task     string
	calls    []call
	counts   map[string]int
	examples map[string]string
	edits    map[string]int
	// judgedAt is each fingerprint's count when the judge last ran, and
	// callsAtJudge the call count; together they are the cooldown.
	judgedAt     map[string]int
	callsAtJudge int
	trigger      string
	redirects    int
	escalated    bool
}

func New(cfg Config, task string) *Guard {
	return &Guard{
		cfg:      cfg.WithDefaults(),
		task:     truncate(strings.TrimSpace(task), maxTaskChars),
		counts:   map[string]int{},
		examples: map[string]string{},
		edits:    map[string]int{},
		judgedAt: map[string]int{},
	}
}

// Observe records one tool result and arms the judge when a trigger fires.
func (g *Guard) Observe(tool, arguments, content string, isErr bool) {
	fp, failed := Classify(tool, content, isErr)
	g.calls = append(g.calls, call{tool: tool, args: summarizeArgs(arguments), fingerprint: fp, failed: failed})
	if failed {
		g.counts[fp]++
		if _, ok := g.examples[fp]; !ok {
			g.examples[fp] = truncate(strings.TrimSpace(content), 400)
		}
	} else if tool == "Edit" || tool == "Write" {
		if path := filePath(arguments); path != "" {
			g.edits[path]++
		}
	}
	if g.trigger == "" {
		g.trigger = g.evaluate()
	}
}

// evaluate returns the trigger that fired, or "". A fingerprint re-arms only
// after it repeats another RepeatThreshold times since the last judgment, and
// the error-rate trigger only after a full window of new calls.
func (g *Guard) evaluate() string {
	threshold := g.cfg.RepeatThreshold
	for _, fp := range g.topFailures(len(g.counts)) {
		if n := g.counts[fp]; n >= threshold && n >= g.judgedAt[fp]+threshold {
			return fmt.Sprintf("repeat: %d× %s", n, fp)
		}
	}
	if len(g.calls)-g.callsAtJudge < defaultErrorWindow {
		return ""
	}
	failed := 0
	for _, c := range g.calls[len(g.calls)-defaultErrorWindow:] {
		if c.failed {
			failed++
		}
	}
	if rate := float64(failed) / defaultErrorWindow; rate >= defaultErrorRate {
		return fmt.Sprintf("error-rate: %d/%d recent calls failed", failed, defaultErrorWindow)
	}
	return ""
}

// Trigger reports the armed trigger, if any; the loop judges when it is set.
func (g *Guard) Trigger() string { return g.trigger }

// Judged disarms the trigger and starts the cooldown, whatever the verdict.
func (g *Guard) Judged() {
	for fp, n := range g.counts {
		g.judgedAt[fp] = n
	}
	g.callsAtJudge = len(g.calls)
	g.trigger = ""
}

// Action is what the loop does with a verdict.
type Action struct {
	// Redirect is a note to append to the tool result the agent sees next.
	Redirect string
	// Escalate is a notice for the user: the turn still looks stuck after
	// the redirect cap.
	Escalate string
}

// Decide turns a verdict into at most one action. Shadow mode never acts.
func (g *Guard) Decide(v Verdict) Action {
	if g.cfg.Mode != ModeActive || !v.Stuck || v.Confidence < defaultConfidence {
		return Action{}
	}
	if g.redirects < defaultMaxRedirects {
		g.redirects++
		return Action{Redirect: g.redirectNote(v)}
	}
	if g.escalated {
		return Action{}
	}
	g.escalated = true
	return Action{Escalate: "this turn still looks stuck after a redirect: " + v.Reason + " — stop it if it isn't making progress."}
}

func (g *Guard) redirectNote(v Verdict) string {
	var b strings.Builder
	b.WriteString("\n\n[loop guard] This turn appears to be stuck in a failure loop.")
	if fps := g.topFailures(1); len(fps) == 1 && g.counts[fps[0]] > 1 {
		fmt.Fprintf(&b, " The same failure has occurred %d times: %s.", g.counts[fps[0]], fps[0])
	}
	b.WriteString(" Stop retrying the same fix.")
	if v.Reason != "" {
		b.WriteString(" Diagnosis: " + v.Reason)
	}
	if v.Alternative != "" {
		b.WriteString("\nTry instead: " + v.Alternative)
	}
	return b.String()
}

// topFailures returns failure fingerprints by count, then by name for a
// stable order.
func (g *Guard) topFailures(n int) []string {
	fps := make([]string, 0, len(g.counts))
	for fp := range g.counts {
		fps = append(fps, fp)
	}
	sort.Slice(fps, func(i, j int) bool {
		if g.counts[fps[i]] != g.counts[fps[j]] {
			return g.counts[fps[i]] > g.counts[fps[j]]
		}
		return fps[i] < fps[j]
	})
	if len(fps) > n {
		fps = fps[:n]
	}
	return fps
}

func summarizeArgs(arguments string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(arguments), &fields) == nil {
		for _, key := range []string{"command", "file_path", "path", "pattern", "query", "url"} {
			if value, ok := fields[key].(string); ok && value != "" {
				return truncate(strings.Join(strings.Fields(value), " "), maxArgsChars)
			}
		}
	}
	return truncate(strings.Join(strings.Fields(arguments), " "), maxArgsChars)
}

func filePath(arguments string) string {
	var args struct {
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal([]byte(arguments), &args)
	return args.FilePath
}

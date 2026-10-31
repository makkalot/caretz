// Package loopguard notices when an agent turn keeps hitting the same
// failure, asks a judge model whether the turn is stuck, and produces one
// redirect note for the agent, then a wrap-up if the turn stays stuck. It
// holds no LLM client: the agent loop makes the judge call, so this package
// stays pure and cheap on every tool result.
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

// DefaultCheckEvery is how many tool calls a turn may make before the judge
// looks at it, failures or not, when the setting is not configured.
const DefaultCheckEvery = 20

const (
	defaultRepeatThreshold = 3
	defaultErrorWindow     = 12
	defaultErrorRate       = 0.65
	defaultConfidence      = 0.7
	defaultMaxRedirects    = 1
	defaultJudgeTimeout    = time.Minute
	recentCallsInDigest    = 12
	topFailuresInDigest    = 3
	maxTaskChars           = 600
	maxArgsChars           = 120
	// A turn message this short is a follow-up such as "continue", so the
	// digest adds the exchange before it.
	followUpChars       = 200
	maxPreviousAnswer   = 400
	maxAssistantNotes   = 3
	maxAssistantChars   = 300
	maxFilesInDigest    = 10
	maxVerdictsInDigest = 5
)

// Config is the guard's policy. Zero values take the defaults above, so only
// Mode must be set to turn the guard on.
type Config struct {
	Mode            Mode
	RepeatThreshold int
	// CheckEvery asks the judge to look at the turn after this many tool
	// calls since its last check, even without failures; 0 turns it off.
	CheckEvery   int
	JudgeTimeout time.Duration
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
	started  time.Time
	calls    []call
	counts   map[string]int
	examples map[string]string
	edits    map[string]int
	// files are the paths the turn read or wrote, in first-touch order.
	files     []string
	filesSeen map[string]bool
	// previousPrompt and previousAnswer are the exchange before a follow-up
	// task such as "continue", which says nothing about the goal on its own.
	previousPrompt, previousAnswer string
	// notes are the agent's latest words alongside its tool calls, so the
	// judge can compare what it says it is doing with what it does.
	notes    []string
	tokens   int64
	verdicts []pastVerdict
	// callCounts counts identical calls (tool and argument summary), so the
	// judge can see a turn going in circles without failing.
	callCounts map[string]int
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
		cfg:        cfg.WithDefaults(),
		task:       truncate(strings.TrimSpace(task), maxTaskChars),
		started:    time.Now(),
		counts:     map[string]int{},
		examples:   map[string]string{},
		edits:      map[string]int{},
		filesSeen:  map[string]bool{},
		callCounts: map[string]int{},
		judgedAt:   map[string]int{},
	}
}

type pastVerdict struct {
	atCall  int
	trigger string
	verdict Verdict
}

// SetPrevious records the exchange before this turn. The digest shows it
// only when the turn's own message is a short follow-up.
func (g *Guard) SetPrevious(prompt, answer string) {
	g.previousPrompt = truncate(strings.TrimSpace(prompt), maxTaskChars)
	g.previousAnswer = truncate(strings.TrimSpace(answer), maxPreviousAnswer)
}

// ObserveResponse records what the agent said alongside a batch of tool calls
// and the turn's token use so far.
func (g *Guard) ObserveResponse(text string, totalTokens int64) {
	g.tokens = totalTokens
	if text = strings.Join(strings.Fields(text), " "); text == "" {
		return
	}
	g.notes = append(g.notes, truncate(text, maxAssistantChars))
	if len(g.notes) > maxAssistantNotes {
		g.notes = g.notes[len(g.notes)-maxAssistantNotes:]
	}
}

// Observe records one tool result and arms the judge when a trigger fires.
func (g *Guard) Observe(tool, arguments, content string, isErr bool) {
	fp, failed := Classify(tool, content, isErr)
	args := summarizeArgs(arguments)
	g.calls = append(g.calls, call{tool: tool, args: args, fingerprint: fp, failed: failed})
	g.callCounts[tool+" "+args]++
	path := filePath(arguments)
	if path != "" && !g.filesSeen[path] {
		g.filesSeen[path] = true
		g.files = append(g.files, path)
	}
	if failed {
		g.counts[fp]++
		if _, ok := g.examples[fp]; !ok {
			g.examples[fp] = truncate(strings.TrimSpace(content), 400)
		}
	} else if (tool == "Edit" || tool == "Write") && path != "" {
		g.edits[path]++
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
	sinceJudge := len(g.calls) - g.callsAtJudge
	if sinceJudge >= defaultErrorWindow {
		failed := 0
		for _, c := range g.calls[len(g.calls)-defaultErrorWindow:] {
			if c.failed {
				failed++
			}
		}
		if rate := float64(failed) / defaultErrorWindow; rate >= defaultErrorRate {
			return fmt.Sprintf("error-rate: %d/%d recent calls failed", failed, defaultErrorWindow)
		}
	}
	if every := g.cfg.CheckEvery; every > 0 && sinceJudge >= every {
		return fmt.Sprintf("checkpoint: %d tool calls since the last check", sinceJudge)
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
	// Note is a message for the agent. The loop keeps it at the end of the
	// agent's requests for the rest of the turn, replacing any earlier note.
	Note string
	// WrapUp ends the turn: the agent's next request carries no tools, so
	// it has to answer with what it has.
	WrapUp bool
	// Escalate is a notice for the user: the turn still looks stuck after
	// the redirect cap, so the guard is ending it.
	Escalate string
}

// Decide records a verdict for later digests and turns it into at most one
// action: a redirect note first, then, if the turn still looks stuck, a
// wrap-up. Shadow mode never acts.
func (g *Guard) Decide(v Verdict) Action {
	g.verdicts = append(g.verdicts, pastVerdict{atCall: len(g.calls), trigger: g.trigger, verdict: v})
	if g.cfg.Mode != ModeActive || !v.Stuck || v.Confidence < defaultConfidence {
		return Action{}
	}
	if g.redirects < defaultMaxRedirects {
		g.redirects++
		return Action{Note: g.redirectNote(v)}
	}
	if g.escalated {
		return Action{}
	}
	g.escalated = true
	return Action{
		Note:     g.wrapUpNote(v),
		WrapUp:   true,
		Escalate: "this turn still looks stuck after a redirect: " + v.Reason + " — asked the agent to stop and report.",
	}
}

// noteHeader marks a note as coming from caretz, not the user, so the agent
// neither mistakes it for a new request nor dismisses it as tool output.
const noteHeader = "[loop guard] Automated message from caretz, not from the user. A reviewer model checked this turn's tool activity"

func (g *Guard) redirectNote(v Verdict) string {
	var b strings.Builder
	b.WriteString(noteHeader + " and judged it stuck or off track.")
	if fps := g.topFailures(1); len(fps) == 1 && g.counts[fps[0]] > 1 {
		fmt.Fprintf(&b, " The same failure has occurred %d times: %s.", g.counts[fps[0]], fps[0])
	}
	g.writeVerdict(&b, v)
	g.writeTask(&b)
	b.WriteString("\nStop repeating what isn't working and get back to that request. Before your next tool call, say in one sentence what you will do differently. This note stays at the end of the conversation for the rest of this turn.")
	return b.String()
}

func (g *Guard) wrapUpNote(v Verdict) string {
	var b strings.Builder
	b.WriteString(noteHeader + " again and judged it still stuck after an earlier redirect.")
	g.writeVerdict(&b, v)
	g.writeTask(&b)
	b.WriteString("\nThis turn is ending: tools are no longer available. Reply to the user now with what you changed and verified, what is still unfinished or uncertain, and what you need from them to continue.")
	return b.String()
}

func (g *Guard) writeVerdict(b *strings.Builder, v Verdict) {
	if v.Reason != "" {
		b.WriteString("\nDiagnosis: " + v.Reason)
	}
	if v.Alternative != "" {
		b.WriteString("\nTry instead: " + v.Alternative)
	}
}

// writeTask restates the request the turn is for, with the one before it
// when the turn's message is a short follow-up such as "continue".
func (g *Guard) writeTask(b *strings.Builder) {
	if g.task == "" {
		return
	}
	fmt.Fprintf(b, "\nThe user's request for this turn: %q", g.task)
	if len(g.task) < followUpChars && g.previousPrompt != "" {
		fmt.Fprintf(b, ", following up on: %q", g.previousPrompt)
	}
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

package loopguard

import (
	"strings"
	"testing"
)

const bashArgs = `{"command":"go test ./..."}`

func TestClassifyNormalizesVolatileParts(t *testing.T) {
	a, failedA := Classify("Bash", "--- FAIL: TestX (0.12s)\n/tmp/build123/x_test.go:41:7: undefined: Foo\nerror: exit status 1", false)
	b, failedB := Classify("Bash", "--- FAIL: TestX (3.40s)\n/tmp/build987/x_test.go:52:3: undefined: Foo\nerror: exit status 1", false)
	if !failedA || !failedB {
		t.Fatalf("both results should be failures")
	}
	if a != b {
		t.Fatalf("fingerprints differ:\n%s\n%s", a, b)
	}
	for _, volatile := range []string{"build123", "41", "0.12s"} {
		if strings.Contains(a, volatile) {
			t.Fatalf("fingerprint %q keeps volatile %q", a, volatile)
		}
	}
}

func TestClassifyKeepsDistinctErrorsDistinct(t *testing.T) {
	a, _ := Classify("Bash", "/repo/pkg/a.go:3:1: undefined: Foo\nerror: exit status 1", false)
	b, _ := Classify("Bash", "/repo/pkg/a.go:3:1: undefined: Bar\nerror: exit status 1", false)
	c, _ := Classify("Bash", "/repo/pkg/b.go:3:1: undefined: Foo\nerror: exit status 1", false)
	if a == b || a == c {
		t.Fatalf("distinct errors collapsed: %q %q %q", a, b, c)
	}
}

func TestClassifyBashShapes(t *testing.T) {
	cases := []struct {
		content string
		failed  bool
	}{
		{"ok  \tpkg\t0.1s\n", false},
		{"sandbox error: ls: x: No such file or directory", true},
		{"out\nsandbox error: boom", true},
		{"out\nerror: exit status 2\n", true},
		{"the word error: appears mid output\nbut it passed", false},
	}
	for _, tc := range cases {
		if _, failed := Classify("Bash", tc.content, false); failed != tc.failed {
			t.Errorf("Classify(%q) failed=%v, want %v", tc.content, failed, tc.failed)
		}
	}
	if _, failed := Classify("Read", "error: file not found", false); failed {
		t.Errorf("non-Bash success content should not count as failure")
	}
	if _, failed := Classify("Edit", "error: old_string not found", true); !failed {
		t.Errorf("tool error should count as failure")
	}
}

func failBash(g *Guard, n int, msg string) {
	for i := 0; i < n; i++ {
		g.Observe("Bash", bashArgs, msg+"\nerror: exit status 1", false)
	}
}

func TestRepeatTriggerAndCooldown(t *testing.T) {
	g := New(Config{Mode: ModeActive}, "fix the test")
	failBash(g, 2, "undefined: Foo")
	if g.Trigger() != "" {
		t.Fatalf("triggered after 2 repeats: %s", g.Trigger())
	}
	failBash(g, 1, "undefined: Foo")
	if !strings.HasPrefix(g.Trigger(), "repeat: 3×") {
		t.Fatalf("want repeat trigger, got %q", g.Trigger())
	}
	g.Judged()
	failBash(g, 2, "undefined: Foo")
	if g.Trigger() != "" {
		t.Fatalf("re-armed before new evidence: %s", g.Trigger())
	}
	failBash(g, 1, "undefined: Foo")
	if !strings.HasPrefix(g.Trigger(), "repeat: 6×") {
		t.Fatalf("want re-arm at 6, got %q", g.Trigger())
	}
}

func TestProgressingTurnStaysQuiet(t *testing.T) {
	g := New(Config{Mode: ModeActive}, "fix the build")
	for _, msg := range []string{"undefined: A", "undefined: B", "undefined: C", "cannot use x", "missing return"} {
		failBash(g, 1, msg)
		g.Observe("Edit", `{"file_path":"a.go"}`, "ok", false)
	}
	if g.Trigger() != "" {
		t.Fatalf("progressing turn triggered: %s", g.Trigger())
	}
}

func TestErrorRateTrigger(t *testing.T) {
	g := New(Config{Mode: ModeShadow}, "task")
	for i := 0; i < defaultErrorWindow; i++ {
		if i%4 == 0 {
			g.Observe("Read", `{"file_path":"a.go"}`, "content", false)
			continue
		}
		failBash(g, 1, "distinct failure "+string(rune('a'+i)))
	}
	if !strings.HasPrefix(g.Trigger(), "error-rate:") {
		t.Fatalf("want error-rate trigger, got %q", g.Trigger())
	}
}

func TestCheckpointTriggerWithoutFailures(t *testing.T) {
	g := New(Config{Mode: ModeActive, CheckEvery: 4}, "refactor the parser")
	for i := 0; i < 3; i++ {
		g.Observe("Read", `{"file_path":"parser.go"}`, "content", false)
	}
	if g.Trigger() != "" {
		t.Fatalf("triggered before the checkpoint: %s", g.Trigger())
	}
	g.Observe("Read", `{"file_path":"parser.go"}`, "content", false)
	if !strings.HasPrefix(g.Trigger(), "checkpoint: 4 tool calls") {
		t.Fatalf("want checkpoint trigger, got %q", g.Trigger())
	}
	if d := g.Digest(); !strings.Contains(d, "Repeated identical calls:\n- 4× Read parser.go") {
		t.Fatalf("digest does not show repeated calls:\n%s", d)
	}
	g.Judged()
	for i := 0; i < 3; i++ {
		g.Observe("Grep", `{"pattern":"x"}`, "match", false)
	}
	if g.Trigger() != "" {
		t.Fatalf("checkpoint re-armed early: %s", g.Trigger())
	}
	g.Observe("Grep", `{"pattern":"x"}`, "match", false)
	if g.Trigger() == "" {
		t.Fatal("checkpoint did not re-arm after another 4 calls")
	}
}

func TestCheckpointOffByDefaultInConfig(t *testing.T) {
	g := New(Config{Mode: ModeActive}, "task")
	for i := 0; i < 30; i++ {
		g.Observe("Read", `{"file_path":"a.go"}`, "content", false)
	}
	if g.Trigger() != "" {
		t.Fatalf("CheckEvery 0 triggered: %s", g.Trigger())
	}
}

func TestDecideRedirectThenWrapUp(t *testing.T) {
	g := New(Config{Mode: ModeActive}, "make the tests pass")
	failBash(g, 3, "undefined: Foo")
	stuck := Verdict{Stuck: true, Confidence: 0.9, Reason: "Foo is never defined", Alternative: "add the missing import"}

	if a := g.Decide(Verdict{Stuck: true, Confidence: 0.5}); a != (Action{}) {
		t.Fatalf("low confidence acted: %+v", a)
	}
	first := g.Decide(stuck)
	if first.WrapUp || first.Escalate != "" {
		t.Fatalf("first verdict should only redirect: %+v", first)
	}
	for _, want := range []string{"not from the user", "3 times", "Diagnosis: Foo is never defined", "Try instead: add the missing import", `request for this turn: "make the tests pass"`} {
		if !strings.Contains(first.Note, want) {
			t.Fatalf("redirect lacks %q:\n%s", want, first.Note)
		}
	}
	second := g.Decide(stuck)
	if !second.WrapUp || second.Escalate == "" || !strings.Contains(second.Note, "tools are no longer available") {
		t.Fatalf("want wrap-up after cap, got %+v", second)
	}
	if third := g.Decide(stuck); third != (Action{}) {
		t.Fatalf("escalated twice: %+v", third)
	}
}

func TestShadowModeNeverActs(t *testing.T) {
	g := New(Config{Mode: ModeShadow}, "task")
	if a := g.Decide(Verdict{Stuck: true, Confidence: 1}); a != (Action{}) {
		t.Fatalf("shadow mode acted: %+v", a)
	}
}

func TestParseVerdict(t *testing.T) {
	v, err := ParseVerdict("```json\n{\"stuck\": true, \"confidence\": 0.8, \"reason\": \" r \", \"alternative\": \"a\"}\n```")
	if err != nil || !v.Stuck || v.Confidence != 0.8 || v.Reason != "r" {
		t.Fatalf("got %+v, %v", v, err)
	}
	for _, bad := range []string{"", "not json", `{"stuck": "yes"}`, `{"stuck": true, "confidence": 7}`} {
		if _, err := ParseVerdict(bad); err == nil {
			t.Errorf("ParseVerdict(%q) should fail", bad)
		}
	}
}

func TestDigestNamesFailuresAndEdits(t *testing.T) {
	g := New(Config{Mode: ModeActive}, "make tests pass")
	g.Observe("Edit", `{"file_path":"pkg/a.go"}`, "ok", false)
	failBash(g, 3, "undefined: Foo")
	d := g.Digest()
	for _, want := range []string{"make tests pass", "3× Bash:", "pkg/a.go (1 edits)", "go test ./..."} {
		if !strings.Contains(d, want) {
			t.Errorf("digest missing %q:\n%s", want, d)
		}
	}
}

func TestDigestShowsWholeTurnIntentAndEarlierChecks(t *testing.T) {
	g := New(Config{Mode: ModeShadow, CheckEvery: 3}, "add a --json flag to the export command")
	for i, note := range []string{"Reading the export command.", "Checking how other flags are parsed.", "Looking at the CI config.", "Reviewing the release workflow."} {
		g.ObserveResponse(note, int64(1000*(i+1)))
	}
	g.Observe("Read", `{"file_path":"cmd/export.go"}`, "content", false)
	g.Observe("Read", `{"file_path":".github/workflows/ci.yml"}`, "content", false)
	failBash(g, 1, "no such file")
	g.Decide(Verdict{Confidence: 0.6, Reason: "still exploring the command"})
	g.Judged()

	d := g.Digest()
	for _, want := range []string{
		"Turn so far: 3 tool calls (1 failed) over ",
		", 4000 tokens used",
		"Calls per tool: Read 2, Bash 1 (1 failed)",
		"Files touched (2): cmd/export.go, .github/workflows/ci.yml",
		"What the agent said it is doing (latest last):\n- Checking how other flags are parsed.\n- Looking at the CI config.\n- Reviewing the release workflow.",
		"Earlier checks this turn:\n- after call 3 (checkpoint: 3 tool calls since the last check): not stuck (0.60): still exploring the command",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("digest missing %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "Reading the export command.") {
		t.Errorf("digest keeps more than %d agent notes:\n%s", maxAssistantNotes, d)
	}
}

func TestDigestAddsPreviousExchangeOnlyForFollowUps(t *testing.T) {
	short := New(Config{Mode: ModeShadow}, "continue")
	short.SetPrevious("migrate the users table to the new schema", "I updated the migration; next I'll verify it locally.")
	d := short.Digest()
	if !strings.Contains(d, "previous user message: migrate the users table") || !strings.Contains(d, "previous agent answer (start): I updated the migration") {
		t.Fatalf("follow-up digest lacks the previous exchange:\n%s", d)
	}

	long := New(Config{Mode: ModeShadow}, strings.Repeat("a detailed task description ", 10))
	long.SetPrevious("earlier task", "earlier answer")
	if d := long.Digest(); strings.Contains(d, "earlier task") {
		t.Fatalf("self-contained task still shows the previous exchange:\n%s", d)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeOff, "off": ModeOff, "Shadow": ModeShadow, " active ": ModeActive} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseMode("on"); err == nil {
		t.Errorf("ParseMode(on) should fail")
	}
}

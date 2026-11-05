package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/loopguard"
	"github.com/makkalot/caretz/lib/llm"
)

func TestWriteLoopGuardJudged(t *testing.T) {
	stuck := loopguard.Verdict{Stuck: true, Confidence: 0.9, Reason: "Foo is never declared", Alternative: "add the import"}
	cases := []struct {
		name   string
		mode   Mode
		record agent.LoopGuardJudged
		want   []string
		absent []string
	}{
		{"redirected", ModeCompact, agent.LoopGuardJudged{Mode: "active", Verdict: stuck, Redirected: true},
			[]string{"[loop-guard] stuck (0.90) → redirected the agent", "reason: Foo is never declared", "try instead: add the import"}, []string{"judge:"}},
		{"shadow", ModeCompact, agent.LoopGuardJudged{Mode: "shadow", Verdict: stuck},
			[]string{"stuck (0.90) → shadow mode, no redirect"}, nil},
		{"escalated", ModeCompact, agent.LoopGuardJudged{Mode: "active", Verdict: stuck, Escalated: true},
			[]string{"still stuck after redirect, ending the turn"}, nil},
		{"not stuck", ModeCompact, agent.LoopGuardJudged{Mode: "active", Verdict: loopguard.Verdict{Confidence: 0.3, Reason: "new errors each run", Alternative: "ignored"}},
			[]string{"[loop-guard] not stuck (0.30)", "reason: new errors each run"}, []string{"try instead"}},
		{"error", ModeCompact, agent.LoopGuardJudged{Mode: "active", Error: "context deadline exceeded"},
			[]string{"judge failed, ignored: context deadline exceeded"}, []string{"stuck"}},
		{"detailed", ModeDetailed, agent.LoopGuardJudged{Mode: "active", Verdict: stuck, Redirected: true, Model: "judge-model", LatencyMS: 1200, Usage: llm.Usage{TotalTokens: 850}},
			[]string{"judge: judge-model · 1200ms · 850 tokens"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeLoopGuardJudged(&buf, NewStyleEnabled(false), tc.mode, tc.record)
			got := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("output has %q:\n%s", absent, got)
				}
			}
		})
	}
}

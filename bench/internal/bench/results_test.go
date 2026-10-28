package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractUsageFromContextEvents(t *testing.T) {
	sessionDir := t.TempDir()
	data := `{"event_type":"LLMAgentMessage.Created","payload":{"usage":{"PromptTokens":10,"CompletionTokens":5,"TotalTokens":15}}}
{"event_type":"LLMToolCallResult.Created","payload":{"tool_call":{"Name":"Bash"}}}
{"event_type":"LLMAgentResponse.Created","payload":{"usage":{"PromptTokens":20,"CompletionTokens":7,"TotalTokens":27}}}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "context.jsonpl"), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	usage, err := ExtractUsage(sessionDir)
	if err != nil {
		t.Fatalf("ExtractUsage() error = %v", err)
	}
	if usage.InputTokens != 30 || usage.OutputTokens != 12 || usage.TotalTokens != 42 {
		t.Fatalf("usage tokens = %#v, want 30/12/42", usage)
	}
	if usage.LLMCalls != 2 || usage.ToolCalls != 1 {
		t.Fatalf("usage calls = %#v, want llm=2 tool=1", usage)
	}
}

func TestCompareShowsOnlyTaskBuckets(t *testing.T) {
	a := []TaskResult{
		{TaskID: "both-pass", Passed: true},
		{TaskID: "a-only", Passed: true},
		{TaskID: "b-only", Passed: false},
	}
	b := []TaskResult{
		{TaskID: "both-pass", Passed: true},
		{TaskID: "a-only", Passed: false},
		{TaskID: "b-only", Passed: true},
	}

	out := Compare("A", a, "B", b)
	for _, want := range []string{"A only", "a-only", "B only", "b-only", "Both pass"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Compare output missing %q:\n%s", want, out)
		}
	}
}

func TestSummarizeShowsTiersInDifficultyOrder(t *testing.T) {
	results := []TaskResult{
		{TaskID: "c1", Tier: "coding-simple", Status: "FAIL"},
		{TaskID: "h1", Tier: "daily-harder", Passed: true, Status: "PASS"},
		{TaskID: "s1", Tier: "daily-simple", Passed: true, Status: "PASS"},
		{TaskID: "s2", Tier: "daily-simple", Status: "FAIL"},
		{TaskID: "x1", Tier: "custom", Passed: true, Status: "PASS"},
	}

	out := Summarize(results)
	want := "\nBy tier\n" +
		"  daily-simple     1/2 50%\n" +
		"  daily-harder     1/1 100%\n" +
		"  coding-simple    0/1 0%\n" +
		"  custom           1/1 100%\n"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("Summarize output missing tier block %q:\n%s", want, out)
	}
}

func TestSummarizeOmitsTiersWhenUnset(t *testing.T) {
	out := Summarize([]TaskResult{{TaskID: "t1", Passed: true, Status: "PASS"}})
	if strings.Contains(out, "By tier") {
		t.Fatalf("Summarize output has tier block without tiers:\n%s", out)
	}
}

func TestCompareShowsTierRowsForBothRuns(t *testing.T) {
	a := []TaskResult{
		{TaskID: "s1", Tier: "daily-simple", Passed: true},
		{TaskID: "a1", Tier: "daily-advanced", Passed: false},
	}
	b := []TaskResult{
		{TaskID: "s1", Tier: "daily-simple", Passed: true},
	}

	out := Compare("A", a, "B", b)
	for _, want := range []string{
		"By tier",
		"daily-simple           1/1 100%     1/1 100%",
		"daily-advanced         0/1 0%       -",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Compare output missing %q:\n%s", want, out)
		}
	}
}

func TestReadSWEBenchResolvedFindsNestedReport(t *testing.T) {
	root := t.TempDir()
	reportPath := filepath.Join(root, "logs", "run-1", "model", "sympy__sympy-20590", "report.json")
	if err := os.MkdirAll(filepath.Dir(reportPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, []byte(`{"sympy__sympy-20590":{"resolved":true}}`), 0644); err != nil {
		t.Fatal(err)
	}

	resolved, found, err := readSWEBenchResolved(root, "sympy__sympy-20590")
	if err != nil {
		t.Fatalf("readSWEBenchResolved() error = %v", err)
	}
	if !found || !resolved {
		t.Fatalf("resolved=%v found=%v, want true/true", resolved, found)
	}
}

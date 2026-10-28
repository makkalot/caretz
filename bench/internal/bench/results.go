package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
	LLMCalls     int   `json:"llm_calls"`
	ToolCalls    int   `json:"tool_calls"`
}

type TaskResult struct {
	TaskID         string `json:"task_id"`
	Tier           string `json:"tier,omitempty"`
	Benchmark      string `json:"benchmark"`
	Suite          string `json:"suite"`
	Model          string `json:"model"`
	CaretzCommit   string `json:"caretz_commit"`
	RunnerCommit   string `json:"runner_commit"`
	Passed         bool   `json:"passed"`
	Status         string `json:"status"`
	ExitReason     string `json:"exit_reason"`
	RuntimeSeconds int64  `json:"runtime_seconds"`
	Usage          Usage  `json:"usage"`
	Error          string `json:"error,omitempty"`
}

type RunManifest struct {
	Model        string         `json:"model"`
	Suite        string         `json:"suite"`
	CaretzCommit string         `json:"caretz_commit"`
	RunnerCommit string         `json:"runner_commit"`
	StartedAt    time.Time      `json:"started_at"`
	Settings     map[string]any `json:"settings"`
}

type storedEvent struct {
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

type usagePayload struct {
	Usage struct {
		PromptTokens     int64 `json:"PromptTokens"`
		CompletionTokens int64 `json:"CompletionTokens"`
		TotalTokens      int64 `json:"TotalTokens"`
	} `json:"usage"`
}

func ExtractUsage(sessionDir string) (Usage, error) {
	path := filepath.Join(sessionDir, "context.jsonpl")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Usage{}, nil
		}
		return Usage{}, err
	}
	defer file.Close()

	var usage Usage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	for scanner.Scan() {
		var event storedEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		switch event.EventType {
		case "LLMAgentMessage.Created", "LLMAgentResponse.Created":
			var payload usagePayload
			if err := json.Unmarshal(event.Payload, &payload); err == nil {
				usage.InputTokens += payload.Usage.PromptTokens
				usage.OutputTokens += payload.Usage.CompletionTokens
				usage.TotalTokens += payload.Usage.TotalTokens
				usage.LLMCalls++
			}
		case "LLMToolCallResult.Created":
			usage.ToolCalls++
		}
	}
	if err := scanner.Err(); err != nil {
		return Usage{}, err
	}
	return usage, nil
}

func WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func LoadResults(runDir string) ([]TaskResult, error) {
	entries, err := os.ReadDir(filepath.Join(runDir, "results"))
	if err != nil {
		return nil, err
	}
	var results []TaskResult
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runDir, "results", entry.Name()))
		if err != nil {
			return nil, err
		}
		var result TaskResult
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].TaskID < results[j].TaskID })
	return results, nil
}

func Summarize(results []TaskResult) string {
	total := len(results)
	counts := map[string]int{}
	var runtimes []int
	var tokens []int
	for _, result := range results {
		counts[result.Status]++
		if result.RuntimeSeconds > 0 {
			runtimes = append(runtimes, int(result.RuntimeSeconds))
		}
		if result.Usage.TotalTokens > 0 {
			tokens = append(tokens, int(result.Usage.TotalTokens))
		}
	}
	passRate := 0
	if total > 0 {
		passRate = counts["PASS"] * 100 / total
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "Tasks             %d\n", total)
	_, _ = fmt.Fprintf(&b, "Passed            %d\n", counts["PASS"])
	_, _ = fmt.Fprintf(&b, "Failed            %d\n", counts["FAIL"])
	_, _ = fmt.Fprintf(&b, "Timeout           %d\n", counts["TIMEOUT"])
	_, _ = fmt.Fprintf(&b, "Errors            %d\n", counts["CARETZ_ERROR"]+counts["MODEL_ERROR"]+counts["BENCHMARK_ERROR"])
	_, _ = fmt.Fprintf(&b, "Pass rate         %d%%\n", passRate)
	if len(runtimes) > 0 {
		_, _ = fmt.Fprintf(&b, "Median runtime    %ds\n", median(runtimes))
	}
	if len(tokens) > 0 {
		_, _ = fmt.Fprintf(&b, "Median tokens     %d\n", median(tokens))
	}
	if tiers := orderedTiers(results); len(tiers) > 0 {
		byTier := groupByTier(results)
		_, _ = fmt.Fprintf(&b, "\nBy tier\n")
		for _, tier := range tiers {
			_, _ = fmt.Fprintf(&b, "  %-16s %s\n", tier, tierCell(byTier[tier]))
		}
	}
	return b.String()
}

// tierOrder lists the daily suite tiers from easiest to hardest so reports
// read as a difficulty ladder; unknown tiers follow alphabetically.
var tierOrder = []string{"daily-simple", "daily-harder", "daily-advanced", "coding-simple"}

func orderedTiers(resultSets ...[]TaskResult) []string {
	seen := map[string]bool{}
	for _, results := range resultSets {
		for _, result := range results {
			if result.Tier != "" {
				seen[result.Tier] = true
			}
		}
	}
	var tiers, others []string
	for _, tier := range tierOrder {
		if seen[tier] {
			tiers = append(tiers, tier)
			delete(seen, tier)
		}
	}
	for tier := range seen {
		others = append(others, tier)
	}
	sort.Strings(others)
	return append(tiers, others...)
}

func groupByTier(results []TaskResult) map[string][]TaskResult {
	byTier := map[string][]TaskResult{}
	for _, result := range results {
		byTier[result.Tier] = append(byTier[result.Tier], result)
	}
	return byTier
}

func median(values []int) int {
	sort.Ints(values)
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return (values[mid-1] + values[mid]) / 2
}

func Compare(aName string, aResults []TaskResult, bName string, bResults []TaskResult) string {
	aByID := map[string]TaskResult{}
	bByID := map[string]TaskResult{}
	for _, result := range aResults {
		aByID[result.TaskID] = result
	}
	for _, result := range bResults {
		bByID[result.TaskID] = result
	}
	var ids []string
	seen := map[string]bool{}
	for id := range aByID {
		ids = append(ids, id)
		seen[id] = true
	}
	for id := range bByID {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var bothPass, aOnly, bOnly, bothFail []string
	for _, id := range ids {
		aPass := aByID[id].Passed
		bPass := bByID[id].Passed
		switch {
		case aPass && bPass:
			bothPass = append(bothPass, id)
		case aPass:
			aOnly = append(aOnly, id)
		case bPass:
			bOnly = append(bOnly, id)
		default:
			bothFail = append(bothFail, id)
		}
	}

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%-24s %-12s %-12s\n", "", aName, bName)
	_, _ = fmt.Fprintf(&b, "%-24s %-12s %-12s\n", "Passed", passCount(aResults), passCount(bResults))
	_, _ = fmt.Fprintf(&b, "%-24s %-12s %-12s\n", "Pass rate", passRate(aResults), passRate(bResults))
	if tiers := orderedTiers(aResults, bResults); len(tiers) > 0 {
		aByTier, bByTier := groupByTier(aResults), groupByTier(bResults)
		_, _ = fmt.Fprintf(&b, "\nBy tier\n")
		for _, tier := range tiers {
			_, _ = fmt.Fprintf(&b, "  %-22s %-12s %-12s\n", tier, tierCell(aByTier[tier]), tierCell(bByTier[tier]))
		}
	}
	_, _ = fmt.Fprintf(&b, "\nBoth pass             %d\n", len(bothPass))
	_, _ = fmt.Fprintf(&b, "%s only             %d\n", aName, len(aOnly))
	_, _ = fmt.Fprintf(&b, "%s only             %d\n", bName, len(bOnly))
	_, _ = fmt.Fprintf(&b, "Both fail              %d\n", len(bothFail))
	if len(aOnly) > 0 {
		_, _ = fmt.Fprintf(&b, "\n%s only:\n%s\n", aName, strings.Join(aOnly, "\n"))
	}
	if len(bOnly) > 0 {
		_, _ = fmt.Fprintf(&b, "\n%s only:\n%s\n", bName, strings.Join(bOnly, "\n"))
	}
	return b.String()
}

func passCount(results []TaskResult) string {
	passed := 0
	for _, result := range results {
		if result.Passed {
			passed++
		}
	}
	return fmt.Sprintf("%d/%d", passed, len(results))
}

func tierCell(results []TaskResult) string {
	if len(results) == 0 {
		return "-"
	}
	return passCount(results) + " " + passRate(results)
}

func passRate(results []TaskResult) string {
	if len(results) == 0 {
		return "0%"
	}
	passed := 0
	for _, result := range results {
		if result.Passed {
			passed++
		}
	}
	return fmt.Sprintf("%d%%", passed*100/len(results))
}

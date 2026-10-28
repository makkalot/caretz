package goal

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Compact bounds model prose before it enters an event or another agent's
// prompt. Commands and tool output are never summarized here.
func Compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-3]) + "..."
}

// Preview is one line of at most max bytes.
func Preview(value string, max int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if value == "" {
		return "(empty response)"
	}
	if len(value) <= max {
		return value
	}
	return value[:max-3] + "..."
}

// OutputTail keeps the end of command output, where failures usually are.
func OutputTail(output string, limit int) string {
	output = strings.TrimSpace(output)
	if len(output) <= limit {
		return output
	}
	return "..." + output[len(output)-limit:]
}

func Tail(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[len(values)-limit:]
}

func DefaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func FirstSentence(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if index := strings.Index(value, ". "); index >= 0 {
		return value[:index+1]
	}
	return value
}

func Contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func sortStrings(values []string) { sort.Strings(values) }

// FailureKey fingerprints a failure report. Task IDs are excluded because
// every replan mints new ones, and numbers are masked so attempt counts and
// timings do not make a repeated failure look new.
func FailureKey(failures []Failure) string {
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		source := failure.Source
		if strings.HasPrefix(source, "task ") || strings.HasPrefix(source, "regression ") {
			source = "task"
		}
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%s", source, DefaultString(failure.Key, failure.Check), failure.Status, digits.ReplaceAllString(failure.Reason, "#")))
	}
	sort.Strings(parts)
	data, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func SummarizeFailures(failures []Failure) string {
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		part := failure.Source + " " + failure.Status
		if failure.Check != "" {
			part += " (" + failure.Check + ")"
		}
		parts = append(parts, part+": "+failure.Reason)
	}
	return Compact(strings.Join(parts, "; "), 500)
}

func FailureLines(failures []Failure) []string {
	lines := make([]string, 0, len(failures))
	for _, failure := range failures {
		line := failure.Source
		if failure.Check != "" {
			line += " `" + failure.Check + "`"
		}
		lines = append(lines, line+": "+failure.Reason)
	}
	return lines
}

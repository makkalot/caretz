package slashcmd

import (
	"sort"
	"strings"
	"unicode"

	"github.com/makkalot/caretz/internal/config"
)

// Completion describes command-owned candidates for the token at the cursor.
// Start and End are rune offsets relative to the command's argument string.
type Completion struct {
	Start      int
	End        int
	Query      string
	Candidates []string
}

// CompletionProvider is implemented by commands with structured arguments.
// The terminal editor owns ranking and applying the returned candidates.
type CompletionProvider interface {
	Complete(args string, cursor int) (Completion, bool)
}

type completionToken struct {
	value string
	start int
	end   int
}

func completionTokens(input string, cursor int) ([]completionToken, int, bool) {
	runes := []rune(input)
	if cursor < 0 || cursor > len(runes) {
		return nil, 0, false
	}

	var tokens []completionToken
	start := -1
	var value strings.Builder
	var quote rune
	escaped := false
	flush := func(end int) {
		if start < 0 {
			return
		}
		tokens = append(tokens, completionToken{value: value.String(), start: start, end: end})
		start = -1
		value.Reset()
	}

	for i, r := range runes {
		if escaped {
			value.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			if start < 0 {
				start = i
			}
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			value.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			if start < 0 {
				start = i
			}
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
		}
		value.WriteRune(r)
	}
	if escaped {
		value.WriteRune('\\')
	}
	flush(len(runes))

	for i, token := range tokens {
		if cursor >= token.start && cursor <= token.end {
			return tokens, i, true
		}
	}
	return append(tokens, completionToken{start: cursor, end: cursor}), len(tokens), true
}

func tokenCompletion(token completionToken, query string, candidates []string) (Completion, bool) {
	if len(candidates) == 0 {
		return Completion{}, false
	}
	return Completion{Start: token.start, End: token.end, Query: query, Candidates: candidates}, true
}

func optionCandidates(defs []optionDef, used map[string]bool) []string {
	var out []string
	for _, def := range defs {
		logical := def.Logical
		if logical == "" {
			logical = def.Name
		}
		if used[logical] {
			continue
		}
		candidate := "--" + def.Name
		if def.TakesValue {
			candidate += " "
		}
		out = append(out, candidate)
	}
	return out
}

func completionUsedOptions(tokens []completionToken, defs []optionDef) map[string]bool {
	byName := make(map[string]optionDef, len(defs))
	for _, def := range defs {
		byName[def.Name] = def
	}
	used := make(map[string]bool)
	for _, token := range tokens {
		name := strings.TrimPrefix(token.value, "--")
		name, _, _ = strings.Cut(name, "=")
		if def, ok := byName[name]; ok {
			logical := def.Logical
			if logical == "" {
				logical = def.Name
			}
			used[logical] = true
		}
	}
	return used
}

func positionalStarted(tokens []completionToken, active int, defs []optionDef) bool {
	values := make(map[string]bool)
	for _, def := range defs {
		if def.TakesValue {
			values["--"+def.Name] = true
		}
	}
	expectingValue := false
	for i := 0; i < active; i++ {
		value := tokens[i].value
		if expectingValue {
			expectingValue = false
			continue
		}
		if value == "--" {
			return true
		}
		if values[value] {
			expectingValue = true
			continue
		}
		if !strings.HasPrefix(value, "--") {
			return true
		}
	}
	return false
}

func optionValueCompletion(token completionToken, key string, candidates []string) (Completion, bool) {
	prefix := key + "="
	if !strings.HasPrefix(token.value, prefix) {
		return Completion{}, false
	}
	query := strings.TrimPrefix(token.value, prefix)
	start := token.start + len([]rune(prefix))
	return Completion{Start: start, End: token.end, Query: query, Candidates: candidates}, len(candidates) > 0
}

func modelRefs(choices []config.ModelChoice) []string {
	refs := make([]string, 0, len(choices))
	for _, choice := range choices {
		if ref := strings.TrimSpace(choice.Ref); ref != "" {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}

func reasoningValues(choices []config.ModelChoice, ref string) []string {
	seen := make(map[string]bool)
	var values []string
	addChoice := func(choice config.ModelChoice) {
		if strings.TrimSpace(choice.Reasoning) == "" {
			return
		}
		choiceValues := choice.ReasoningValues
		if len(choiceValues) == 0 {
			choiceValues = []string{"low", "medium", "high", "xhigh"}
		}
		choiceValues = append([]string{choice.Reasoning}, choiceValues...)
		for _, value := range choiceValues {
			value = strings.TrimSpace(value)
			if value != "" && !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
	}
	for _, choice := range choices {
		if ref == "" || choice.Ref == ref {
			addChoice(choice)
		}
	}
	return values
}

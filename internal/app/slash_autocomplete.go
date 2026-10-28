package app

import (
	"sort"
	"strings"
	"unicode"

	"github.com/makkalot/caretz/internal/slashcmd"
)

type slashAutocomplete struct {
	registry *slashcmd.Registry
}

type slashCompletionMatch struct {
	start      int
	end        int
	candidates []string
}

func (a slashAutocomplete) complete(value string, cursor int) (start, end int, replacement string, ok bool) {
	match, ok := a.match(value, cursor)
	if !ok || len(match.candidates) == 0 {
		return 0, 0, "", false
	}
	return match.start, match.end, match.candidates[0], true
}

func (a slashAutocomplete) match(value string, cursor int) (slashCompletionMatch, bool) {
	if a.registry == nil || strings.Contains(value, "\n") {
		return slashCompletionMatch{}, false
	}
	runes := []rune(value)
	if cursor < 0 || cursor > len(runes) {
		return slashCompletionMatch{}, false
	}

	commandStart := 0
	for commandStart < len(runes) && unicode.IsSpace(runes[commandStart]) {
		commandStart++
	}
	if commandStart >= len(runes) || runes[commandStart] != '/' || cursor <= commandStart {
		return slashCompletionMatch{}, false
	}
	nameStart := commandStart + 1
	nameEnd := nameStart
	for nameEnd < len(runes) && !unicode.IsSpace(runes[nameEnd]) {
		nameEnd++
	}

	if cursor <= nameEnd {
		query := string(runes[nameStart:cursor])
		candidates := rankSlashCandidates(commandNames(a.registry), query)
		for i := range candidates {
			candidates[i] += " "
		}
		return slashCompletionMatch{start: nameStart, end: nameEnd, candidates: candidates}, len(candidates) > 0
	}

	name := strings.ToLower(string(runes[nameStart:nameEnd]))
	cmd, found := resolveCommand(a.registry, name)
	if !found {
		return slashCompletionMatch{}, false
	}
	provider, found := cmd.(slashcmd.CompletionProvider)
	if !found {
		return slashCompletionMatch{}, false
	}

	argsStart := nameEnd
	for argsStart < len(runes) && unicode.IsSpace(runes[argsStart]) {
		argsStart++
	}
	if cursor < argsStart {
		argsStart = cursor
	}
	completion, found := provider.Complete(string(runes[argsStart:]), cursor-argsStart)
	if !found {
		return slashCompletionMatch{}, false
	}
	candidates := rankSlashCandidates(completion.Candidates, completion.Query)
	return slashCompletionMatch{
		start:      argsStart + completion.Start,
		end:        argsStart + completion.End,
		candidates: candidates,
	}, len(candidates) > 0
}

func commandNames(registry *slashcmd.Registry) []string {
	if registry == nil {
		return nil
	}
	seen := make(map[string]bool)
	var names []string
	for _, cmd := range registry.Commands() {
		for _, name := range append([]string{cmd.Name()}, cmd.Aliases()...) {
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func resolveCommand(registry *slashcmd.Registry, name string) (slashcmd.Command, bool) {
	for _, cmd := range registry.Commands() {
		if strings.EqualFold(cmd.Name(), name) {
			return cmd, true
		}
		for _, alias := range cmd.Aliases() {
			if strings.EqualFold(alias, name) {
				return cmd, true
			}
		}
	}
	return nil, false
}

func rankSlashCandidates(candidates []string, query string) []string {
	query = strings.ToLower(query)
	seen := make(map[string]bool)
	type scoredCandidate struct {
		value string
		score int
		index int
	}
	var ranked []scoredCandidate
	for index, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		score, ok := slashCandidateScore(candidate, query)
		if !ok {
			continue
		}
		ranked = append(ranked, scoredCandidate{value: candidate, score: score, index: index})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].index != ranked[j].index {
			return ranked[i].index < ranked[j].index
		}
		return ranked[i].value < ranked[j].value
	})
	out := make([]string, len(ranked))
	for i, candidate := range ranked {
		out[i] = candidate.value
	}
	return out
}

func slashCandidateScore(candidate, query string) (int, bool) {
	match := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), "="))
	if query == "" {
		return 1, true
	}
	if match == query {
		return 1000, true
	}
	if strings.HasPrefix(match, query) {
		return 800 - len([]rune(match)), true
	}
	base := match
	if slash := strings.LastIndex(base, "/"); slash >= 0 {
		base = base[slash+1:]
	}
	if base == query {
		return 900, true
	}
	if strings.HasPrefix(base, query) {
		return 700 - len([]rune(base)), true
	}
	if score, ok := fuzzySlashScore(match, query); ok {
		return 400 + score, true
	}
	if base != match {
		if score, ok := fuzzySlashScore(base, query); ok {
			return 500 + score, true
		}
	}
	return 0, false
}

func fuzzySlashScore(candidate, query string) (int, bool) {
	queryRunes := []rune(query)
	qi, score, previous := 0, 0, -2
	for i, r := range candidate {
		if qi >= len(queryRunes) || r != queryRunes[qi] {
			continue
		}
		score += 10
		if i == previous+1 {
			score += 5
		}
		previous = i
		qi++
	}
	return score - len([]rune(candidate)), qi == len(queryRunes)
}

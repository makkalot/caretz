// Package wordfreq finds the most common words in a text.
package wordfreq

import (
	"sort"
	"strings"
	"unicode"
)

// WordCount is a word and how many times it appears.
type WordCount struct {
	Word  string
	Count int
}

// TopWords returns the n most frequent words in text, most frequent first.
//
// A word is a run of letters and apostrophes; anything else separates words.
// Words are compared case-insensitively and returned in lowercase. Words with
// the same count are ordered alphabetically. If n is larger than the number of
// distinct words, all words are returned. If n <= 0, TopWords returns an empty
// slice.
func TopWords(text string, n int) []WordCount {
	if n <= 0 {
		return nil
	}
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	})
	counts := map[string]int{}
	for _, w := range words {
		counts[w]++
	}
	out := make([]WordCount, 0, len(counts))
	for w, c := range counts {
		out = append(out, WordCount{Word: w, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Word < out[j].Word
	})
	if n < len(out) {
		out = out[:n]
	}
	return out
}

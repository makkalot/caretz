// Package wordfreq finds the most common words in a text.
package wordfreq

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
	// TODO: implement
	return nil
}

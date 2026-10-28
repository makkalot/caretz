package syntax

import (
	"unicode"
)

const (
	// minSimilarity is the share of non-space text two lines must have in
	// common before their differences are emphasized; below it the lines
	// are simply different and emphasis would light up everything.
	minSimilarity = 0.4
	// maxWordDiffCells bounds the comparison table for very long lines.
	maxWordDiffCells = 40000
)

type piece struct {
	start, end int
	text       string
	space      bool
}

// pieces splits a line into words, whitespace runs and single punctuation
// runes, with rune offsets.
func pieces(line string) []piece {
	rs := []rune(line)
	var out []piece
	for i := 0; i < len(rs); {
		j := i + 1
		switch {
		case isWordRune(rs[i]):
			j = scanWhile(rs, i, isWordRune)
		case unicode.IsSpace(rs[i]):
			j = scanWhile(rs, i, unicode.IsSpace)
		}
		out = append(out, piece{start: i, end: j, text: string(rs[i:j]), space: unicode.IsSpace(rs[i])})
		i = j
	}
	return out
}

// ChangedRanges finds what changed between a removed line and the added
// line that replaced it, as rune spans in each. It returns no spans when
// the lines are too different for word emphasis to help.
func ChangedRanges(oldLine, newLine string) (oldSpans, newSpans []Span) {
	a, b := pieces(oldLine), pieces(newLine)
	if len(a) == 0 || len(b) == 0 || (len(a)+1)*(len(b)+1) > maxWordDiffCells {
		return nil, nil
	}
	// lcs[i][j] is the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i].text == b[j].text {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	matchedA, matchedB := make([]bool, len(a)), make([]bool, len(b))
	common := 0
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i].text == b[j].text:
			matchedA[i], matchedB[j] = true, true
			if !a[i].space {
				common += len([]rune(a[i].text))
			}
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}
	total := solidRunes(a) + solidRunes(b)
	if total == 0 || float64(2*common)/float64(total) < minSimilarity {
		return nil, nil
	}
	return unmatchedSpans(a, matchedA), unmatchedSpans(b, matchedB)
}

func solidRunes(ps []piece) int {
	n := 0
	for _, p := range ps {
		if !p.space {
			n += len([]rune(p.text))
		}
	}
	return n
}

// unmatchedSpans merges runs of unmatched pieces into spans.
func unmatchedSpans(ps []piece, matched []bool) []Span {
	var spans []Span
	for i, p := range ps {
		if matched[i] {
			continue
		}
		if n := len(spans); n > 0 && spans[n-1].End == p.start {
			spans[n-1].End = p.end
			continue
		}
		spans = append(spans, Span{Start: p.start, End: p.end})
	}
	return spans
}

// BlockSpans emphasizes a block of removed lines followed by the added
// lines that replaced them, pairing lines by position the way delta does.
func BlockSpans(removed, added []string) (removedSpans, addedSpans [][]Span) {
	removedSpans = make([][]Span, len(removed))
	addedSpans = make([][]Span, len(added))
	for i := 0; i < len(removed) && i < len(added); i++ {
		removedSpans[i], addedSpans[i] = ChangedRanges(removed[i], added[i])
	}
	return removedSpans, addedSpans
}

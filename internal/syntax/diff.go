package syntax

type DiffKind int

const (
	DiffContext DiffKind = iota
	DiffAdded
	DiffRemoved
)

// DiffLine is one line of a hunk body, without its +/- sign.
type DiffLine struct {
	Kind DiffKind
	Text string
}

// HighlightHunk returns each hunk line's tokens and the spans that changed
// against the line it replaced. Removed lines continue the old file's
// highlighting state and added lines the new file's; context feeds both. A
// nil lang leaves every line plain.
func HighlightHunk(lang *Lang, lines []DiffLine) (toks [][]Token, spans [][]Span) {
	toks = make([][]Token, len(lines))
	spans = make([][]Span, len(lines))
	var oldSide, newSide *Highlighter
	if lang != nil {
		oldSide, newSide = lang.New(), lang.New()
	}
	for i, line := range lines {
		switch {
		case lang == nil:
			toks[i] = []Token{{Kind: Plain, Text: line.Text}}
		case line.Kind == DiffRemoved:
			toks[i] = oldSide.Line(line.Text)
		case line.Kind == DiffAdded:
			toks[i] = newSide.Line(line.Text)
		default:
			toks[i] = newSide.Line(line.Text)
			oldSide.Line(line.Text)
		}
	}
	// Pair each run of removed lines with the added lines right after it.
	for i := 0; i < len(lines); {
		start := i
		for i < len(lines) && lines[i].Kind == DiffRemoved {
			i++
		}
		mid := i
		for i < len(lines) && lines[i].Kind == DiffAdded {
			i++
		}
		if mid > start && i > mid {
			removedSpans, addedSpans := BlockSpans(diffTexts(lines[start:mid]), diffTexts(lines[mid:i]))
			copy(spans[start:mid], removedSpans)
			copy(spans[mid:i], addedSpans)
		}
		if i == start {
			i++
		}
	}
	return toks, spans
}

func diffTexts(lines []DiffLine) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line.Text
	}
	return out
}

// RenderDiffLine draws a hunk line with its sign. Added and removed lines
// get a tinted background with the changed spans brighter, padded to width
// when width is positive; without backgrounds they keep the classic green
// and red with changed spans in reverse video. Width also truncates.
func RenderDiffLine(theme *Theme, kind DiffKind, toks []Token, spans []Span, width int) string {
	opts := RenderOptions{MaxWidth: width, Spans: spans}
	sign, signColor := " ", ""
	switch kind {
	case DiffAdded:
		sign, signColor = "+", theme.AddedSign
		opts.Background, opts.Emphasis = theme.AddedBg, theme.AddedEmphasis
	case DiffRemoved:
		sign, signColor = "-", theme.RemovedSign
		opts.Background, opts.Emphasis = theme.RemovedBg, theme.RemovedEmphasis
	default:
		opts.Spans = nil
	}
	if kind != DiffContext {
		if theme.Backgrounds() {
			opts.PadTo = width
		} else {
			opts.Foreground, opts.Emphasis = signColor, "7"
		}
	}
	text := RenderLine(toks, theme, opts)
	if signColor == "" {
		return sign + text
	}
	signStyle := signColor
	if opts.Background != "" {
		signStyle += ";" + opts.Background
	}
	return "\x1b[0;" + signStyle + "m" + sign + "\x1b[0m" + text
}

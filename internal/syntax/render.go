package syntax

import (
	"strings"

	"github.com/makkalot/caretz/internal/ansi"
)

// Span is a half-open range of rune offsets in a line.
type Span struct {
	Start, End int
}

type RenderOptions struct {
	// Background is the line's SGR background, empty for none.
	Background string
	// Emphasis replaces Background inside Spans.
	Emphasis string
	Spans    []Span
	// Foreground, when set, replaces every token's color.
	Foreground string
	Dim        bool
	// TabWidth is how many spaces a tab becomes; zero means 4.
	TabWidth int
	// MaxWidth truncates the line to that many cells, ending with "…".
	// Zero means no limit.
	MaxWidth int
	// PadTo fills the line with its background up to that many cells.
	PadTo int
}

// RenderLine draws tokens as one terminal line. Every style change resets
// and restates all attributes, so a background survives colored tokens and
// nothing leaks past the end of the line.
func RenderLine(toks []Token, theme *Theme, opts RenderOptions) string {
	tab := strings.Repeat(" ", max(1, opts.TabWidth))
	if opts.TabWidth == 0 {
		tab = "    "
	}
	cellsOf := func(r rune) int {
		if r == '\t' {
			return len(tab)
		}
		return ansi.CellWidth(r)
	}
	total := 0
	for _, tok := range toks {
		for _, r := range tok.Text {
			total += cellsOf(r)
		}
	}
	limit := -1
	if opts.MaxWidth > 0 && total > opts.MaxWidth {
		limit = opts.MaxWidth - 1
	}

	var b strings.Builder
	current, styled := "", false
	setStyle := func(fg, bg string) {
		var params []string
		if opts.Dim {
			params = append(params, "2")
		}
		for _, p := range []string{fg, bg} {
			if p != "" {
				params = append(params, p)
			}
		}
		style := strings.Join(params, ";")
		if style == current {
			return
		}
		if style == "" {
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString("\x1b[0;" + style + "m")
			styled = true
		}
		current = style
	}

	cells, pos := 0, 0
	spans := opts.Spans
	truncated := false
	for _, tok := range toks {
		fg := opts.Foreground
		if fg == "" && theme != nil {
			fg = theme.Foreground(tok.Kind)
		}
		for _, r := range tok.Text {
			for len(spans) > 0 && pos >= spans[0].End {
				spans = spans[1:]
			}
			bg := opts.Background
			if len(spans) > 0 && pos >= spans[0].Start {
				bg = opts.Emphasis
			}
			w := cellsOf(r)
			if limit >= 0 && cells+w > limit {
				setStyle(fg, bg)
				b.WriteString("…")
				cells++
				truncated = true
				break
			}
			setStyle(fg, bg)
			if r == '\t' {
				b.WriteString(tab)
			} else {
				b.WriteRune(r)
			}
			cells += w
			pos++
		}
		if truncated {
			break
		}
	}
	if opts.PadTo > cells {
		setStyle("", opts.Background)
		b.WriteString(strings.Repeat(" ", opts.PadTo-cells))
	}
	if styled {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// Text joins the tokens' text without any styling.
func Text(toks []Token) string {
	var b strings.Builder
	for _, tok := range toks {
		b.WriteString(tok.Text)
	}
	return b.String()
}

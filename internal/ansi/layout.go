package ansi

import (
	"strings"
	"unicode"
)

type LayoutOptions struct {
	Width              int
	FirstPrefix        string
	ContinuationPrefix string
	TabWidth           int
}

type Row struct {
	Text                   string
	SourceStart, SourceEnd int
	offsets                []int
}

type TextLayout struct {
	Rows   []Row
	Cursor Position
}

func LayoutText(text []rune, cursor int, options LayoutOptions) TextLayout {
	width := max(options.Width, 2)
	prefixWidth := VisibleWidth(options.FirstPrefix)
	continuation := options.ContinuationPrefix
	if continuation == "" {
		continuation = strings.Repeat(" ", prefixWidth)
	}
	contentWidth := max(width-prefixWidth-1, 1)
	tabWidth := options.TabWidth
	if tabWidth <= 0 {
		tabWidth = 4
	}

	rows := make([]Row, 0, strings.Count(string(text), "\n")+1)
	lineStart := 0
	for lineStart <= len(text) {
		lineEnd := lineStart
		for lineEnd < len(text) && text[lineEnd] != '\n' {
			lineEnd++
		}
		rows = appendLineRows(rows, text, lineStart, lineEnd, contentWidth, tabWidth)
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}

	for i := range rows {
		prefix := continuation
		if i == 0 {
			prefix = options.FirstPrefix
		}
		rows[i].Text = prefix + rows[i].Text
	}
	cursor = min(max(cursor, 0), len(text))
	cursorRow := CursorRow(rows, cursor)
	cursorCol := prefixWidth + rows[cursorRow].CellOffset(cursor)
	return TextLayout{Rows: rows, Cursor: Position{Row: cursorRow, Col: cursorCol}}
}

func appendLineRows(rows []Row, text []rune, start, end, width, tabWidth int) []Row {
	if start == end {
		return append(rows, Row{SourceStart: start, SourceEnd: end, offsets: []int{0}})
	}
	rowStart := start
	var rendered strings.Builder
	offsets := []int{0}
	cellWidth := 0
	for pos := start; pos < end; pos++ {
		r := text[pos]
		runeWidth := CellWidth(r)
		if r == '\t' {
			runeWidth = tabWidth - cellWidth%tabWidth
		}
		if cellWidth > 0 && cellWidth+runeWidth > width {
			rows = append(rows, Row{Text: rendered.String(), SourceStart: rowStart, SourceEnd: pos, offsets: offsets})
			rowStart = pos
			rendered.Reset()
			offsets = []int{0}
			cellWidth = 0
			if r == '\t' {
				runeWidth = tabWidth
			}
		}
		if r == '\t' && runeWidth > width-cellWidth {
			runeWidth = width - cellWidth
		}
		if r == '\t' {
			rendered.WriteString(strings.Repeat(" ", runeWidth))
		} else if runeWidth > width-cellWidth {
			rendered.WriteRune('?')
			runeWidth = 1
		} else {
			rendered.WriteRune(r)
		}
		cellWidth += runeWidth
		offsets = append(offsets, cellWidth)
	}
	return append(rows, Row{Text: rendered.String(), SourceStart: rowStart, SourceEnd: end, offsets: offsets})
}

func CursorRow(rows []Row, cursor int) int {
	for i, row := range rows {
		if cursor < row.SourceEnd || cursor == row.SourceEnd && (i == len(rows)-1 || rows[i+1].SourceStart != cursor) {
			return i
		}
	}
	return len(rows) - 1
}

func (r Row) CellOffset(sourceIndex int) int {
	index := min(max(sourceIndex-r.SourceStart, 0), len(r.offsets)-1)
	return r.offsets[index]
}

func (r Row) SourceIndexAt(cellOffset int) int {
	index := 0
	for i, candidate := range r.offsets {
		if candidate > cellOffset {
			break
		}
		index = i
	}
	return r.SourceStart + index
}

func VisibleWidth(value string) int {
	width := 0
	for _, r := range Strip(value) {
		width += CellWidth(r)
	}
	return width
}

func CellWidth(r rune) int {
	if r == 0 || r == '\u200c' || r == '\u200d' || r >= '\ufe00' && r <= '\ufe0f' ||
		unicode.IsControl(r) || unicode.In(r, unicode.Mn, unicode.Me) {
		return 0
	}
	if isWide(r) {
		return 2
	}
	return 1
}

func isWide(r rune) bool {
	return r >= 0x1100 && r <= 0x115f || r == 0x2329 || r == 0x232a ||
		r >= 0x2e80 && r <= 0xa4cf && r != 0x303f ||
		r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff ||
		r >= 0xfe10 && r <= 0xfe19 || r >= 0xfe30 && r <= 0xfe6f ||
		r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 ||
		r >= 0x1f000 && r <= 0x1faff || r >= 0x20000 && r <= 0x3fffd
}

func Strip(value string) string {
	runes := []rune(value)
	var visible strings.Builder
	for i := 0; i < len(runes); i++ {
		if runes[i] != '\x1b' {
			visible.WriteRune(runes[i])
			continue
		}
		if i+1 >= len(runes) {
			break
		}
		switch runes[i+1] {
		case '[':
			i += 2
			for i < len(runes) && (runes[i] < '@' || runes[i] > '~') {
				i++
			}
		case ']':
			i += 2
			for i < len(runes) && runes[i] != '\a' {
				if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '\\' {
					i++
					break
				}
				i++
			}
		default:
			i++
		}
	}
	return visible.String()
}

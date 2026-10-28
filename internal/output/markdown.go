package output

import (
	"os"
	"strings"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/syntax"
)

// MarkdownRenderer renders a small, well-defined subset of markdown into
// ANSI-styled plain text. The supported block constructs are headings, bullet
// and numbered lists, fenced code blocks, and blockquotes. The supported
// inline constructs are code spans, links, bold, and italic. Anything outside
// the supported subset is emitted as plain text so the output stays readable.
type MarkdownRenderer struct {
	style Style
	width func() int
	// theme colors fenced code in a known language; nil without color.
	theme *syntax.Theme
}

// NewMarkdownRenderer returns a renderer that applies the given Style.
func NewMarkdownRenderer(style Style) *MarkdownRenderer {
	return NewMarkdownRendererWithWidth(style, func() int { return 80 })
}

// NewMarkdownRendererWithWidth returns a renderer whose available width is
// evaluated for every render. This lets interactive output follow resizes.
func NewMarkdownRendererWithWidth(style Style, width func() int) *MarkdownRenderer {
	r := &MarkdownRenderer{style: style, width: width}
	if style.Enabled() {
		r.theme = syntax.NewTheme(syntax.DepthFromEnv(os.Getenv))
	}
	return r
}

// Render converts markdown text into styled plain text.
func (r *MarkdownRenderer) Render(md string) string {
	lines := strings.Split(md, "\n")
	var b strings.Builder

	inCode := false
	codeFence := "```"
	var code *syntax.Highlighter

	for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
		line := lines[lineIndex]
		trimmed := strings.TrimSpace(line)

		if inCode {
			if strings.HasPrefix(trimmed, codeFence) {
				inCode = false
				continue
			}
			if code != nil {
				b.WriteString(syntax.RenderLine(code.Line(line), r.theme, syntax.RenderOptions{}))
			} else {
				b.WriteString(r.style.Muted(line))
			}
			b.WriteString("\n")
			continue
		}

		if strings.HasPrefix(trimmed, "```") {
			inCode = true
			codeFence = "```"
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			code = nil
			if l := syntax.Lookup(lang); l != nil && r.theme != nil {
				code = l.New()
			}
			if lang == "" {
				lang = "code"
			}
			b.WriteString(r.style.Muted(GlyphRule + GlyphRule + " " + lang))
			b.WriteString("\n")
			continue
		}

		if lineIndex+1 < len(lines) {
			if table, consumed, ok := parseTable(lines[lineIndex:]); ok {
				b.WriteString(r.renderTable(table))
				b.WriteString("\n")
				lineIndex += consumed - 1
				continue
			}
		}

		if heading, level, ok := parseHeading(trimmed); ok {
			text := r.style.MarkdownHeading(r.renderInline(heading))
			if level == 1 {
				text = r.style.Underline(text)
			}
			b.WriteString(text)
			b.WriteString("\n")
			continue
		}

		if strings.HasPrefix(trimmed, ">") {
			quote := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			b.WriteString(r.style.Quote("  ❝ "))
			b.WriteString(r.style.Quote(r.renderInline(quote)))
			b.WriteString("\n")
			continue
		}

		if item, ok := parseBullet(trimmed); ok {
			b.WriteString("  ")
			b.WriteString(r.style.Muted(GlyphBullet + " "))
			b.WriteString(r.renderInline(item))
			b.WriteString("\n")
			continue
		}

		if num, item, ok := parseNumbered(trimmed); ok {
			b.WriteString("  ")
			b.WriteString(r.style.Muted(num + ". "))
			b.WriteString(r.renderInline(item))
			b.WriteString("\n")
			continue
		}

		if trimmed == "" {
			b.WriteString("\n")
			continue
		}

		b.WriteString(r.renderInline(line))
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

type tableAlignment uint8

const (
	tableAlignLeft tableAlignment = iota
	tableAlignCenter
	tableAlignRight
)

type markdownTable struct {
	rows       [][]string
	alignments []tableAlignment
}

func parseTable(lines []string) (markdownTable, int, bool) {
	if len(lines) < 2 {
		return markdownTable{}, 0, false
	}
	header, headerOK := splitTableRow(lines[0])
	delimiters, delimiterOK := splitTableRow(lines[1])
	if !headerOK || !delimiterOK || len(header) == 0 || len(header) != len(delimiters) {
		return markdownTable{}, 0, false
	}
	alignments := make([]tableAlignment, len(delimiters))
	for i, delimiter := range delimiters {
		alignment, ok := parseTableDelimiter(delimiter)
		if !ok {
			return markdownTable{}, 0, false
		}
		alignments[i] = alignment
	}
	table := markdownTable{rows: [][]string{header}, alignments: alignments}
	consumed := 2
	for consumed < len(lines) {
		row, ok := splitTableRow(lines[consumed])
		if !ok {
			break
		}
		if len(row) < len(header) {
			row = append(row, make([]string, len(header)-len(row))...)
		} else if len(row) > len(header) {
			break
		}
		table.rows = append(table.rows, row)
		consumed++
	}
	return table, consumed, true
}

func splitTableRow(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.Contains(trimmed, "|") {
		return nil, false
	}
	trimmed = strings.TrimPrefix(trimmed, "|")
	if strings.HasSuffix(trimmed, "|") && !strings.HasSuffix(trimmed, `\|`) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	var cells []string
	var cell strings.Builder
	escaped, inCode := false, false
	for _, char := range trimmed {
		switch {
		case escaped:
			if char != '|' && char != '\\' {
				cell.WriteRune('\\')
			}
			cell.WriteRune(char)
			escaped = false
		case char == '\\':
			escaped = true
		case char == '`':
			inCode = !inCode
			cell.WriteRune(char)
		case char == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteRune(char)
		}
	}
	if escaped {
		cell.WriteRune('\\')
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	return cells, len(cells) > 1
}

func parseTableDelimiter(cell string) (tableAlignment, bool) {
	cell = strings.TrimSpace(cell)
	left, right := strings.HasPrefix(cell, ":"), strings.HasSuffix(cell, ":")
	cell = strings.TrimPrefix(strings.TrimSuffix(cell, ":"), ":")
	if len(cell) < 3 || strings.Trim(cell, "-") != "" {
		return 0, false
	}
	if left && right {
		return tableAlignCenter, true
	}
	if right {
		return tableAlignRight, true
	}
	return tableAlignLeft, true
}

func (r *MarkdownRenderer) renderTable(table markdownTable) string {
	width := 80
	if r.width != nil {
		if detected := r.width(); detected > 0 {
			width = detected
		}
	}
	// A stacked representation remains readable when even modest columns would
	// leave almost no room for cell content.
	if width < 48 || len(table.rows[0])*10+(len(table.rows[0])-1)*2 > width {
		return r.renderStackedTable(table, width)
	}
	columnWidths := make([]int, len(table.rows[0]))
	for _, row := range table.rows {
		for column, cell := range row {
			// Keep enough room for inline Markdown delimiters while wrapping. The
			// delimiters disappear during rendering, but splitting a code span can
			// leave unmatched backticks visible in the resulting fragments.
			columnWidths[column] = max(columnWidths[column], ansi.VisibleWidth(cell))
		}
	}
	available := width - 2*(len(columnWidths)-1)
	for sumInts(columnWidths) > available {
		widest := 0
		for i := range columnWidths {
			if columnWidths[i] > columnWidths[widest] {
				widest = i
			}
		}
		if columnWidths[widest] <= 8 {
			return r.renderStackedTable(table, width)
		}
		columnWidths[widest]--
	}
	var out strings.Builder
	for rowIndex, row := range table.rows {
		wrapped := make([][]string, len(row))
		height := 1
		for column, cell := range row {
			wrapped[column] = wrapTableCell(cell, columnWidths[column])
			height = max(height, len(wrapped[column]))
		}
		for line := 0; line < height; line++ {
			for column := range row {
				cell := ""
				if line < len(wrapped[column]) {
					cell = r.renderInline(wrapped[column][line])
				}
				if rowIndex == 0 {
					cell = r.style.Bold(cell)
				}
				out.WriteString(padTableCell(cell, columnWidths[column], table.alignments[column]))
				if column+1 < len(row) {
					out.WriteString("  ")
				}
			}
			out.WriteByte('\n')
		}
		if rowIndex == 0 {
			for column, columnWidth := range columnWidths {
				out.WriteString(r.style.Rule(columnWidth))
				if column+1 < len(columnWidths) {
					out.WriteString("  ")
				}
			}
			out.WriteByte('\n')
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func (r *MarkdownRenderer) renderStackedTable(table markdownTable, width int) string {
	var out strings.Builder
	headings := table.rows[0]
	for rowIndex, row := range table.rows[1:] {
		if rowIndex > 0 {
			out.WriteByte('\n')
		}
		for column, value := range row {
			label := r.style.Bold(r.renderInline(headings[column])) + ": "
			indent := strings.Repeat(" ", ansi.VisibleWidth(label))
			valueWidth := max(width-ansi.VisibleWidth(label), 1)
			for line, part := range wrapTableCell(value, valueWidth) {
				if line == 0 {
					out.WriteString(label)
				} else {
					out.WriteString(indent)
				}
				out.WriteString(r.renderInline(part))
				out.WriteByte('\n')
			}
		}
	}
	if len(table.rows) == 1 {
		out.WriteString(r.style.Bold(strings.Join(headings, "  ")))
	}
	return strings.TrimRight(out.String(), "\n")
}

func wrapTableCell(cell string, width int) []string {
	if ansi.VisibleWidth(cell) <= width {
		return []string{cell}
	}
	var lines []string
	var current strings.Builder
	for _, word := range strings.Fields(cell) {
		if current.Len() > 0 && ansi.VisibleWidth(current.String())+1+ansi.VisibleWidth(word) <= width {
			current.WriteByte(' ')
			current.WriteString(word)
			continue
		}
		if current.Len() > 0 {
			lines = append(lines, current.String())
			current.Reset()
		}
		for ansi.VisibleWidth(word) > width {
			runes := []rune(word)
			cut := 0
			for cut < len(runes) && ansi.VisibleWidth(string(runes[:cut+1])) <= width {
				cut++
			}
			lines = append(lines, string(runes[:cut]))
			word = string(runes[cut:])
		}
		current.WriteString(word)
	}
	if current.Len() > 0 || len(lines) == 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func padTableCell(cell string, width int, alignment tableAlignment) string {
	padding := max(width-ansi.VisibleWidth(cell), 0)
	left, right := 0, padding
	switch alignment {
	case tableAlignRight:
		left, right = padding, 0
	case tableAlignCenter:
		left, right = padding/2, padding-padding/2
	}
	return strings.Repeat(" ", left) + cell + strings.Repeat(" ", right)
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

// parseHeading reports whether line is an ATX heading and returns its text and
// level (1-6).
func parseHeading(line string) (text string, level int, ok bool) {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i < 1 || i > 6 {
		return "", 0, false
	}
	if i == len(line) {
		return "", 0, false
	}
	if line[i] != ' ' && line[i] != '\t' {
		return "", 0, false
	}
	text = strings.TrimLeft(line[i:], " \t")
	return text, i, true
}

// parseBullet reports whether line is a bullet list item and returns its text.
func parseBullet(line string) (string, bool) {
	if len(line) < 2 {
		return "", false
	}
	marker := line[0]
	if marker != '-' && marker != '*' && marker != '+' {
		return "", false
	}
	if line[1] != ' ' && line[1] != '\t' {
		return "", false
	}
	return strings.TrimLeft(line[1:], " \t"), true
}

// parseNumbered reports whether line is a numbered list item and returns the
// number and the item text.
func parseNumbered(line string) (num, text string, ok bool) {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i == len(line) {
		return "", "", false
	}
	if line[i] != '.' && line[i] != ')' {
		return "", "", false
	}
	num = line[:i]
	rest := line[i+1:]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", "", false
	}
	return num, strings.TrimLeft(rest, " \t"), true
}

// renderInline renders inline markdown (code spans, links, bold, italic).
func (r *MarkdownRenderer) renderInline(text string) string {
	var b strings.Builder
	runes := []rune(text)
	n := len(runes)
	i := 0

	for i < n {
		c := runes[i]

		// Code span: `...`
		if c == '`' {
			if close := indexRune(runes, i+1, '`'); close >= 0 {
				b.WriteString(r.style.Code(string(runes[i+1 : close])))
				i = close + 1
				continue
			}
		}

		// Link: [text](url)
		if c == '[' {
			if close := indexRune(runes, i+1, ']'); close >= 0 {
				if close+1 < n && runes[close+1] == '(' {
					if urlEnd := indexRune(runes, close+2, ')'); urlEnd >= 0 {
						label := string(runes[i+1 : close])
						url := string(runes[close+2 : urlEnd])
						b.WriteString(r.style.Underline(r.renderInline(label)))
						b.WriteString(" ")
						b.WriteString(r.style.Link(url))
						i = urlEnd + 1
						continue
					}
				}
			}
		}

		// Bold: **...**
		if c == '*' && i+1 < n && runes[i+1] == '*' {
			if close := indexRune(runes, i+2, '*'); close >= 0 && close > i+1 {
				end := close
				if end+1 < n && runes[end+1] == '*' {
					end++
				}
				inner := string(runes[i+2 : close])
				b.WriteString(r.style.Bold(r.renderInline(inner)))
				i = end + 1
				continue
			}
		}

		// Italic: *...*
		if c == '*' {
			if close := findItalicClose(runes, i+1); close >= 0 {
				inner := string(runes[i+1 : close])
				b.WriteString(r.style.Underline(r.renderInline(inner)))
				i = close + 1
				continue
			}
		}

		b.WriteRune(c)
		i++
	}

	return b.String()
}

// indexRune returns the index of the first occurrence of r in runes starting
// at start, or -1 if not found.
func indexRune(runes []rune, start int, r rune) int {
	for j := start; j < len(runes); j++ {
		if runes[j] == r {
			return j
		}
	}
	return -1
}

// findItalicClose finds the index of the closing single '*' for an italic span
// opened just before start. It ignores '*' that are part of a '**' pair.
func findItalicClose(runes []rune, start int) int {
	for j := start; j < len(runes); j++ {
		if runes[j] != '*' {
			continue
		}
		prevStar := j > 0 && runes[j-1] == '*'
		nextStar := j+1 < len(runes) && runes[j+1] == '*'
		if prevStar || nextStar {
			continue
		}
		return j
	}
	return -1
}

package syntax

import (
	"strings"
	"unicode"
)

// shortLiteralLimit is how far a Short string literal may run before its
// opening quote is treated as plain text instead.
const shortLiteralLimit = 12

// Highlighter tokenizes the lines of one file in order, carrying state such
// as an open block comment or multi-line string from line to line.
type Highlighter struct {
	lang *Lang
	// str is the multi-line string that is still open.
	str *StringRule
	// strTok is the token index where the open string started.
	strTok int
	// block is one more than the index of the open block comment.
	block int
	// inner highlights embedded code: a markdown fence's body, a Makefile
	// recipe or a Dockerfile RUN command.
	inner *Highlighter
	// fence is the open markdown fence marker.
	fence string
	// inTag is set inside an HTML tag that spans lines.
	inTag bool
	// depth is the CSS brace depth.
	depth int
	// shellContinues is set when a Dockerfile shell command continues on
	// the next line.
	shellContinues bool
}

// New returns a highlighter positioned at the start of a file.
func (l *Lang) New() *Highlighter { return &Highlighter{lang: l} }

// Reset forgets all state, for when the next line does not follow the last
// one, such as at the start of the next diff hunk.
func (h *Highlighter) Reset() { *h = Highlighter{lang: h.lang} }

// Line returns the tokens of the next line. Their texts concatenate to line.
func (h *Highlighter) Line(line string) []Token {
	b := &builder{}
	switch h.lang.Mode {
	case ModeMarkdown:
		h.markdown(line, b)
	case ModeHTML:
		h.html([]rune(line), b)
	case ModeYAML:
		h.yaml(line, b)
	case ModeTOML:
		h.toml(line, b)
	case ModeMake:
		h.makefile(line, b)
	case ModeDocker:
		h.dockerfile(line, b)
	default:
		h.code([]rune(line), 0, b)
	}
	return b.toks
}

type builder struct {
	toks []Token
}

func (b *builder) add(kind Kind, text string) {
	if text == "" {
		return
	}
	if n := len(b.toks); n > 0 && b.toks[n-1].Kind == kind {
		b.toks[n-1].Text += text
		return
	}
	b.toks = append(b.toks, Token{Kind: kind, Text: text})
}

func (b *builder) addTokens(toks []Token) {
	for _, tok := range toks {
		b.add(tok.Kind, tok.Text)
	}
}

// code scans rs[i:] as source code.
func (h *Highlighter) code(rs []rune, i int, b *builder) {
	l := h.lang
	for i < len(rs) {
		if h.block > 0 {
			i = h.blockComment(rs, i, b)
			continue
		}
		if h.str != nil {
			i = h.stringBody(rs, i, b)
			continue
		}
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			j := i
			for j < len(rs) && unicode.IsSpace(rs[j]) {
				j++
			}
			b.add(Plain, string(rs[i:j]))
			i = j
		case l.Preprocessor && r == '#' && strings.TrimSpace(string(rs[:i])) == "":
			j := scanWhile(rs, i+1, isWordRune)
			directive := string(rs[i:j])
			b.add(Keyword, directive)
			i = j
			if directive == "#include" || directive == "#import" {
				// #include <stdio.h>: the bracketed name is a string.
				k := scanWhile(rs, j, unicode.IsSpace)
				if k < len(rs) && rs[k] == '<' {
					b.add(Plain, string(rs[j:k]))
					b.add(String, string(rs[k:]))
					return
				}
			}
		case h.lineComment(rs, i):
			b.add(Comment, string(rs[i:]))
			return
		case h.openBlockComment(rs, i, b):
			i += len([]rune(l.BlockComments[h.block-1][0]))
		case h.openString(rs, i, b):
			i += len([]rune(h.str.Open))
		case l.Mode == ModeCSS:
			i = h.css(rs, i, b)
		case isDigit(r) || r == '.' && i+1 < len(rs) && isDigit(rs[i+1]):
			j := scanNumber(rs, i)
			b.add(Number, string(rs[i:j]))
			i = j
		case l.Variables && r == '$':
			i = h.variable(rs, i, b)
		case l.Annotations && r == '@' && i+1 < len(rs) && h.identStart(rs[i+1]):
			j := scanWhile(rs, i+1, h.identRune)
			b.add(Attr, string(rs[i:j]))
			i = j
		case h.identStart(r):
			j := scanWhile(rs, i+1, h.identRune)
			word := string(rs[i:j])
			if l.StringPrefixes[word] && j < len(rs) && h.stringRuleAt(rs, j) != nil {
				b.add(String, word)
			} else {
				b.add(h.classify(word, rs, j), word)
			}
			i = j
		case isOperator(r):
			j := scanWhile(rs, i, isOperator)
			b.add(Operator, string(rs[i:j]))
			i = j
		default:
			b.add(Plain, string(r))
			i++
		}
	}
	if h.str != nil && !h.str.Multiline {
		h.str = nil
	}
}

func (h *Highlighter) blockComment(rs []rune, i int, b *builder) int {
	end := h.lang.BlockComments[h.block-1][1]
	j := indexAt(rs, i, end)
	if j < 0 {
		b.add(Comment, string(rs[i:]))
		return len(rs)
	}
	j += len([]rune(end))
	b.add(Comment, string(rs[i:j]))
	h.block = 0
	return j
}

func (h *Highlighter) openBlockComment(rs []rune, i int, b *builder) bool {
	for k, pair := range h.lang.BlockComments {
		if hasPrefixAt(rs, i, pair[0]) {
			h.block = k + 1
			b.add(Comment, pair[0])
			return true
		}
	}
	return false
}

func (h *Highlighter) lineComment(rs []rune, i int) bool {
	for _, marker := range h.lang.LineComments {
		if !hasPrefixAt(rs, i, marker) {
			continue
		}
		// "#" only starts a comment at a word boundary: not in $#, a#b or
		// a URL fragment.
		if marker == "#" && i > 0 && !unicode.IsSpace(rs[i-1]) {
			continue
		}
		return true
	}
	return false
}

// stringRuleAt returns the string literal that opens at rs[i], if any.
// Rules are tried in order, so longer delimiters must come first.
func (h *Highlighter) stringRuleAt(rs []rune, i int) *StringRule {
	for k := range h.lang.Strings {
		rule := &h.lang.Strings[k]
		if !hasPrefixAt(rs, i, rule.Open) {
			continue
		}
		if rule.Short && !closesWithin(rs, i+len([]rune(rule.Open)), *rule) {
			continue
		}
		return rule
	}
	return nil
}

func (h *Highlighter) openString(rs []rune, i int, b *builder) bool {
	rule := h.stringRuleAt(rs, i)
	if rule == nil {
		return false
	}
	copied := *rule
	h.str = &copied
	h.strTok = len(b.toks)
	b.add(String, rule.Open)
	return true
}

// closesWithin reports whether a short literal closes soon after start.
func closesWithin(rs []rune, start int, rule StringRule) bool {
	for j := start; j < len(rs) && j-start < shortLiteralLimit; j++ {
		if rule.Escape && rs[j] == '\\' {
			j++
			continue
		}
		if hasPrefixAt(rs, j, rule.Close) {
			return j > start
		}
	}
	return false
}

// stringBody scans the open string from rs[i] to its close or the end of
// the line.
func (h *Highlighter) stringBody(rs []rune, i int, b *builder) int {
	rule := h.str
	start := i
	for i < len(rs) {
		if rule.Escape && rs[i] == '\\' {
			b.add(String, string(rs[start:i]))
			end := min(i+2, len(rs))
			b.add(Escape, string(rs[i:end]))
			i, start = end, end
			continue
		}
		// Shell double quotes expand variables.
		if h.lang.Variables && rule.Open == `"` && rs[i] == '$' && i+1 < len(rs) && rs[i+1] != '"' {
			b.add(String, string(rs[start:i]))
			i = h.variable(rs, i, b)
			start = i
			continue
		}
		if hasPrefixAt(rs, i, rule.Close) {
			end := i + len([]rune(rule.Close))
			b.add(String, string(rs[start:end]))
			h.str = nil
			if h.lang.KeyStrings && nextNonSpace(rs, end) == ':' {
				for k := h.strTok; k < len(b.toks); k++ {
					b.toks[k].Kind = Key
				}
			}
			return end
		}
		i++
	}
	b.add(String, string(rs[start:]))
	return len(rs)
}

func (h *Highlighter) classify(word string, rs []rune, end int) Kind {
	l := h.lang
	key := word
	if l.CaseInsensitive {
		key = strings.ToLower(word)
	}
	switch {
	case l.Keywords[key]:
		return Keyword
	case l.Constants[key]:
		return Constant
	case l.Types[key]:
		return Type
	case l.Builtins[key]:
		return Builtin
	case l.Macros && end < len(rs) && rs[end] == '!':
		return Builtin
	case nextNonSpace(rs, end) == '(':
		return Function
	case l.CapitalTypes && unicode.IsUpper([]rune(word)[0]):
		return Type
	}
	return Plain
}

// variable scans a shell or make variable reference starting at '$'.
func (h *Highlighter) variable(rs []rune, i int, b *builder) int {
	if i+1 >= len(rs) {
		b.add(Plain, "$")
		return i + 1
	}
	switch next := rs[i+1]; {
	case next == '{' || next == '(' && h.lang.Mode == ModeMake:
		closer := map[rune]rune{'{': '}', '(': ')'}[next]
		j := i + 2
		for j < len(rs) && rs[j] != closer {
			j++
		}
		j = min(j+1, len(rs))
		b.add(Variable, string(rs[i:j]))
		return j
	case next == '(':
		b.add(Operator, "$(")
		return i + 2
	case isWordRune(next):
		j := scanWhile(rs, i+1, isWordRune)
		b.add(Variable, string(rs[i:j]))
		return j
	case strings.ContainsRune("@*#?$!<^+%-", next):
		b.add(Variable, string(rs[i:i+2]))
		return i + 2
	}
	b.add(Plain, "$")
	return i + 1
}

// css handles what is specific to stylesheets; comments and strings are
// already handled by code.
func (h *Highlighter) css(rs []rune, i int, b *builder) int {
	r := rs[i]
	inBlock := h.depth > 0
	switch {
	case r == '{' || r == '}':
		if r == '{' {
			h.depth++
		} else if h.depth > 0 {
			h.depth--
		}
		b.add(Operator, string(r))
		return i + 1
	case r == '@' || r == '!':
		j := scanWhile(rs, i+1, h.identRune)
		b.add(Keyword, string(rs[i:j]))
		return j
	case r == '#' && inBlock:
		j := scanWhile(rs, i+1, isWordRune)
		b.add(Number, string(rs[i:j]))
		return j
	case (r == '#' || r == '.') && !inBlock && i+1 < len(rs) && h.identStart(rs[i+1]):
		j := scanWhile(rs, i+1, h.identRune)
		b.add(Attr, string(rs[i:j]))
		return j
	case r == ':' && !inBlock && i+1 < len(rs) && rs[i+1] != ' ':
		j := scanWhile(rs, i, func(r rune) bool { return r == ':' })
		j = scanWhile(rs, j, h.identRune)
		b.add(Attr, string(rs[i:j]))
		return j
	case isDigit(r) || r == '.' && i+1 < len(rs) && isDigit(rs[i+1]) || r == '-' && i+1 < len(rs) && isDigit(rs[i+1]):
		j := scanNumber(rs, i+1)
		j = scanWhile(rs, j, func(r rune) bool { return r == '%' })
		b.add(Number, string(rs[i:j]))
		return j
	case h.identStart(r) || r == '-' || r == '*':
		j := scanWhile(rs, i+1, h.identRune)
		word := string(rs[i:j])
		switch {
		case !inBlock && hasPrefixAt(rs, j, ": "):
			// A property in a media query: (max-width: 600px).
			b.add(Key, word)
		case !inBlock:
			b.add(Tag, word)
		case nextNonSpace(rs, j) == ':':
			b.add(Key, word)
		case h.lang.Constants[strings.ToLower(word)]:
			b.add(Constant, word)
		case nextNonSpace(rs, j) == '(':
			b.add(Function, word)
		default:
			b.add(Plain, word)
		}
		return j
	case isOperator(r):
		b.add(Operator, string(r))
		return i + 1
	}
	b.add(Plain, string(r))
	return i + 1
}

func (h *Highlighter) identStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_' || r == '$' && strings.ContainsRune(h.lang.IdentRunes, '$')
}

func (h *Highlighter) identRune(r rune) bool {
	return isWordRune(r) || strings.ContainsRune(h.lang.IdentRunes, r)
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isOperator(r rune) bool { return strings.ContainsRune("+-*/%=<>!&|^~?:", r) }

// scanNumber reads a numeric literal: digits, separators, a fraction, an
// exponent and any base prefix or type suffix letters.
func scanNumber(rs []rune, i int) int {
	j := i
	for j < len(rs) {
		c := rs[j]
		switch {
		case isDigit(c) || c == '_':
			j++
		case c == '.' && j+1 < len(rs) && isDigit(rs[j+1]):
			j++
		case (c == 'e' || c == 'E') && j+2 < len(rs) && (rs[j+1] == '+' || rs[j+1] == '-') && isDigit(rs[j+2]):
			j += 2
		case unicode.IsLetter(c):
			j++
		default:
			return j
		}
	}
	return j
}

func scanWhile(rs []rune, i int, ok func(rune) bool) int {
	for i < len(rs) && ok(rs[i]) {
		i++
	}
	return i
}

func hasPrefixAt(rs []rune, i int, prefix string) bool {
	if prefix == "" {
		return false
	}
	for _, p := range prefix {
		if i >= len(rs) || rs[i] != p {
			return false
		}
		i++
	}
	return true
}

func indexAt(rs []rune, i int, needle string) int {
	for j := i; j < len(rs); j++ {
		if hasPrefixAt(rs, j, needle) {
			return j
		}
	}
	return -1
}

func nextNonSpace(rs []rune, i int) rune {
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
		i++
	}
	if i < len(rs) {
		return rs[i]
	}
	return 0
}

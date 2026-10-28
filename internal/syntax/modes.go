package syntax

import (
	"strings"
	"unicode"
)

// Line scanners for languages that are not shaped like source code. Each
// falls back to the code scanner for the parts that are, such as values.

func (h *Highlighter) markdown(line string, b *builder) {
	trimmed := strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(trimmed)]
	if h.fence != "" {
		if strings.HasPrefix(trimmed, h.fence) && strings.TrimRight(strings.TrimLeft(trimmed, h.fence[:1]), " ") == "" {
			h.fence, h.inner = "", nil
			b.add(Tag, line)
			return
		}
		if h.inner != nil {
			b.addTokens(h.inner.Line(line))
			return
		}
		b.add(String, line)
		return
	}
	if marker := fenceMarker(trimmed); marker != "" {
		h.fence = marker
		if lang := Lookup(trimmed[len(marker):]); lang != nil {
			h.inner = lang.New()
		}
		b.add(Tag, indent+marker)
		b.add(Type, trimmed[len(marker):])
		return
	}
	switch {
	case isHeading(trimmed):
		b.add(Heading, line)
		return
	case strings.HasPrefix(trimmed, ">"):
		b.add(Comment, line)
		return
	case isRule(trimmed):
		b.add(Operator, line)
		return
	}
	b.add(Plain, indent)
	if marker := listMarker(trimmed); marker != "" {
		b.add(Operator, marker)
		trimmed = trimmed[len(marker):]
	}
	markdownInline([]rune(trimmed), b)
}

func fenceMarker(line string) string {
	for _, c := range []string{"`", "~"} {
		if strings.HasPrefix(line, c+c+c) {
			n := len(line) - len(strings.TrimLeft(line, c))
			return line[:n]
		}
	}
	return ""
}

func isHeading(line string) bool {
	n := len(line) - len(strings.TrimLeft(line, "#"))
	return n >= 1 && n <= 6 && (len(line) == n || line[n] == ' ')
}

func isRule(line string) bool {
	line = strings.ReplaceAll(line, " ", "")
	if len(line) < 3 {
		return false
	}
	return strings.Trim(line, "-") == "" || strings.Trim(line, "*") == "" || strings.Trim(line, "_") == ""
}

func listMarker(line string) string {
	for _, bullet := range []string{"- [ ] ", "- [x] ", "- ", "* ", "+ "} {
		if strings.HasPrefix(line, bullet) {
			return bullet
		}
	}
	digits := len(line) - len(strings.TrimLeft(line, "0123456789"))
	if digits > 0 && digits < len(line)-1 && (line[digits] == '.' || line[digits] == ')') && line[digits+1] == ' ' {
		return line[:digits+2]
	}
	return ""
}

func markdownInline(rs []rune, b *builder) {
	for i := 0; i < len(rs); {
		switch {
		case rs[i] == '`':
			ticks := scanWhile(rs, i, func(r rune) bool { return r == '`' }) - i
			if end := indexAt(rs, i+ticks, strings.Repeat("`", ticks)); end >= 0 {
				b.add(String, string(rs[i:end+ticks]))
				i = end + ticks
				continue
			}
		case rs[i] == '[' || rs[i] == '!' && i+1 < len(rs) && rs[i+1] == '[':
			if end := linkEnd(rs, i); end > 0 {
				b.add(Link, string(rs[i:end]))
				i = end
				continue
			}
		case hasPrefixAt(rs, i, "**") || hasPrefixAt(rs, i, "__"):
			if end := indexAt(rs, i+2, string(rs[i:i+2])); end > i+2 {
				b.add(Keyword, string(rs[i:end+2]))
				i = end + 2
				continue
			}
		case hasPrefixAt(rs, i, "http://") || hasPrefixAt(rs, i, "https://"):
			j := scanWhile(rs, i, func(r rune) bool { return !unicode.IsSpace(r) && r != ')' && r != '>' })
			b.add(Link, string(rs[i:j]))
			i = j
			continue
		}
		b.add(Plain, string(rs[i]))
		i++
	}
}

// linkEnd returns the index after a [text](target) link starting at i, or
// zero when there is none.
func linkEnd(rs []rune, i int) int {
	if rs[i] == '!' {
		i++
	}
	close := indexAt(rs, i, "](")
	if close < 0 {
		return 0
	}
	end := indexAt(rs, close+2, ")")
	if end < 0 {
		return 0
	}
	return end + 1
}

func (h *Highlighter) html(rs []rune, b *builder) {
	for i := 0; i < len(rs); {
		switch {
		case h.block > 0:
			i = h.blockComment(rs, i, b)
		case h.inTag:
			i = h.htmlTag(rs, i, b)
		case h.openBlockComment(rs, i, b):
			i += len([]rune(h.lang.BlockComments[h.block-1][0]))
		case rs[i] == '<' && i+1 < len(rs) && (rs[i+1] == '/' || rs[i+1] == '!' || rs[i+1] == '?' || unicode.IsLetter(rs[i+1])):
			j := scanWhile(rs, i+1, func(r rune) bool { return r == '/' || r == '!' || r == '?' })
			j = scanWhile(rs, j, func(r rune) bool { return isWordRune(r) || r == '-' || r == ':' || r == '.' })
			b.add(Tag, string(rs[i:j]))
			h.inTag = true
			i = j
		case rs[i] == '&':
			j := scanWhile(rs, i+1, func(r rune) bool { return isWordRune(r) || r == '#' })
			if j < len(rs) && rs[j] == ';' {
				b.add(Escape, string(rs[i:j+1]))
				i = j + 1
				continue
			}
			b.add(Plain, "&")
			i++
		default:
			j := scanWhile(rs, i+1, func(r rune) bool { return r != '<' && r != '&' })
			b.add(Plain, string(rs[i:j]))
			i = j
		}
	}
}

func (h *Highlighter) htmlTag(rs []rune, i int, b *builder) int {
	r := rs[i]
	switch {
	case r == '>' || r == '/' && i+1 < len(rs) && rs[i+1] == '>' || r == '?' && i+1 < len(rs) && rs[i+1] == '>':
		j := i + 1
		if r != '>' {
			j++
		}
		b.add(Tag, string(rs[i:j]))
		h.inTag = false
		return j
	case r == '"' || r == '\'':
		j := i + 1
		for j < len(rs) && rs[j] != r {
			j++
		}
		j = min(j+1, len(rs))
		b.add(String, string(rs[i:j]))
		return j
	case r == '=':
		b.add(Operator, "=")
		return i + 1
	case unicode.IsSpace(r):
		j := scanWhile(rs, i, unicode.IsSpace)
		b.add(Plain, string(rs[i:j]))
		return j
	}
	j := scanWhile(rs, i, func(r rune) bool {
		return !unicode.IsSpace(r) && r != '=' && r != '>' && r != '"' && r != '\'' && r != '/'
	})
	if j == i {
		j++
	}
	b.add(Attr, string(rs[i:j]))
	return j
}

func (h *Highlighter) yaml(line string, b *builder) {
	trimmed := strings.TrimLeft(line, " ")
	if strings.HasPrefix(trimmed, "#") {
		b.add(Comment, line)
		return
	}
	if trimmed == "---" || trimmed == "..." {
		b.add(Operator, line)
		return
	}
	rs := []rune(line)
	i := scanWhile(rs, 0, func(r rune) bool { return r == ' ' })
	b.add(Plain, string(rs[:i]))
	for hasPrefixAt(rs, i, "- ") || i == len(rs)-1 && rs[i] == '-' {
		b.add(Operator, "-")
		j := scanWhile(rs, i+1, func(r rune) bool { return r == ' ' })
		b.add(Plain, string(rs[i+1:j]))
		i = j
	}
	if end := yamlKeyEnd(rs, i); end > i {
		b.add(Key, string(rs[i:end]))
		b.add(Operator, ":")
		i = end + 1
	}
	j := scanWhile(rs, i, func(r rune) bool { return r == ' ' })
	b.add(Plain, string(rs[i:j]))
	i = j
	if i < len(rs) {
		switch rs[i] {
		case '&', '*':
			j := scanWhile(rs, i+1, func(r rune) bool { return !unicode.IsSpace(r) })
			b.add(Variable, string(rs[i:j]))
			i = j
		case '!':
			j := scanWhile(rs, i+1, func(r rune) bool { return !unicode.IsSpace(r) })
			b.add(Type, string(rs[i:j]))
			i = j
		case '|', '>':
			b.add(Operator, string(rs[i]))
			i++
		}
	}
	h.code(rs, i, b)
}

// yamlKeyEnd returns the index of the colon that ends a mapping key at
// rs[i:], or -1. The colon must be followed by a space or end the line.
func yamlKeyEnd(rs []rune, i int) int {
	if i >= len(rs) || rs[i] == '#' || rs[i] == '{' || rs[i] == '[' {
		return -1
	}
	var quote rune
	if rs[i] == '"' || rs[i] == '\'' {
		quote = rs[i]
	}
	for j := i; j < len(rs); j++ {
		switch {
		case quote != 0:
			if j > i && rs[j] == quote {
				quote = 0
			}
		case rs[j] == '#' && j > i && rs[j-1] == ' ':
			return -1
		case rs[j] == ':' && (j+1 == len(rs) || rs[j+1] == ' '):
			return j
		}
	}
	return -1
}

func (h *Highlighter) toml(line string, b *builder) {
	rs := []rune(line)
	if h.str != nil {
		h.code(rs, 0, b)
		return
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "[") {
		end := strings.LastIndex(line, "]")
		if end > 0 {
			b.add(Heading, line[:end+1])
			h.code([]rune(line[end+1:]), 0, b)
			return
		}
	}
	if strings.HasPrefix(trimmed, "#") {
		b.add(Comment, line)
		return
	}
	if eq := tomlKeyEnd(rs); eq > 0 {
		key := string(rs[:eq])
		lead := key[:len(key)-len(strings.TrimLeft(key, " \t"))]
		name := strings.TrimRight(key[len(lead):], " \t")
		b.add(Plain, lead)
		b.add(Key, name)
		b.add(Plain, key[len(lead)+len(name):])
		b.add(Operator, "=")
		h.code(rs, eq+1, b)
		return
	}
	h.code(rs, 0, b)
}

// tomlKeyEnd returns the index of the '=' after a key, or -1.
func tomlKeyEnd(rs []rune) int {
	var quote rune
	for j, r := range rs {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '=':
			return j
		case r == '#' || r == '[' || r == '{':
			return -1
		}
	}
	return -1
}

func (h *Highlighter) shell() *Highlighter {
	if h.inner == nil {
		h.inner = languages["shell"].New()
	}
	return h.inner
}

func (h *Highlighter) makefile(line string, b *builder) {
	if strings.HasPrefix(line, "\t") {
		b.add(Plain, "\t")
		b.addTokens(h.shell().Line(line[1:]))
		return
	}
	rs := []rune(line)
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		b.add(Comment, line)
		return
	}
	nameEnd := scanWhile(rs, 0, func(r rune) bool { return isWordRune(r) || r == '.' || r == '-' || r == '/' })
	afterName := scanWhile(rs, nameEnd, func(r rune) bool { return r == ' ' })
	for _, op := range []string{"::=", ":=", "?=", "+=", "!=", "="} {
		if nameEnd > 0 && hasPrefixAt(rs, afterName, op) {
			b.add(Variable, string(rs[:nameEnd]))
			b.add(Plain, string(rs[nameEnd:afterName]))
			b.add(Operator, op)
			h.code(rs, afterName+len(op), b)
			return
		}
	}
	if colon := strings.IndexByte(line, ':'); colon > 0 && !strings.ContainsAny(line[:colon], "=$#") && !strings.HasPrefix(line[colon:], ":=") {
		targets := []rune(line[:colon])
		if h.lang.Builtins[strings.TrimSpace(string(targets))] {
			b.add(Builtin, string(targets))
		} else {
			b.add(Key, string(targets))
		}
		b.add(Operator, ":")
		h.code(rs, len(targets)+1, b)
		return
	}
	h.code(rs, 0, b)
}

func (h *Highlighter) dockerfile(line string, b *builder) {
	continues := strings.HasSuffix(strings.TrimRight(line, " \t"), "\\")
	if h.shellContinues {
		b.addTokens(h.shell().Line(line))
		h.shellContinues = continues
		return
	}
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "#") {
		b.add(Comment, line)
		return
	}
	rs := []rune(line)
	start := len([]rune(line)) - len([]rune(trimmed))
	end := scanWhile(rs, start, unicode.IsLetter)
	instruction := strings.ToUpper(string(rs[start:end]))
	if !h.lang.Keywords[instruction] {
		h.code(rs, 0, b)
		return
	}
	b.add(Plain, string(rs[:start]))
	b.add(Keyword, string(rs[start:end]))
	switch instruction {
	case "RUN", "CMD", "ENTRYPOINT":
		b.addTokens(h.shell().Line(string(rs[end:])))
		h.shellContinues = continues
	default:
		h.code(rs, end, b)
	}
}

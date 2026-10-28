package syntax

import (
	"fmt"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/ansi"
)

var kindNames = map[Kind]string{
	Plain: "plain", Keyword: "kw", Type: "type", Builtin: "builtin", Constant: "const",
	String: "str", Escape: "esc", Number: "num", Comment: "comment", Function: "fn",
	Operator: "op", Key: "key", Tag: "tag", Attr: "attr", Heading: "heading",
	Variable: "var", Link: "link",
}

// highlight runs lines through one highlighter and returns each line's
// tokens.
func highlight(t *testing.T, lang string, lines ...string) [][]Token {
	t.Helper()
	l := Lookup(lang)
	if l == nil {
		t.Fatalf("no language %q", lang)
	}
	h := l.New()
	var out [][]Token
	for _, line := range lines {
		toks := h.Line(line)
		if got := Text(toks); got != line {
			t.Fatalf("%s: tokens rebuild %q, want %q", lang, got, line)
		}
		out = append(out, toks)
	}
	return out
}

// has reports whether toks contains a token of kind with exactly text.
func has(toks []Token, kind Kind, text string) bool {
	for _, tok := range toks {
		if tok.Kind == kind && tok.Text == text {
			return true
		}
	}
	return false
}

func describe(toks []Token) string {
	var parts []string
	for _, tok := range toks {
		parts = append(parts, fmt.Sprintf("%s(%q)", kindNames[tok.Kind], tok.Text))
	}
	return strings.Join(parts, " ")
}

type expect struct {
	kind Kind
	text string
}

func TestLanguages(t *testing.T) {
	cases := []struct {
		lang string
		line string
		want []expect
	}{
		{"go", `func main() { x := len(s) + 0x1F // done`, []expect{{Keyword, "func"}, {Function, "main"}, {Builtin, "len"}, {Number, "0x1F"}, {Operator, ":="}, {Comment, "// done"}}},
		{"go", `var r rune = '\n'; s := "a\tb"; ok := true`, []expect{{Type, "rune"}, {String, "'"}, {Escape, `\n`}, {Escape, `\t`}, {Constant, "true"}}},
		{"python", `@cache`, []expect{{Attr, "@cache"}}},
		{"python", `def f(x): return r"\d+" if x is None else 1.5e-3  # why`, []expect{{Keyword, "def"}, {Function, "f"}, {String, `r"`}, {Constant, "None"}, {Number, "1.5e-3"}, {Comment, "# why"}}},
		{"ts", `export interface User { id: number; name?: string }`, []expect{{Keyword, "export"}, {Keyword, "interface"}, {Type, "User"}, {Type, "number"}, {Type, "string"}}},
		{"js", "const $el = document.querySelector(`#${id}`);", []expect{{Keyword, "const"}, {Plain, " $el "}, {Builtin, "document"}, {Function, "querySelector"}, {String, "`#${id}`"}}},
		{"rust", `fn get<'a>(v: &'a Vec<u8>) -> Option<&'a u8> { println!("{}", v[0]); None }`, []expect{{Keyword, "fn"}, {Type, "Vec"}, {Type, "u8"}, {Builtin, "println"}, {Constant, "None"}}},
		{"c", `#include <stdio.h>`, []expect{{Keyword, "#include"}, {String, "<stdio.h>"}}},
		{"c", `int main(void) { char c = 'x'; return NULL; }`, []expect{{Type, "int"}, {Function, "main"}, {String, "'x'"}, {Constant, "NULL"}}},
		{"java", `@Override public String toString() { return "x"; }`, []expect{{Attr, "@Override"}, {Keyword, "public"}, {Type, "String"}, {Function, "toString"}}},
		{"sh", `if [ -n "$HOME" ]; then echo ${PATH} $1 # comment`, []expect{{Keyword, "if"}, {Variable, "$HOME"}, {Builtin, "echo"}, {Variable, "${PATH}"}, {Variable, "$1"}, {Comment, "# comment"}}},
		{"sh", `echo $(date) a#b`, []expect{{Operator, "$("}, {Plain, "date) a#b"}}},
		{"json", `{"name": "caretz", "n": 3, "ok": true, "x": null}`, []expect{{Key, `"name"`}, {String, `"caretz"`}, {Number, "3"}, {Constant, "true"}, {Constant, "null"}}},
		{"yaml", `  - name: build # step`, []expect{{Operator, "-"}, {Key, "name"}, {Comment, "# step"}}},
		{"yaml", `base: &base {retries: 3, enabled: yes}`, []expect{{Key, "base"}, {Variable, "&base"}, {Number, "3"}, {Constant, "yes"}}},
		{"yaml", `url: "http://x:80/a#b"`, []expect{{Key, "url"}, {String, `"http://x:80/a#b"`}}},
		{"toml", `[tool.caretz]`, []expect{{Heading, "[tool.caretz]"}}},
		{"toml", `timeout = 30 # seconds`, []expect{{Key, "timeout"}, {Number, "30"}, {Comment, "# seconds"}}},
		{"md", `## Install`, []expect{{Heading, "## Install"}}},
		{"md", "- run `make build` then see [docs](docs/cli.md) and **really**", []expect{{Operator, "- "}, {String, "`make build`"}, {Link, "[docs](docs/cli.md)"}, {Keyword, "**really**"}}},
		{"html", `<a href="/x" class='y'>go &amp; see</a><!-- note -->`, []expect{{Tag, "<a"}, {Attr, "href"}, {String, `"/x"`}, {String, "'y'"}, {Tag, ">"}, {Escape, "&amp;"}, {Tag, "</a>"}, {Comment, "<!-- note -->"}}},
		{"css", `.btn:hover > a { color: #fff; margin: 0 -2px 10%; } /* c */`, []expect{{Attr, ".btn:hover"}, {Tag, "a"}, {Key, "color"}, {Number, "#fff"}, {Number, "-2px"}, {Number, "10%"}, {Comment, "/* c */"}}},
		{"css", `@media (max-width: 600px) {`, []expect{{Keyword, "@media"}, {Key, "max-width"}, {Number, "600px"}}},
		{"sql", `SELECT id, count(*) FROM users WHERE name = 'o''k' -- x`, []expect{{Keyword, "SELECT"}, {Builtin, "count"}, {Keyword, "FROM"}, {String, "'o''k'"}, {Comment, "-- x"}}},
		{"sql", `select 1 from t`, []expect{{Keyword, "select"}, {Keyword, "from"}}},
		{"makefile", `build: deps ## Compile`, []expect{{Key, "build"}, {Operator, ":"}}},
		{"makefile", `GOFLAGS ?= -trimpath $(EXTRA)`, []expect{{Variable, "GOFLAGS"}, {Operator, "?="}, {Variable, "$(EXTRA)"}}},
		{"makefile", "\tgo build -o $@ ./cmd # out", []expect{{Variable, "$@"}, {Comment, "# out"}}},
		{"dockerfile", `FROM golang:1.25 AS build`, []expect{{Keyword, "FROM"}, {Keyword, "AS"}}},
		{"dockerfile", `RUN if [ -f go.mod ]; then go mod download; fi`, []expect{{Keyword, "RUN"}, {Keyword, "if"}, {Keyword, "fi"}}},
	}
	for _, tc := range cases {
		toks := highlight(t, tc.lang, tc.line)[0]
		for _, want := range tc.want {
			if !has(toks, want.kind, want.text) {
				t.Errorf("%s %q: missing %s(%q)\n  got %s", tc.lang, tc.line, kindNames[want.kind], want.text, describe(toks))
			}
		}
	}
}

func TestStateCarriesAcrossLines(t *testing.T) {
	goLines := highlight(t, "go", "s := `raw", "still raw // not a comment", "end` + x", "/* block", "still */ y")
	if !has(goLines[1], String, "still raw // not a comment") || !has(goLines[2], String, "end`") || !has(goLines[2], Plain, " x") {
		t.Fatalf("go raw string: %s | %s", describe(goLines[1]), describe(goLines[2]))
	}
	if !has(goLines[4], Comment, "still */") || !has(goLines[4], Plain, " y") {
		t.Fatalf("go block comment: %s", describe(goLines[4]))
	}

	py := highlight(t, "python", `doc = """first`, `def not_code():`, `"""`, `def f(): pass`)
	if !has(py[1], String, "def not_code():") || !has(py[3], Keyword, "def") {
		t.Fatalf("python triple quote: %s | %s", describe(py[1]), describe(py[3]))
	}

	// A plain string never runs onto the next line.
	c := highlight(t, "c", `char *s = "unterminated`, `int x;`)
	if !has(c[1], Type, "int") {
		t.Fatalf("unterminated string leaked: %s", describe(c[1]))
	}

	md := highlight(t, "markdown", "```go", "func main() {}", "```", "# after")
	if !has(md[0], Type, "go") || !has(md[1], Keyword, "func") || !has(md[2], Tag, "```") || !has(md[3], Heading, "# after") {
		t.Fatalf("markdown fence: %s | %s | %s", describe(md[0]), describe(md[1]), describe(md[3]))
	}
	unknown := highlight(t, "markdown", "```", "plain text", "```")
	if !has(unknown[1], String, "plain text") {
		t.Fatalf("unknown fence: %s", describe(unknown[1]))
	}

	html := highlight(t, "html", `<input`, `  type="text"`, `  disabled>text`)
	if !has(html[1], Attr, "type") || !has(html[2], Tag, ">") || !has(html[2], Plain, "text") {
		t.Fatalf("html tag across lines: %s | %s", describe(html[1]), describe(html[2]))
	}

	docker := highlight(t, "dockerfile", `RUN apt-get update && \`, `    apt-get install -y git`, `COPY . .`)
	if !has(docker[2], Keyword, "COPY") {
		t.Fatalf("dockerfile continuation: %s", describe(docker[2]))
	}

	h := Lookup("go").New()
	h.Line("/* open")
	h.Reset()
	if toks := h.Line("x := 1"); has(toks, Comment, "x := 1") {
		t.Fatal("Reset kept the open comment")
	}
}

func TestDetectAndLookup(t *testing.T) {
	cases := map[string]string{
		"main.go": "go", "a/b/app.tsx": "typescript", "x.PY": "python", "Makefile": "makefile",
		"build.mk": "makefile", "Dockerfile": "dockerfile", "Dockerfile.dev": "dockerfile", "ci.yml": "yaml",
		"Cargo.toml": "toml", "README.md": "markdown", "index.html": "html", "lib.rs": "rust", "x.hpp": "cpp",
	}
	for path, want := range cases {
		if lang := Detect(path, ""); lang == nil || lang.Name != want {
			t.Errorf("Detect(%q) = %v, want %s", path, lang, want)
		}
	}
	if lang := Detect("bin/deploy", "#!/usr/bin/env bash"); lang == nil || lang.Name != "shell" {
		t.Errorf("shebang = %v", lang)
	}
	if Detect("notes.unknown", "") != nil || Lookup("") != nil || Lookup("klingon") != nil {
		t.Error("unknown language detected")
	}
	if lang := Lookup("golang {.numberLines}"); lang == nil || lang.Name != "go" {
		t.Errorf("fence info = %v", lang)
	}
}

func TestChangedRanges(t *testing.T) {
	oldSpans, newSpans := ChangedRanges(`	return fmt.Errorf("open %s", path)`, `	return fmt.Errorf("read %s: %w", path, err)`)
	if len(oldSpans) == 0 || len(newSpans) == 0 {
		t.Fatalf("no spans: %v %v", oldSpans, newSpans)
	}
	runes := []rune(`	return fmt.Errorf("open %s", path)`)
	if got := string(runes[oldSpans[0].Start:oldSpans[0].End]); got != "open" {
		t.Fatalf("old span = %q (%v)", got, oldSpans)
	}
	if a, b := ChangedRanges("x := computeTotal(items)", "fmt.Println(\"hello world\")"); a != nil || b != nil {
		t.Fatalf("unrelated lines emphasized: %v %v", a, b)
	}
	if a, b := ChangedRanges("same", "same"); a != nil || b != nil {
		t.Fatalf("identical lines emphasized: %v %v", a, b)
	}
	removed, added := BlockSpans([]string{"a := 1", "b := 2"}, []string{"a := 10"})
	if len(removed) != 2 || len(added) != 1 || removed[1] != nil || len(added[0]) == 0 {
		t.Fatalf("block = %v %v", removed, added)
	}
}

func TestRenderLine(t *testing.T) {
	theme := NewTheme(DepthTrueColor)
	toks := highlight(t, "go", "\tx := \"hi\"")[0]
	out := RenderLine(toks, theme, RenderOptions{Background: theme.AddedBg, Emphasis: theme.AddedEmphasis, Spans: []Span{{Start: 6, End: 10}}, PadTo: 20})
	if ansi.Strip(out) != "    x := \"hi\"       " {
		t.Fatalf("text = %q", ansi.Strip(out))
	}
	// Every styled run restates the line background or the emphasis.
	for _, seq := range strings.Split(out, "\x1b[0;")[1:] {
		style, _, _ := strings.Cut(seq, "m")
		if !strings.Contains(style, theme.AddedBg) && !strings.Contains(style, theme.AddedEmphasis) {
			t.Fatalf("run without background: %q in %q", style, out)
		}
	}
	if !strings.Contains(out, theme.AddedEmphasis+"m\"") || !strings.HasSuffix(out, "\x1b[0m") {
		t.Fatalf("emphasis or reset missing: %q", out)
	}

	cut := RenderLine(toks, theme, RenderOptions{MaxWidth: 8})
	if got := ansi.Strip(cut); got != "    x :…" || ansi.VisibleWidth(cut) != 8 {
		t.Fatalf("truncated = %q", got)
	}
	if plain := RenderLine(toks, nil, RenderOptions{}); plain != "    x := \"hi\"" {
		t.Fatalf("uncolored = %q", plain)
	}
}

func TestThemeDepths(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	if DepthFromEnv(env(map[string]string{"COLORTERM": "truecolor"})) != DepthTrueColor ||
		DepthFromEnv(env(map[string]string{"TERM": "xterm-256color"})) != Depth256 ||
		DepthFromEnv(env(nil)) != Depth16 {
		t.Fatal("depth detection")
	}
	if t256 := NewTheme(Depth256); t256.AddedBg != "48;5;22" || !strings.HasPrefix(t256.Foreground(Keyword), "38;5;") {
		t.Fatalf("256 theme = %q %q", t256.AddedBg, t256.Foreground(Keyword))
	}
	if t16 := NewTheme(Depth16); t16.Backgrounds() || t16.Foreground(String) != "32" {
		t.Fatal("16-color theme")
	}
}

func BenchmarkHighlightDiff(b *testing.B) {
	line := `	if err := s.store.Add(Note{Author: AuthorUser, File: "a.go", Line: 3}); err != nil { // keep`
	theme := NewTheme(DepthTrueColor)
	for b.Loop() {
		h := Lookup("go").New()
		for range 10000 {
			RenderLine(h.Line(line), theme, RenderOptions{Background: theme.AddedBg, PadTo: 120})
		}
	}
}

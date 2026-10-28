package output

import (
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/syntax"
)

func testRenderer() *MarkdownRenderer {
	return NewMarkdownRenderer(NewStyleEnabled(false))
}

func TestMarkdownPlainPassthrough(t *testing.T) {
	got := testRenderer().Render("just a line")
	if got != "just a line" {
		t.Errorf("Render() = %q, want %q", got, "just a line")
	}
}

func TestMarkdownHeadings(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"# One", "One"},
		{"## Two", "Two"},
		{"### Three", "Three"},
		{"###### Six", "Six"},
		{"####### Seven", "####### Seven"}, // 7 hashes is not a heading
		{"#NoSpace", "#NoSpace"},           // no space after #
	}
	for _, tt := range tests {
		if got := testRenderer().Render(tt.in); got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMarkdownFencedCodeBlock(t *testing.T) {
	in := "```\nfunc main() {}\n```"
	got := testRenderer().Render(in)
	want := "── code\nfunc main() {}"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestMarkdownFencedCodeBlockWithLanguage(t *testing.T) {
	in := "```sh\nmake test \\\n  && make lint \\\n  && make build\n```"
	got := testRenderer().Render(in)
	want := "── sh\nmake test \\\n  && make lint \\\n  && make build"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	if strings.Contains(got, "│") {
		t.Errorf("Render() includes a gutter that contaminates copied code: %q", got)
	}
}

func TestMarkdownFencedCodeIsSyntaxHighlighted(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	theme := syntax.NewTheme(syntax.DepthTrueColor)
	got := NewMarkdownRenderer(NewStyleEnabled(true)).Render("```go\nfunc main() { return \"x\" }\n```\n\n```text\nfunc plain\n```")
	for _, want := range []string{theme.Foreground(syntax.Keyword) + "mfunc", theme.Foreground(syntax.String) + "m\"x\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("Render() missing %q: %q", want, got)
		}
	}
	if !strings.Contains(got, NewStyleEnabled(true).Muted("func plain")) {
		t.Fatalf("unknown language not gray: %q", got)
	}
	if stripped := ansi.Strip(got); !strings.Contains(stripped, "func main() { return \"x\" }\n") {
		t.Fatalf("code text changed: %q", stripped)
	}
}

func TestMarkdownFencedCodeBlockUnterminated(t *testing.T) {
	in := "```go\nfunc main() {}"
	got := testRenderer().Render(in)
	if !strings.Contains(got, "func main() {}") {
		t.Errorf("Render() = %q, want to contain code line", got)
	}
}

func TestMarkdownBulletList(t *testing.T) {
	in := "- one\n* two\n+ three"
	got := testRenderer().Render(in)
	for _, want := range []string{"one", "two", "three"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, want to contain %q", got, want)
		}
	}
}

func TestMarkdownBulletNotList(t *testing.T) {
	// A lone dash with no space is not a list item.
	if got := testRenderer().Render("-x"); got != "-x" {
		t.Errorf("Render() = %q, want %q", got, "-x")
	}
}

func TestMarkdownNumberedList(t *testing.T) {
	in := "1. first\n2. second"
	got := testRenderer().Render(in)
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, want to contain %q", got, want)
		}
	}
}

func TestMarkdownBlockquote(t *testing.T) {
	in := "> quoted text"
	got := testRenderer().Render(in)
	if !strings.Contains(got, "quoted text") {
		t.Errorf("Render() = %q, want to contain %q", got, "quoted text")
	}
}

func TestMarkdownInlineCode(t *testing.T) {
	in := "use `foo` here"
	got := testRenderer().Render(in)
	if !strings.Contains(got, "foo") {
		t.Errorf("Render() = %q, want to contain %q", got, "foo")
	}
}

func TestMarkdownLink(t *testing.T) {
	in := "see [docs](https://example.com) now"
	got := testRenderer().Render(in)
	if !strings.Contains(got, "docs") || !strings.Contains(got, "https://example.com") {
		t.Errorf("Render() = %q, want to contain label and url", got)
	}
}

func TestMarkdownLinkMalformed(t *testing.T) {
	// Unbalanced link falls through as plain text.
	in := "see [docs here"
	got := testRenderer().Render(in)
	if got != "see [docs here" {
		t.Errorf("Render() = %q, want %q", got, "see [docs here")
	}
}

func TestMarkdownBold(t *testing.T) {
	in := "this is **bold** text"
	got := testRenderer().Render(in)
	if !strings.Contains(got, "bold") {
		t.Errorf("Render() = %q, want to contain %q", got, "bold")
	}
}

func TestMarkdownItalic(t *testing.T) {
	in := "this is *italic* text"
	got := testRenderer().Render(in)
	if !strings.Contains(got, "italic") {
		t.Errorf("Render() = %q, want to contain %q", got, "italic")
	}
}

func TestMarkdownBoldNotItalic(t *testing.T) {
	// ** should be treated as bold, not two italics.
	in := "**bold**"
	got := testRenderer().Render(in)
	if got != "bold" {
		t.Errorf("Render() = %q, want %q", got, "bold")
	}
}

func TestMarkdownMixed(t *testing.T) {
	in := "# Title\n\nSome **bold** and *italic* and `code`.\n\n- item one\n- item two\n\n> a quote\n\n```\ncode line\n```"
	got := testRenderer().Render(in)
	for _, want := range []string{"Title", "bold", "italic", "code", "item one", "item two", "a quote", "code line"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, want to contain %q", got, want)
		}
	}
}

func TestMarkdownStyleApplied(t *testing.T) {
	r := NewMarkdownRenderer(NewStyleEnabled(true))
	got := r.Render("# Title")
	if !strings.Contains(got, "\x1b[1m") {
		t.Errorf("Render() = %q, want to contain bold SGR", got)
	}
}

func TestMarkdownHeadingsUseBlue(t *testing.T) {
	r := NewMarkdownRenderer(NewStyleEnabled(true))
	got := r.Render("## Section")
	if !strings.Contains(got, "\x1b[34mSection\x1b[0m") {
		t.Fatalf("Render() = %q, want blue heading text", got)
	}
}

func TestMarkdownInlineCodeUsesTealWithoutBackticks(t *testing.T) {
	r := NewMarkdownRenderer(NewStyleEnabled(true))
	got := r.Render("use `foo`")
	if !strings.Contains(got, "\x1b[38;2;129;181;174mfoo\x1b[0m") {
		t.Fatalf("Render() = %q, want teal inline code without backticks", got)
	}
	if strings.Contains(got, "`foo`") || strings.Contains(got, "\x1b[33m") {
		t.Fatalf("Render() = %q, should not show backticks or use yellow", got)
	}
}

func TestMarkdownTable(t *testing.T) {
	in := "| Name | State |\n| :--- | ---: |\n| parser | ready |\n| renderer | active |"
	got := testRenderer().Render(in)
	for _, want := range []string{"Name", "State", "────", "parser", "ready", "renderer", "active"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, want to contain %q", got, want)
		}
	}
	if strings.Contains(got, "|") || strings.Contains(got, "---") {
		t.Errorf("Render() left Markdown table syntax in output: %q", got)
	}
}

func TestMarkdownTableHandlesEscapedAndCodePipes(t *testing.T) {
	in := "A | B\n--- | ---\none \\| two | `x|y`"
	got := testRenderer().Render(in)
	for _, want := range []string{"one | two", "x|y"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, want to contain %q", got, want)
		}
	}
}

func TestMarkdownTableNarrowOutputIsStacked(t *testing.T) {
	r := NewMarkdownRendererWithWidth(NewStyleEnabled(false), func() int { return 30 })
	got := r.Render("Name | Description\n--- | ---\nparser | Parses tables")
	for _, want := range []string{"Name: parser", "Description: Parses tables"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, want to contain %q", got, want)
		}
	}
}

func TestMarkdownTableWrapsAndMeasuresWideUnicode(t *testing.T) {
	r := NewMarkdownRendererWithWidth(NewStyleEnabled(false), func() int { return 50 })
	got := r.Render("Item | Description\n--- | ---\n界界 | a description that needs wrapping in its cell")
	for _, line := range strings.Split(got, "\n") {
		if width := ansi.VisibleWidth(line); width > 50 {
			t.Errorf("rendered line width = %d, want <= 50: %q", width, line)
		}
	}
}

func TestMarkdownMalformedTableFallsBackToPlainText(t *testing.T) {
	in := "A | B\n-- | not-a-delimiter\none | two"
	if got := testRenderer().Render(in); got != in {
		t.Errorf("Render() = %q, want malformed table unchanged %q", got, in)
	}
}

func TestParseHeading(t *testing.T) {
	tests := []struct {
		line  string
		text  string
		level int
		ok    bool
	}{
		{"# A", "A", 1, true},
		{"## B", "B", 2, true},
		{"###### F", "F", 6, true},
		{"####### G", "", 0, false},
		{"#X", "", 0, false},
		{"######", "", 0, false},
		{"", "", 0, false},
	}
	for _, tt := range tests {
		text, level, ok := parseHeading(tt.line)
		if ok != tt.ok || text != tt.text || level != tt.level {
			t.Errorf("parseHeading(%q) = (%q,%d,%v), want (%q,%d,%v)",
				tt.line, text, level, ok, tt.text, tt.level, tt.ok)
		}
	}
}

func TestParseBullet(t *testing.T) {
	tests := []struct {
		line string
		text string
		ok   bool
	}{
		{"- a", "a", true},
		{"* b", "b", true},
		{"+ c", "c", true},
		{"-x", "", false},
		{"a", "", false},
	}
	for _, tt := range tests {
		text, ok := parseBullet(tt.line)
		if ok != tt.ok || text != tt.text {
			t.Errorf("parseBullet(%q) = (%q,%v), want (%q,%v)", tt.line, text, ok, tt.text, tt.ok)
		}
	}
}

func TestParseNumbered(t *testing.T) {
	tests := []struct {
		line string
		num  string
		text string
		ok   bool
	}{
		{"1. first", "1", "first", true},
		{"10) tenth", "10", "tenth", true},
		{"1x", "", "", false},
		{"1.", "", "", false},
		{"abc", "", "", false},
	}
	for _, tt := range tests {
		num, text, ok := parseNumbered(tt.line)
		if ok != tt.ok || num != tt.num || text != tt.text {
			t.Errorf("parseNumbered(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tt.line, num, text, ok, tt.num, tt.text, tt.ok)
		}
	}
}

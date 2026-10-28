package output

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDetectStyle(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		isTerminal bool
		want       bool
	}{
		{name: "color terminal", env: map[string]string{}, isTerminal: true, want: true},
		{name: "no color set empty", env: map[string]string{"NO_COLOR": ""}, isTerminal: true, want: true},
		{name: "no color set any value", env: map[string]string{"NO_COLOR": "1"}, isTerminal: true, want: false},
		{name: "no color set whitespace", env: map[string]string{"NO_COLOR": " "}, isTerminal: true, want: false},
		{name: "term dumb", env: map[string]string{"TERM": "dumb"}, isTerminal: true, want: false},
		{name: "term normal", env: map[string]string{"TERM": "xterm-256color"}, isTerminal: true, want: true},
		{name: "non terminal file", env: map[string]string{}, isTerminal: false, want: false},
		{name: "no color wins over term", env: map[string]string{"NO_COLOR": "1", "TERM": "dumb"}, isTerminal: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := func(key string) string { return tt.env[key] }
			isTerminal := func(*os.File) bool { return tt.isTerminal }
			if got := detectStyle(os.Stdout, env, isTerminal); got != tt.want {
				t.Errorf("detectStyle() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectStyleNonOSFileWriter(t *testing.T) {
	buf := &bytes.Buffer{}
	s := NewStyle(buf)
	if s.Enabled() {
		t.Error("NewStyle(non *os.File).Enabled() = true, want false")
	}
}

func TestStyleHelpersDisabled(t *testing.T) {
	s := NewStyleEnabled(false)
	if got := s.Bold("hello"); got != "hello" {
		t.Errorf("Bold() = %q, want %q", got, "hello")
	}
	if got := s.Muted("hello"); got != "hello" {
		t.Errorf("Muted() = %q, want %q", got, "hello")
	}
	if got := s.Error("hello"); got != "hello" {
		t.Errorf("Error() = %q, want %q", got, "hello")
	}
	if got := s.Rule(3); got != "───" {
		t.Errorf("Rule() = %q, want %q", got, "───")
	}
	if s.Enabled() {
		t.Error("Enabled() = true, want false")
	}
}

func TestStyleHelpersEnabled(t *testing.T) {
	s := NewStyleEnabled(true)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Bold", s.Bold("x"), "\x1b[1mx\x1b[0m"},
		{"Underline", s.Underline("x"), "\x1b[4mx\x1b[0m"},
		{"Muted", s.Muted("x"), "\x1b[2mx\x1b[0m"},
		{"Heading", s.Heading("x"), "\x1b[1m\x1b[36mx\x1b[0m"},
		{"ToolName", s.ToolName("x"), "\x1b[1m\x1b[33mx\x1b[0m"},
		{"Agent", s.Agent("x"), "\x1b[35mx\x1b[0m"},
		{"Success", s.Success("x"), "\x1b[32mx\x1b[0m"},
		{"Error", s.Error("x"), "\x1b[31mx\x1b[0m"},
		{"Warning", s.Warning("x"), "\x1b[33mx\x1b[0m"},
		{"MarkdownHeading", s.MarkdownHeading("x"), "\x1b[1m\x1b[34mx\x1b[0m"},
		{"Code", s.Code("x"), "\x1b[38;2;129;181;174mx\x1b[0m"},
		{"PromptCard", s.PromptCard("x"), "\x1b[48;2;56;59;70mx\x1b[0m"},
		{"Mark success", s.Mark(GlyphSuccess), "\x1b[32m✓\x1b[0m"},
		{"Mark bullet", s.Mark(GlyphBullet), "•"},
		{"Status stalled", s.Status("stalled", "x"), "\x1b[31mx\x1b[0m"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s() = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	if !s.Enabled() {
		t.Error("Enabled() = false, want true")
	}
}

// Every role must have a code in every theme at every depth, or the role
// would silently render unstyled; and no theme may emit a color the
// terminal's depth cannot show.
func TestThemesSetEveryRoleWithinDepth(t *testing.T) {
	for _, name := range ThemeNames() {
		for _, depth := range []colorDepth{depth16, depth256, depth24} {
			theme := reflect.ValueOf(themeFor(name, depth))
			for i := 0; i < theme.NumField(); i++ {
				field, code := theme.Type().Field(i).Name, theme.Field(i).String()
				if code == "" {
					t.Errorf("%s at depth %d: %s is empty", name, depth, field)
				}
				if depth < depth24 && strings.Contains(code, ";2;") {
					t.Errorf("%s at depth %d: %s = %q uses a 24-bit color", name, depth, field, code)
				}
				if depth == depth16 && strings.Contains(code, ";5;") {
					t.Errorf("%s at depth %d: %s = %q uses a 256-color code", name, depth, field, code)
				}
			}
		}
	}
}

func TestPaletteThemesFallBackToANSIOn16Colors(t *testing.T) {
	if themeFor("dracula", depth16) != themeFor(ThemeANSI, depth16) {
		t.Error("dracula at 16 colors differs from ansi")
	}
	if themeFor("dracula", depth24).Error != "\x1b[38;2;255;85;85m" {
		t.Errorf("dracula Error = %q", themeFor("dracula", depth24).Error)
	}
}

func TestDetectDepth(t *testing.T) {
	tests := []struct {
		colorterm, term string
		want            colorDepth
	}{
		{"truecolor", "xterm", depth24},
		{"24bit", "", depth24},
		{"", "xterm-256color", depth256},
		{"", "screen-256color", depth256},
		{"", "xterm", depth16},
		{"", "", depth16},
	}
	for _, tt := range tests {
		env := map[string]string{"COLORTERM": tt.colorterm, "TERM": tt.term}
		if got := detectDepth(func(key string) string { return env[key] }); got != tt.want {
			t.Errorf("detectDepth(COLORTERM=%q, TERM=%q) = %d, want %d", tt.colorterm, tt.term, got, tt.want)
		}
	}
}

func TestXterm256(t *testing.T) {
	tests := []struct {
		hex  string
		want int
	}{
		{"#000000", 16},
		{"#ff0000", 196},
		{"#ffffff", 231},
		{"#808080", 244},
		{"#5f87af", 67},
	}
	for _, tt := range tests {
		r, g, b := parseHex(tt.hex)
		if got := xterm256(r, g, b); got != tt.want {
			t.Errorf("xterm256(%s) = %d, want %d", tt.hex, got, tt.want)
		}
	}
}

func TestParseTheme(t *testing.T) {
	for input, want := range map[string]string{"": ThemeANSI, "ANSI": ThemeANSI, " Dracula ": "dracula", "tokyo-night": "tokyo-night"} {
		if got, err := ParseTheme(input); err != nil || got != want {
			t.Errorf("ParseTheme(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := ParseTheme("solarized"); err == nil || !strings.Contains(err.Error(), "catppuccin-mocha") {
		t.Errorf("ParseTheme(solarized) error = %v, want one listing the themes", err)
	}
	if names := ThemeNames(); names[0] != ThemeANSI || len(names) != 5 {
		t.Errorf("ThemeNames() = %v", names)
	}
}

func TestSetThemeReachesExistingStyles(t *testing.T) {
	t.Cleanup(func() { _ = SetTheme(ThemeANSI) })
	s := Style{enabled: true, depth: depth24}
	before := s.Error("x")
	if err := SetTheme("dracula"); err != nil {
		t.Fatal(err)
	}
	if after := s.Error("x"); after == before || after != "\x1b[38;2;255;85;85mx\x1b[0m" {
		t.Errorf("Error() after SetTheme = %q, before %q", after, before)
	}
	if ActiveTheme() != "dracula" {
		t.Errorf("ActiveTheme() = %q", ActiveTheme())
	}
	if pinned := s.WithTheme(ThemeANSI).Error("x"); pinned != before {
		t.Errorf("WithTheme(ansi).Error() = %q, want %q", pinned, before)
	}
	if err := SetTheme("nope"); err == nil {
		t.Error("SetTheme(nope) succeeded")
	}
}

// Colors belong to themes: SGR color codes outside theme.go would bypass
// theming. Cursor and erase sequences (internal/ansi, spinner) and a plain
// attribute reset are allowed.
func TestColorCodesOnlyInTheme(t *testing.T) {
	sgr := regexp.MustCompile(`\\x1b\[[0-9;]*[1-9][0-9;]*m`)
	for _, dir := range []string{".", "../app", "../slashcmd", "../explorer", "../ansi"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if base := filepath.Base(file); strings.HasSuffix(base, "_test.go") || base == "theme.go" || base == "themes.go" {
				continue
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if match := sgr.Find(data); match != nil {
				t.Errorf("%s contains color code %s; add a Theme role instead", file, match)
			}
		}
	}
}

func TestNotices(t *testing.T) {
	var buf bytes.Buffer
	Errorf(&buf, "bad %d", 1)
	Warnf(&buf, "careful")
	Notef(&buf, "fyi")
	if want := "error: bad 1\nwarning: careful\nnote: fyi\n"; buf.String() != want {
		t.Errorf("notices = %q, want %q", buf.String(), want)
	}
}

func TestTruncateRunesKeepsCharactersWhole(t *testing.T) {
	got := truncateRunes("héllo wörld ✓✓✓", 8)
	if got != "héllo..." || !utf8.ValidString(got) {
		t.Errorf("truncateRunes() = %q, want %q", got, "héllo...")
	}
	if got := truncateLine("  a  ✓ b ", 10); got != "a ✓ b" {
		t.Errorf("truncateLine() = %q, want %q", got, "a ✓ b")
	}
}

func TestNewStyleOnBuffer(t *testing.T) {
	s := NewStyle(&bytes.Buffer{})
	if s.Enabled() {
		t.Error("NewStyle(buffer).Enabled() = true, want false")
	}
}

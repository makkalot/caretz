package output

import (
	"io"
	"os"

	"golang.org/x/term"
)

// Style applies a Theme's roles to text. When disabled, every helper returns
// its input unchanged so callers can use it unconditionally. The theme is
// looked up on every call, so a SetTheme reaches Styles created before it.
type Style struct {
	enabled bool
	depth   colorDepth
	// theme pins a theme by name; empty follows the active theme.
	theme string
}

// Enabled reports whether color output is active for this Style.
func (s Style) Enabled() bool { return s.enabled }

// NewStyle detects whether ANSI color is appropriate for w. Color is disabled
// when NO_COLOR is set to any non-empty value, when TERM is "dumb", or when w
// is a non-terminal *os.File. Writers that are not *os.File (in-memory
// buffers, piped output, etc.) are treated as non-terminals.
func NewStyle(w io.Writer) Style {
	f, ok := w.(*os.File)
	if !ok {
		return Style{enabled: false}
	}
	return Style{
		enabled: detectStyle(f, os.Getenv, func(file *os.File) bool {
			return term.IsTerminal(int(file.Fd()))
		}),
		depth: detectDepth(os.Getenv),
	}
}

// NewStyleEnabled returns a Style with color forced on or off, using the ansi
// theme at 24-bit depth. Callers that already know the answer (and tests) use
// this.
func NewStyleEnabled(enabled bool) Style {
	return Style{enabled: enabled, depth: depth24, theme: ThemeANSI}
}

// WithTheme returns a copy of s pinned to the named theme, for previews.
func (s Style) WithTheme(name string) Style {
	s.theme = name
	return s
}

func (s Style) codes() Theme { return themeFor(s.theme, s.depth) }

// detectStyle is the pure decision logic behind NewStyle. env and isTerminal
// are injectable so the behavior is testable without a real TTY.
func detectStyle(f *os.File, env func(string) string, isTerminal func(*os.File) bool) bool {
	if env("NO_COLOR") != "" {
		return false
	}
	if env("TERM") == "dumb" {
		return false
	}
	return isTerminal(f)
}

// apply wraps text with the given SGR code, or returns it unchanged when color
// is disabled.
func (s Style) apply(code, text string) string {
	if !s.enabled || code == "" {
		return text
	}
	return code + text + ansiReset
}

// Bold renders text in bold.
func (s Style) Bold(text string) string { return s.apply(ansiBold, text) }

// Underline renders text underlined.
func (s Style) Underline(text string) string { return s.apply(ansiUnder, text) }

// Muted renders secondary text: status lines, hints, metadata.
func (s Style) Muted(text string) string { return s.apply(s.codes().Muted, text) }

// Heading renders the title of a UI section.
func (s Style) Heading(text string) string { return s.apply(s.codes().Heading, text) }

// Accent highlights a value the user should notice.
func (s Style) Accent(text string) string { return s.apply(s.codes().Accent, text) }

// ToolName renders the name of a tool call.
func (s Style) ToolName(text string) string { return s.apply(s.codes().ToolName, text) }

// Agent renders a subagent or background-source prefix.
func (s Style) Agent(text string) string { return s.apply(s.codes().Agent, text) }

// Success renders a successful outcome.
func (s Style) Success(text string) string { return s.apply(s.codes().Success, text) }

// Error renders a failure.
func (s Style) Error(text string) string { return s.apply(s.codes().Error, text) }

// Warning renders a warning or something waiting on the user.
func (s Style) Warning(text string) string { return s.apply(s.codes().Warning, text) }

// Prompt renders the input prompt and selected menu items.
func (s Style) Prompt(text string) string { return s.apply(s.codes().Prompt, text) }

// PromptCard renders text on the echoed user-prompt background.
func (s Style) PromptCard(text string) string { return s.apply(s.codes().PromptCard, text) }

// DiffAdd renders an added diff line.
func (s Style) DiffAdd(text string) string { return s.apply(s.codes().DiffAdd, text) }

// DiffDel renders a removed diff line.
func (s Style) DiffDel(text string) string { return s.apply(s.codes().DiffDel, text) }

// DiffHunk renders a diff hunk header.
func (s Style) DiffHunk(text string) string { return s.apply(s.codes().DiffHunk, text) }

// MarkdownHeading renders a Markdown heading.
func (s Style) MarkdownHeading(text string) string {
	return s.apply(s.codes().MarkdownHeading, text)
}

// Quote renders a Markdown blockquote.
func (s Style) Quote(text string) string { return s.apply(s.codes().Quote, text) }

// Code renders inline Markdown code.
func (s Style) Code(text string) string { return s.apply(s.codes().Code, text) }

// Link renders a link target.
func (s Style) Link(text string) string { return s.apply(s.codes().Link, text) }

// SupportsThemes reports whether the terminal shows at least 256 colors;
// below that, every theme renders as ansi.
func (s Style) SupportsThemes() bool { return s.depth >= depth256 }

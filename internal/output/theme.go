package output

import "strings"

// ANSI SGR (Select Graphic Rendition) codes. They are referenced only by
// themes; every other package styles text through Style's role methods.
const (
	ansiReset       = "\x1b[0m"
	ansiBold        = "\x1b[1m"
	ansiDim         = "\x1b[2m"
	ansiUnder       = "\x1b[4m"
	ansiRed         = "\x1b[31m"
	ansiGreen       = "\x1b[32m"
	ansiYellow      = "\x1b[33m"
	ansiBlue        = "\x1b[34m"
	ansiMagenta     = "\x1b[35m"
	ansiCyan        = "\x1b[36m"
	ansiBrightBlack = "\x1b[100m"
)

// Theme maps semantic roles to SGR codes. Callers pick a role by meaning
// (Success, Muted, ToolName, ...) and never a color, so a theme can recolor
// every place a role appears without touching the renderers.
type Theme struct {
	// Muted is secondary text: status lines, hints, metadata, separators.
	Muted string
	// Heading titles a section of UI output (startup resources, goal runs).
	Heading string
	// Accent highlights a value the user should notice: a path, a command,
	// a permission target, or a neutral lifecycle event.
	Accent string
	// ToolName is the name of a tool call.
	ToolName string
	// Agent prefixes output that comes from a subagent or a background source.
	Agent string
	// Success, Error and Warning mark outcomes.
	Success string
	Error   string
	Warning string
	// Prompt is the input prompt and the selected item in a menu or button row.
	Prompt string
	// PromptCard is the background of echoed user prompts.
	PromptCard string
	// DiffAdd, DiffDel and DiffHunk color edit diffs.
	DiffAdd  string
	DiffDel  string
	DiffHunk string
	// MarkdownHeading, Quote, Code and Link style rendered Markdown.
	MarkdownHeading string
	Quote           string
	Code            string
	Link            string
}

// colorDepth is how many colors the terminal can show.
type colorDepth int

const (
	depth16 colorDepth = iota
	depth256
	depth24
)

// detectDepth reads the color depth from COLORTERM and TERM.
func detectDepth(env func(string) string) colorDepth {
	switch strings.ToLower(env("COLORTERM")) {
	case "truecolor", "24bit":
		return depth24
	}
	if strings.Contains(env("TERM"), "256color") {
		return depth256
	}
	return depth16
}

// ansiTheme is the built-in "ansi" theme. It uses the terminal's own 16
// colors, so it follows whatever scheme the user configured. Only the
// prompt card and inline code use extra colors, and only where the terminal
// supports them.
func ansiTheme(depth colorDepth) Theme {
	promptCard, code := ansiBrightBlack, ansiCyan
	switch depth {
	case depth256:
		promptCard, code = "\x1b[48;5;237m", "\x1b[38;5;109m"
	case depth24:
		promptCard, code = "\x1b[48;2;56;59;70m", "\x1b[38;2;129;181;174m"
	}
	return Theme{
		Muted:           ansiDim,
		Heading:         ansiBold + ansiCyan,
		Accent:          ansiCyan,
		ToolName:        ansiBold + ansiYellow,
		Agent:           ansiMagenta,
		Success:         ansiGreen,
		Error:           ansiRed,
		Warning:         ansiYellow,
		Prompt:          ansiGreen,
		PromptCard:      promptCard,
		DiffAdd:         ansiGreen,
		DiffDel:         ansiRed,
		DiffHunk:        ansiCyan,
		MarkdownHeading: ansiBold + ansiBlue,
		Quote:           ansiCyan,
		Code:            code,
		Link:            ansiDim,
	}
}

// palette is a theme's colors as "#rrggbb". Every palette maps to roles the
// same way (see compile), so themes differ only in color, never in meaning.
type palette struct {
	Muted, Red, Green, Yellow, Blue, Magenta, Cyan, Code, Surface string
}

// compile turns a palette into SGR codes for depth. Hex colors cannot map to
// the user's 16-color palette in any meaningful way, so 16-color terminals
// get the ansi theme instead.
func (p palette) compile(depth colorDepth) Theme {
	if depth == depth16 {
		return ansiTheme(depth16)
	}
	fg := func(hex string) string { return colorCode(38, hex, depth) }
	return Theme{
		Muted:           fg(p.Muted),
		Heading:         ansiBold + fg(p.Cyan),
		Accent:          fg(p.Cyan),
		ToolName:        ansiBold + fg(p.Yellow),
		Agent:           fg(p.Magenta),
		Success:         fg(p.Green),
		Error:           fg(p.Red),
		Warning:         fg(p.Yellow),
		Prompt:          fg(p.Green),
		PromptCard:      colorCode(48, p.Surface, depth),
		DiffAdd:         fg(p.Green),
		DiffDel:         fg(p.Red),
		DiffHunk:        fg(p.Cyan),
		MarkdownHeading: ansiBold + fg(p.Blue),
		Quote:           fg(p.Cyan),
		Code:            fg(p.Code),
		Link:            fg(p.Muted),
	}
}

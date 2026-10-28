package slashcmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/output"
)

type terminalKey struct{}

// WithTerminal marks ctx as running in an interactive terminal that writes to
// w, so commands can print styled output directly instead of returning
// Markdown that other adapters would also receive.
func WithTerminal(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, terminalKey{}, w)
}

func terminalWriter(ctx context.Context) io.Writer {
	w, _ := ctx.Value(terminalKey{}).(io.Writer)
	return w
}

// ThemeCommand lists the color themes or switches the active one for the
// rest of the process.
type ThemeCommand struct{}

func (ThemeCommand) Name() string        { return "theme" }
func (ThemeCommand) Description() string { return "List color themes or switch theme for this session" }
func (ThemeCommand) Usage() string       { return "/theme [name]" }
func (ThemeCommand) Aliases() []string   { return nil }

// Complete offers theme names for the single positional argument.
func (ThemeCommand) Complete(args string, cursor int) (Completion, bool) {
	tokens, active, ok := completionTokens(args, cursor)
	if !ok || active > 0 {
		return Completion{}, false
	}
	token := tokens[active]
	return tokenCompletion(token, token.value, output.ThemeNames())
}

func (ThemeCommand) Run(ctx context.Context, _ *agent.AgentSession, args string) (Result, error) {
	name := strings.TrimSpace(args)
	if name == "" {
		if w := terminalWriter(ctx); w != nil {
			_, _ = fmt.Fprint(w, themePreview(output.NewStyle(w)))
			return Result{}, nil
		}
		return Result{Message: themeList()}, nil
	}
	if err := output.SetTheme(name); err != nil {
		return Result{}, err
	}
	message := fmt.Sprintf("theme: %s (this session; set output.theme in config to keep it)", output.ActiveTheme())
	if w := terminalWriter(ctx); w != nil && output.ActiveTheme() != output.ThemeANSI && !output.NewStyle(w).SupportsThemes() {
		message += "\nThis terminal reports fewer than 256 colors, so it keeps the ansi colors. Set COLORTERM=truecolor or a 256color TERM if it supports more."
	}
	return Result{Message: message}, nil
}

// themeList is the plain listing for adapters without a terminal.
func themeList() string {
	var b strings.Builder
	b.WriteString("Themes (switch with /theme <name>):\n")
	for _, name := range output.ThemeNames() {
		if name == output.ActiveTheme() {
			fmt.Fprintf(&b, "- **%s** (active)\n", name)
			continue
		}
		fmt.Fprintf(&b, "- %s\n", name)
	}
	return b.String()
}

// themePreview renders every theme's name next to a sample of its roles.
func themePreview(s output.Style) string {
	width := 0
	for _, name := range output.ThemeNames() {
		width = max(width, len(name))
	}
	var b strings.Builder
	b.WriteString(s.Muted("Themes (switch with /theme <name>):") + "\n")
	for _, name := range output.ThemeNames() {
		t := s.WithTheme(name)
		marker := "  "
		if name == output.ActiveTheme() {
			marker = t.Prompt("* ")
		}
		sample := strings.Join([]string{
			t.Mark(output.GlyphSuccess) + " " + t.Success("passed"),
			t.Mark(output.GlyphFailure) + " " + t.Error("failed"),
			t.Warning(output.GlyphWarning + " warning"),
			output.GlyphTool + " " + t.ToolName("Bash"),
			t.Code("code"),
			t.Accent("path/"),
			t.Muted("muted"),
		}, "  ")
		fmt.Fprintf(&b, "%s%s  %s\n", marker, t.Heading(fmt.Sprintf("%-*s", width, name)), sample)
	}
	return b.String()
}

package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/makkalot/caretz/lib/tui"
)

type responseOutput struct {
	out      io.Writer
	markdown *tui.MarkdownRenderer
}

func newResponseOutput(out io.Writer) *responseOutput {
	return responseOutputWithStyle(out, tui.NewStyle(out))
}

func responseOutputWithStyle(out io.Writer, style tui.Style) *responseOutput {
	r := &responseOutput{out: out}
	// Keep the original Markdown for redirected output and terminals where
	// styling is disabled, so callers can save it as a note unchanged.
	if style.Enabled() {
		terminal := tui.NewTerminal(strings.NewReader(""), out)
		r.markdown = tui.NewMarkdownRenderer(style, func() int { return terminal.Size().Width })
	}
	return r
}

func (r *responseOutput) print(text string, timestamp time.Time) error {
	if r.markdown != nil {
		text = r.markdown.Render(text)
	}
	if !timestamp.IsZero() {
		_, err := fmt.Fprintf(r.out, "[%s] %s\n", timestamp.Local().Format("15:04:05"), text)
		return err
	}
	_, err := fmt.Fprintln(r.out, text)
	return err
}

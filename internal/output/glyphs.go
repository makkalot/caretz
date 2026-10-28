package output

import "strings"

// Glyphs shared by every renderer. Each one has a single meaning, so a mark
// reads the same in tool output, goal replay, task summaries and progress.
const (
	GlyphSuccess  = "✓" // passed, done, met
	GlyphFailure  = "✗" // failed, gap, rejected
	GlyphWarning  = "⚠" // degraded but continuing (e.g. unreachable provider)
	GlyphQuestion = "?" // waiting for user input
	GlyphPending  = "○" // not started yet
	GlyphRunning  = "▶" // in progress
	GlyphRetry    = "↻" // replanned, retried or updated
	GlyphStopped  = "■" // blocked or stopped
	GlyphOnline   = "●" // reachable
	GlyphBullet   = "•" // neutral list item
	GlyphTool     = "⚙" // tool call
	GlyphResult   = "→" // tool result
	GlyphArrow    = "→" // from → to
	GlyphEllipsis = "…" // ongoing or truncated
	GlyphRule     = "─" // horizontal rule
)

// RuleWidth is the width of the separators between output blocks.
const RuleWidth = 64

// Rule renders a muted horizontal separator of the given width.
func (s Style) Rule(width int) string {
	return s.Muted(strings.Repeat(GlyphRule, max(width, 1)))
}

// Mark colors a status glyph by its meaning. Glyphs without an outcome
// (bullets, pending) are returned unchanged.
func (s Style) Mark(glyph string) string { return s.MarkText(glyph, glyph) }

// MarkText colors text with the role that glyph's meaning maps to.
func (s Style) MarkText(glyph, text string) string {
	switch glyph {
	case GlyphSuccess, GlyphOnline:
		return s.Success(text)
	case GlyphFailure:
		return s.Error(text)
	case GlyphWarning, GlyphQuestion, GlyphRetry, GlyphStopped:
		return s.Warning(text)
	case GlyphRunning:
		return s.Accent(text)
	}
	return text
}

// StatusGlyph maps a goal or task status to its glyph, or "" for statuses
// that have no outcome yet.
func StatusGlyph(status string) string {
	switch status {
	case "done", "passed":
		return GlyphSuccess
	case "failed", "stalled", "over_budget":
		return GlyphFailure
	case "awaiting_input", "awaiting_approval":
		return GlyphQuestion
	case "stopped", "blocked":
		return GlyphStopped
	case "running", "started":
		return GlyphRunning
	case "pending":
		return GlyphPending
	}
	return ""
}

// Status colors text with the role of status's glyph.
func (s Style) Status(status, text string) string {
	switch StatusGlyph(status) {
	case GlyphSuccess:
		return s.Success(text)
	case GlyphFailure:
		return s.Error(text)
	case GlyphQuestion, GlyphStopped:
		return s.Warning(text)
	}
	return text
}

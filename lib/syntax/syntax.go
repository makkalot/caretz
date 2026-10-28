// Package syntax exposes Caretz's terminal syntax highlighter, the one that
// colors fenced code and Edit diffs, so integrations can render code the same
// way.
package syntax

import internalsyntax "github.com/makkalot/caretz/internal/syntax"

type Kind = internalsyntax.Kind
type Token = internalsyntax.Token
type Span = internalsyntax.Span
type Lang = internalsyntax.Lang
type Highlighter = internalsyntax.Highlighter
type Depth = internalsyntax.Depth
type Theme = internalsyntax.Theme
type RenderOptions = internalsyntax.RenderOptions
type DiffKind = internalsyntax.DiffKind
type DiffLine = internalsyntax.DiffLine

const (
	Plain    = internalsyntax.Plain
	Keyword  = internalsyntax.Keyword
	Type     = internalsyntax.Type
	Builtin  = internalsyntax.Builtin
	Constant = internalsyntax.Constant
	String   = internalsyntax.String
	Escape   = internalsyntax.Escape
	Number   = internalsyntax.Number
	Comment  = internalsyntax.Comment
	Function = internalsyntax.Function
	Operator = internalsyntax.Operator
	Key      = internalsyntax.Key
	Tag      = internalsyntax.Tag
	Attr     = internalsyntax.Attr
	Heading  = internalsyntax.Heading
	Variable = internalsyntax.Variable
	Link     = internalsyntax.Link

	Depth16        = internalsyntax.Depth16
	Depth256       = internalsyntax.Depth256
	DepthTrueColor = internalsyntax.DepthTrueColor

	DiffContext = internalsyntax.DiffContext
	DiffAdded   = internalsyntax.DiffAdded
	DiffRemoved = internalsyntax.DiffRemoved
)

var (
	Lookup         = internalsyntax.Lookup
	Detect         = internalsyntax.Detect
	DepthFromEnv   = internalsyntax.DepthFromEnv
	NewTheme       = internalsyntax.NewTheme
	RenderLine     = internalsyntax.RenderLine
	Text           = internalsyntax.Text
	HighlightHunk  = internalsyntax.HighlightHunk
	RenderDiffLine = internalsyntax.RenderDiffLine
	ChangedRanges  = internalsyntax.ChangedRanges
	BlockSpans     = internalsyntax.BlockSpans
)

package syntax

import (
	"fmt"
	"strings"
)

// Depth is how many colors the terminal can show.
type Depth int

const (
	Depth16 Depth = iota
	Depth256
	DepthTrueColor
)

// DepthFromEnv guesses the terminal's color depth the way most terminal
// programs do: COLORTERM announces true color, TERM names 256 colors.
func DepthFromEnv(getenv func(string) string) Depth {
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return DepthTrueColor
	}
	if strings.Contains(getenv("TERM"), "256color") {
		return Depth256
	}
	return Depth16
}

type rgb struct{ r, g, b int }

// palette is a dark-background palette in the spirit of One Dark.
var palette = map[Kind]rgb{
	Keyword:  {198, 120, 221},
	Type:     {229, 192, 123},
	Builtin:  {86, 182, 194},
	Constant: {209, 154, 102},
	String:   {152, 195, 121},
	Escape:   {86, 182, 194},
	Number:   {209, 154, 102},
	Comment:  {127, 132, 142},
	Function: {97, 175, 239},
	Operator: {86, 182, 194},
	Key:      {224, 108, 117},
	Tag:      {224, 108, 117},
	Attr:     {209, 154, 102},
	Heading:  {97, 175, 239},
	Variable: {224, 108, 117},
	Link:     {97, 175, 239},
}

// basic is the 16-color fallback as SGR foreground codes.
var basic = map[Kind]string{
	Keyword: "35", Type: "33", Builtin: "36", Constant: "33", String: "32",
	Escape: "36", Number: "33", Comment: "90", Function: "34", Operator: "36",
	Key: "31", Tag: "31", Attr: "33", Heading: "34", Variable: "31", Link: "34",
}

var (
	addedBg         = rgb{24, 52, 34}
	addedEmphasis   = rgb{38, 96, 58}
	removedBg       = rgb{62, 26, 32}
	removedEmphasis = rgb{122, 42, 52}
	addedSign       = rgb{110, 200, 120}
	removedSign     = rgb{230, 100, 110}
)

// Theme maps token kinds and diff line states to SGR parameters.
type Theme struct {
	Depth Depth
	fg    map[Kind]string
	// The diff backgrounds are empty at 16 colors, where tints are not
	// available and diff lines keep a green or red foreground instead.
	AddedBg, AddedEmphasis     string
	RemovedBg, RemovedEmphasis string
	AddedSign, RemovedSign     string
}

func NewTheme(depth Depth) *Theme {
	t := &Theme{Depth: depth, fg: map[Kind]string{}}
	if depth == Depth16 {
		for kind, code := range basic {
			t.fg[kind] = code
		}
		t.AddedSign, t.RemovedSign = "32", "31"
		return t
	}
	for kind, color := range palette {
		t.fg[kind] = t.color(38, color)
	}
	t.AddedBg, t.AddedEmphasis = t.color(48, addedBg), t.color(48, addedEmphasis)
	t.RemovedBg, t.RemovedEmphasis = t.color(48, removedBg), t.color(48, removedEmphasis)
	t.AddedSign, t.RemovedSign = t.color(38, addedSign), t.color(38, removedSign)
	return t
}

// Backgrounds reports whether diff lines can be tinted.
func (t *Theme) Backgrounds() bool { return t.AddedBg != "" }

// Foreground returns the SGR parameters of kind, empty for plain text.
func (t *Theme) Foreground(kind Kind) string { return t.fg[kind] }

// color renders c as a foreground (38) or background (48) parameter.
func (t *Theme) color(layer int, c rgb) string {
	if t.Depth == DepthTrueColor {
		return fmt.Sprintf("%d;2;%d;%d;%d", layer, c.r, c.g, c.b)
	}
	return fmt.Sprintf("%d;5;%d", layer, to256(c))
}

// to256 maps a color into the xterm 6x6x6 cube, or onto the gray ramp for
// near-grays, which the cube renders poorly.
func to256(c rgb) int {
	if max(c.r, c.g, c.b)-min(c.r, c.g, c.b) < 12 {
		gray := (c.r + c.g + c.b) / 3
		if gray < 8 {
			return 16
		}
		if gray > 238 {
			return 231
		}
		return 232 + (gray-8)*24/231
	}
	level := func(v int) int {
		if v < 48 {
			return 0
		}
		if v < 115 {
			return 1
		}
		return (v - 35) / 40
	}
	return 16 + 36*level(c.r) + 6*level(c.g) + level(c.b)
}

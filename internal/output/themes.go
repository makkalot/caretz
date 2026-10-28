package output

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

// ThemeANSI is the default theme. It follows the terminal's own palette.
const ThemeANSI = "ansi"

// palettes are the built-in themes besides ansi, from each project's
// published dark palette.
var palettes = map[string]palette{
	"catppuccin-mocha": {
		Muted: "#7f849c", Red: "#f38ba8", Green: "#a6e3a1", Yellow: "#f9e2af", Blue: "#89b4fa",
		Magenta: "#cba6f7", Cyan: "#89dceb", Code: "#fab387", Surface: "#313244",
	},
	"tokyo-night": {
		Muted: "#565f89", Red: "#f7768e", Green: "#9ece6a", Yellow: "#e0af68", Blue: "#7aa2f7",
		Magenta: "#bb9af7", Cyan: "#7dcfff", Code: "#73daca", Surface: "#292e42",
	},
	"dracula": {
		Muted: "#6272a4", Red: "#ff5555", Green: "#50fa7b", Yellow: "#f1fa8c", Blue: "#bd93f9",
		Magenta: "#ff79c6", Cyan: "#8be9fd", Code: "#ffb86c", Surface: "#44475a",
	},
	"github-dark": {
		Muted: "#8b949e", Red: "#ff7b72", Green: "#3fb950", Yellow: "#d29922", Blue: "#58a6ff",
		Magenta: "#d2a8ff", Cyan: "#39c5cf", Code: "#79c0ff", Surface: "#21262d",
	},
}

// compiledThemes holds every theme at every depth, built once at startup.
var compiledThemes = func() map[string][3]Theme {
	themes := map[string][3]Theme{
		ThemeANSI: {ansiTheme(depth16), ansiTheme(depth256), ansiTheme(depth24)},
	}
	for name, p := range palettes {
		themes[name] = [3]Theme{p.compile(depth16), p.compile(depth256), p.compile(depth24)}
	}
	return themes
}()

var activeTheme atomic.Pointer[string]

// ThemeNames lists the built-in themes, ansi first.
func ThemeNames() []string {
	names := make([]string, 0, len(palettes))
	for name := range palettes {
		names = append(names, name)
	}
	slices.Sort(names)
	return append([]string{ThemeANSI}, names...)
}

// ParseTheme normalizes a theme name. Empty means the default theme.
func ParseTheme(value string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(value))
	if name == "" {
		return ThemeANSI, nil
	}
	if _, ok := compiledThemes[name]; !ok {
		return "", fmt.Errorf("theme must be one of %s, got %q", strings.Join(ThemeNames(), ", "), value)
	}
	return name, nil
}

// SetTheme makes name the theme for all output, including Styles that were
// created earlier.
func SetTheme(name string) error {
	parsed, err := ParseTheme(name)
	if err != nil {
		return err
	}
	activeTheme.Store(&parsed)
	return nil
}

// ActiveTheme returns the name of the theme in use.
func ActiveTheme() string {
	if name := activeTheme.Load(); name != nil {
		return *name
	}
	return ThemeANSI
}

// themeFor returns name's codes at depth, or the active theme's when name is
// empty.
func themeFor(name string, depth colorDepth) Theme {
	if name == "" {
		name = ActiveTheme()
	}
	themes, ok := compiledThemes[name]
	if !ok {
		themes = compiledThemes[ThemeANSI]
	}
	return themes[depth]
}

// colorCode renders hex as a foreground (layer 38) or background (layer 48)
// SGR code at depth.
func colorCode(layer int, hex string, depth colorDepth) string {
	r, g, b := parseHex(hex)
	if depth == depth24 {
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", layer, r, g, b)
	}
	return fmt.Sprintf("\x1b[%d;5;%dm", layer, xterm256(r, g, b))
}

func parseHex(hex string) (r, g, b int) {
	value, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		panic(fmt.Sprintf("invalid theme color %q", hex))
	}
	return int(value >> 16 & 0xff), int(value >> 8 & 0xff), int(value & 0xff)
}

// xterm256 returns the xterm-256 index nearest to r, g, b: either a cell of
// the 6×6×6 color cube (16–231) or a step of the gray ramp (232–255).
func xterm256(r, g, b int) int {
	levels := []int{0, 95, 135, 175, 215, 255}
	nearestLevel := func(v int) int {
		best := 0
		for i, level := range levels {
			if abs(v-level) < abs(v-levels[best]) {
				best = i
			}
		}
		return best
	}
	ri, gi, bi := nearestLevel(r), nearestLevel(g), nearestLevel(b)
	cube := 16 + 36*ri + 6*gi + bi
	cubeDistance := distance(r, g, b, levels[ri], levels[gi], levels[bi])

	grayStep := min(max((r+g+b)/3-8+5, 0)/10, 23)
	grayLevel := 8 + 10*grayStep
	if distance(r, g, b, grayLevel, grayLevel, grayLevel) < cubeDistance {
		return 232 + grayStep
	}
	return cube
}

func distance(r1, g1, b1, r2, g2, b2 int) int {
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return dr*dr + dg*dg + db*db
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

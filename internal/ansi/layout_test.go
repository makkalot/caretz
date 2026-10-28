package ansi

import (
	"strings"
	"testing"
)

func TestLayoutWrapsBeforeTerminalEdge(t *testing.T) {
	layout := LayoutText([]rune("abcdefgh"), 8, LayoutOptions{Width: 12, FirstPrefix: ">>> "})
	if len(layout.Rows) != 2 || layout.Rows[0].Text != ">>> abcdefg" || layout.Rows[1].Text != "    h" {
		t.Fatalf("rows = %#v", layout.Rows)
	}
	if layout.Cursor != (Position{Row: 1, Col: 5}) {
		t.Fatalf("cursor = %#v", layout.Cursor)
	}
}

func TestLayoutExpandsTabsWithoutChangingSourceMapping(t *testing.T) {
	text := []rune("?\tgithub.com/makkalot/caretz/bench/cmd/bench\t[no test files]\nok\tpackage")
	layout := LayoutText(text, len(text), LayoutOptions{Width: 32, FirstPrefix: ">>> "})
	for _, row := range layout.Rows {
		if strings.ContainsRune(row.Text, '\t') {
			t.Fatalf("row contains tab: %q", row.Text)
		}
		if width := VisibleWidth(row.Text); width >= 32 {
			t.Fatalf("row width = %d: %q", width, row.Text)
		}
	}
	last := layout.Rows[len(layout.Rows)-1]
	if got := last.SourceIndexAt(last.CellOffset(last.SourceEnd)); got != last.SourceEnd {
		t.Fatalf("source mapping = %d, want %d", got, last.SourceEnd)
	}
}

func TestDisplayWidthHandlesANSICombiningAndWideRunes(t *testing.T) {
	if got := VisibleWidth("\x1b[32mA\x1b[0m" + "e\u0301" + "界"); got != 4 {
		t.Fatalf("width = %d, want 4", got)
	}
	if got := Strip("a\x1b]0;title\a\x1b[31mb\x1b[0m"); got != "ab" {
		t.Fatalf("stripped = %q", got)
	}
}

func TestLayoutPreservesBlankAndTrailingLines(t *testing.T) {
	layout := LayoutText([]rune("a\n\n"), 3, LayoutOptions{Width: 20, FirstPrefix: ">>> "})
	if len(layout.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(layout.Rows))
	}
	if layout.Cursor.Row != 2 {
		t.Fatalf("cursor row = %d, want 2", layout.Cursor.Row)
	}
}

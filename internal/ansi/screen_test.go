package ansi

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestScreenReplacesMultilineFrameWithoutStaleRows(t *testing.T) {
	var output bytes.Buffer
	screen := NewScreen(&output)
	terminal := newVirtualTerminal()

	screen.Draw(Frame{Rows: []string{">>> first", "    second", "    third"}, Cursor: Position{Row: 2, Col: 9}})
	terminal.apply(t, takeOutput(&output))
	assertVisible(t, terminal, []string{">>> first", "    second", "    third"})

	screen.Draw(Frame{Rows: []string{">>> first!", "    second"}, Cursor: Position{Row: 0, Col: 7}})
	terminal.apply(t, takeOutput(&output))
	assertVisible(t, terminal, []string{">>> first!", "    second"})

	screen.Clear()
	terminal.apply(t, takeOutput(&output))
	assertVisible(t, terminal, nil)
}

func TestScreenRegressionTabbedGoTestPasteRepaintsOnce(t *testing.T) {
	text := []rune("cd ./bench && go test ./...\n?\tgithub.com/makkalot/caretz/bench/cmd/bench\t[no test files]\nok\tgithub.com/makkalot/caretz/bench/internal/bench\t1.886s\ncd ./integrations/tools && go test ./...")
	layout := LayoutText(text, len(text), LayoutOptions{Width: 38, FirstPrefix: ">>> "})
	frame := layoutFrame(layout)

	var output bytes.Buffer
	screen := NewScreen(&output)
	terminal := newVirtualTerminal()
	screen.Draw(frame)
	terminal.apply(t, takeOutput(&output))

	editAt := strings.Index(string(text), "internal/bench")
	edited := append([]rune(nil), text...)
	edited = append(edited[:editAt], append([]rune("EDIT-"), edited[editAt:]...)...)
	editedLayout := LayoutText(edited, editAt+len("EDIT-"), LayoutOptions{Width: 38, FirstPrefix: ">>> "})
	screen.Draw(layoutFrame(editedLayout))
	terminal.apply(t, takeOutput(&output))
	visible := strings.Join(terminal.visible(), "\n")
	if strings.Count(visible, ">>> ") != 1 {
		t.Fatalf("visible prompt count = %d\n%s", strings.Count(visible, ">>> "), visible)
	}
	if strings.Count(visible, "cd ./bench") != 1 {
		t.Fatalf("first command repeated\n%s", visible)
	}
	if !strings.Contains(visible, "EDIT-") {
		t.Fatalf("edited content missing\n%s", visible)
	}
}

func layoutFrame(layout TextLayout) Frame {
	rows := make([]string, len(layout.Rows))
	for i, row := range layout.Rows {
		rows[i] = row.Text
	}
	return Frame{Rows: rows, Cursor: layout.Cursor}
}

func takeOutput(buffer *bytes.Buffer) string {
	value := buffer.String()
	buffer.Reset()
	return value
}

type virtualTerminal struct {
	rows [][]rune
	row  int
	col  int
}

func newVirtualTerminal() *virtualTerminal { return &virtualTerminal{rows: [][]rune{{}}} }

func (v *virtualTerminal) apply(t *testing.T, value string) {
	t.Helper()
	for i := 0; i < len(value); {
		switch value[i] {
		case '\r':
			v.col = 0
			i++
		case '\n':
			v.row++
			v.ensureRow(v.row)
			i++
		case '\x1b':
			if i+1 >= len(value) || value[i+1] != '[' {
				t.Fatalf("unsupported escape in %q", value[i:])
			}
			end := i + 2
			for end < len(value) && (value[end] < '@' || value[end] > '~') {
				end++
			}
			if end == len(value) {
				t.Fatalf("unterminated escape in %q", value[i:])
			}
			v.applyCSI(t, value[i+2:end], value[end])
			i = end + 1
		default:
			r := rune(value[i])
			v.ensureCell(v.row, v.col)
			v.rows[v.row][v.col] = r
			v.col++
			i++
		}
	}
}

func (v *virtualTerminal) applyCSI(t *testing.T, raw string, command byte) {
	t.Helper()
	if strings.HasPrefix(raw, "?") {
		return
	}
	n := 1
	if raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("CSI %q%c: %v", raw, command, err)
		}
		n = parsed
	}
	switch command {
	case 'A':
		v.row = max(0, v.row-n)
	case 'B':
		v.row += n
		v.ensureRow(v.row)
	case 'G':
		v.col = max(0, n-1)
	case 'K':
		if raw != "2" {
			t.Fatalf("unsupported clear mode %q", raw)
		}
		v.rows[v.row] = nil
	default:
		t.Fatalf("unsupported CSI command %c", command)
	}
}

func (v *virtualTerminal) ensureRow(row int) {
	for len(v.rows) <= row {
		v.rows = append(v.rows, nil)
	}
}

func (v *virtualTerminal) ensureCell(row, col int) {
	v.ensureRow(row)
	for len(v.rows[row]) <= col {
		v.rows[row] = append(v.rows[row], ' ')
	}
}

func (v *virtualTerminal) visible() []string {
	last := len(v.rows) - 1
	for last >= 0 && strings.TrimSpace(string(v.rows[last])) == "" {
		last--
	}
	if last < 0 {
		return nil
	}
	rows := make([]string, last+1)
	for i := range rows {
		rows[i] = strings.TrimRight(string(v.rows[i]), " ")
	}
	return rows
}

func assertVisible(t *testing.T, terminal *virtualTerminal, want []string) {
	t.Helper()
	got := terminal.visible()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("visible screen:\n%q\nwant:\n%q", got, want)
	}
}

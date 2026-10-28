package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatReadOutput(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		filePath        string
		offset          int
		limit           int
		wantContains    []string
		wantNotContains []string
		wantLineCount   int
		wantHeaderLines bool
		wantTruncation  bool
	}{
		{
			name:            "small file, defaults",
			content:         makeLines(10),
			filePath:        "test.txt",
			offset:          0,
			limit:           0,
			wantContains:    []string{"1: line 1", "10: line 10", "lines 1-10 of 10 total"},
			wantNotContains: []string{"truncated"},
		},
		{
			name:            "explicit offset and limit (more content after)",
			content:         makeLines(10),
			filePath:        "test.txt",
			offset:          3,
			limit:           4,
			wantContains:    []string{"3: line 3", "6: line 6", "Showing lines 3-6 of 10", "offset=7"},
			wantNotContains: []string{"2: line 2", "7: line 7"},
		},
		{
			name:            "large file, default truncation",
			content:         makeLines(3000),
			filePath:        "big.txt",
			offset:          0,
			limit:           0,
			wantContains:    []string{"1: line 1", "200: line 200", "Showing lines 1-200 of 3000", "offset=201"},
			wantNotContains: []string{"201: line 201"},
			wantTruncation:  true,
		},
		{
			name:            "offset beyond end of file",
			content:         makeLines(10),
			filePath:        "test.txt",
			offset:          20,
			limit:           5,
			wantContains:    []string{"lines 11-10 of 10 total"},
			wantNotContains: []string{": line "},
		},
		{
			name:            "single line read",
			content:         makeLines(10),
			filePath:        "test.txt",
			offset:          1,
			limit:           1,
			wantContains:    []string{"1: line 1", "lines 1-1 of 10 total"},
			wantNotContains: []string{"2: line 2"},
		},
		{
			name:         "line number prefixes correct",
			content:      "aaa\nbbb\nccc\nddd\neee",
			filePath:     "test.txt",
			offset:       0,
			limit:        0,
			wantContains: []string{"1: aaa", "2: bbb", "3: ccc", "4: ddd", "5: eee"},
		},
		{
			name:         "empty file",
			content:      "",
			filePath:     "empty.txt",
			offset:       0,
			limit:        0,
			wantContains: []string{"0 total"},
		},
		{
			name:         "file with no trailing newline",
			content:      "line1\nline2",
			filePath:     "test.txt",
			offset:       0,
			limit:        0,
			wantContains: []string{"1: line1", "2: line2", "lines 1-2 of 2 total"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatReadOutput([]byte(tt.content), tt.filePath, tt.offset, tt.limit)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("output missing expected %q\nGot:\n%s", want, got)
				}
			}
			for _, notWant := range tt.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("output should not contain %q\nGot:\n%s", notWant, got)
				}
			}
		})
	}
}

func TestFormatReadOutput_ByteCap(t *testing.T) {
	longLines := make([]string, 500)
	for i := range longLines {
		longLines[i] = strings.Repeat("x", 200)
	}
	content := strings.Join(longLines, "\n")

	got := FormatReadOutput([]byte(content), "big.txt", 0, 500)

	if len(got) > MaxReadBytes+200 {
		t.Errorf("output too large: %d bytes (limit %d)", len(got), MaxReadBytes)
	}
	if !strings.Contains(got, "20KB limit") {
		t.Errorf("expected byte limit truncation message (20KB)")
	}
	if !strings.Contains(got, "offset=") {
		t.Errorf("expected continuation offset in byte truncation message")
	}
	idx := strings.Index(got, "offset=")
	if idx >= 0 {
		remaining := got[idx+7:]
		endIdx := strings.IndexAny(remaining, " ].\n")
		if endIdx < 0 {
			endIdx = len(remaining)
		}
		offsetStr := remaining[:endIdx]
		if offsetStr == "0" || offsetStr == "" {
			t.Errorf("offset value should be > 0, got %q", offsetStr)
		}
	}
}

func TestFormatReadOutput_ContinuationOffset(t *testing.T) {
	content := makeLines(500)

	got := FormatReadOutput([]byte(content), "test.txt", 100, 50)
	if !strings.Contains(got, "offset=150") {
		t.Errorf("expected offset=150 in continuation notice, got:\n%s", got)
	}
}

func TestFormatReadOutput_NoNoticeWhenComplete(t *testing.T) {
	content := makeLines(10)

	got := FormatReadOutput([]byte(content), "test.txt", 0, 0)
	if strings.Contains(got, "offset=") {
		t.Errorf("should not have continuation notice for small file, got:\n%s", got)
	}
	if strings.Contains(got, "Showing lines") {
		t.Errorf("should not have showing lines notice for complete file, got:\n%s", got)
	}
}

func TestFormatReadOutput_NoNoticeAtEndOfFile(t *testing.T) {
	content := makeLines(10)

	got := FormatReadOutput([]byte(content), "test.txt", 8, 5)
	if strings.Contains(got, "offset=") {
		t.Errorf("should not have continuation notice at end of file, got:\n%s", got)
	}
	if !strings.Contains(got, "8: line 8") {
		t.Errorf("missing line 8")
	}
	if !strings.Contains(got, "10: line 10") {
		t.Errorf("missing line 10")
	}
}

func TestFormatReadOutput_ByteTruncationLineCount(t *testing.T) {
	longLines := make([]string, 300)
	for i := range longLines {
		longLines[i] = fmt.Sprintf("line %d: %s", i+1, strings.Repeat("x", 90))
	}
	content := strings.Join(longLines, "\n")

	got := FormatReadOutput([]byte(content), "test.txt", 0, 300)

	if !strings.Contains(got, "20KB limit") {
		t.Error("expected 20KB limit notice")
	}
	if !strings.Contains(got, "offset=") {
		t.Error("expected offset in byte truncation notice")
	}
	idx := strings.Index(got, "offset=")
	if idx >= 0 {
		remaining := got[idx+7:]
		endIdx := strings.IndexAny(remaining, " ].\n")
		if endIdx < 0 {
			endIdx = len(remaining)
		}
		offsetStr := remaining[:endIdx]
		var offsetVal int
		if _, err := fmt.Sscanf(offsetStr, "%d", &offsetVal); err != nil {
			t.Fatalf("parse offset %q: %v", offsetStr, err)
		}
		if offsetVal < 50 || offsetVal > 300 {
			t.Errorf("offset=%d seems unreasonable for 300 lines with byte truncation", offsetVal)
		}
	}
}

func TestReadBinaryFileReturnsMetadataOnly(t *testing.T) {
	tmp := t.TempDir()
	tmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "app.apk")
	content := []byte{'P', 'K', 3, 4, 0, 1, 2, 3, 0, 4}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	registry := (&Registry{}).WithWorkspaceDir(tmp)
	msg, err := registry.HandleToolCall(makeToolCall("Read", "read-bin", map[string]any{"file_path": "app.apk"}))
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !strings.Contains(msg.Content, "Binary file contents are not shown") {
		t.Fatalf("expected binary metadata message, got %q", msg.Content)
	}
	if strings.Contains(msg.Content, "PK") {
		t.Fatalf("binary payload leaked into read output: %q", msg.Content)
	}
}

func makeLines(n int) string {
	lines := make([]string, n)
	for i := 1; i <= n; i++ {
		lines[i-1] = fmt.Sprintf("line %d", i)
	}
	return strings.Join(lines, "\n")
}

package output

import "testing"

func TestParseMode(t *testing.T) {
	tests := []struct {
		value string
		want  Mode
		ok    bool
	}{
		{"", ModeCompact, true},
		{"compact", ModeCompact, true},
		{" DETAILED ", ModeDetailed, true},
		{"json", "", false},
	}
	for _, tt := range tests {
		got, err := ParseMode(tt.value)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ParseMode(%q) = (%q, %v), want (%q, ok=%v)", tt.value, got, err, tt.want, tt.ok)
		}
	}
}

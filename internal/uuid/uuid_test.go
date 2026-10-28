package uuid

import (
	"encoding"
	"regexp"
	"testing"
)

func TestNewV4ShapeVersionAndVariant(t *testing.T) {
	u := NewV4()
	got := u.String()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(got) {
		t.Fatalf("NewV4() = %q, want lowercase RFC 9562 UUIDv4", got)
	}
	if u[6]>>4 != 4 {
		t.Fatalf("version bits = %04b, want 4", u[6]>>4)
	}
	if u[8]>>6 != 0b10 {
		t.Fatalf("variant bits = %02b, want 10", u[8]>>6)
	}
}

func TestParseAcceptedForms(t *testing.T) {
	want := "f81d4fae-7dec-11d0-a765-00a0c91e6bf6"
	for _, input := range []string{
		want,
		"{f81d4fae-7dec-11d0-a765-00a0c91e6bf6}",
		"urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
		"f81d4fae7dec11d0a76500a0c91e6bf6",
	} {
		u, err := Parse(input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", input, err)
		}
		if got := u.String(); got != want {
			t.Fatalf("Parse(%q).String() = %q, want %q", input, got, want)
		}
	}
}

func TestTextRoundTrip(t *testing.T) {
	var _ encoding.TextMarshaler = UUID{}
	var _ encoding.TextUnmarshaler = (*UUID)(nil)

	u := NewV4()
	text, err := u.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var decoded UUID
	if err := decoded.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if decoded != u {
		t.Fatalf("decoded = %v, want %v", decoded, u)
	}
}

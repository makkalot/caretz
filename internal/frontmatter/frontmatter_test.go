package frontmatter

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSupportedFrontmatter(t *testing.T) {
	parsed, err := Parse([]byte(`---
# ignored comment
name: "review"
description: 'Review code'
reasoning: high # inline comment
permission:
  edit: deny
  bash: 'allow'
---

Body text.
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	wantFields := map[string]string{
		"name":        "review",
		"description": "Review code",
		"reasoning":   "high",
	}
	if !reflect.DeepEqual(parsed.Fields, wantFields) {
		t.Fatalf("Fields = %#v, want %#v", parsed.Fields, wantFields)
	}
	wantMaps := map[string]map[string]string{
		"permission": {
			"edit": "deny",
			"bash": "allow",
		},
	}
	if !reflect.DeepEqual(parsed.Maps, wantMaps) {
		t.Fatalf("Maps = %#v, want %#v", parsed.Maps, wantMaps)
	}
	if parsed.Body != "Body text." {
		t.Fatalf("Body = %q, want trimmed body", parsed.Body)
	}
}

func TestParseEmptyMap(t *testing.T) {
	parsed, err := Parse([]byte(`---
description: Test
permission:
---
body`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.Maps["permission"] == nil {
		t.Fatalf("permission map missing: %#v", parsed.Maps)
	}
	if len(parsed.Maps["permission"]) != 0 {
		t.Fatalf("permission map = %#v, want empty", parsed.Maps["permission"])
	}
}

func TestParseSingleQuotedScalarEscapesQuote(t *testing.T) {
	parsed, err := Parse([]byte(`---
description: 'Owner''s review'
---
body`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := parsed.Fields["description"]; got != "Owner's review" {
		t.Fatalf("description = %q, want escaped quote", got)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "missing opening delimiter",
			input:   "description: Test\n---\nbody",
			wantErr: "no opening frontmatter delimiter",
		},
		{
			name:    "missing closing delimiter",
			input:   "---\ndescription: Test\nbody",
			wantErr: "no closing frontmatter delimiter",
		},
		{
			name:    "malformed line",
			input:   "---\ndescription Test\n---\nbody",
			wantErr: "invalid frontmatter line",
		},
		{
			name:    "empty key",
			input:   "---\n: Test\n---\nbody",
			wantErr: "invalid empty frontmatter key",
		},
		{
			name:    "nested field without parent",
			input:   "---\n  edit: deny\n---\nbody",
			wantErr: "nested frontmatter field",
		},
		{
			name:    "unsupported top-level sequence",
			input:   "---\n- edit\n---\nbody",
			wantErr: "unsupported frontmatter sequence",
		},
		{
			name:    "unsupported nested sequence",
			input:   "---\npermission:\n  - edit\n---\nbody",
			wantErr: "unsupported frontmatter sequence",
		},
		{
			name:    "unterminated single quote",
			input:   "---\ndescription: 'Test\n---\nbody",
			wantErr: "unterminated single-quoted scalar",
		},
		{
			name:    "unterminated double quote",
			input:   "---\ndescription: \"Test\n---\nbody",
			wantErr: "invalid syntax",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.input))
			if err == nil {
				t.Fatal("Parse() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

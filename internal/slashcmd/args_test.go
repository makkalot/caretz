package slashcmd

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCommandArgs(t *testing.T) {
	defs := []optionDef{{Name: "count", TakesValue: true}, {Name: "review", Logical: "review", Value: "true"}, {Name: "no-review", Logical: "review", Value: "false"}}
	args, err := parseCommandArgs(`--count=10 --no-review "read plan.md" extra`, defs)
	if err != nil {
		t.Fatalf("parseCommandArgs() error = %v", err)
	}
	if !reflect.DeepEqual(args.Positionals, []string{"read plan.md", "extra"}) {
		t.Fatalf("Positionals = %#v", args.Positionals)
	}
	wantOptions := map[string]string{"count": "10", "review": "false"}
	if !reflect.DeepEqual(args.Options, wantOptions) {
		t.Fatalf("Options = %#v, want %#v", args.Options, wantOptions)
	}
}

func TestParseCommandArgsValidation(t *testing.T) {
	defs := []optionDef{{Name: "model", TakesValue: true}, {Name: "new-session", Logical: "new-session", Value: "true"}, {Name: "no-new-session", Logical: "new-session", Value: "false"}}
	for _, tc := range []struct{ input, want string }{
		{`task --model x`, "options must precede arguments"},
		{`model=x task`, "legacy option"},
		{`--new_session task`, "use --new-session"},
		{`--unknown task`, "unknown option"},
		{`--model`, "requires a value"},
		{`--model x --model y task`, "duplicate"},
		{`--new-session --no-new-session task`, "conflicting"},
	} {
		if _, err := parseCommandArgs(tc.input, defs); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseCommandArgs(%q) error = %v, want %q", tc.input, err, tc.want)
		}
	}
}

func TestParseArgsUnterminatedQuote(t *testing.T) {
	if _, err := parseCommandArgs(`"missing`, nil); err == nil {
		t.Fatal("parseCommandArgs() succeeded for unterminated quote, want error")
	}
}

package clicmd

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"
)

type customValue string

func (v *customValue) String() string { return string(*v) }
func (v *customValue) Set(s string) error {
	if s == "bad" {
		return fmt.Errorf("bad value")
	}
	*v = customValue(s)
	return nil
}

func TestParseTaggedOptions(t *testing.T) {
	type shared struct {
		Count int `flag:"count" default:"3" help:"count" group:"Common"`
	}
	type options struct {
		shared
		Name    string        `flag:"name" help:"name" group:"Common"`
		Enabled bool          `flag:"enabled" default:"true" help:"enabled"`
		Delay   time.Duration `flag:"delay" default:"1s" help:"delay"`
		Values  []string      `flag:"value" help:"value"`
		Custom  customValue   `flag:"custom" help:"custom"`
		Ignored string
	}
	values := options{}
	cmd := &Command{Use: "tool FILE...", Options: &values, Args: MinimumNArgs(1)}
	result, err := cmd.Parse([]string{"first", "--name", "caretz", "--value=a", "second", "--value", "b", "--enabled=false", "--custom", "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if values.Count != 3 || values.Name != "caretz" || values.Enabled || values.Delay != time.Second || fmt.Sprint(values.Values) != "[a b]" || values.Custom != "ok" {
		t.Fatalf("values=%+v", values)
	}
	if fmt.Sprint(result.Args) != "[first second]" {
		t.Fatalf("args=%v", result.Args)
	}
	if !result.Options.Changed("enabled") || result.Options.Changed("count") {
		t.Fatalf("changed state is wrong")
	}
}

func TestParseSelectsRunnerWithoutInvokingIt(t *testing.T) {
	type options struct {
		Name string `flag:"name"`
	}
	values := &options{}
	called := false
	command := &Command{Use: "tool FILE", Options: values, Args: ExactArgs(1)}
	command.Run = func(_ context.Context, result *Result) int {
		called = true
		if result.Command != command {
			t.Fatalf("runner command = %p, want %p", result.Command, command)
		}
		if result.Options.Value != values || values.Name != "caretz" {
			t.Fatalf("runner options = %#v, values = %#v", result.Options.Value, values)
		}
		if fmt.Sprint(result.Args) != "[input.txt]" {
			t.Fatalf("runner args = %v", result.Args)
		}
		return 7
	}

	result, err := command.Parse([]string{"--name", "caretz", "input.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("Parse invoked the command runner")
	}
	if result.Command.Run == nil {
		t.Fatal("selected command has no runner")
	}
	if code := result.Command.Run(context.Background(), result); code != 7 {
		t.Fatalf("runner exit code = %d, want 7", code)
	}
	if !called {
		t.Fatal("runner was not invoked")
	}
}

func TestRootInheritanceAndInterspersedFlags(t *testing.T) {
	type rootOptions struct {
		Workspace string `flag:"workspace" help:"workspace" group:"Location"`
	}
	type childOptions struct {
		Force bool `flag:"force" help:"force" group:"Action"`
	}
	rootValues, childValues := rootOptions{}, childOptions{}
	root := &Command{Use: "tool", Options: &rootValues, Args: NoArgs}
	child := &Command{Use: "tool run FILE", Options: &childValues, InheritRootOptions: true, Args: ExactArgs(1)}
	root.AddCommand(child)
	result, err := root.Parse([]string{"--workspace", "here", "run", "file", "--force"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Command != child || rootValues.Workspace != "here" || !childValues.Force || result.Args[0] != "file" {
		t.Fatalf("result=%+v root=%+v child=%+v", result, rootValues, childValues)
	}
	if !result.RootOptions.Changed("workspace") || !result.Options.Changed("force") {
		t.Fatal("changed flags not recorded by owner")
	}
}

func TestInheritedRootFlagAfterPositional(t *testing.T) {
	type rootOptions struct {
		Workspace string `flag:"workspace"`
	}
	rootValues := rootOptions{}
	root := &Command{Use: "tool", Options: &rootValues}
	child := &Command{Use: "run FILE", InheritRootOptions: true, Args: ExactArgs(1)}
	root.AddCommand(child)
	result, err := root.Parse([]string{"run", "file", "--workspace", "here"})
	if err != nil || rootValues.Workspace != "here" || fmt.Sprint(result.Args) != "[file]" {
		t.Fatalf("result=%+v values=%+v err=%v", result, rootValues, err)
	}
}

func TestEmbeddedOptionPointer(t *testing.T) {
	type Shared struct {
		Count uint `flag:"count" default:"2"`
	}
	type options struct{ *Shared }
	values := options{}
	cmd := &Command{Use: "tool", Options: &values}
	if _, err := cmd.Parse([]string{"--count", "4"}); err != nil {
		t.Fatal(err)
	}
	if values.Shared == nil || values.Count != 4 {
		t.Fatalf("values=%+v", values)
	}
}

func TestNonInheritingChildRejectsRootFlag(t *testing.T) {
	type options struct {
		Local bool `flag:"local" help:"local"`
	}
	root := &Command{Use: "tool", Options: &options{}, Args: NoArgs}
	child := &Command{Use: "tool child", Args: NoArgs}
	root.AddCommand(child)
	if _, err := root.Parse([]string{"--local", "child"}); err == nil || !strings.Contains(err.Error(), "not valid with subcommand") {
		t.Fatalf("err=%v", err)
	}
}

func TestHelpSelectsCommandAndGroupsOptions(t *testing.T) {
	type options struct {
		Workspace string `flag:"workspace" help:"workspace" group:"Location"`
		Force     bool   `flag:"force" help:"force" group:"Action"`
	}
	root := &Command{Use: "tool"}
	child := &Command{Use: "tool child ID", Short: "child help", Options: &options{}, Args: ExactArgs(1)}
	root.AddCommand(child)
	result, err := root.Parse([]string{"child", "--help"})
	if err != nil || !result.Help || result.Command != child {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var out strings.Builder
	result.Command.Help(&out)
	for _, want := range []string{"Usage: tool child ID", "child help", "Location:", "Action:", "-workspace string", "-force"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, out.String())
		}
	}
}

func TestDoubleDashStopsFlagParsing(t *testing.T) {
	type options struct {
		Force bool `flag:"force" help:"force"`
	}
	cmd := &Command{Use: "tool ARGS", Options: &options{}}
	result, err := cmd.Parse([]string{"--", "--force"})
	if err != nil || fmt.Sprint(result.Args) != "[--force]" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInvocationErrors(t *testing.T) {
	type options struct {
		Name string `flag:"name" help:"name"`
	}
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"--missing"}, "flag provided but not defined"},
		{"missing value", []string{"--name"}, "flag needs an argument"},
		{"bad args", nil, "expected 1 argument"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &Command{Use: "tool ID", Options: &options{}, Args: ExactArgs(1)}
			_, err := cmd.Parse(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestUnknownCommand(t *testing.T) {
	root := &Command{Use: "tool"}
	root.AddCommand(&Command{Use: "tool known"})
	_, err := root.Parse([]string{"unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err=%v", err)
	}
}

func TestInvalidSchemasPanic(t *testing.T) {
	tests := []struct {
		name    string
		options any
	}{
		{"non pointer", struct{}{}},
		{"unsupported", &struct {
			Value float64 `flag:"value"`
		}{}},
		{"duplicate", &struct {
			A string `flag:"same"`
			B string `flag:"same"`
		}{}},
		{"invalid default", &struct {
			Count int `flag:"count" default:"x"`
		}{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("did not panic")
				}
			}()
			(&Command{Use: "tool"}).AddCommand(&Command{Use: "tool child", Options: tt.options})
		})
	}
}

func TestAddCommandRejectsDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("did not panic")
		}
	}()
	root := &Command{Use: "tool"}
	root.AddCommand(&Command{Use: "tool child"})
	root.AddCommand(&Command{Use: "tool child"})
}

var _ flag.Value = (*customValue)(nil)

type listValue []string

func (v *listValue) String() string     { return strings.Join(*v, ",") }
func (v *listValue) Set(s string) error { *v = append(*v, s); return nil }

func TestFlagArgsReemitsSetFlagsByOwner(t *testing.T) {
	type rootOptions struct {
		Profile string    `flag:"profile"`
		Plan    bool      `flag:"plan"`
		Roots   listValue `flag:"read-root"`
		Session string    `flag:"s"`
	}
	type childOptions struct {
		Force bool `flag:"force"`
	}
	rootValues, childValues := rootOptions{}, childOptions{}
	root := &Command{Use: "tool", Options: &rootValues, Args: NoArgs}
	child := &Command{Use: "tool run NAME", Options: &childValues, InheritRootOptions: true, Args: ExactArgs(1)}
	root.AddCommand(child)
	args := []string{"--profile", "foo", "run", "bot", "--plan", "--read-root=a", "-s", "id", "--force", "--read-root", "b"}
	result, err := root.Parse(args)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(result.RootOptions.FlagArgs("s"), " ")
	if want := "--profile=foo --plan=true --read-root=a --read-root=b"; got != want {
		t.Fatalf("root FlagArgs = %q, want %q", got, want)
	}
	if got := strings.Join(result.Options.FlagArgs(), " "); got != "--force=true" {
		t.Fatalf("child FlagArgs = %q", got)
	}

	// The emitted tokens parse back to the same values.
	again := rootOptions{}
	reparse := &Command{Use: "tool", Options: &again, Args: NoArgs}
	if _, err := reparse.Parse(result.RootOptions.FlagArgs("s")); err != nil {
		t.Fatal(err)
	}
	if again.Profile != "foo" || !again.Plan || fmt.Sprint(again.Roots) != "[a b]" || again.Session != "" {
		t.Fatalf("reparsed = %+v", again)
	}
}

func TestFlagArgsOnRootCommand(t *testing.T) {
	type rootOptions struct {
		Model string `flag:"model"`
	}
	values := rootOptions{}
	root := &Command{Use: "tool", Options: &values, Args: NoArgs}
	result, err := root.Parse([]string{"-model", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.RootOptions.FlagArgs(), " "); got != "--model=x" {
		t.Fatalf("FlagArgs = %q", got)
	}
}

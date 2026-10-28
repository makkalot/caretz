package slashcmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/output"
)

func TestThemeCommandListsThemesAsMarkdownWithoutTerminal(t *testing.T) {
	t.Cleanup(func() { _ = output.SetTheme(output.ThemeANSI) })
	result, err := ThemeCommand{}.Run(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range output.ThemeNames() {
		if !strings.Contains(result.Message, name) {
			t.Errorf("Message missing %q: %q", name, result.Message)
		}
	}
	if !strings.Contains(result.Message, "**ansi** (active)") {
		t.Errorf("Message does not mark ansi active: %q", result.Message)
	}
}

func TestThemeCommandPrintsPreviewToTerminal(t *testing.T) {
	t.Cleanup(func() { _ = output.SetTheme(output.ThemeANSI) })
	if err := output.SetTheme("dracula"); err != nil {
		t.Fatal(err)
	}
	var terminal bytes.Buffer
	result, err := ThemeCommand{}.Run(WithTerminal(context.Background(), &terminal), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "" {
		t.Errorf("Message = %q, want the preview printed directly", result.Message)
	}
	got := terminal.String()
	if !strings.Contains(got, "* dracula") || !strings.Contains(got, "  tokyo-night") || !strings.Contains(got, "✓ passed") {
		t.Errorf("preview = %q", got)
	}
}

func TestThemeCommandSwitchesTheme(t *testing.T) {
	t.Cleanup(func() { _ = output.SetTheme(output.ThemeANSI) })
	result, err := ThemeCommand{}.Run(context.Background(), nil, "Tokyo-Night")
	if err != nil {
		t.Fatal(err)
	}
	if output.ActiveTheme() != "tokyo-night" || !strings.HasPrefix(result.Message, "theme: tokyo-night") {
		t.Errorf("ActiveTheme() = %q, Message = %q", output.ActiveTheme(), result.Message)
	}
	if _, err := (ThemeCommand{}).Run(context.Background(), nil, "solarized"); err == nil || !strings.Contains(err.Error(), "github-dark") {
		t.Errorf("unknown theme error = %v, want one listing themes", err)
	}
	if output.ActiveTheme() != "tokyo-night" {
		t.Errorf("failed switch changed theme to %q", output.ActiveTheme())
	}
}

func TestThemeCommandCompletesThemeNames(t *testing.T) {
	completion, ok := ThemeCommand{}.Complete("dra", 3)
	if !ok {
		t.Fatal("Complete() did not offer theme names")
	}
	if completion.Start != 0 || completion.End != 3 || completion.Query != "dra" {
		t.Errorf("Completion range = %d..%d query %q, want 0..3 query %q", completion.Start, completion.End, completion.Query, "dra")
	}
	if strings.Join(completion.Candidates, ",") != strings.Join(output.ThemeNames(), ",") {
		t.Errorf("Candidates = %v, want %v", completion.Candidates, output.ThemeNames())
	}
	if _, ok := (ThemeCommand{}).Complete("dracula ", 8); ok {
		t.Error("Complete() offered candidates for a second argument")
	}
}

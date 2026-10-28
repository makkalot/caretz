package slashcmd

import (
	"context"

	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/lib/eventing"
)

// PermissionPrompt answers one permission request on the interactive
// terminal, replacing the built-in prompt for the capabilities it is
// registered for.
type PermissionPrompt func(*ansi.Terminal, eventing.PermissionRequest) (eventing.PermissionResolution, error)

// ScreenRunner hands the terminal to view for the duration of the call. A
// command runs while the interactive loop reads keys, so a full-screen view
// asks the loop to stop reading and lend it the terminal instead of reading
// it directly.
type ScreenRunner func(view func(*ansi.Terminal) error) error

type screenRunnerKey struct{}

func WithScreenRunner(ctx context.Context, runner ScreenRunner) context.Context {
	return context.WithValue(ctx, screenRunnerKey{}, runner)
}

// ScreenRunnerFrom returns the runner of an interactive terminal, or nil
// when the command is not running in one.
func ScreenRunnerFrom(ctx context.Context) ScreenRunner {
	runner, _ := ctx.Value(screenRunnerKey{}).(ScreenRunner)
	return runner
}

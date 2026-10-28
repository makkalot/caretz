package slashcmd

import (
	"context"

	"github.com/makkalot/caretz/lib/progress"
)

type ProgressFunc = progress.Func

func WithProgress(ctx context.Context, progressFn ProgressFunc) context.Context {
	return progress.With(ctx, progressFn)
}

func EmitProgress(ctx context.Context, message string) {
	progress.Emit(ctx, message)
}

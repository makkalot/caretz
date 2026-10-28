package progress

import "context"

type Func func(message string)

type contextKey struct{}

func With(ctx context.Context, progress Func) context.Context {
	if progress == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, progress)
}

func Emit(ctx context.Context, message string) {
	progress, ok := ctx.Value(contextKey{}).(Func)
	if !ok || progress == nil {
		return
	}
	progress(message)
}

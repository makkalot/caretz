package llm

import (
	"context"
	"strings"
	"time"
)

type RetryInfo struct {
	Attempt     int
	Delay       time.Duration
	Error       error
	RateLimited bool
}

type RetryHandler func(RetryInfo)

type retryHandlerKey struct{}

type ReasoningUpdate struct {
	Delta string
	Text  string
	Done  bool
}

type ReasoningHandler func(ReasoningUpdate)

type reasoningHandlerKey struct{}

type sessionIDKey struct{}

// WithSessionID associates a stable conversation identifier with LLM requests.
// Providers that support session affinity may forward it in their wire format.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

func sessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sessionID, _ := ctx.Value(sessionIDKey{}).(string)
	return strings.TrimSpace(sessionID)
}

func WithRetryHandler(ctx context.Context, handler RetryHandler) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return ctx
	}
	return context.WithValue(ctx, retryHandlerKey{}, handler)
}

func retryHandlerFromContext(ctx context.Context) RetryHandler {
	if ctx == nil {
		return nil
	}
	handler, _ := ctx.Value(retryHandlerKey{}).(RetryHandler)
	return handler
}

func WithReasoningHandler(ctx context.Context, handler ReasoningHandler) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return ctx
	}
	return context.WithValue(ctx, reasoningHandlerKey{}, handler)
}

func ReasoningHandlerFromContext(ctx context.Context) ReasoningHandler {
	if ctx == nil {
		return nil
	}
	handler, _ := ctx.Value(reasoningHandlerKey{}).(ReasoningHandler)
	return handler
}

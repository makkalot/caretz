package llm

import "context"

type Provider interface {
	// systemPrompt is passed separately so adapters can send it
	// the way their API requires (e.g. Anthropic uses a top-level
	// system param, OpenAI uses a system message).
	//
	// onDelta is called for every content delta when streaming;
	// non-streaming adapters may call it once with the full text.
	Chat(systemPrompt string, messages []Message, tools []ToolDefinition, onDelta func(string)) (Response, error)
	GetModel() string
	GetBaseURL() string
}

type ContextProvider interface {
	Provider
	ChatContext(ctx context.Context, systemPrompt string, messages []Message, tools []ToolDefinition, onDelta func(string)) (Response, error)
}

type ReasoningProvider interface {
	Provider
	ReasoningEffort() string
}

func ChatContext(ctx context.Context, provider Provider, systemPrompt string, messages []Message, tools []ToolDefinition, onDelta func(string)) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if providerWithContext, ok := provider.(ContextProvider); ok {
		return providerWithContext.ChatContext(ctx, systemPrompt, messages, tools, onDelta)
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	resp, err := provider.Chat(systemPrompt, messages, tools, onDelta)
	if err != nil {
		return Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	return resp, nil
}

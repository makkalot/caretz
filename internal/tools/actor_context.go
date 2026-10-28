package tools

import "context"

type ActorMetadata struct {
	Adapter   string
	ChannelID string
	UserID    string
	MessageID string
}

type actorMetadataContextKey struct{}

func WithActorMetadata(ctx context.Context, metadata ActorMetadata) context.Context {
	return context.WithValue(ctx, actorMetadataContextKey{}, metadata)
}

func ActorMetadataFromContext(ctx context.Context) ActorMetadata {
	if ctx == nil {
		return ActorMetadata{}
	}
	metadata, _ := ctx.Value(actorMetadataContextKey{}).(ActorMetadata)
	return metadata
}

func ActorMetadataEnv(ctx context.Context) map[string]string {
	metadata := ActorMetadataFromContext(ctx)
	env := map[string]string{}
	if metadata.Adapter != "" {
		env["CARETZ_ADAPTER"] = metadata.Adapter
	}
	if metadata.ChannelID != "" {
		env["CARETZ_CHANNEL_ID"] = metadata.ChannelID
	}
	if metadata.UserID != "" {
		env["CARETZ_USER_ID"] = metadata.UserID
	}
	if metadata.MessageID != "" {
		env["CARETZ_MESSAGE_ID"] = metadata.MessageID
	}
	return env
}

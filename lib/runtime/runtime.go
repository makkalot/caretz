package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/makkalot/caretz/lib/conversation"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/progress"
	"github.com/makkalot/caretz/lib/tools"
	"github.com/makkalot/caretz/lib/types"
)

var ErrGracefulStop = errors.New("agent stopped at safe point")

type Logger interface {
	Info(format string, args ...interface{})
	SetSender(fn func(prefix, content string))
}

type Session interface {
	conversation.Session
	CallUntilStop(ctx context.Context, prompt string, stop <-chan struct{}) (*llm.Response, error)
	CallUntilStopWithPermissions(ctx context.Context, prompt string, stop <-chan struct{}) (*llm.Response, error)
	PendingPermissions() []eventing.PermissionRequest
	ResolvePermission(eventing.PermissionResolution) error
	GetContextMessages() []llm.Message
	EstimatedContextTokens() int
	ContextWindow() int
}

type SlashCommand interface {
	Name() string
	Description() string
	Usage() string
	Aliases() []string
}

// CustomSlashCommand is a slash command an embedder adds to the built-in
// interactive TUI, next to the commands Caretz ships.
type CustomSlashCommand interface {
	SlashCommand
	Run(ctx context.Context, session CommandSession, args string) (SlashCommandResult, error)
}

// CommandSession is the session a CustomSlashCommand runs against.
type CommandSession interface {
	Session
	ChannelID() string
	SessionDir() string
	WorkspaceDir() string
	// Call sends prompt to the agent as a user turn and waits for its answer.
	Call(ctx context.Context, prompt string) (*llm.Response, error)
	// RunVerificationCommand runs command in the session's sandbox without
	// showing it in the conversation.
	RunVerificationCommand(ctx context.Context, command string) (tools.VerificationResult, error)
}

type SlashCommandResult struct {
	Message string
	// MessagePublished means the message was already sent as a conversation event.
	MessagePublished      bool
	ExitCode              int
	ActiveModel           string
	ActiveModelRef        string
	ActiveReasoningEffort string
	ActiveReasoningSet    bool
	NewSession            Session
	Exit                  bool
}

type ProgressFunc = progress.Func

func WithProgress(ctx context.Context, progressFn ProgressFunc) context.Context {
	return progress.With(ctx, progressFn)
}

type SlashCommandRegistry interface {
	Handles(input string) bool
	Execute(ctx context.Context, session Session, input string) (SlashCommandResult, bool, error)
	Commands() []SlashCommand
}

type ModelChoice struct {
	Ref             string
	Provider        string
	ProviderName    string
	ModelKey        string
	ModelName       string
	Model           string
	Reasoning       string
	ReasoningValues []string
}

type InteractiveRunner func(InteractiveParams) error

type InteractiveParams struct {
	ModelRule            string
	ModelNotices         []string
	Session              Session
	Logger               Logger
	Model                string
	ModelRef             string
	Profile              string
	ReasoningEffort      string
	Root                 string
	AgentsDirs           []string
	SlashCommandRegistry SlashCommandRegistry
	ModelChoices         []ModelChoice
}

type ExplorerRunner func(ExplorerParams) error

type ExplorerParams struct {
	SessionDir  string
	SessionID   string
	AdapterType string
}

type TUIAgentMessage struct {
	Payload eventing.LLMAgentMessage
}

type TUIReasoningDelta struct {
	Payload eventing.LLMReasoningDelta
}

type TUIToolResult struct {
	Payload eventing.LLMToolCallResult
}

type TUIAgentResponse struct {
	Payload eventing.LLMAgentResponse
}

type TUICompactionCompleted struct {
	Payload eventing.CompactionCompleted
}

type TUIRequestRetry struct {
	Payload eventing.LLMRequestRetry
}

type TUISubagentInvocation struct {
	EventType string
	Payload   eventing.SubagentInvocation
}

// TUIStatus is a progress update from a long-running command.
type TUIStatus struct {
	Payload eventing.Status
}

type TUIOutput struct {
	send func(interface{})
}

func NewTUIOutput(send func(interface{})) *TUIOutput {
	return &TUIOutput{send: send}
}

func (o *TUIOutput) RegisterHandlers(session Session) error {
	for _, et := range []string{
		eventing.LLMUserMessageCreated,
		eventing.LLMAgentMessageCreated,
		eventing.LLMReasoningDeltaCreated,
		eventing.LLMToolCallResultCreated,
		eventing.LLMAgentResponseCreated,
		eventing.LLMRequestRetryScheduled,
		eventing.CompactionCompletedCreated,
	} {
		if err := session.OnContextEvent(et, o.DispatchEvent); err != nil {
			return err
		}
	}
	for _, et := range []string{
		eventing.SubagentInvocationStarted,
		eventing.SubagentInvocationProgress,
		eventing.SubagentInvocationCompleted,
		eventing.SubagentInvocationFailed,
		eventing.StatusUpdated,
	} {
		if err := session.OnAuditEvent(et, o.DispatchEvent); err != nil {
			return err
		}
	}
	return nil
}

func (o *TUIOutput) DispatchEvent(e *types.Event) error {
	switch e.EventType {
	case eventing.LLMUserMessageCreated:
		return nil
	case eventing.LLMAgentMessageCreated:
		var payload eventing.LLMAgentMessage
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal LLMAgentMessage: %w", err)
		}
		o.send(TUIAgentMessage{Payload: payload})
	case eventing.LLMReasoningDeltaCreated:
		var payload eventing.LLMReasoningDelta
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal LLMReasoningDelta: %w", err)
		}
		o.send(TUIReasoningDelta{Payload: payload})
	case eventing.LLMToolCallResultCreated:
		var payload eventing.LLMToolCallResult
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal LLMToolCallResult: %w", err)
		}
		o.send(TUIToolResult{Payload: payload})
	case eventing.LLMAgentResponseCreated:
		var payload eventing.LLMAgentResponse
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal LLMAgentResponse: %w", err)
		}
		o.send(TUIAgentResponse{Payload: payload})
	case eventing.LLMRequestRetryScheduled:
		var payload eventing.LLMRequestRetry
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal LLMRequestRetry: %w", err)
		}
		o.send(TUIRequestRetry{Payload: payload})
	case eventing.CompactionCompletedCreated:
		var payload eventing.CompactionCompleted
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal CompactionCompleted: %w", err)
		}
		o.send(TUICompactionCompleted{Payload: payload})
	case eventing.SubagentInvocationStarted, eventing.SubagentInvocationProgress, eventing.SubagentInvocationCompleted, eventing.SubagentInvocationFailed:
		var payload eventing.SubagentInvocation
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal SubagentInvocation: %w", err)
		}
		o.send(TUISubagentInvocation{EventType: e.EventType, Payload: payload})
	case eventing.StatusUpdated:
		var payload eventing.Status
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			return fmt.Errorf("failed to unmarshal Status: %w", err)
		}
		o.send(TUIStatus{Payload: payload})
	}
	return nil
}

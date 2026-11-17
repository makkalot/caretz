package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

type sessionExecutionState struct {
	mu      sync.Mutex
	factory func() *sandbox.Session
	session *sandbox.Session
	closed  bool
}

func (s *sessionExecutionState) get() (*sandbox.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("session executor is closed")
	}
	if s.session == nil {
		if s.factory == nil {
			return nil, fmt.Errorf("session executor factory is not configured")
		}
		s.session = s.factory()
	}
	return s.session, nil
}

func (s *sessionExecutionState) addFilesystemRoots(readable, writable []string) (bool, error) {
	session, err := s.get()
	if err != nil {
		return false, err
	}
	return session.AddFilesystemRoots(readable, writable)
}

func (r *Registry) runSessionBash(ctx context.Context, toolCall llm.ToolCall, cwd, command string, timeoutSeconds int, background bool) (llm.Message, error) {
	session, err := r.executionState.get()
	if err != nil {
		return llm.Message{}, err
	}
	authorizeNetwork := networkAuthorizer(ctx, toolCall, command)
	readable, writable, sessionNetwork := r.permissionTargets()
	if _, err := session.AddFilesystemRoots(readable, writable); err != nil {
		return llm.Message{}, fmt.Errorf("refreshing session filesystem access: %w", err)
	}
	networkAllowlist := append(permissionTargets(ctx, PermissionNetwork), sessionNetwork...)
	opts := sandbox.RunOptions{TimeoutSeconds: timeoutSeconds, Env: ActorMetadataEnv(ctx), Background: background, NetworkAllowlist: networkAllowlist, AuthorizeNetwork: authorizeNetwork}
	if background {
		opts.OnBackgroundExit = backgroundJobNotifier(ctx)
	}
	stdout, stderr, runErr := session.Run(ctx, cwd, command, opts)
	if runErr != nil {
		if ctx.Err() != nil {
			return llm.Message{Role: llm.RoleTool, ToolCallID: toolCall.ID, Content: TruncateBashOutput(stdout+stderr+fmt.Sprintf("\n[Command interrupted; output may be incomplete.]\n%v", runErr), MaxBashOutputBytes)}, errors.Join(ctx.Err(), runErr)
		}
		var blocked *sandbox.NetworkBlockedError
		if errors.As(runErr, &blocked) {
			return llm.Message{}, networkBlocked(toolCall.ID, command, stdout, stderr, blocked)
		}
		failed := llm.Message{Role: llm.RoleTool, Content: TruncateBashOutput(stdout+fmt.Sprintf("error: %v", runErr), MaxBashOutputBytes), ToolCallID: toolCall.ID}
		if stderr != "" {
			failed.Content = TruncateBashOutput(stdout+"sandbox error: "+stderr, MaxBashOutputBytes)
		}
		if escalation := r.sandboxOffEscalation(cwd, command, stdout, stderr, background, failed, false); escalation != nil {
			return llm.Message{}, escalation
		}
		return failed, nil
	}
	if opts.OnBackgroundExit != nil {
		stdout += "\nYou will get a message when it exits; do not poll for it."
	}
	result := llm.Message{Role: llm.RoleTool, Content: TruncateBashOutput(stdout, MaxBashOutputBytes), ToolCallID: toolCall.ID}
	if escalation := r.sandboxOffEscalation(cwd, command, stdout, stderr, background, result, true); escalation != nil {
		return llm.Message{}, escalation
	}
	return result, nil
}

// BackgroundJobNotifier learns when a background Bash job ends on its own, so
// the session can report it without the model polling. It must not block.
type BackgroundJobNotifier func(sandbox.BackgroundExit)

type backgroundJobNotifierKey struct{}

// WithBackgroundJobNotifier sets who hears about background jobs that tool
// calls under ctx start.
func WithBackgroundJobNotifier(ctx context.Context, notify BackgroundJobNotifier) context.Context {
	return context.WithValue(ctx, backgroundJobNotifierKey{}, notify)
}

// HasBackgroundJobNotifier reports whether ctx already routes job exits, as
// it does in a subagent run inside its parent's turn.
func HasBackgroundJobNotifier(ctx context.Context) bool {
	return backgroundJobNotifier(ctx) != nil
}

func backgroundJobNotifier(ctx context.Context) func(sandbox.BackgroundExit) {
	notify, _ := ctx.Value(backgroundJobNotifierKey{}).(BackgroundJobNotifier)
	if notify == nil {
		return nil
	}
	return notify
}

func (s *sessionExecutionState) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	session := s.session
	s.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	return nil
}

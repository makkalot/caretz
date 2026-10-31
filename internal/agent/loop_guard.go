package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/loopguard"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

const loopGuardSource = "loop-guard"

// loopGuardHooks connects the loop guard to the session around the loop: the
// judge's provider and where verdicts and notices go. The loop itself only
// sees these callbacks, so a guard failure never reaches the turn.
type loopGuardHooks struct {
	cfg           loopguard.Config
	judgeProvider func(ctx context.Context) (llm.Provider, error)
	record        func(record LoopGuardJudged)
	notify        func(message string)
}

// SetLoopGuard turns the loop guard on for this session's turns; child and
// sibling sessions inherit it. A disabled config removes it.
func (a *AgentSession) SetLoopGuard(cfg loopguard.Config) {
	a.loopGuardMu.Lock()
	defer a.loopGuardMu.Unlock()
	if !cfg.Enabled() {
		a.loopGuard = nil
		return
	}
	a.loopGuard = &loopGuardHooks{
		cfg:           cfg.WithDefaults(),
		judgeProvider: func(ctx context.Context) (llm.Provider, error) { return a.loopGuardJudgeProvider(ctx, cfg) },
		record: func(record LoopGuardJudged) {
			payload, _ := json.Marshal(record)
			if err := a.AppendAuditEvent(types.NewEvent(types.NewOriginator(a.channelId, 0), LoopGuardJudgedEvent, string(payload))); err != nil {
				a.logger.Warn("loop guard: failed to record event: %v", err)
			}
		},
		notify: func(message string) {
			if err := a.PublishAuditEvent(eventing.NewStatusUpdated(a.channelId, loopGuardSource, message)); err != nil {
				a.logger.Warn("loop guard: failed to publish notice: %v", err)
			}
		},
	}
}

// LoopGuard returns the session's loop guard config, off when unset.
func (a *AgentSession) LoopGuard() loopguard.Config {
	if hooks := a.loopGuardHooks(); hooks != nil {
		return hooks.cfg
	}
	return loopguard.Config{Mode: loopguard.ModeOff}
}

// ValidateLoopGuard resolves the judge model without calling it, so an
// unknown or blocked model fails at startup instead of at the first judgment.
func (a *AgentSession) ValidateLoopGuard(ctx context.Context) error {
	cfg := a.LoopGuard()
	if !cfg.Enabled() || cfg.JudgeModel == "" {
		return nil
	}
	if a.subagentModelResolver == nil {
		return fmt.Errorf("loop guard: no model resolver for judge model %q", cfg.JudgeModel)
	}
	_, err := a.subagentModelResolver.ResolveSubagentModel(ctx, SubagentModelRequest{
		Name:            loopGuardSource,
		WorkspaceDir:    a.WorkspaceDir(),
		Ref:             cfg.JudgeModel,
		ReasoningEffort: cfg.JudgeReasoning,
		ValidateOnly:    true,
	})
	return err
}

func (a *AgentSession) loopGuardJudgeProvider(ctx context.Context, cfg loopguard.Config) (llm.Provider, error) {
	if cfg.JudgeModel == "" {
		return a.currentProvider(), nil
	}
	if a.subagentModelResolver == nil {
		return nil, fmt.Errorf("no model resolver for judge model %q", cfg.JudgeModel)
	}
	resolution, err := a.subagentModelResolver.ResolveSubagentModel(ctx, SubagentModelRequest{
		Name:            loopGuardSource,
		WorkspaceDir:    a.WorkspaceDir(),
		Ref:             cfg.JudgeModel,
		ReasoningEffort: cfg.JudgeReasoning,
	})
	if err != nil {
		return nil, err
	}
	return resolution.Provider, nil
}

func (a *AgentLoop) loopGuardHooks() *loopGuardHooks {
	a.loopGuardMu.RLock()
	defer a.loopGuardMu.RUnlock()
	return a.loopGuard
}

// newTurnLoopGuard starts a guard for one turn, or nil when it is off.
// messages is the turn's request context, ending with its new message.
func (a *AgentLoop) newTurnLoopGuard(messages []llm.Message, summary bool) *turnLoopGuard {
	hooks := a.loopGuardHooks()
	if hooks == nil || summary || len(messages) == 0 {
		return nil
	}
	guard := loopguard.New(hooks.cfg, messages[len(messages)-1].Content)
	guard.SetPrevious(previousExchange(messages[:len(messages)-1]))
	return &turnLoopGuard{hooks: hooks, guard: guard}
}

// previousExchange returns the last user message and the agent's last text
// answer before the current turn.
func previousExchange(messages []llm.Message) (prompt, answer string) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		switch {
		case msg.Role == llm.RoleAssistant && answer == "" && strings.TrimSpace(msg.Content) != "":
			answer = msg.Content
		case msg.Role == llm.RoleUser:
			return msg.Content, answer
		}
	}
	return "", answer
}

type turnLoopGuard struct {
	hooks *loopGuardHooks
	guard *loopguard.Guard
	// note is the guard's latest message for the agent, and wrapUp whether
	// it has ended the turn's tool use.
	note   string
	wrapUp bool
}

// request returns the messages for the agent's next request: the turn's
// messages, then the guard's note as a final user message. The note is a
// message of its own so the agent reads it as coming from caretz rather than
// from a tool's output, and it stays last so it is never buried. It is not
// part of the session's history, so turn boundaries, resume and the cached
// prefix are unchanged; its audit record keeps the text.
func (t *turnLoopGuard) request(messages []llm.Message) []llm.Message {
	if t == nil || t.note == "" {
		return messages
	}
	return append(messages[:len(messages):len(messages)], llm.Message{Role: llm.RoleUser, Content: t.note})
}

// wrappingUp reports whether the guard has ended the turn's tool use, so
// the agent's next request goes without tools and it has to answer.
func (t *turnLoopGuard) wrappingUp() bool {
	return t != nil && t.wrapUp
}

// observeResponse passes the guard what the agent said with its tool calls,
// falling back to its reasoning summary, and the turn's tokens so far.
func (t *turnLoopGuard) observeResponse(result llm.Response, totalTokens int64) {
	if t == nil {
		return
	}
	text := result.Message.Content
	if strings.TrimSpace(text) == "" {
		text = result.ReasoningSummary
	}
	t.guard.ObserveResponse(text, totalTokens)
}

// LoopGuardJudged is the audit record of one judge call, written in every
// mode so shadow runs leave a reviewable log.
type LoopGuardJudged struct {
	Mode       string            `json:"mode"`
	Trigger    string            `json:"trigger"`
	Model      string            `json:"model,omitempty"`
	LatencyMS  int64             `json:"latency_ms"`
	Usage      llm.Usage         `json:"usage"`
	Error      string            `json:"error,omitempty"`
	Verdict    loopguard.Verdict `json:"verdict"`
	Redirected bool              `json:"redirected,omitempty"`
	// Escalated means the turn was still stuck after the redirect, so the
	// guard asked the agent to wrap up and took its tools away.
	Escalated bool `json:"escalated,omitempty"`
	// Note is the message the agent was sent, which the session's context
	// does not keep.
	Note string `json:"note,omitempty"`
	// Digest is exactly what the judge was shown, for reviewing verdicts.
	Digest string `json:"digest,omitempty"`
}

const LoopGuardJudgedEvent = "LoopGuard.Judged"

// afterToolResult records one tool result and, on the last result of a
// batch with a trigger armed, runs the judge and keeps any note it produces
// for the agent's next requests. It never fails the turn.
func (t *turnLoopGuard) afterToolResult(ctx context.Context, a *AgentLoop, tc llm.ToolCall, content string, isErr, lastInBatch bool) {
	if t == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			a.logger.Warn("loop guard: recovered from panic: %v", r)
		}
	}()
	t.guard.Observe(tc.Name, tc.Arguments, content, isErr)
	trigger := t.guard.Trigger()
	if trigger == "" || !lastInBatch {
		return
	}
	// Disarm after judging, so the digest still names the trigger.
	defer t.guard.Judged()
	a.logger.Info("loop guard: %s; judging", trigger)
	t.hooks.notify("judging: " + trigger)

	digest := t.guard.Digest()
	record := LoopGuardJudged{Mode: string(t.hooks.cfg.Mode), Trigger: trigger, Digest: digest}
	start := time.Now()
	verdict, model, usage, err := t.judge(ctx, digest)
	record.LatencyMS = time.Since(start).Milliseconds()
	record.Model, record.Usage, record.Verdict = model, usage, verdict
	var action loopguard.Action
	if err != nil {
		record.Error = err.Error()
		a.logger.Info("loop guard: judge failed after %s, treating as not stuck: %v", formatDuration(time.Since(start)), err)
	} else {
		action = t.guard.Decide(verdict)
		a.logger.Info("loop guard: stuck=%v confidence=%.2f after %s: %s", verdict.Stuck, verdict.Confidence, formatDuration(time.Since(start)), verdict.Reason)
	}
	record.Redirected = action.Note != "" && !action.WrapUp
	record.Escalated = action.WrapUp
	record.Note = action.Note
	t.hooks.record(record)
	if action.Escalate != "" {
		t.hooks.notify(action.Escalate)
	}
	if action.Note != "" {
		t.note = action.Note
		t.wrapUp = t.wrapUp || action.WrapUp
	}
}

// judge asks the judge model for a verdict within the timeout. Any error,
// including a malformed reply, is returned and treated as not stuck.
func (t *turnLoopGuard) judge(ctx context.Context, digest string) (loopguard.Verdict, string, llm.Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, t.hooks.cfg.JudgeTimeout)
	defer cancel()
	provider, err := t.hooks.judgeProvider(ctx)
	if err != nil {
		return loopguard.Verdict{}, "", llm.Usage{}, err
	}
	resp, err := llm.ChatContext(ctx, provider, loopguard.JudgeSystemPrompt, []llm.Message{{Role: llm.RoleUser, Content: digest}}, nil, func(string) {})
	if err != nil {
		return loopguard.Verdict{}, provider.GetModel(), resp.Usage, err
	}
	reply := resp.Message.Content
	if reply == "" {
		reply = resp.Content
	}
	verdict, err := loopguard.ParseVerdict(reply)
	return verdict, provider.GetModel(), resp.Usage, err
}

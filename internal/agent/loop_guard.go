package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
	cfg loopguard.Config
	// judgeProvider resolves one model of the judge's chain; an empty ref
	// is the session's current model.
	judgeProvider func(ctx context.Context, ref, reasoning string) (llm.Provider, error)
	record        func(record LoopGuardJudged)
	notify        func(message string)
	// unreachable holds the judge models already reported unreachable, so
	// each is announced once per session.
	unreachable sync.Map
}

// judgeModel is one model of the judge's chain; an empty ref is the
// session's current model.
type judgeModel struct{ ref, reasoning string }

// judgeChain is the judge model, its fallback, then the session's current
// model, without repeats. The judge moves down the chain only when a model's
// endpoint cannot be reached.
func judgeChain(cfg loopguard.Config) []judgeModel {
	var chain []judgeModel
	for _, m := range []judgeModel{{cfg.JudgeModel, cfg.JudgeReasoning}, {cfg.JudgeFallbackModel, cfg.JudgeFallbackReasoning}} {
		if m.ref != "" && (len(chain) == 0 || chain[0].ref != m.ref) {
			chain = append(chain, m)
		}
	}
	return append(chain, judgeModel{})
}

func (m judgeModel) name(provider llm.Provider) string {
	if m.ref != "" {
		return m.ref
	}
	if provider != nil {
		return "the session's model " + provider.GetModel()
	}
	return "the session's model"
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
		judgeProvider: a.loopGuardJudgeProvider,
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

// ValidateLoopGuard resolves the judge model and its fallback without
// calling them, so an unknown or blocked model fails at startup instead of
// at the first judgment.
func (a *AgentSession) ValidateLoopGuard(ctx context.Context) error {
	cfg := a.LoopGuard()
	if !cfg.Enabled() {
		return nil
	}
	for _, m := range judgeChain(cfg) {
		if m.ref == "" {
			continue
		}
		if a.subagentModelResolver == nil {
			return fmt.Errorf("loop guard: no model resolver for judge model %q", m.ref)
		}
		if _, err := a.subagentModelResolver.ResolveSubagentModel(ctx, SubagentModelRequest{
			Name:            loopGuardSource,
			WorkspaceDir:    a.WorkspaceDir(),
			Ref:             m.ref,
			ReasoningEffort: m.reasoning,
			ValidateOnly:    true,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *AgentSession) loopGuardJudgeProvider(ctx context.Context, ref, reasoning string) (llm.Provider, error) {
	if ref == "" {
		return a.currentProvider(), nil
	}
	if a.subagentModelResolver == nil {
		return nil, fmt.Errorf("no model resolver for judge model %q", ref)
	}
	resolution, err := a.subagentModelResolver.ResolveSubagentModel(ctx, SubagentModelRequest{
		Name:            loopGuardSource,
		WorkspaceDir:    a.WorkspaceDir(),
		Ref:             ref,
		ReasoningEffort: reasoning,
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

// newTurnLoopGuard starts a guard for one turn, or nil when it is off or
// limited to other models. The model is checked per turn, so /model switches
// and subagents on their own models are covered.
// messages is the turn's request context, ending with its new message.
func (a *AgentLoop) newTurnLoopGuard(messages []llm.Message, summary bool) *turnLoopGuard {
	hooks := a.loopGuardHooks()
	if hooks == nil || summary || len(messages) == 0 {
		return nil
	}
	if provider := a.currentProvider(); provider != nil && !hooks.cfg.AppliesTo(provider.GetModel(), provider.GetBaseURL()) {
		a.logger.Debug("loop guard: off for model %s (not in loopGuard.models)", provider.GetModel())
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
	// FallbackFrom names the judge models skipped because their endpoints
	// could not be reached, and FallbackReason the last one's error.
	FallbackFrom   []string `json:"fallback_from,omitempty"`
	FallbackReason string   `json:"fallback_reason,omitempty"`
}

const LoopGuardJudgedEvent = "LoopGuard.Judged"

// Outcome is the verdict and what the guard did about it, in one line, so
// the main session's output and subagent progress word it the same way.
func (r LoopGuardJudged) Outcome() string {
	if r.Error != "" {
		return "judge failed, ignored: " + r.Error
	}
	v := r.Verdict
	verdict := fmt.Sprintf("not stuck (%.2f)", v.Confidence)
	if v.Stuck {
		verdict = fmt.Sprintf("stuck (%.2f)", v.Confidence)
	}
	switch {
	case r.Redirected:
		return verdict + " → redirected the agent"
	case r.Escalated:
		return verdict + " → still stuck after redirect, ending the turn"
	case v.Stuck && r.Mode == string(loopguard.ModeShadow):
		return verdict + " → shadow mode, no redirect"
	case v.Stuck:
		return verdict + " → below confidence threshold, no redirect"
	}
	return verdict
}

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
	result, err := t.judge(ctx, digest)
	verdict := result.verdict
	record.LatencyMS = time.Since(start).Milliseconds()
	record.Model, record.Usage, record.Verdict = result.model, result.usage, verdict
	record.FallbackFrom, record.FallbackReason = result.fallbackFrom, result.fallbackReason
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

// judgeResult is one judgment: the verdict, the model that gave it, and the
// models skipped on the way because their endpoints were unreachable.
type judgeResult struct {
	verdict loopguard.Verdict
	model   string
	usage   llm.Usage
	// fallbackFrom names the unreachable models, in order, and
	// fallbackReason the last one's error.
	fallbackFrom   []string
	fallbackReason string
}

// judge asks the judge's chain for a verdict within one timeout shared by
// the whole chain. A model whose endpoint cannot be reached hands the same
// request to the next one; any other error, including a malformed reply, is
// returned and treated as not stuck.
func (t *turnLoopGuard) judge(ctx context.Context, digest string) (judgeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, t.hooks.cfg.JudgeTimeout)
	defer cancel()
	var result judgeResult
	var lastErr error
	// unreachableURLs skips a later model served from an endpoint that has
	// already failed, such as the session's model on the same local server.
	unreachableURLs := map[string]bool{}
	chain := judgeChain(t.hooks.cfg)
	for i, m := range chain {
		provider, err := t.hooks.judgeProvider(ctx, m.ref, m.reasoning)
		if err != nil {
			return result, err
		}
		if unreachableURLs[provider.GetBaseURL()] {
			continue
		}
		resp, err := llm.ChatContext(ctx, provider, loopguard.JudgeSystemPrompt, []llm.Message{{Role: llm.RoleUser, Content: digest}}, nil, func(string) {})
		result.model, result.usage = provider.GetModel(), resp.Usage
		if llm.IsUnreachable(err) {
			lastErr = err
			unreachableURLs[provider.GetBaseURL()] = true
			result.fallbackFrom = append(result.fallbackFrom, m.name(provider))
			result.fallbackReason = err.Error()
			if i < len(chain)-1 {
				t.announceUnreachable(m.name(provider), err)
			}
			continue
		}
		if err != nil {
			return result, err
		}
		reply := resp.Message.Content
		if reply == "" {
			reply = resp.Content
		}
		result.verdict, err = loopguard.ParseVerdict(reply)
		return result, err
	}
	return result, lastErr
}

// announceUnreachable tells the user once per session that a judge model
// could not be reached and the judge moved on.
func (t *turnLoopGuard) announceUnreachable(name string, err error) {
	if _, seen := t.hooks.unreachable.LoadOrStore(name, true); seen {
		return
	}
	t.hooks.notify(fmt.Sprintf("judge model %s unreachable, using the fallback: %v", name, err))
}

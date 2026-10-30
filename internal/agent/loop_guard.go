package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
func (a *AgentLoop) newTurnLoopGuard(task string, summary bool) *turnLoopGuard {
	hooks := a.loopGuardHooks()
	if hooks == nil || summary {
		return nil
	}
	return &turnLoopGuard{hooks: hooks, guard: loopguard.New(hooks.cfg, task)}
}

type turnLoopGuard struct {
	hooks *loopGuardHooks
	guard *loopguard.Guard
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
	Escalated  bool              `json:"escalated,omitempty"`
}

const LoopGuardJudgedEvent = "LoopGuard.Judged"

// afterToolResult records one tool result and, on the last result of a
// batch with a trigger armed, runs the judge. It returns text to append to
// that result, empty unless the guard redirects. It never fails the turn.
func (t *turnLoopGuard) afterToolResult(ctx context.Context, a *AgentLoop, tc llm.ToolCall, content string, isErr, lastInBatch bool) (note string) {
	if t == nil {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			a.logger.Warn("loop guard: recovered from panic: %v", r)
			note = ""
		}
	}()
	t.guard.Observe(tc.Name, tc.Arguments, content, isErr)
	trigger := t.guard.Trigger()
	if trigger == "" || !lastInBatch {
		return ""
	}
	t.guard.Judged()
	a.logger.Info("loop guard: %s; judging", trigger)
	t.hooks.notify("judging: " + trigger)

	record := LoopGuardJudged{Mode: string(t.hooks.cfg.Mode), Trigger: trigger}
	start := time.Now()
	verdict, model, usage, err := t.judge(ctx)
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
	record.Redirected = action.Redirect != ""
	record.Escalated = action.Escalate != ""
	t.hooks.record(record)
	if action.Escalate != "" {
		t.hooks.notify(action.Escalate)
	}
	return action.Redirect
}

// judge asks the judge model for a verdict within the timeout. Any error,
// including a malformed reply, is returned and treated as not stuck.
func (t *turnLoopGuard) judge(ctx context.Context) (loopguard.Verdict, string, llm.Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, t.hooks.cfg.JudgeTimeout)
	defer cancel()
	provider, err := t.hooks.judgeProvider(ctx)
	if err != nil {
		return loopguard.Verdict{}, "", llm.Usage{}, err
	}
	digest := t.guard.Digest()
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

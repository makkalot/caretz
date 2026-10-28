package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

var contextTypes = []string{
	eventing.LLMUserMessageCreated, eventing.LLMAgentMessageCreated,
	eventing.LLMToolCallResultCreated, eventing.LLMAgentResponseCreated,
	eventing.LLMAgentInterruptedResponseCreated,
	eventing.LLMReasoningDeltaCreated, eventing.LLMRequestRetryScheduled,
}

type frame struct {
	Kind       string        `json:"kind"`
	Text       string        `json:"text,omitempty"`
	Tool       string        `json:"tool,omitempty"`
	CallID     string        `json:"call_id,omitempty"`
	Arguments  string        `json:"arguments,omitempty"`
	Permission any           `json:"permission,omitempty"`
	History    []chatMessage `json:"history,omitempty"`
	Delta      string        `json:"delta,omitempty"`
	Started    bool          `json:"started,omitempty"`
	Done       bool          `json:"done,omitempty"`
}

func eventFrames(e *types.Event) []frame {
	if e == nil {
		return nil
	}
	switch e.EventType {
	case eventing.LLMReasoningDeltaCreated:
		var p eventing.LLMReasoningDelta
		if json.Unmarshal([]byte(e.Payload), &p) == nil {
			return []frame{{Kind: "reasoning", Text: p.Text, Delta: p.Delta, Started: p.Started, Done: p.Done}}
		}
	case eventing.LLMRequestRetryScheduled:
		var p eventing.LLMRequestRetry
		if json.Unmarshal([]byte(e.Payload), &p) == nil {
			text := fmt.Sprintf("Retry %d in %.1fs: %s", p.Attempt, float64(p.DelayMillis)/1000, p.Message)
			if p.Error != "" {
				text += " · " + p.Error
			}
			return []frame{{Kind: "status", Text: text}}
		}
	case eventing.PermissionRequested:
		p, err := eventing.DecodePermissionRequest(e)
		if err == nil {
			return []frame{{Kind: "permission", Permission: p}}
		}
	case eventing.PermissionResolved:
		p, err := eventing.DecodePermissionResolution(e)
		if err == nil {
			return []frame{{Kind: "permission_resolved", Permission: p}}
		}
	case eventing.StatusUpdated:
		var p eventing.Status
		if json.Unmarshal([]byte(e.Payload), &p) == nil {
			return []frame{{Kind: "status", Text: p.Message}}
		}
	case eventing.LLMUserMessageCreated, eventing.LLMAgentMessageCreated,
		eventing.LLMToolCallResultCreated, eventing.LLMAgentResponseCreated,
		eventing.LLMAgentInterruptedResponseCreated:
		m, err := eventing.EventToLLMMessage(e)
		if err != nil {
			return nil
		}
		kind := "assistant"
		if m.Role == llm.RoleUser {
			kind = "user"
		}
		if m.Role == llm.RoleTool {
			var payload eventing.LLMToolCallResult
			if json.Unmarshal([]byte(e.Payload), &payload) != nil {
				return nil
			}
			return []frame{{Kind: "tool_result", Text: m.Content, CallID: m.ToolCallID, Tool: payload.Tool.Name, Arguments: payload.Tool.Arguments}}
		}
		var frames []frame
		if m.Content != "" {
			frames = append(frames, frame{Kind: kind, Text: m.Content})
		}
		for _, call := range m.ToolCalls {
			frames = append(frames, frame{Kind: "tool_call", Tool: call.Name, CallID: call.ID, Arguments: call.Arguments})
		}
		return frames
	}
	return nil
}

func (a *app) events(w http.ResponseWriter, r *http.Request, p *project) {
	contextSub, err := p.session.SubscribeContextEvents(contextTypes...)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer contextSub.Close()
	auditSub, err := p.session.SubscribeAuditEvents(eventing.PermissionRequested, eventing.PermissionResolved, eventing.StatusUpdated)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer auditSub.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(value any) error {
		// Bound writes so a stalled browser cannot retain a subscription forever.
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		contents, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", contents); err != nil {
			return err
		}
		return controller.Flush()
	}
	if send(frame{Kind: "history", History: history(p)}) != nil {
		return
	}
	for _, request := range p.session.PendingPermissions() {
		if send(frame{Kind: "permission", Permission: request}) != nil {
			return
		}
	}
	state := p.snapshot()
	if state.Kind == "" {
		state.Kind = "status"
	}
	if send(state) != nil {
		return
	}
	live, liveVersion := p.liveSnapshot()
	if send(live) != nil {
		return
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case <-heartbeat.C:
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || controller.Flush() != nil {
				return
			}
		case <-tick.C:
			live, version := p.liveSnapshot()
			if version != liveVersion {
				liveVersion = version
				if send(live) != nil {
					return
				}
			}
			next := p.snapshot()
			if next.Revision != state.Revision {
				state = next
				if send(state) != nil {
					return
				}
			}
		case e, ok := <-contextSub.Events():
			if !ok {
				return
			}
			for _, f := range eventFrames(e) {
				if send(f) != nil {
					return
				}
			}
		case e, ok := <-auditSub.Events():
			if !ok {
				return
			}
			for _, f := range eventFrames(e) {
				if send(f) != nil {
					return
				}
			}
		}
	}
}

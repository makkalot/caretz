package agent

import (
	"encoding/json"

	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type taskInterval struct{ first, last int }

// SessionStats counts model usage and non-task tool calls in one session.
func (a *AgentSession) SessionStats() (*tasks.Stats, error) {
	events, err := a.GetContextEvents()
	if err != nil {
		return nil, err
	}
	return aggregateSessionStats(events), nil
}

func aggregateSessionStats(events []*types.Event) *tasks.Stats {
	stats := &tasks.Stats{ToolCalls: map[string]int{}}
	for _, e := range events {
		addEventStats(stats, e)
	}
	return stats
}

func addEventStats(stats *tasks.Stats, e *types.Event) {
	switch e.EventType {
	case eventing.LLMAgentMessageCreated, eventing.LLMAgentResponseCreated, eventing.LLMAgentSummaryResponseCreated:
		var p eventing.LLMAgentResponse
		if json.Unmarshal([]byte(e.Payload), &p) != nil || invalidUsage(p.Usage) {
			return
		}
		stats.PromptTokens += p.Usage.PromptTokens
		stats.CachedPromptTokens += p.Usage.CachedPromptTokens
		stats.CompletionTokens += p.Usage.CompletionTokens
		stats.TotalTokens += p.Usage.TotalTokens
	case eventing.LLMToolCallResultCreated:
		var p eventing.LLMToolCallResult
		if json.Unmarshal([]byte(e.Payload), &p) != nil || p.Tool.Name == updateTasksToolName || p.Tool.Name == listTasksToolName || p.Tool.Name == "" {
			return
		}
		stats.ToolCalls[p.Tool.Name]++
	}
}

func aggregateTaskStats(events []*types.Event, taskID string) *tasks.Stats {
	boundaries := make([]struct {
		index int
		op    string
	}, 0)
	for i, e := range events {
		if e.EventType != eventing.TaskUpdatedCreated {
			continue
		}
		var p eventing.TaskUpdated
		if json.Unmarshal([]byte(e.Payload), &p) != nil || p.Update.ID != taskID {
			continue
		}
		if p.Update.Op == tasks.OpStart || p.Update.Op == tasks.OpBlock {
			boundaries = append(boundaries, struct {
				index int
				op    string
			}{i, p.Update.Op})
		}
	}
	// The done update is being accounted before it is appended.
	boundaries = append(boundaries, struct {
		index int
		op    string
	}{len(events), tasks.OpDone})
	var intervals []taskInterval
	active, start := false, -1
	for _, b := range boundaries {
		model := precedingModelEvent(events, b.index)
		switch b.op {
		case tasks.OpStart:
			if !active {
				active, start = true, model
			}
		case tasks.OpBlock, tasks.OpDone:
			last := endOfModelIteration(events, model)
			if active {
				intervals = append(intervals, taskInterval{start, last})
				active = false
			} else if b.op == tasks.OpDone && len(intervals) == 0 {
				intervals = append(intervals, taskInterval{model, last})
			}
		}
	}
	stats := &tasks.Stats{ToolCalls: map[string]int{}}
	for i, e := range events {
		if !inTaskIntervals(i, intervals) {
			continue
		}
		addEventStats(stats, e)
	}
	return stats
}

func endOfModelIteration(events []*types.Event, model int) int {
	for i := model + 1; i < len(events); i++ {
		switch events[i].EventType {
		case eventing.LLMAgentMessageCreated, eventing.LLMAgentResponseCreated:
			return i - 1
		}
	}
	return len(events) - 1
}

func precedingModelEvent(events []*types.Event, before int) int {
	for i := before - 1; i >= 0; i-- {
		switch events[i].EventType {
		case eventing.LLMAgentMessageCreated, eventing.LLMAgentResponseCreated:
			return i
		}
	}
	return 0
}

func inTaskIntervals(index int, intervals []taskInterval) bool {
	for _, interval := range intervals {
		if index >= interval.first && index <= interval.last {
			return true
		}
	}
	return false
}

func invalidUsage(u llm.Usage) bool {
	return u.PromptTokens < 0 || u.CachedPromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 || u.CachedPromptTokens > u.PromptTokens
}

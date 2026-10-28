package agent

import (
	"testing"

	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

func TestAggregateTaskStatsAcrossActiveWindows(t *testing.T) {
	events := []*types.Event{
		eventing.NewLLMAgentResponse("s", "p", 1, llm.Message{}, llm.Usage{PromptTokens: 99, TotalTokens: 99}, 99),
		eventing.NewLLMAgentResponse("s", "p", 1, llm.Message{}, llm.Usage{PromptTokens: 10, CachedPromptTokens: 4, CompletionTokens: 2, TotalTokens: 12}, 12),
		eventing.NewTaskUpdated("s", "p", 1, tasks.Update{Op: tasks.OpStart, ID: "T1", NextAction: "work"}),
		eventing.NewLLMToolCallResult("s", "p", 1, llm.ToolCall{Name: "Read"}, "", "", llm.Message{}),
		eventing.NewLLMAgentSummaryResponse("s", "p", 1, llm.Message{}, llm.Usage{PromptTokens: 20, CachedPromptTokens: 5, CompletionTokens: 3, TotalTokens: 23}, 23),
		eventing.NewLLMAgentResponse("s", "p", 1, llm.Message{}, llm.Usage{PromptTokens: 30, CachedPromptTokens: 10, CompletionTokens: 4, TotalTokens: 34}, 34),
		eventing.NewTaskUpdated("s", "p", 1, tasks.Update{Op: tasks.OpBlock, ID: "T1"}),
		eventing.NewLLMToolCallResult("s", "p", 1, llm.ToolCall{Name: "Bash"}, "failed", "", llm.Message{}),
		eventing.NewLLMAgentResponse("s", "p", 2, llm.Message{}, llm.Usage{PromptTokens: 100, TotalTokens: 100}, 100),
		eventing.NewLLMAgentResponse("s", "p", 3, llm.Message{}, llm.Usage{PromptTokens: 40, CachedPromptTokens: 20, CompletionTokens: 5, TotalTokens: 45}, 45),
		eventing.NewTaskUpdated("s", "p", 3, tasks.Update{Op: tasks.OpStart, ID: "T1", NextAction: "resume"}),
		eventing.NewLLMToolCallResult("s", "p", 3, llm.ToolCall{Name: "UpdateTasks"}, "", "", llm.Message{}),
		eventing.NewLLMToolCallResult("s", "p", 3, llm.ToolCall{Name: "Read"}, "failed", "", llm.Message{}),
		eventing.NewLLMToolCallResult("s", "p", 3, llm.ToolCall{Name: "CallSubagent"}, "", "", llm.Message{}),
		eventing.NewLLMAgentResponse("s", "p", 3, llm.Message{}, llm.Usage{PromptTokens: 50, CachedPromptTokens: 25, CompletionTokens: 6, TotalTokens: 56}, 56),
	}
	stats := aggregateTaskStats(events, "T1")
	if stats.PromptTokens != 150 || stats.CachedPromptTokens != 64 || stats.CompletionTokens != 20 || stats.TotalTokens != 170 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.ToolCalls["Read"] != 2 || stats.ToolCalls["Bash"] != 1 || stats.ToolCalls["CallSubagent"] != 1 || stats.ToolCalls["UpdateTasks"] != 0 {
		t.Fatalf("tool calls = %#v", stats.ToolCalls)
	}
}

func TestAggregateTaskStatsDirectCompletionUsesOnlyCompletionIteration(t *testing.T) {
	events := []*types.Event{
		eventing.NewLLMAgentResponse("s", "p", 1, llm.Message{}, llm.Usage{PromptTokens: 90, TotalTokens: 90}, 90),
		eventing.NewLLMAgentResponse("s", "p", 2, llm.Message{}, llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, 12),
		eventing.NewLLMToolCallResult("s", "p", 2, llm.ToolCall{Name: "Edit"}, "", "", llm.Message{}),
	}
	stats := aggregateTaskStats(events, "T1")
	if stats.TotalTokens != 12 || stats.ToolCalls["Edit"] != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

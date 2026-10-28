package slashcmd

import (
	"github.com/makkalot/caretz/internal/goal"
)

const goalPromptFeedbackTail = 8

type goalPlannerTask struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Kind       string   `json:"kind,omitempty"`
	NextAction string   `json:"next_action"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Validation []string `json:"validation"`
	Check      string   `json:"check,omitempty"`
	Status     string   `json:"status"`
	Attempts   int      `json:"attempts,omitempty"`
	LastCheck  string   `json:"last_check,omitempty"`
}

func goalPlannerTaskView(state goal.State) []goalPlannerTask {
	view := make([]goalPlannerTask, 0, len(state.Tasks))
	short := func(ids []string) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = state.ShortTaskID(id)
		}
		return out
	}
	for _, task := range state.Tasks {
		if task.Status == goal.TaskSuperseded {
			continue
		}
		item := goalPlannerTask{
			ID: state.ShortTaskID(task.ID), Title: task.Title, Kind: task.Kind, NextAction: task.NextAction,
			DependsOn: short(task.DependsOn), Validation: task.Validation, Status: string(task.Status),
			Attempts: task.Attempts, LastCheck: goal.Compact(task.LastCheck, 300),
		}
		if !task.Check.Empty() {
			item.Check = task.Check.Label()
		}
		view = append(view, item)
	}
	return view
}

package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

const (
	updateTasksToolName          = "UpdateTasks"
	listTasksToolName            = "ListTasks"
	taskSnapshotMutationInterval = 25
)

const taskSystemGuidance = `Task guidance:
- Use UpdateTasks for work spanning multiple steps.
- Use ListTasks when you need to inspect the full current task list.
- Mark one task per scope in progress before working on it, and mark it done immediately after.
- Keep next_action specific enough that someone with no other context could continue.
- Add validation instructions when useful so another model or process can independently check the result.`

func systemPromptWithTaskGuidance(systemPrompt string) string {
	if strings.Contains(systemPrompt, "Task guidance:") {
		return systemPrompt
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return taskSystemGuidance
	}
	return strings.TrimRight(systemPrompt, "\n") + "\n\n" + taskSystemGuidance
}

func (a *AgentLoop) handleUpdateTasksToolCall(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, tc llm.ToolCall) (llm.Message, error) {
	if a.toolRegistry != nil && !a.toolRegistry.IsToolEnabled(updateTasksToolName) {
		return llm.Message{}, fmt.Errorf("tool %s is disabled", updateTasksToolName)
	}
	if a.disableTasks {
		return llm.Message{}, fmt.Errorf("UpdateTasks is disabled")
	}
	if a.taskState == nil {
		a.taskState = tasks.NewState()
	}
	var input tasks.Update
	if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
		return llm.Message{}, fmt.Errorf("invalid UpdateTasks arguments: %w", err)
	}
	ack, err := a.applyTaskUpdate(originatorID, parentOriginatorID, parentOriginatorVersion, input)
	if err != nil {
		return llm.Message{}, err
	}
	return llm.Message{
		Role:       llm.RoleTool,
		Content:    ack + "\n\n" + tasks.Render(a.taskState),
		ToolCallID: tc.ID,
	}, nil
}

func (a *AgentLoop) applyTaskUpdate(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, input tasks.Update) (string, error) {
	return a.applyTaskUpdateWithStats(originatorID, parentOriginatorID, parentOriginatorVersion, input, nil)
}

func (a *AgentLoop) applyTaskUpdateWithStats(originatorID, parentOriginatorID string, parentOriginatorVersion uint64, input tasks.Update, completionStats *tasks.Stats) (string, error) {
	if a.disableTasks {
		return "", fmt.Errorf("UpdateTasks is disabled")
	}
	if a.taskState == nil {
		a.taskState = tasks.NewState()
	}
	nextState := a.taskState.Clone()
	ack, err := nextState.Apply(input)
	if err != nil {
		return "", err
	}
	stats := completionStats
	if input.Op == tasks.OpDone {
		if stats == nil {
			events, accountingErr := a.ContextEvents(originatorID)
			if accountingErr != nil {
				if a.logger != nil {
					a.logger.Info("Failed to calculate completed task statistics: %v", accountingErr)
				}
			} else {
				stats = aggregateTaskStats(events, input.ID)
			}
		}
	}
	updateEvent := eventing.NewTaskUpdated(originatorID, parentOriginatorID, parentOriginatorVersion, input)
	if input.Op == tasks.OpDone {
		updateEvent = eventing.NewTaskCompleted(originatorID, parentOriginatorID, parentOriginatorVersion, input, stats)
	}
	if _, err := a.contextSessionStore.AppendTaskEvent(updateEvent); err != nil {
		return "", fmt.Errorf("failed to append task update: %w", err)
	}
	a.taskState = nextState
	if input.Op == tasks.OpDone {
		a.taskState.MarkCompletedAt(input.ID, updateEvent.OccurredOn)
		a.taskState.SetStats(input.ID, stats)
	}
	a.taskMutationsSinceSnapshot++
	if a.taskMutationsSinceSnapshot >= taskSnapshotMutationInterval {
		snapshotEvent := eventing.NewTaskSnapshot(originatorID, parentOriginatorID, parentOriginatorVersion, a.taskState.Snapshot())
		if _, err := a.contextSessionStore.AppendTaskEvent(snapshotEvent); err != nil {
			if a.logger != nil {
				a.logger.Info("Failed to append task snapshot: %v", err)
			}
		} else {
			a.taskMutationsSinceSnapshot = 0
		}
	}
	return ack, nil
}

func (a *AgentLoop) handleListTasksToolCall(tc llm.ToolCall) (llm.Message, error) {
	if a.toolRegistry != nil && !a.toolRegistry.IsToolEnabled(listTasksToolName) {
		return llm.Message{}, fmt.Errorf("tool %s is disabled", listTasksToolName)
	}
	if a.disableTasks {
		return llm.Message{}, fmt.Errorf("ListTasks is disabled")
	}
	if strings.TrimSpace(tc.Arguments) != "" && strings.TrimSpace(tc.Arguments) != "{}" {
		var input map[string]any
		if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
			return llm.Message{}, fmt.Errorf("invalid ListTasks arguments: %w", err)
		}
		if len(input) > 0 {
			return llm.Message{}, fmt.Errorf("ListTasks does not accept arguments")
		}
	}
	if a.taskState == nil {
		a.taskState = tasks.NewState()
	}
	return llm.Message{
		Role:       llm.RoleTool,
		Content:    tasks.RenderFull(a.taskState),
		ToolCallID: tc.ID,
	}, nil
}

func (a *AgentLoop) renderedTaskState() string {
	if a.disableTasks {
		return ""
	}
	if a.taskState == nil || len(a.taskState.Tasks) == 0 {
		return ""
	}
	return tasks.Render(a.taskState)
}

// TasksEnabled reports whether this session has harness-managed task support.
func (a *AgentSession) TasksEnabled() bool {
	return a != nil && a.AgentLoop != nil && !a.disableTasks
}

// TaskSnapshot returns a detached copy of the session's current task state.
func (a *AgentSession) TaskSnapshot() tasks.Snapshot {
	if a == nil || a.AgentLoop == nil {
		return tasks.Snapshot{}
	}
	a.executionMu.Lock()
	defer a.executionMu.Unlock()
	if a.taskState == nil {
		return tasks.Snapshot{}
	}
	return a.taskState.Clone().Snapshot()
}

// TasksInScope returns detached tasks in their stable creation order.
func (a *AgentSession) TasksInScope(scope string) []tasks.Task {
	if a == nil || a.AgentLoop == nil {
		return nil
	}
	a.executionMu.Lock()
	defer a.executionMu.Unlock()
	if a.taskState == nil {
		return nil
	}
	var scoped []tasks.Task
	for _, task := range a.taskState.Clone().Snapshot().Tasks {
		if task.Scope == scope {
			scoped = append(scoped, task)
		}
	}
	return scoped
}

// ApplyTaskUpdate applies and durably records an orchestrator-owned task update.
func (a *AgentSession) ApplyTaskUpdate(update tasks.Update) (string, error) {
	if a == nil || a.AgentLoop == nil {
		return "", fmt.Errorf("agent session is required")
	}
	a.executionMu.Lock()
	defer a.executionMu.Unlock()
	return a.applyTaskUpdate(a.channelId, a.channelId, 0, update)
}

// ApplyScopedTaskUpdate ensures an orchestrator can only mutate tasks in its scope.
func (a *AgentSession) ApplyScopedTaskUpdate(scope string, update tasks.Update) (string, error) {
	if a == nil || a.AgentLoop == nil {
		return "", fmt.Errorf("agent session is required")
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", fmt.Errorf("task scope is required")
	}
	a.executionMu.Lock()
	defer a.executionMu.Unlock()
	if update.Op != tasks.OpAdd {
		task, ok := a.taskState.Tasks[strings.TrimSpace(update.ID)]
		if !ok {
			return "", fmt.Errorf("unknown task %s", strings.TrimSpace(update.ID))
		}
		if task.Scope != scope {
			return "", fmt.Errorf("task %s belongs to scope %q, not %q", task.ID, task.Scope, scope)
		}
	}
	update.Scope = scope
	return a.applyTaskUpdate(a.channelId, a.channelId, 0, update)
}

// CompleteScopedTask records worker usage with a goal task in the parent session.
func (a *AgentSession) CompleteScopedTask(scope, id, outcome string, stats *tasks.Stats) (string, error) {
	if a == nil || a.AgentLoop == nil {
		return "", fmt.Errorf("agent session is required")
	}
	a.executionMu.Lock()
	defer a.executionMu.Unlock()
	task, ok := a.taskState.Tasks[id]
	if !ok || task.Scope != scope || strings.TrimSpace(scope) == "" {
		return "", fmt.Errorf("task %s does not belong to scope %q", id, scope)
	}
	return a.applyTaskUpdateWithStats(a.channelId, a.channelId, 0, tasks.Update{Op: tasks.OpDone, ID: id, Scope: scope, Outcome: outcome}, stats)
}

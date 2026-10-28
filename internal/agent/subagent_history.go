package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/lib/eventing"
)

// SubagentReplay is the durable, user-facing portion of a subagent run.
// Intermediate reasoning and tool activity are intentionally excluded.
type SubagentReplay struct {
	Name   string
	Task   string
	Prompt string
	Result string
	Error  string
	Stats  *eventing.SubagentStats
}

func (a *AgentSession) GoalEvents() ([]eventing.GoalEvent, error) {
	events, err := a.AuditEvents()
	if err != nil {
		return nil, err
	}
	result := make([]eventing.GoalEvent, 0)
	for _, event := range events {
		if !eventing.IsGoalEvent(event.EventType) {
			continue
		}
		var payload eventing.GoalEvent
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return nil, err
		}
		payload.Type = event.EventType
		if payload.At.IsZero() {
			payload.At = event.OccurredOn
		}
		result = append(result, payload)
	}
	return result, nil
}

// SubagentReplays returns completed subagent runs in invocation order. Older
// sessions may not have stored prompt/result on the parent invocation events;
// for those, only the first user prompt and last assistant response are read
// from the child context.
func (a *AgentSession) SubagentReplays() ([]SubagentReplay, error) {
	events, err := a.AuditEvents()
	if err != nil {
		return nil, err
	}
	type pendingRun struct {
		index int
		child string
	}
	runs := make([]SubagentReplay, 0)
	pending := make(map[string]pendingRun)
	finished := make(map[int]bool)
	for _, event := range events {
		if event.EventType != eventing.SubagentInvocationStarted && event.EventType != eventing.SubagentInvocationCompleted && event.EventType != eventing.SubagentInvocationFailed {
			continue
		}
		var payload eventing.SubagentInvocation
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return nil, err
		}
		switch event.EventType {
		case eventing.SubagentInvocationStarted:
			runs = append(runs, SubagentReplay{Name: payload.Name, Task: payload.Task, Prompt: payload.Prompt})
			pending[payload.ChildSessionID] = pendingRun{index: len(runs) - 1, child: payload.ChildSessionID}
		case eventing.SubagentInvocationCompleted, eventing.SubagentInvocationFailed:
			entry, ok := pending[payload.ChildSessionID]
			if !ok {
				continue
			}
			runs[entry.index].Result = payload.Result
			runs[entry.index].Error = payload.Error
			runs[entry.index].Stats = payload.Stats
			if a.SessionDir() != "" && (runs[entry.index].Prompt == "" || (runs[entry.index].Result == "" && runs[entry.index].Error == "")) {
				prompt, result, readErr := readSubagentEndpoints(filepath.Join(filepath.Dir(a.SessionDir()), entry.child, "context.jsonpl"))
				if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
					return nil, readErr
				}
				if runs[entry.index].Prompt == "" {
					runs[entry.index].Prompt = prompt
				}
				if runs[entry.index].Result == "" && runs[entry.index].Error == "" {
					runs[entry.index].Result = result
				}
			}
			finished[entry.index] = true
			delete(pending, payload.ChildSessionID)
		}
	}
	completed := make([]SubagentReplay, 0, len(finished))
	for index, run := range runs {
		if finished[index] {
			completed = append(completed, run)
		}
	}
	return completed, nil
}

func readSubagentEndpoints(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = file.Close() }()
	var prompt, result string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record struct {
			EventType string          `json:"event_type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		switch record.EventType {
		case eventing.LLMUserMessageCreated:
			if prompt == "" {
				var payload eventing.LLMUserMessage
				if json.Unmarshal(record.Payload, &payload) == nil {
					prompt = strings.TrimSpace(payload.Message.Content)
				}
			}
		case eventing.LLMAgentResponseCreated:
			var payload eventing.LLMAgentResponse
			if json.Unmarshal(record.Payload, &payload) == nil && strings.TrimSpace(payload.Message.Content) != "" {
				result = strings.TrimSpace(payload.Message.Content)
			}
		}
	}
	return prompt, result, scanner.Err()
}

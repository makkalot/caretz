package agent

import (
	"bufio"
	"context"
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

// SubagentRunStatus is where a subagent run stands.
type SubagentRunStatus string

const (
	SubagentRunning     SubagentRunStatus = "running"
	SubagentCompleted   SubagentRunStatus = "completed"
	SubagentFailed      SubagentRunStatus = "failed"
	SubagentCancelled   SubagentRunStatus = "cancelled"
	SubagentTimedOut    SubagentRunStatus = "timed_out"
	SubagentInterrupted SubagentRunStatus = "interrupted"
)

// SubagentRun is one child of this session, derived from its audit log so it
// survives compaction and session restarts.
type SubagentRun struct {
	Name            string
	ChildSessionID  string
	Task            string
	Model           string
	ReasoningEffort string
	Status          SubagentRunStatus
	// StopReason is a SubagentStop constant; empty unless the run stopped
	// without a result.
	StopReason string
	Result     string
	Error      string
	// Resumable reports whether the child can be reopened: it is not running
	// and was started from an agent definition, not by a goal or a schedule.
	Resumable bool
}

// SubagentRuns returns this session's subagent runs, oldest first. A child
// started more than once appears once, with its latest state.
func (a *AgentSession) SubagentRuns() ([]SubagentRun, error) {
	events, err := a.AuditEvents()
	if err != nil {
		return nil, err
	}
	runs := make([]SubagentRun, 0)
	index := make(map[string]int)
	for _, event := range events {
		switch event.EventType {
		case eventing.SubagentInvocationStarted, eventing.SubagentInvocationCompleted, eventing.SubagentInvocationFailed:
		default:
			continue
		}
		var payload eventing.SubagentInvocation
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return nil, err
		}
		i, known := index[payload.ChildSessionID]
		switch event.EventType {
		case eventing.SubagentInvocationStarted:
			if !known {
				i = len(runs)
				index[payload.ChildSessionID] = i
				runs = append(runs, SubagentRun{Name: payload.Name, ChildSessionID: payload.ChildSessionID, Task: payload.Task})
			}
			runs[i].Model, runs[i].ReasoningEffort = payload.Model, payload.ReasoningEffort
			runs[i].Status = SubagentRunning
			runs[i].StopReason, runs[i].Result, runs[i].Error = "", "", ""
		case eventing.SubagentInvocationCompleted:
			if known {
				runs[i].Status, runs[i].StopReason, runs[i].Result, runs[i].Error = SubagentCompleted, "", payload.Result, ""
			}
		case eventing.SubagentInvocationFailed:
			if known {
				runs[i].StopReason = stoppedRunReason(payload)
				runs[i].Status = stoppedRunStatus(runs[i].StopReason)
				runs[i].Error = payload.Error
			}
		}
	}
	for i := range runs {
		if runs[i].Status == SubagentRunning && !a.childActive(runs[i].ChildSessionID) {
			runs[i].Status, runs[i].StopReason = SubagentInterrupted, eventing.SubagentStopProcessExit
		}
		runs[i].Resumable = runs[i].Status != SubagentRunning && a.ownsResumableChild(runs[i].Name, runs[i].ChildSessionID)
	}
	return runs, nil
}

// ownsResumableChild matches the ID NewSubagentChildSession gives a child of
// this session started from an agent definition. Goal and scheduled children
// use other prefixes, and the parent's ID keeps other sessions' children out.
func (a *AgentSession) ownsResumableChild(name, childID string) bool {
	return strings.HasPrefix(childID, a.channelId+"-subagent-"+name+"-")
}

// stoppedRunReason falls back to the error text for events written before
// StopReason existed.
func stoppedRunReason(payload eventing.SubagentInvocation) string {
	switch {
	case payload.StopReason != "":
		return payload.StopReason
	case strings.Contains(payload.Error, context.Canceled.Error()):
		return eventing.SubagentStopUserCancelled
	case strings.Contains(payload.Error, context.DeadlineExceeded.Error()):
		return eventing.SubagentStopTimeout
	default:
		return eventing.SubagentStopError
	}
}

func stoppedRunStatus(reason string) SubagentRunStatus {
	switch reason {
	case eventing.SubagentStopUserCancelled, eventing.SubagentStopModelCancelled:
		return SubagentCancelled
	case eventing.SubagentStopTimeout:
		return SubagentTimedOut
	case eventing.SubagentStopSessionClosed:
		return SubagentInterrupted
	default:
		return SubagentFailed
	}
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

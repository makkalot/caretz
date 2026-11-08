package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

// DefaultSubagentLogEntries is how many entries /bg log shows when the user
// does not ask for a number.
const DefaultSubagentLogEntries = 20

// SubagentLog is a run and the tail of its conversation, for /bg log.
type SubagentLog struct {
	Run     SubagentRun
	Entries []SubagentLogEntry
}

// SubagentLogEntry is one message of a subagent's conversation: an assistant
// turn with its tool calls, a tool result, or the final answer.
type SubagentLogEntry struct {
	Message llm.Message
	// Tool is the call a tool result answers.
	Tool llm.ToolCall
	// Final marks the run's answer to its task.
	Final bool
}

// SubagentRunLog returns the run ref names and its last n entries, oldest
// first, read from the child's own log. Background runs do not stream their
// activity into the chat, so this is how the user looks inside one. ref is a
// child session ID or an agent name; a name picks that agent's latest run.
func (a *AgentSession) SubagentRunLog(ref string, n int) (SubagentLog, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return SubagentLog{}, errors.New("subagent name or child session ID must not be empty")
	}
	if n <= 0 {
		n = DefaultSubagentLogEntries
	}
	runs, err := a.ListedSubagentRuns()
	if err != nil {
		return SubagentLog{}, err
	}
	var run *SubagentRun
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].ChildSessionID == ref || strings.EqualFold(runs[i].Name, ref) {
			run = &runs[i]
			break
		}
	}
	if run == nil {
		return SubagentLog{}, fmt.Errorf("no subagent run matches %q", ref)
	}
	entries, err := a.subagentLogEntries(*run)
	if err != nil {
		return SubagentLog{Run: *run}, err
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return SubagentLog{Run: *run, Entries: entries}, nil
}

// subagentLogEntries reads every entry of run. A running background child is
// read through its live session; any other run from its log file.
func (a *AgentSession) subagentLogEntries(run SubagentRun) ([]SubagentLogEntry, error) {
	var entries []SubagentLogEntry
	add := func(eventType string, raw []byte) error {
		switch eventType {
		case eventing.LLMAgentMessageCreated:
			var payload eventing.LLMAgentMessage
			if err := json.Unmarshal(raw, &payload); err != nil {
				return err
			}
			if len(payload.Message.ToolCalls) > 0 || strings.TrimSpace(payload.Message.Content) != "" {
				entries = append(entries, SubagentLogEntry{Message: payload.Message})
			}
		case eventing.LLMToolCallResultCreated:
			var payload eventing.LLMToolCallResult
			if err := json.Unmarshal(raw, &payload); err != nil {
				return err
			}
			message := payload.Message
			if payload.Error != "" && !strings.HasPrefix(message.Content, "error: ") {
				message.Content = "error: " + payload.Error
			}
			entries = append(entries, SubagentLogEntry{Message: message, Tool: payload.Tool})
		case eventing.LLMAgentResponseCreated:
			// The final answer is a response event, not an agent message.
			var payload eventing.LLMAgentResponse
			if err := json.Unmarshal(raw, &payload); err != nil {
				return err
			}
			if strings.TrimSpace(payload.Message.Content) != "" {
				entries = append(entries, SubagentLogEntry{Message: payload.Message, Final: true})
			}
		}
		return nil
	}

	a.asyncOwnedMu.Lock()
	live := a.backgroundRuns[run.ChildSessionID]
	a.asyncOwnedMu.Unlock()
	if live != nil && live.child != nil {
		events, err := live.child.ContextEvents(live.child.ChannelID())
		if err == nil {
			for _, event := range events {
				if err := add(event.EventType, []byte(event.Payload)); err != nil {
					return nil, err
				}
			}
			return entries, nil
		}
	}

	if a.SessionDir() == "" {
		return nil, errors.New("this session keeps no log files, so only a running background subagent's log can be shown")
	}
	file, err := os.Open(filepath.Join(filepath.Dir(a.SessionDir()), run.ChildSessionID, "context.jsonpl"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()
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
		if err := add(record.EventType, record.Payload); err != nil {
			return nil, err
		}
	}
	return entries, scanner.Err()
}

// SubagentLogHeader describes the run a log belongs to, as /bg lists it.
func SubagentLogHeader(run SubagentRun) string {
	var b strings.Builder
	writeSubagentRunHeader(&b, run)
	return strings.TrimRight(b.String(), "\n")
}

// FormatSubagentRunLog renders a log as plain text, for clients that cannot
// draw the conversation itself.
func FormatSubagentRunLog(log SubagentLog) string {
	var b strings.Builder
	b.WriteString(SubagentLogHeader(log.Run))
	b.WriteString("\n")
	if len(log.Entries) == 0 {
		b.WriteString("  no activity yet\n")
	}
	for _, line := range SubagentLogLines(log.Entries) {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	return strings.TrimRight(b.String(), "\n")
}

// SubagentLogLines gives each entry as one or more short lines.
func SubagentLogLines(entries []SubagentLogEntry) []string {
	var lines []string
	for _, entry := range entries {
		message := entry.Message
		switch {
		case entry.Final:
			lines = append(lines, "result: "+previewLine(message.Content, 180))
		case message.Role == llm.RoleTool:
			lines = append(lines, fmt.Sprintf("tool result %s: %s", entry.Tool.Name, previewLine(message.Content, 140)))
		default:
			for _, tc := range message.ToolCalls {
				lines = append(lines, fmt.Sprintf("tool call %s(%s)", tc.Name, tc.Arguments))
			}
			if strings.TrimSpace(message.Content) != "" {
				lines = append(lines, "assistant: "+previewLine(message.Content, 180))
			}
		}
	}
	return lines
}

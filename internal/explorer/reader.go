package explorer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/makkalot/caretz/lib/eventing"
)

// storedFileEvent mirrors the on-disk JSON structure.
type storedFileEvent struct {
	OriginatorID      string          `json:"originator_id"`
	OriginatorVersion uint64          `json:"originator_version"`
	EventType         string          `json:"event_type"`
	Payload           json.RawMessage `json:"payload"`
	CreatedAt         int64           `json:"created_at"`
}

// ExplorerEvent is a unified view of an event from either log.
type ExplorerEvent struct {
	Source       string
	Type         string
	CreatedAt    time.Time
	OriginatorID string
	Version      uint64
	ParentID     string
	Payload      interface{}
	RawPayload   string
	Preview      string
	IsCompaction bool
	Depth        int
}

// filterMode controls which events are shown.
type filterMode int

const (
	FilterAll filterMode = iota
	FilterConversation
	FilterToolCalls
	FilterCompaction
	FilterContext
)

func (f filterMode) String() string {
	switch f {
	case FilterAll:
		return "all"
	case FilterConversation:
		return "conversation"
	case FilterToolCalls:
		return "toolcalls"
	case FilterCompaction:
		return "compaction"
	case FilterContext:
		return "context"
	default:
		return "unknown"
	}
}

func (f filterMode) Next() filterMode {
	switch f {
	case FilterAll:
		return FilterConversation
	case FilterConversation:
		return FilterToolCalls
	case FilterToolCalls:
		return FilterCompaction
	case FilterCompaction:
		return FilterContext
	case FilterContext:
		return FilterAll
	default:
		return FilterAll
	}
}

// ReadSessionEvents reads both events.jsonpl and context.jsonpl from a session
// directory and returns a unified, sorted list of ExplorerEvents.
func ReadSessionEvents(sessionDir string) ([]ExplorerEvent, error) {
	var events []ExplorerEvent

	// Read events log.
	eventsPath := filepath.Join(sessionDir, "events.jsonpl")
	if _, err := os.Stat(eventsPath); err == nil {
		evts, err := readEventsFile(eventsPath, "events")
		if err != nil {
			return nil, fmt.Errorf("failed to read events log: %w", err)
		}
		events = append(events, evts...)
	}

	// Read context log.
	contextPath := filepath.Join(sessionDir, "context.jsonpl")
	if _, err := os.Stat(contextPath); err == nil {
		evts, err := readEventsFile(contextPath, "context")
		if err != nil {
			return nil, fmt.Errorf("failed to read context log: %w", err)
		}
		events = append(events, evts...)
	}

	// Sort globally by CreatedAt ascending.
	// Use SliceStable so that events with the same timestamp keep their
	// original file-read order. This ensures tool results always follow
	// the assistant message that triggered them.
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})

	// Compute indentation / threading.
	computeDepths(&events)

	return events, nil
}

func readEventsFile(path, source string) ([]ExplorerEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var events []ExplorerEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var fe storedFileEvent
		if err := json.Unmarshal(line, &fe); err != nil {
			continue // skip malformed lines
		}

		ev := ExplorerEvent{
			Source:       source,
			Type:         fe.EventType,
			CreatedAt:    time.Unix(fe.CreatedAt, 0).UTC(),
			OriginatorID: fe.OriginatorID,
			Version:      fe.OriginatorVersion,
			RawPayload:   string(fe.Payload),
		}

		// Parse payload based on event type.
		switch fe.EventType {
		case eventing.LogMessageUserCreated:
			var p eventing.LogMessage
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.Preview = truncate(p.Content, 70)
			}
		case eventing.SubagentInvocationStarted:
			var p eventing.SubagentInvocation
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.Preview = fmt.Sprintf("%s started → %s: %s", p.Name, p.ChildSessionID, truncate(p.Task, 50))
			}
		case eventing.SubagentInvocationProgress:
			var p eventing.SubagentInvocation
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.Preview = fmt.Sprintf("%s progress: %s", p.Name, truncate(p.Message, 55))
			}
		case eventing.SubagentInvocationCompleted:
			var p eventing.SubagentInvocation
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.Preview = fmt.Sprintf("%s completed: %s", p.Name, truncate(p.Result, 55))
			}
		case eventing.SubagentInvocationFailed:
			var p eventing.SubagentInvocation
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.Preview = fmt.Sprintf("%s failed: %s", p.Name, truncate(p.Error, 60))
			}
		case eventing.LogMessageAgentCreated:
			var p eventing.LogMessage
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.Preview = truncate(p.Content, 70)
			}
		case eventing.LogMessageAgentInterrupted:
			var p eventing.RecoveryPayload
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.Preview = "interrupted: " + truncate(p.Message, 57)
			}
		case eventing.LLMUserMessageCreated:
			var p eventing.LLMUserMessage
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.ParentID = p.ParentOriginatorID
				ev.Preview = truncate(p.Message.Content, 70)
			}
		case eventing.LLMAgentMessageCreated:
			var p eventing.LLMAgentMessage
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.ParentID = p.ParentOriginatorID
				var parts []string
				if p.Message.Content != "" {
					parts = append(parts, truncate(p.Message.Content, 50))
				}
				for _, tc := range p.Message.ToolCalls {
					args := tc.Arguments
					if len(args) > 40 {
						args = args[:37] + "..."
					}
					parts = append(parts, fmt.Sprintf("⚙ %s(%s)", tc.Name, args))
				}
				ev.Preview = strings.Join(parts, " | ")
			}
		case eventing.LLMAgentResponseCreated:
			var p eventing.LLMAgentResponse
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.ParentID = p.ParentOriginatorID
				ev.Preview = truncate(p.Message.Content, 70)
			}
		case eventing.LLMAgentInterruptedResponseCreated:
			var p eventing.RecoveryPayload
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.ParentID = p.InterruptedParentOriginatorID
				ev.Preview = "interrupted: " + truncate(p.Message, 57)
			}
		case eventing.LLMAgentSummaryResponseCreated:
			var p eventing.LLMAgentResponse
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.ParentID = p.ParentOriginatorID
				ev.Preview = truncate(p.Message.Content, 70)
				ev.IsCompaction = true
			}
		case eventing.LLMToolCallResultCreated:
			var p eventing.LLMToolCallResult
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.ParentID = p.ParentOriginatorID
				preview := truncate(p.Message.Content, 70)
				if p.Error != "" {
					preview = "error: " + truncate(p.Error, 60)
				}
				if p.RawResultPath != "" {
					preview += fmt.Sprintf(" [full: %s]", p.RawResultPath)
				}
				ev.Preview = preview
			}
		case eventing.LLMToolCallRecoveryResultCreated:
			var p eventing.RecoveryPayload
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.ParentID = p.InterruptedParentOriginatorID
				ev.Preview = fmt.Sprintf("synthetic tool result %s: %s", p.ToolCallID, truncate(p.Message, 45))
			}
		case eventing.SummaryPromptCreated:
			var p eventing.SummaryPrompt
			if err := json.Unmarshal(fe.Payload, &p); err == nil {
				ev.Payload = p
				ev.ParentID = p.ParentOriginatorID
				ev.Preview = "compaction prompt"
				ev.IsCompaction = true
			}
		}

		events = append(events, ev)
	}

	return events, scanner.Err()
}

// computeDepths assigns a visual indentation depth to each event based on
// conversation threading within a turn (shared ParentID).
func computeDepths(events *[]ExplorerEvent) {
	evts := *events

	// Group events by ParentID for context events.
	parentGroups := make(map[string][]int) // ParentID -> indices in evts
	for i, ev := range evts {
		if ev.Source == "context" && ev.ParentID != "" {
			parentGroups[ev.ParentID] = append(parentGroups[ev.ParentID], i)
		}
	}

	for _, indices := range parentGroups {
		if len(indices) == 0 {
			continue
		}
		// First event in the group (user message) at depth 0.
		// Agent message/response at depth 1.
		// Tool results at depth 2.
		for _, idx := range indices {
			ev := &evts[idx]
			switch ev.Type {
			case eventing.LLMUserMessageCreated:
				ev.Depth = 0
			case eventing.LLMAgentMessageCreated, eventing.LLMAgentResponseCreated,
				eventing.LLMAgentInterruptedResponseCreated, eventing.LLMAgentSummaryResponseCreated,
				eventing.SummaryPromptCreated:
				ev.Depth = 1
			case eventing.LLMToolCallResultCreated, eventing.LLMToolCallRecoveryResultCreated:
				ev.Depth = 2
			}
		}
	}
}

// FilterEvents returns a subset of events matching the current filter mode.
func FilterEvents(events []ExplorerEvent, filter filterMode) []int {
	var indices []int
	for i, ev := range events {
		switch filter {
		case FilterAll:
			indices = append(indices, i)
		case FilterConversation:
			if ev.Source == "events" && (ev.Type == eventing.LogMessageUserCreated || ev.Type == eventing.LogMessageAgentCreated || ev.Type == eventing.LogMessageAgentInterrupted) {
				indices = append(indices, i)
			}
		case FilterToolCalls:
			if ev.Type == eventing.LLMAgentMessageCreated || ev.Type == eventing.LLMToolCallResultCreated || ev.Type == eventing.LLMToolCallRecoveryResultCreated {
				indices = append(indices, i)
			}
		case FilterCompaction:
			if ev.IsCompaction {
				indices = append(indices, i)
			}
		case FilterContext:
			if ev.Source == "context" {
				indices = append(indices, i)
			}
		}
	}
	return indices
}

const detailMaxContent = 5000

func truncateContent(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n\n... (%d more chars)", len(s)-max)
}

// DetailText returns a human-readable detail string for an event.
func DetailText(ev ExplorerEvent) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "Type:    %s\n", ev.Type)
	_, _ = fmt.Fprintf(&b, "Source:  %s\n", ev.Source)
	_, _ = fmt.Fprintf(&b, "Time:    %s\n", ev.CreatedAt.Format("2006-01-02 15:04:05"))
	_, _ = fmt.Fprintf(&b, "Origin:  %s v%d\n", ev.OriginatorID, ev.Version)
	if ev.ParentID != "" {
		_, _ = fmt.Fprintf(&b, "Parent:  %s\n", ev.ParentID)
	}
	_, _ = fmt.Fprintln(&b, strings.Repeat("─", 40))

	switch p := ev.Payload.(type) {
	case eventing.LogMessage:
		_, _ = fmt.Fprintln(&b, truncateContent(p.Content, detailMaxContent))
	case eventing.SubagentInvocation:
		_, _ = fmt.Fprintf(&b, "Subagent:      %s\n", p.Name)
		_, _ = fmt.Fprintf(&b, "Child session: %s\n", p.ChildSessionID)
		_, _ = fmt.Fprintf(&b, "Parent session: %s\n", p.ParentSessionID)
		if p.ParentOriginatorID != "" {
			_, _ = fmt.Fprintf(&b, "Parent event:  %s v%d\n", p.ParentOriginatorID, p.ParentOriginatorVersion)
		}
		_, _ = fmt.Fprintln(&b)
		switch ev.Type {
		case eventing.SubagentInvocationStarted:
			_, _ = fmt.Fprintf(&b, "Task:\n\n%s\n", truncateContent(p.Task, detailMaxContent))
		case eventing.SubagentInvocationProgress:
			_, _ = fmt.Fprintf(&b, "Progress:\n\n%s\n", truncateContent(p.Message, detailMaxContent))
		case eventing.SubagentInvocationCompleted:
			_, _ = fmt.Fprintf(&b, "Result:\n\n%s\n", truncateContent(p.Result, detailMaxContent))
		case eventing.SubagentInvocationFailed:
			_, _ = fmt.Fprintf(&b, "Error:\n\n%s\n", truncateContent(p.Error, detailMaxContent))
		}
	case eventing.LLMUserMessage:
		_, _ = fmt.Fprintf(&b, "Role: %s\n\n", p.Message.Role)
		_, _ = fmt.Fprintln(&b, truncateContent(p.Message.Content, detailMaxContent))
	case eventing.LLMAgentMessage:
		_, _ = fmt.Fprintf(&b, "Role: %s\n", p.Message.Role)
		if p.Usage.TotalTokens > 0 {
			_, _ = fmt.Fprintf(&b, "Usage: prompt=%d completion=%d total=%d\n", p.Usage.PromptTokens, p.Usage.CompletionTokens, p.Usage.TotalTokens)
		}
		if p.CumulativeTotal > 0 {
			_, _ = fmt.Fprintf(&b, "Cumulative: %d\n", p.CumulativeTotal)
		}
		_, _ = fmt.Fprintln(&b)
		if p.Message.Content != "" {
			_, _ = fmt.Fprintln(&b, truncateContent(p.Message.Content, detailMaxContent))
		}
		for _, tc := range p.Message.ToolCalls {
			_, _ = fmt.Fprintf(&b, "\n--- Tool Call ---\n")
			_, _ = fmt.Fprintf(&b, "ID:   %s\n", tc.ID)
			_, _ = fmt.Fprintf(&b, "Name: %s\n", tc.Name)
			_, _ = fmt.Fprintf(&b, "Args: %s\n", tc.Arguments)
		}
	case eventing.LLMAgentResponse:
		_, _ = fmt.Fprintf(&b, "Role: %s\n", p.Message.Role)
		if p.Usage.TotalTokens > 0 {
			_, _ = fmt.Fprintf(&b, "Usage: prompt=%d completion=%d total=%d\n", p.Usage.PromptTokens, p.Usage.CompletionTokens, p.Usage.TotalTokens)
		}
		if p.CumulativeTotal > 0 {
			_, _ = fmt.Fprintf(&b, "Cumulative: %d\n", p.CumulativeTotal)
		}
		_, _ = fmt.Fprintln(&b)
		_, _ = fmt.Fprintln(&b, truncateContent(p.Message.Content, detailMaxContent))
	case eventing.LLMToolCallResult:
		_, _ = fmt.Fprintf(&b, "Tool: %s (id: %s)\n", p.Tool.Name, p.Tool.ID)
		if p.Error != "" {
			_, _ = fmt.Fprintf(&b, "Error: %s\n", p.Error)
		}
		if p.RawResultPath != "" {
			_, _ = fmt.Fprintf(&b, "Raw result saved to: %s\n", p.RawResultPath)
		}
		_, _ = fmt.Fprintln(&b)
		content := p.Message.Content
		if len(content) > 2000 {
			content = content[:2000] + fmt.Sprintf("\n\n... (%d more chars)", len(p.Message.Content)-2000)
		}
		_, _ = fmt.Fprintln(&b, content)
	case eventing.SummaryPrompt:
		_, _ = fmt.Fprintf(&b, "Prompt:\n\n%s", truncateContent(p.Prompt, detailMaxContent))
	default:
		_, _ = fmt.Fprintln(&b, truncateContent(ev.RawPayload, detailMaxContent))
	}

	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// Badge returns a short display badge for the event type.
func Badge(ev ExplorerEvent) (string, string) {
	switch ev.Type {
	case eventing.LogMessageUserCreated:
		return "user", "6"
	case eventing.LogMessageAgentCreated:
		return "agent", "15"
	case eventing.SubagentInvocationStarted:
		return "sub-start", "13"
	case eventing.SubagentInvocationProgress:
		return "sub-prog", "13"
	case eventing.SubagentInvocationCompleted:
		return "sub-done", "10"
	case eventing.SubagentInvocationFailed:
		return "sub-fail", "9"
	case eventing.LLMUserMessageCreated:
		return "ctx-user", "4"
	case eventing.LLMAgentMessageCreated:
		return "ctx-agent", "3"
	case eventing.LLMAgentResponseCreated:
		return "response", "2"
	case eventing.LLMAgentSummaryResponseCreated:
		return "summary", "5"
	case eventing.LLMToolCallResultCreated:
		return "tool", "3"
	case eventing.SummaryPromptCreated:
		return "compact", "1"
	default:
		return ev.Type, "7"
	}
}

// EventHasToolCalls returns true if the event is an agent message containing tool calls.
func EventHasToolCalls(ev ExplorerEvent) bool {
	if p, ok := ev.Payload.(eventing.LLMAgentMessage); ok {
		return len(p.Message.ToolCalls) > 0
	}
	return false
}

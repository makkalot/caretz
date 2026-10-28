package eventing

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/makkalot/caretz/lib/types"
)

const (
	AsyncRunRequested = "Agent.AsyncRunRequested"
	AsyncRunStarted   = "Agent.AsyncRunStarted"
	AsyncRunCompleted = "Agent.AsyncRunCompleted"

	AsyncRunForeground = "foreground"
	AsyncRunSubagent   = "subagent"
	// AsyncRunCommand runs Prompt as a shell command in the sandbox, then
	// gives the session one turn to read its output.
	AsyncRunCommand = "command"
)

type AsyncRunRequest struct {
	RequestID       string         `json:"request_id"`
	Source          string         `json:"source"`
	SourceID        string         `json:"source_id,omitempty"`
	Prompt          string         `json:"prompt"`
	Mode            string         `json:"mode"`
	Model           string         `json:"model,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	ContextLimits   *ContextLimits `json:"context_limits,omitempty"`
	// Timeout bounds the whole run, apart from waiting for the session to
	// finish another turn; zero uses the executor default.
	Timeout time.Duration `json:"timeout,omitempty"`
	// ModelTimeout bounds each model phase of the run: the subagent, and the
	// session turn that reads the result. Zero leaves them bounded only by
	// Timeout.
	ModelTimeout time.Duration `json:"model_timeout,omitempty"`
	// CommandTimeoutSeconds bounds an AsyncRunCommand command; zero uses the
	// sandbox default.
	CommandTimeoutSeconds int `json:"command_timeout_seconds,omitempty"`
	// PreviousRun describes how the previous run from the same source ended
	// when it did not succeed, so this run can change course.
	PreviousRun string `json:"previous_run,omitempty"`
}

// ContextLimits override the token limits of the run's model. A zero field
// keeps the model's value.
type ContextLimits struct {
	Context          int `json:"context,omitempty"`
	ReservedTokens   int `json:"reserved_tokens,omitempty"`
	KeepRecentTokens int `json:"keep_recent_tokens,omitempty"`
	SummaryTokens    int `json:"summary_tokens,omitempty"`
}

type AsyncRunResult struct {
	RequestID string `json:"request_id"`
	Source    string `json:"source"`
	SourceID  string `json:"source_id,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

func NewAsyncRunRequested(originatorID string, request AsyncRunRequest) *types.Event {
	return newAsyncRunEvent(originatorID, AsyncRunRequested, request)
}

func NewAsyncRunStarted(originatorID string, request AsyncRunRequest) *types.Event {
	return newAsyncRunEvent(originatorID, AsyncRunStarted, request)
}

func NewAsyncRunCompleted(originatorID string, result AsyncRunResult) *types.Event {
	return newAsyncRunEvent(originatorID, AsyncRunCompleted, result)
}

func DecodeAsyncRunRequest(event *types.Event) (AsyncRunRequest, error) {
	var request AsyncRunRequest
	if event == nil {
		return request, fmt.Errorf("async run event is nil")
	}
	if err := json.Unmarshal([]byte(event.Payload), &request); err != nil {
		return request, err
	}
	if request.RequestID == "" || request.Prompt == "" {
		return request, fmt.Errorf("async run request is incomplete")
	}
	return request, nil
}

func DecodeAsyncRunResult(event *types.Event) (AsyncRunResult, error) {
	var result AsyncRunResult
	if event == nil {
		return result, fmt.Errorf("async run event is nil")
	}
	if err := json.Unmarshal([]byte(event.Payload), &result); err != nil {
		return result, err
	}
	if result.RequestID == "" {
		return result, fmt.Errorf("async run result is incomplete")
	}
	return result, nil
}

func newAsyncRunEvent(originatorID, eventType string, payload any) *types.Event {
	data, _ := json.Marshal(payload)
	return types.NewEvent(types.NewOriginator(originatorID, 0), eventType, string(data))
}

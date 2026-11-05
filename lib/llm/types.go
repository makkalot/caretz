package llm

import "time"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type Usage struct {
	PromptTokens       int64
	CachedPromptTokens int64
	CompletionTokens   int64
	// ReasoningTokens is the part of CompletionTokens spent thinking, when the
	// provider reports it.
	ReasoningTokens int64 `json:",omitempty"`
	TotalTokens     int64
}

func (u Usage) UncachedPromptTokens() int64 {
	if u.PromptTokens <= u.CachedPromptTokens {
		return 0
	}
	return u.PromptTokens - u.CachedPromptTokens
}

type Response struct {
	Content          string
	ToolCalls        []ToolCall
	Usage            Usage
	Message          Message
	ReasoningSummary string
	Timing           Timing
}

// Timing records when the request that produced a response was sent, when its
// first generated token arrived, and when the response ended.
type Timing struct {
	Start time.Time
	// FirstToken is set only when the stream carried every generated token,
	// thinking included, so End-FirstToken covers all of CompletionTokens.
	// Streams that hide thinking leave it zero.
	FirstToken time.Time
	End        time.Time
}

// Speed is how fast a response was generated.
type Speed struct {
	TokensPerSecond float64 `json:"tokens_per_second,omitempty"`
	// TTFTMs is the time to the first token; zero when unknown.
	TTFTMs int64 `json:"ttft_ms,omitempty"`
}

// Speed divides CompletionTokens by the time spent generating them. With a
// known first token that is decode time only; otherwise it is the whole
// request, which includes prompt prefill but never overstates the rate.
func (r Response) Speed() Speed {
	t := r.Timing
	if t.Start.IsZero() || !t.End.After(t.Start) || r.Usage.CompletionTokens <= 0 {
		return Speed{}
	}
	var speed Speed
	from := t.Start
	if !t.FirstToken.IsZero() && t.FirstToken.After(t.Start) && t.End.After(t.FirstToken) {
		from = t.FirstToken
		speed.TTFTMs = t.FirstToken.Sub(t.Start).Milliseconds()
	}
	speed.TokensPerSecond = float64(r.Usage.CompletionTokens) / t.End.Sub(from).Seconds()
	return speed
}

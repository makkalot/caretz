package llm

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
	TotalTokens        int64
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
}

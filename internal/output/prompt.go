package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/types"
)

// PromptOutput is the quiet one-shot (-p) output: it writes each final
// assistant response to w as raw text and nothing else, so the result can be
// piped into other tools. Activity rendering is CLIOutput's job, used with -v.
type PromptOutput struct {
	w io.Writer
}

func NewPromptOutput(w io.Writer) *PromptOutput {
	return &PromptOutput{w: w}
}

func (o *PromptOutput) RegisterHandlers(agentSession *agent.AgentSession) error {
	return agentSession.OnContextEvent(eventing.LLMAgentResponseCreated, o.HandleLLMAgentResponseCreated)
}

func (o *PromptOutput) HandleLLMAgentResponseCreated(e *types.Event) error {
	var payload eventing.LLMAgentResponse
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	content := strings.TrimRight(payload.Message.Content, "\n")
	if strings.TrimSpace(content) == "" {
		return nil
	}
	_, err := fmt.Fprintln(o.w, content)
	return err
}

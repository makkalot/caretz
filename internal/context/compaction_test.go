package context

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

func TestEstimateMessageTokens(t *testing.T) {
	t.Run("user message", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: llm.RoleUser, Content: "Hello, this is a test message with some content"},
		}
		got := estimateMessageTokensTotal(msgs)
		min := estimateStringTokens("Hello, this is a test message with some content")
		if got <= min {
			t.Errorf("estimateMessageTokensTotal() = %d, want > content-only estimate %d", got, min)
		}
	})

	t.Run("assistant message", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: llm.RoleAssistant, Content: "I will help you with that"},
		}
		got := estimateMessageTokensTotal(msgs)
		min := estimateStringTokens("I will help you with that")
		if got <= min {
			t.Errorf("estimateMessageTokensTotal() = %d, want > content-only estimate %d", got, min)
		}
	})

	t.Run("tool message", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: llm.RoleTool, Content: "file content here", ToolCallID: "call_1"},
		}
		got := estimateMessageTokensTotal(msgs)
		withoutID := estimateMessageTokens(llm.Message{Role: llm.RoleTool, Content: "file content here"})
		if got <= withoutID {
			t.Errorf("estimateMessageTokensTotal() = %d, want > estimate without tool_call_id %d", got, withoutID)
		}
	})

	t.Run("system message", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: llm.RoleSystem, Content: "You are a helpful assistant"},
		}
		got := estimateMessageTokensTotal(msgs)
		min := estimateStringTokens("You are a helpful assistant")
		if got <= min {
			t.Errorf("estimateMessageTokensTotal() = %d, want > content-only estimate %d", got, min)
		}
	})

	t.Run("multiple messages", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: llm.RoleSystem, Content: "System prompt here"},
			{Role: llm.RoleUser, Content: "Hello world"},
			{Role: llm.RoleAssistant, Content: "Hi there"},
		}
		got := estimateMessageTokensTotal(msgs)
		want := estimateMessageTokens(msgs[0]) + estimateMessageTokens(msgs[1]) + estimateMessageTokens(msgs[2])
		if got != want {
			t.Errorf("estimateMessageTokensTotal() = %d, want %d", got, want)
		}
	})

	t.Run("empty messages", func(t *testing.T) {
		msgs := []llm.Message{}
		if got := estimateMessageTokensTotal(msgs); got != 0 {
			t.Errorf("estimateMessageTokensTotal() = %d, want 0", got)
		}
	})
}

func TestEstimateToolsOverhead(t *testing.T) {
	t.Run("no tools", func(t *testing.T) {
		if got := estimateToolsOverhead([]llm.ToolDefinition{}); got != 0 {
			t.Errorf("estimateToolsOverhead() = %d, want 0", got)
		}
	})

	t.Run("single tool", func(t *testing.T) {
		tools := []llm.ToolDefinition{
			{
				Name:        "Read",
				Description: "Read a file from disk",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string"},
					},
					"required": []string{"file_path"},
				},
			},
		}
		got := estimateToolsOverhead(tools)
		if got <= 0 {
			t.Errorf("estimateToolsOverhead() = %d, want > 0", got)
		}
	})

	t.Run("multiple tools", func(t *testing.T) {
		tools := []llm.ToolDefinition{
			{
				Name:        "Read",
				Description: "Read a file",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			{
				Name:        "Write",
				Description: "Write a file",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		}
		got := estimateToolsOverhead(tools)
		if got <= 0 {
			t.Errorf("estimateToolsOverhead() = %d, want > 0", got)
		}
	})
}

func TestEstimateTokens(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "hello"},
	}
	tools := []llm.ToolDefinition{
		{
			Name:        "test",
			Description: "test tool",
			Parameters:  map[string]any{"type": "object"},
		},
	}

	msgTokens := estimateMessageTokensTotal(msgs)
	toolTokens := estimateToolsOverhead(tools)
	want := tokenEstimateRequestOverhead + msgTokens + toolTokens

	got := estimateTokens(msgs, tools, 0)
	if got != want {
		t.Errorf("estimateTokens() = %d, want %d", got, want)
	}
}

func TestCompactionManagerTokenCalibration(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	msgs := []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("x", 300)}}
	tools := []llm.ToolDefinition{{Name: "Read", Description: "Read files", Parameters: map[string]any{"type": "object"}}}

	raw := cm.EstimateRawTokens(msgs, tools)
	initial := cm.EstimateTokens(msgs, tools)
	if initial != raw {
		t.Fatalf("expected initial calibrated estimate to equal raw estimate, got %d raw %d", initial, raw)
	}

	cm.UpdateTokenCalibration(raw, int64(raw*3/2))
	if cm.TokenEstimateMultiplier() <= 1.0 {
		t.Fatalf("expected multiplier to increase, got %.2f", cm.TokenEstimateMultiplier())
	}
	if got := cm.EstimateTokens(msgs, tools); got <= raw {
		t.Fatalf("expected calibrated estimate > raw estimate, got %d raw %d", got, raw)
	}

	cm.UpdateTokenCalibration(raw, int64(raw/2))
	if got, want := cm.TokenEstimateMultiplier(), 1.0; got != want {
		t.Fatalf("expected lower ratio to decay multiplier to %.2f, got %.2f", want, got)
	}

	cm.UpdateTokenCalibration(raw, int64(raw/2))
	if got, want := cm.TokenEstimateMultiplier(), 0.75; got != want {
		t.Fatalf("expected repeated lower ratio to decay multiplier to %.2f, got %.2f", want, got)
	}

	cm.UpdateTokenCalibration(raw, int64(raw*10))
	if cm.TokenEstimateMultiplier() != tokenEstimateMaxCalibration {
		t.Fatalf("expected multiplier to clamp at %.2f, got %.2f", tokenEstimateMaxCalibration, cm.TokenEstimateMultiplier())
	}

	cm.UpdateTokenCalibration(raw, 1)
	if got, want := cm.TokenEstimateMultiplier(), 1.25; got != want {
		t.Fatalf("expected minimum-clamped ratio to decay multiplier to %.2f, got %.2f", want, got)
	}

	cm.UpdateTokenCalibration(0, int64(raw))
	cm.UpdateTokenCalibration(raw, 0)
	if got, want := cm.TokenEstimateMultiplier(), 1.25; got != want {
		t.Fatalf("expected invalid usage to leave multiplier at %.2f, got %.2f", want, got)
	}
}

func TestCompactionManagerRestoreContextUsesLatestPersistedTokenCalibration(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendCalibratedTurn := func(userContent, assistantContent string, multiplier float64) {
		t.Helper()
		if err := ctxStore.AppendUserMessage(eventing.NewLLMUserMessage("ch-1", "parent-1", 0,
			llm.Message{Role: llm.RoleUser, Content: userContent})); err != nil {
			t.Fatalf("append user: %v", err)
		}
		response := eventing.NewLLMAgentResponseWithReasoning("ch-1", "parent-1", 0,
			llm.Message{Role: llm.RoleAssistant, Content: assistantContent}, llm.Usage{}, 0, "", multiplier)
		if _, err := ctxStore.AppendAgentResponse(response); err != nil {
			t.Fatalf("append response: %v", err)
		}
	}

	appendCalibratedTurn("first", "first response", 0.82)
	appendCalibratedTurn("second", "second response", 0.68)
	// A response written by an older caretz version has no calibration field and
	// must not erase the newest calibration sample that is present.
	appendTurn(t, ctxStore, "ch-1", "legacy", "legacy response")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	if got := cm.TokenEstimateMultiplier(); got != tokenEstimateInitialCalibration {
		t.Fatalf("initial multiplier = %.2f, want %.2f", got, tokenEstimateInitialCalibration)
	}
	if _, err := cm.RestoreContext("ch-1"); err != nil {
		t.Fatalf("RestoreContext failed: %v", err)
	}
	if got, want := cm.TokenEstimateMultiplier(), 0.68; got != want {
		t.Fatalf("restored multiplier = %.2f, want latest persisted %.2f", got, want)
	}
}

func TestDisabledManagerRestoreContextUsesPersistedTokenCalibration(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	if err := ctxStore.AppendUserMessage(eventing.NewLLMUserMessage("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleUser, Content: "hello"})); err != nil {
		t.Fatalf("append user: %v", err)
	}
	response := eventing.NewLLMAgentResponseWithReasoning("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, Content: "hi"}, llm.Usage{}, 0, "", 0.73)
	if _, err := ctxStore.AppendAgentResponse(response); err != nil {
		t.Fatalf("append response: %v", err)
	}

	dm := NewDisabledManager(ctxStore, 50000, 1000, 10000, "system prompt")
	if _, err := dm.RestoreContext("ch-1"); err != nil {
		t.Fatalf("RestoreContext failed: %v", err)
	}
	if got, want := dm.TokenEstimateMultiplier(), 0.73; got != want {
		t.Fatalf("restored multiplier = %.2f, want %.2f", got, want)
	}
}

func TestDefaultSummaryTokens(t *testing.T) {
	tests := []struct {
		name       string
		keepRecent int
		want       int
	}{
		{name: "small uses half keep recent", keepRecent: 1000, want: 500},
		{name: "half keep recent", keepRecent: 8192, want: 4096},
		{name: "large does not clamp to fixed maximum", keepRecent: 30000, want: 15000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultSummaryTokens(tt.keepRecent); got != tt.want {
				t.Fatalf("DefaultSummaryTokens(%d) = %d, want %d", tt.keepRecent, got, tt.want)
			}
		})
	}
}

func TestSummaryPromptsDoNotIncludeFixedBudget(t *testing.T) {
	initial := buildInitialSummaryPrompt("<conversation>\nhello\n</conversation>", "", "")
	if strings.Contains(initial, "under 12288 characters") || !strings.Contains(initial, "fixed token or character budget") {
		t.Fatalf("expected initial prompt to avoid fixed budget caps, got %q", initial)
	}
	if !strings.Contains(initial, "most recent state and actionable details") {
		t.Fatalf("expected initial prompt to prioritize recent actionable context, got %q", initial)
	}

	update := buildUpdateSummaryPrompt("<conversation>\nnew\n</conversation>", "old summary", "", "")
	if strings.Contains(update, "under 12288 characters") || !strings.Contains(update, "fixed token or character budget") {
		t.Fatalf("expected update prompt to avoid fixed budget caps, got %q", update)
	}
	if strings.Contains(update, "PRESERVE all existing information") {
		t.Fatalf("expected update prompt not to preserve all stale information, got %q", update)
	}
	if !strings.Contains(update, "currently relevant") {
		t.Fatalf("expected update prompt to require current relevance, got %q", update)
	}
	if !strings.Contains(update, "PRIORITIZE the most recent state and actionable details") {
		t.Fatalf("expected update prompt to prioritize recent actionable context, got %q", update)
	}
}

// appendTurn stores one user message + one agent response to ctxStore.
func appendTurn(t *testing.T, ctxStore *ContextSessionStore, channelID, userContent, assistantContent string) {
	t.Helper()
	userEvent := eventing.NewLLMUserMessage(channelID, "parent-1", 0, llm.Message{Role: llm.RoleUser, Content: userContent})
	if err := ctxStore.AppendUserMessage(userEvent); err != nil {
		t.Fatalf("appendTurn user: %v", err)
	}
	asstEvent := eventing.NewLLMAgentResponse(channelID, "parent-1", 0, llm.Message{Role: llm.RoleAssistant, Content: assistantContent}, llm.Usage{}, 0)
	if _, err := ctxStore.AppendAgentResponse(asstEvent); err != nil {
		t.Fatalf("appendTurn asst: %v", err)
	}
}

// appendSummary stores a SummaryPromptCreated + LLMAgentSummaryResponseCreated pair.
func appendSummary(t *testing.T, ctxStore *ContextSessionStore, channelID, summaryContent string) {
	t.Helper()
	promptEvent := eventing.NewSummaryPromptCreated(channelID, "parent-1", 0, "summarize please")
	if err := ctxStore.AppendUserMessage(promptEvent); err != nil {
		t.Fatalf("appendSummary prompt: %v", err)
	}
	respEvent := eventing.NewLLMAgentSummaryResponse(channelID, "parent-1", 0, llm.Message{Role: llm.RoleAssistant, Content: summaryContent}, llm.Usage{}, 0)
	if _, err := ctxStore.AppendAgentResponse(respEvent); err != nil {
		t.Fatalf("appendSummary response: %v", err)
	}
}

func appendToolPair(t *testing.T, ctxStore *ContextSessionStore, channelID, id, command, result string) {
	t.Helper()
	toolCall := llm.ToolCall{ID: id, Name: "Bash", Arguments: `{"command":"` + command + `"}`}
	if _, err := ctxStore.AppendAgentMessage(eventing.NewLLMAgentMessage(channelID, "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0)); err != nil {
		t.Fatalf("appendToolPair assistant: %v", err)
	}
	if _, err := ctxStore.AppendToolResult(eventing.NewLLMToolCallResult(channelID, "parent-1", 0,
		toolCall, "", "", llm.Message{Role: llm.RoleTool, Content: result, ToolCallID: id})); err != nil {
		t.Fatalf("appendToolPair result: %v", err)
	}
}

func messagesContent(messages []llm.Message) string {
	var parts []string
	for _, msg := range messages {
		parts = append(parts, msg.Content)
	}
	return strings.Join(parts, "\n")
}

func TestGetNewContext_NoSummary(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "user1", "asst1")
	appendTurn(t, ctxStore, "ch-1", "user2", "asst2")
	appendTurn(t, ctxStore, "ch-1", "user3", "asst3")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	result, err := cm.GetNewContext("ch-1")
	if err != nil {
		t.Fatalf("GetNewContext failed: %v", err)
	}

	if len(result) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(result))
	}
	if result[0].Content != "user1" || result[1].Content != "asst1" {
		t.Fatalf("expected first messages to be [user1, asst1], got [%q, %q]", result[0].Content, result[1].Content)
	}
	if result[4].Content != "user3" || result[5].Content != "asst3" {
		t.Fatalf("expected last messages to be [user3, asst3], got [%q, %q]", result[4].Content, result[5].Content)
	}
	for _, msg := range result {
		if strings.Contains(msg.Content, "<summary>") {
			t.Fatalf("expected no summary message, found: %q", msg.Content)
		}
	}
}

func TestGetSummaryPromptEvent_PrunesOversizedStaleToolResult(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "original task", "assistant")
	toolCall := llm.ToolCall{ID: "tc-large", Name: "Bash", Arguments: `{"command":"cat huge.log"}`}
	if _, err := ctxStore.AppendAgentMessage(eventing.NewLLMAgentMessage("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	largeToolResult := strings.Repeat("important output line ", 200)
	if _, err := ctxStore.AppendToolResult(eventing.NewLLMToolCallResult("ch-1", "parent-1", 0,
		toolCall, "", "", llm.Message{Role: llm.RoleTool, Content: largeToolResult, ToolCallID: toolCall.ID})); err != nil {
		t.Fatal(err)
	}
	appendTurn(t, ctxStore, "ch-1", "next user", "next assistant")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 1, "system prompt")
	event, err := cm.GetSummaryPromptEvent("ch-1", "parent-1", 0, "")
	if err != nil {
		t.Fatalf("GetSummaryPromptEvent failed: %v", err)
	}
	if event == nil {
		t.Fatal("expected summary prompt event")
	}
	var payload eventing.SummaryPrompt
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal prompt: %v", err)
	}
	if !strings.Contains(payload.Prompt, "stale oversized tool result pruned before summarization") {
		t.Fatalf("expected oversized tool result digest, got %q", payload.Prompt)
	}
	if strings.Contains(payload.Prompt, largeToolResult) {
		t.Fatalf("summary prompt leaked full oversized tool result")
	}
}

func TestGetSummaryPromptEvent_IncludesRenderedTaskState(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "user", "assistant")
	appendTurn(t, ctxStore, "ch-1", "next user", "next assistant")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 1, "system prompt")
	renderedTasks := "## Tasks\n▶ T1: implement task state — next: update tests\n○ T2: run tests\n✓ T0: inspect code — outcome: done"
	event, err := cm.GetSummaryPromptEvent("ch-1", "parent-1", 0, renderedTasks)
	if err != nil {
		t.Fatalf("GetSummaryPromptEvent failed: %v", err)
	}
	if event == nil {
		t.Fatal("expected summary prompt event")
	}
	var payload eventing.SummaryPrompt
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("failed to unmarshal summary prompt: %v", err)
	}
	for _, want := range []string{"<current-task-state>", "▶ T1: implement task state", "Use <current-task-state> as the authoritative source"} {
		if !strings.Contains(payload.Prompt, want) {
			t.Fatalf("summary prompt missing task state %q: %q", want, payload.Prompt)
		}
	}
}

func TestGetSummaryPromptEvent_IncludesActiveUserRequest(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "original task", "assistant")
	appendTurn(t, ctxStore, "ch-1", "next user", "next assistant")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 1, "system prompt")
	activeRequest := "build me a great medicine tracker app"
	event, err := cm.GetSummaryPromptEventWithActiveRequest("ch-1", "parent-1", 0, "", activeRequest)
	if err != nil {
		t.Fatalf("GetSummaryPromptEventWithActiveRequest failed: %v", err)
	}
	if event == nil {
		t.Fatal("expected summary prompt event")
	}
	msg, err := eventing.EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("EventToLLMMessage failed: %v", err)
	}
	for _, want := range []string{"<active-user-request>", activeRequest, "current top-priority user request", "Preserve <active-user-request>"} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("summary prompt missing active request context %q: %q", want, msg.Content)
		}
	}
}

func TestActiveToolChainSummaryProjectsByCoversThroughVersion(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	if err := ctxStore.AppendUserMessage(eventing.NewLLMUserMessage("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleUser, Content: "build the extension"})); err != nil {
		t.Fatal(err)
	}
	appendToolPair(t, ctxStore, "ch-1", "tc-old-1", "inspect old 1", "old-result-1 "+strings.Repeat("x ", 500))
	appendToolPair(t, ctxStore, "ch-1", "tc-old-2", "inspect old 2", "old-result-2 "+strings.Repeat("y ", 500))
	appendToolPair(t, ctxStore, "ch-1", "tc-recent", "inspect recent", "recent-result")

	cm := NewCompactionManager(ctxStore, 5000, 1000, 300, "system prompt", 1024)
	promptEvent, err := cm.GetActiveToolChainSummaryPromptEventWithActiveRequest("ch-1", "parent-1", 0, "", "")
	if err != nil {
		t.Fatalf("GetActiveToolChainSummaryPromptEventWithActiveRequest failed: %v", err)
	}
	if promptEvent == nil {
		t.Fatal("expected active tool-chain summary prompt")
	}
	var prompt eventing.SummaryPrompt
	if err := json.Unmarshal([]byte(promptEvent.Payload), &prompt); err != nil {
		t.Fatal(err)
	}
	if prompt.CompactionScope != "active_tool_chain" {
		t.Fatalf("CompactionScope = %q, want active_tool_chain", prompt.CompactionScope)
	}
	if prompt.ActiveUserVersion != 1 {
		t.Fatalf("ActiveUserVersion = %d, want 1", prompt.ActiveUserVersion)
	}
	if prompt.CoversThroughVersion <= prompt.ActiveUserVersion {
		t.Fatalf("CoversThroughVersion = %d, want > active user version", prompt.CoversThroughVersion)
	}
	if !strings.Contains(prompt.Prompt, "old-result-1") {
		t.Fatalf("summary prompt should include old covered tool result:\n%s", prompt.Prompt)
	}

	if err := ctxStore.AppendUserMessage(promptEvent); err != nil {
		t.Fatal(err)
	}
	respEvent := eventing.NewLLMAgentSummaryResponseWithMetadata("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, Content: "summary of old tool work"}, llm.Usage{}, 0,
		prompt.CompactionScope, prompt.ActiveUserVersion, prompt.CoversThroughVersion)
	if _, err := ctxStore.AppendAgentResponse(respEvent); err != nil {
		t.Fatal(err)
	}

	projected, err := cm.GetNewContext("ch-1")
	if err != nil {
		t.Fatalf("GetNewContext failed: %v", err)
	}
	got := messagesContent(projected)
	if !strings.Contains(got, "build the extension") {
		t.Fatalf("projected context lost active user:\n%s", got)
	}
	if !strings.Contains(got, "summary of old tool work") {
		t.Fatalf("projected context missing active summary:\n%s", got)
	}
	if strings.Contains(got, "old-result-1") {
		t.Fatalf("projected context retained covered raw tool result:\n%s", got)
	}
	if !strings.Contains(got, "recent-result") {
		t.Fatalf("projected context lost raw event after coversThrough cursor:\n%s", got)
	}
}

func TestActiveToolChainSummaryUpdatesFromPreviousCursor(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	if err := ctxStore.AppendUserMessage(eventing.NewLLMUserMessage("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleUser, Content: "debug the build"})); err != nil {
		t.Fatal(err)
	}
	appendToolPair(t, ctxStore, "ch-1", "tc-old", "old", "old-result "+strings.Repeat("x ", 400))
	appendToolPair(t, ctxStore, "ch-1", "tc-mid", "mid", "mid-result "+strings.Repeat("y ", 400))

	cm := NewCompactionManager(ctxStore, 5000, 1000, 250, "system prompt", 1024)
	firstPromptEvent, err := cm.GetActiveToolChainSummaryPromptEventWithActiveRequest("ch-1", "parent-1", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var firstPrompt eventing.SummaryPrompt
	if err := json.Unmarshal([]byte(firstPromptEvent.Payload), &firstPrompt); err != nil {
		t.Fatal(err)
	}
	if err := ctxStore.AppendUserMessage(firstPromptEvent); err != nil {
		t.Fatal(err)
	}
	firstResp := eventing.NewLLMAgentSummaryResponseWithMetadata("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, Content: "previous active summary"}, llm.Usage{}, 0,
		firstPrompt.CompactionScope, firstPrompt.ActiveUserVersion, firstPrompt.CoversThroughVersion)
	if _, err := ctxStore.AppendAgentResponse(firstResp); err != nil {
		t.Fatal(err)
	}

	appendToolPair(t, ctxStore, "ch-1", "tc-new", "new", "new-result "+strings.Repeat("z ", 400))
	secondPromptEvent, err := cm.GetActiveToolChainSummaryPromptEventWithActiveRequest("ch-1", "parent-1", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if secondPromptEvent == nil {
		t.Fatal("expected second active tool-chain summary prompt")
	}
	var secondPrompt eventing.SummaryPrompt
	if err := json.Unmarshal([]byte(secondPromptEvent.Payload), &secondPrompt); err != nil {
		t.Fatal(err)
	}
	if secondPrompt.CoversThroughVersion <= firstPrompt.CoversThroughVersion {
		t.Fatalf("second coversThrough = %d, want > first %d", secondPrompt.CoversThroughVersion, firstPrompt.CoversThroughVersion)
	}
	if !strings.Contains(secondPrompt.Prompt, "previous active summary") {
		t.Fatalf("second prompt missing previous summary:\n%s", secondPrompt.Prompt)
	}
	if strings.Contains(secondPrompt.Prompt, "old-result") {
		t.Fatalf("second prompt re-summarized already covered raw tool result:\n%s", secondPrompt.Prompt)
	}
}

func TestGetSummaryPromptEvent_EmergencyTrimsOversizedPromptOnTurnBoundary(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendSummary(t, ctxStore, "ch-1", "previous summary must remain")
	if err := ctxStore.AppendUserMessage(eventing.NewLLMUserMessage("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleUser, Content: "tool-user " + strings.Repeat("large-user ", 5000)})); err != nil {
		t.Fatal(err)
	}
	toolCall := llm.ToolCall{ID: "tc-big", Name: "Bash", Arguments: `{"command":"cat very-large.log"}`}
	if _, err := ctxStore.AppendAgentMessage(eventing.NewLLMAgentMessage("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := ctxStore.AppendToolResult(eventing.NewLLMToolCallResult("ch-1", "parent-1", 0,
		toolCall, "", "", llm.Message{Role: llm.RoleTool, Content: "tool-result-unique " + strings.Repeat("large-result ", 2000), ToolCallID: toolCall.ID})); err != nil {
		t.Fatal(err)
	}
	if _, err := ctxStore.AppendAgentResponse(eventing.NewLLMAgentResponse("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, Content: "tool-final-unique"}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	appendTurn(t, ctxStore, "ch-1", "small-user-keep", "small-assistant-keep")
	appendTurn(t, ctxStore, "ch-1", "new-user-keep", "new-assistant-keep")

	cm := NewCompactionManager(ctxStore, 12000, 1000, 1, "system prompt", 1024)
	event, err := cm.GetSummaryPromptEventWithContextLimit("ch-1", "parent-1", 0, "rendered task state must remain", 12000)
	if err != nil {
		t.Fatalf("GetSummaryPromptEventWithContextLimit failed: %v", err)
	}
	if event == nil {
		t.Fatal("expected summary prompt event")
	}
	msg, err := eventing.EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("EventToLLMMessage failed: %v", err)
	}
	budget := summaryPromptTokenBudget(12000, 1024)
	if got := estimateTokens([]llm.Message{msg}, nil, 0); got > budget {
		t.Fatalf("summary prompt estimate = %d, want <= budget %d", got, budget)
	}
	for _, unwanted := range []string{"tool-user", "tc-big", "tool-result-unique", "tool-final-unique"} {
		if strings.Contains(msg.Content, unwanted) {
			t.Fatalf("trimmed prompt split or retained oversized tool turn %q:\n%s", unwanted, msg.Content)
		}
	}
	for _, want := range []string{"Emergency compaction trimmed", "previous summary must remain", "rendered task state must remain", "small-user-keep", "small-assistant-keep"} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("summary prompt missing %q:\n%s", want, msg.Content)
		}
	}
}

func TestRestoreContext_AllowsChainedToolCallsInCompletedTurn(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	if err := ctxStore.AppendUserMessage(eventing.NewLLMUserMessage("ch-1", "parent-1", 1,
		llm.Message{Role: llm.RoleUser, Content: "inspect files"})); err != nil {
		t.Fatal(err)
	}
	toolCall1 := llm.ToolCall{ID: "tc-1", Name: "Read", Arguments: `{"file":"a.txt"}`}
	if _, err := ctxStore.AppendAgentMessage(eventing.NewLLMAgentMessage("ch-1", "parent-1", 1,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall1}}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := ctxStore.AppendToolResult(eventing.NewLLMToolCallResult("ch-1", "parent-1", 1,
		toolCall1, "", "", llm.Message{Role: llm.RoleTool, Content: "a", ToolCallID: "tc-1"})); err != nil {
		t.Fatal(err)
	}
	toolCall2 := llm.ToolCall{ID: "tc-2", Name: "Read", Arguments: `{"file":"b.txt"}`}
	if _, err := ctxStore.AppendAgentMessage(eventing.NewLLMAgentMessage("ch-1", "parent-1", 1,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall2}}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := ctxStore.AppendToolResult(eventing.NewLLMToolCallResult("ch-1", "parent-1", 1,
		toolCall2, "", "", llm.Message{Role: llm.RoleTool, Content: "b", ToolCallID: "tc-2"})); err != nil {
		t.Fatal(err)
	}
	if _, err := ctxStore.AppendAgentResponse(eventing.NewLLMAgentResponse("ch-1", "parent-1", 1,
		llm.Message{Role: llm.RoleAssistant, Content: "done"}, llm.Usage{}, 0)); err != nil {
		t.Fatal(err)
	}

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	result, err := cm.RestoreContext("ch-1")
	if err != nil {
		t.Fatalf("RestoreContext failed: %v", err)
	}
	if len(result) != 6 {
		t.Fatalf("expected completed chained tool turn to remain, got %d messages: %#v", len(result), result)
	}
	if result[len(result)-1].Content != "done" {
		t.Fatalf("expected final assistant response, got %#v", result)
	}
}

func TestTrimIncompleteToolTurnKeepsLaterSummaryAndConversation(t *testing.T) {
	toolCall := llm.ToolCall{ID: "tc-1", Name: "Read", Arguments: `{"file":"a.txt"}`}
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "old user"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{toolCall}},
		{Role: llm.RoleTool, Content: "tool output", ToolCallID: "tc-1"},
		{Role: llm.RoleUser, Content: "The conversation history before this point was compacted into the following summary:\n\n<summary>\nvalid summary\n</summary>"},
		{Role: llm.RoleUser, Content: "new user"},
		{Role: llm.RoleAssistant, Content: "new response"},
	}

	result, reason := trimIncompleteToolTailWithReason(messages)
	if len(result) != 3 {
		t.Fatalf("expected incomplete tool turn to be dropped while later context remains, got %d messages (%s): %#v", len(result), reason, result)
	}
	if !strings.Contains(result[0].Content, "valid summary") {
		t.Fatalf("expected summary to remain, got %#v", result)
	}
	if result[1].Content != "new user" || result[2].Content != "new response" {
		t.Fatalf("expected later conversation to remain, got %#v", result)
	}
}

func TestRestoreContext_CompactedActiveToolFragmentBeforeSummaryDoesNotRequireRepair(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendSummary(t, ctxStore, "ch-1", "previous summary")
	appendToolPair(t, ctxStore, "ch-1", "tc-fragment", "echo fragment", "fragment output")
	appendSummary(t, ctxStore, "ch-1", "latest summary after fragment")
	appendTurn(t, ctxStore, "ch-1", "after summary user", "after summary assistant")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	result, err := cm.RestoreContext("ch-1")
	if err != nil {
		t.Fatalf("RestoreContext failed: %v", err)
	}
	joined := messagesContent(result)
	if !strings.Contains(joined, "latest summary after fragment") {
		t.Fatalf("expected latest summary to remain, got %#v", result)
	}
	if strings.Contains(joined, "fragment output") {
		t.Fatalf("expected pre-summary active tool fragment to be trimmed, got %#v", result)
	}
	if !strings.Contains(joined, "after summary assistant") {
		t.Fatalf("expected post-summary conversation to remain, got %#v", result)
	}
}

func TestValidateConversationForRestoreReportsOutstandingToolIDs(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "run tools"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "tc-b", Name: "Bash", Arguments: `{"command":"b"}`},
			{ID: "tc-a", Name: "Bash", Arguments: `{"command":"a"}`},
		}},
		{Role: llm.RoleTool, Content: "a output", ToolCallID: "tc-a"},
		{Role: llm.RoleUser, Content: "next user"},
	}

	result := validateConversationForRestore(messages)
	if result.DroppedMessages != 3 {
		t.Fatalf("expected incomplete turn to be dropped, got %d dropped messages: %#v", result.DroppedMessages, result)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected one finding, got %#v", result.Findings)
	}
	finding := result.Findings[0]
	if finding.Kind != "incomplete_tool_turn_before_user_or_system" {
		t.Fatalf("unexpected finding kind: %#v", finding)
	}
	if got, want := strings.Join(finding.PendingToolCallIDs, ","), "tc-b"; got != want {
		t.Fatalf("expected pending tool IDs %q, got %q", want, got)
	}
	if !finding.AwaitingFinalAssistant {
		t.Fatalf("expected validator to report missing terminal assistant: %#v", finding)
	}
}

func TestRestoreContext_IgnoresBlankLatestSummary(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "old-user", "old-asst")
	appendSummary(t, ctxStore, "ch-1", "valid summary")
	appendTurn(t, ctxStore, "ch-1", "new-user", "new-asst")
	appendSummary(t, ctxStore, "ch-1", "")
	appendTurn(t, ctxStore, "ch-1", "after-blank-user", "after-blank-asst")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	result, err := cm.RestoreContext("ch-1")
	if err != nil {
		t.Fatalf("RestoreContext failed: %v", err)
	}
	joined := messagesContent(result)
	if !strings.Contains(joined, "valid summary") {
		t.Fatalf("expected restored context to use valid previous summary, got %#v", result)
	}
	if strings.Contains(joined, "<summary>\n\n</summary>") {
		t.Fatalf("blank summary was restored: %#v", result)
	}
	summaryIdx := -1
	newUserIdx := -1
	afterBlankIdx := -1
	for i, msg := range result {
		if strings.Contains(msg.Content, "valid summary") {
			summaryIdx = i
		}
		if msg.Content == "new-user" {
			newUserIdx = i
		}
		if msg.Content == "after-blank-user" {
			afterBlankIdx = i
		}
	}
	if summaryIdx == -1 || newUserIdx == -1 || afterBlankIdx == -1 {
		t.Fatalf("expected valid summary and all post-valid-summary turns, got %#v", result)
	}
	if summaryIdx >= newUserIdx || newUserIdx >= afterBlankIdx {
		t.Fatalf("expected blank summary to be ignored as a cut point; summary=%d new=%d afterBlank=%d result=%#v", summaryIdx, newUserIdx, afterBlankIdx, result)
	}
}

func TestGetNewContext_OneCompactionMidSession(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "pre1", "pre-asst1")
	appendTurn(t, ctxStore, "ch-1", "pre2", "pre-asst2")
	appendSummary(t, ctxStore, "ch-1", "first summary content")
	appendTurn(t, ctxStore, "ch-1", "post1", "post-asst1")
	appendTurn(t, ctxStore, "ch-1", "post2", "post-asst2")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	result, err := cm.GetNewContext("ch-1")
	if err != nil {
		t.Fatalf("GetNewContext failed: %v", err)
	}

	// Find summary index
	summaryIdx := -1
	for i, msg := range result {
		if strings.Contains(msg.Content, "<summary>") && strings.Contains(msg.Content, "first summary content") {
			summaryIdx = i
			break
		}
	}
	if summaryIdx == -1 {
		t.Fatalf("expected a summary message in result, none found")
	}
	if summaryIdx == 0 {
		t.Fatalf("expected summary not to be the first element, got index 0")
	}
	if summaryIdx == len(result)-1 {
		t.Fatalf("expected summary not to be the last element, got index %d", summaryIdx)
	}

	// Messages before summary
	wantBefore := []string{"pre1", "pre-asst1", "pre2", "pre-asst2"}
	gotBefore := make([]string, 0, summaryIdx)
	for i := 0; i < summaryIdx; i++ {
		gotBefore = append(gotBefore, result[i].Content)
	}
	if len(gotBefore) != len(wantBefore) {
		t.Fatalf("expected %d messages before summary, got %d: %v", len(wantBefore), len(gotBefore), gotBefore)
	}
	for i, want := range wantBefore {
		if gotBefore[i] != want {
			t.Fatalf("expected before-summary message %d to be %q, got %q", i, want, gotBefore[i])
		}
	}

	// Messages after summary
	wantAfter := []string{"post1", "post-asst1", "post2", "post-asst2"}
	afterStart := summaryIdx + 1
	gotAfter := make([]string, 0, len(result)-afterStart)
	for i := afterStart; i < len(result); i++ {
		gotAfter = append(gotAfter, result[i].Content)
	}
	if len(gotAfter) != len(wantAfter) {
		t.Fatalf("expected %d messages after summary, got %d: %v", len(wantAfter), len(gotAfter), gotAfter)
	}
	for i, want := range wantAfter {
		if gotAfter[i] != want {
			t.Fatalf("expected after-summary message %d to be %q, got %q", i, want, gotAfter[i])
		}
	}

	if result[len(result)-1].Content != "post-asst2" {
		t.Fatalf("expected last element to contain post-asst2, got %q", result[len(result)-1].Content)
	}
}

func TestGetNewContext_SessionEndedWithCompaction(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "old1", "old-asst1")
	appendTurn(t, ctxStore, "ch-1", "old2", "old-asst2")
	appendSummary(t, ctxStore, "ch-1", "prev summary content")
	appendTurn(t, ctxStore, "ch-1", "mid1", "mid-asst1")
	appendTurn(t, ctxStore, "ch-1", "mid2", "mid-asst2")
	appendSummary(t, ctxStore, "ch-1", "last summary content")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	result, err := cm.GetNewContext("ch-1")
	if err != nil {
		t.Fatalf("GetNewContext failed: %v", err)
	}

	if !strings.Contains(result[len(result)-1].Content, "<summary>") || !strings.Contains(result[len(result)-1].Content, "last summary content") {
		t.Fatalf("expected last element to contain <summary> and last summary content, got %q", result[len(result)-1].Content)
	}
	if strings.Contains(result[0].Content, "<summary>") {
		t.Fatalf("expected first element NOT to contain <summary>, got %q", result[0].Content)
	}

	// mid messages should be present
	var contents []string
	for _, msg := range result {
		contents = append(contents, msg.Content)
	}
	for _, want := range []string{"mid1", "mid-asst1", "mid2", "mid-asst2"} {
		found := false
		for _, c := range contents {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected result to contain %q, not found", want)
		}
	}

	// old messages should NOT be present
	for _, unwanted := range []string{"old1", "old-asst1", "old2", "old-asst2"} {
		for _, msg := range result {
			if msg.Content == unwanted {
				t.Fatalf("expected result NOT to contain %q, but found it", unwanted)
			}
		}
	}

	// previous summary content should NOT appear separately
	for _, msg := range result {
		if strings.Contains(msg.Content, "prev summary content") {
			t.Fatalf("expected result NOT to contain previous summary content, but found: %q", msg.Content)
		}
	}
}

func TestGetNewContext_ResumedAfterCompaction(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "old1", "old-asst1")
	appendTurn(t, ctxStore, "ch-1", "old2", "old-asst2")
	appendSummary(t, ctxStore, "ch-1", "prev summary content")
	appendTurn(t, ctxStore, "ch-1", "mid1", "mid-asst1")
	appendTurn(t, ctxStore, "ch-1", "mid2", "mid-asst2")
	appendSummary(t, ctxStore, "ch-1", "last summary content")
	appendTurn(t, ctxStore, "ch-1", "new1", "new-asst1")
	appendTurn(t, ctxStore, "ch-1", "new2", "new-asst2")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 10000, "system prompt")
	result, err := cm.GetNewContext("ch-1")
	if err != nil {
		t.Fatalf("GetNewContext failed: %v", err)
	}

	// Summary should exist
	summaryIdx := -1
	for i, msg := range result {
		if strings.Contains(msg.Content, "<summary>") && strings.Contains(msg.Content, "last summary content") {
			summaryIdx = i
			break
		}
	}
	if summaryIdx == -1 {
		t.Fatalf("expected a summary message in result, none found")
	}
	if summaryIdx == len(result)-1 {
		t.Fatalf("expected summary not to be the last element, got index %d", summaryIdx)
	}

	// Messages before summary
	wantBefore := []string{"mid1", "mid-asst1", "mid2", "mid-asst2"}
	gotBefore := make([]string, 0, summaryIdx)
	for i := 0; i < summaryIdx; i++ {
		gotBefore = append(gotBefore, result[i].Content)
	}
	if len(gotBefore) != len(wantBefore) {
		t.Fatalf("expected %d messages before summary, got %d: %v", len(wantBefore), len(gotBefore), gotBefore)
	}
	for i, want := range wantBefore {
		if gotBefore[i] != want {
			t.Fatalf("expected before-summary message %d to be %q, got %q", i, want, gotBefore[i])
		}
	}

	// Messages after summary
	wantAfter := []string{"new1", "new-asst1", "new2", "new-asst2"}
	afterStart := summaryIdx + 1
	gotAfter := make([]string, 0, len(result)-afterStart)
	for i := afterStart; i < len(result); i++ {
		gotAfter = append(gotAfter, result[i].Content)
	}
	if len(gotAfter) != len(wantAfter) {
		t.Fatalf("expected %d messages after summary, got %d: %v", len(wantAfter), len(gotAfter), gotAfter)
	}
	for i, want := range wantAfter {
		if gotAfter[i] != want {
			t.Fatalf("expected after-summary message %d to be %q, got %q", i, want, gotAfter[i])
		}
	}

	if result[len(result)-1].Content != "new-asst2" {
		t.Fatalf("expected last element to contain new-asst2, got %q", result[len(result)-1].Content)
	}

	// old messages should NOT be present
	for _, unwanted := range []string{"old1", "old-asst1", "old2", "old-asst2"} {
		for _, msg := range result {
			if msg.Content == unwanted {
				t.Fatalf("expected result NOT to contain %q, but found it", unwanted)
			}
		}
	}

	// previous summary content should NOT appear separately
	for _, msg := range result {
		if strings.Contains(msg.Content, "prev summary content") {
			t.Fatalf("expected result NOT to contain previous summary content, but found: %q", msg.Content)
		}
	}
}

func TestGetNewContext_CutPointAlignsToUserMessage(t *testing.T) {
	// When findCuttingPoint lands on an assistant tool-call message, the
	// alignment retreats BACKWARD to the nearest preceding user message so
	// the complete turn (user + tool calls + responses) is included.
	//
	// Between-summary messages (0-indexed after filtering):
	//   0: user  "big-user"       (8 chars = 2 tokens)
	//   1: asst  "big-asst"       (8 chars = 2 tokens)
	//   2: user  "user-start"     (10 chars = 2 tokens)  ← retreat lands here
	//   3: asst  toolCallAsst     (" ReadFile xxxxxxxx" = 18 chars = 4 tokens)
	//   4: tool  "tool-result"    (skipped by findCuttingPoint; 11 chars = 2 tokens added to total)
	//   5: asst  "rrrrrrrr"       (8 chars = 2 tokens)
	//   6: user  "uuuu"           (4 chars = 1 token)
	//   7: asst  "aaaa"           (4 chars = 1 token)
	//
	// Walking backwards with keepRecentTokens=100 under the conservative
	// estimator crosses the threshold at the assistant tool-call message.
	//
	// Backward retreat: index 3 = asst → index 2 = user "user-start" → stop.
	// keepRecent = messages[2:] = [user-start, toolCallAsst, toolResult, rrrr, uuuu, aaaa]
	// "big-user" and "big-asst" are excluded.

	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "old1", "old-asst1")
	appendSummary(t, ctxStore, "ch-1", "prev summary content")

	// Two turns between summaries: one that gets cut off, one that anchors keepRecent
	appendTurn(t, ctxStore, "ch-1", "big-user", "big-asst")

	// "user-start" turn: user → tool call → tool result → final response
	userStartEvent := eventing.NewLLMUserMessage("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleUser, Content: "user-start"})
	if err := ctxStore.AppendUserMessage(userStartEvent); err != nil {
		t.Fatalf("failed to append user-start: %v", err)
	}
	toolCallMsg := llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "tc-1", Name: "ReadFile", Arguments: "xxxxxxxx"}},
	}
	toolCallEvent := eventing.NewLLMAgentMessage("ch-1", "parent-1", 0, toolCallMsg, llm.Usage{}, 0)
	if _, err := ctxStore.AppendAgentMessage(toolCallEvent); err != nil {
		t.Fatalf("failed to append tool call msg: %v", err)
	}
	toolResEvent := eventing.NewLLMToolCallResult("ch-1", "parent-1", 0,
		llm.ToolCall{ID: "tc-1", Name: "ReadFile", Arguments: "xxxxxxxx"}, "", "",
		llm.Message{Role: llm.RoleTool, Content: "tool-result", ToolCallID: "tc-1"})
	if _, err := ctxStore.AppendToolResult(toolResEvent); err != nil {
		t.Fatalf("failed to append tool result: %v", err)
	}
	if _, err := ctxStore.AppendAgentResponse(eventing.NewLLMAgentResponse("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, Content: "rrrrrrrr"}, llm.Usage{}, 0)); err != nil {
		t.Fatalf("failed to append rrrr response: %v", err)
	}

	// Small trailing turn
	if err := ctxStore.AppendUserMessage(eventing.NewLLMUserMessage("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleUser, Content: "uuuu"})); err != nil {
		t.Fatalf("failed to append uuuu: %v", err)
	}
	if _, err := ctxStore.AppendAgentResponse(eventing.NewLLMAgentResponse("ch-1", "parent-1", 0,
		llm.Message{Role: llm.RoleAssistant, Content: "aaaa"}, llm.Usage{}, 0)); err != nil {
		t.Fatalf("failed to append aaaa: %v", err)
	}

	appendSummary(t, ctxStore, "ch-1", "last summary content")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 100, "system prompt")
	result, err := cm.GetNewContext("ch-1")
	if err != nil {
		t.Fatalf("GetNewContext failed: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}

	// First message must be a user message (the preceding turn boundary, not an orphan)
	if result[0].Role != llm.RoleUser {
		t.Fatalf("expected first message role=user, got role=%v content=%q", result[0].Role, result[0].Content)
	}
	// Retreat lands on "user-start", not forward to "uuuu"
	if result[0].Content != "user-start" {
		t.Fatalf("expected first message 'user-start', got %q", result[0].Content)
	}

	// "big-user"/"big-asst" were before the retreat point — must be excluded
	for _, unwanted := range []string{"big-user", "big-asst", "old1", "old-asst1"} {
		for _, msg := range result {
			if msg.Content == unwanted {
				t.Fatalf("expected result NOT to contain %q, but found it", unwanted)
			}
		}
	}

	// The tool-call sequence belonging to "user-start" must be present
	hasToolCall := false
	for _, msg := range result {
		if len(msg.ToolCalls) > 0 {
			hasToolCall = true
			break
		}
	}
	if !hasToolCall {
		t.Fatalf("expected tool-call message from user-start's turn to be present")
	}

	// Summary must be the last element
	if !strings.Contains(result[len(result)-1].Content, "<summary>") ||
		!strings.Contains(result[len(result)-1].Content, "last summary content") {
		t.Fatalf("expected last element to be the summary, got %q", result[len(result)-1].Content)
	}
}

func TestGetSummaryPromptEvent_UpdateOnlyIncludesMessagesSinceLatestSummary(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendTurn(t, ctxStore, "ch-1", "old1", "old-asst1")
	appendTurn(t, ctxStore, "ch-1", "old2", "old-asst2")
	appendSummary(t, ctxStore, "ch-1", "first summary content")
	appendTurn(t, ctxStore, "ch-1", "mid1", "mid-asst1")
	appendTurn(t, ctxStore, "ch-1", "mid2", "mid-asst2")
	appendSummary(t, ctxStore, "ch-1", "latest summary content")
	appendTurn(t, ctxStore, "ch-1", "new1", "new-asst1")
	appendTurn(t, ctxStore, "ch-1", "new2", "new-asst2")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 1, "system prompt")
	event, err := cm.GetSummaryPromptEvent("ch-1", "parent-1", 0, "")
	if err != nil {
		t.Fatalf("GetSummaryPromptEvent failed: %v", err)
	}
	if event == nil {
		t.Fatal("expected summary prompt event")
	}

	msg, err := eventing.EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("EventToLLMMessage failed: %v", err)
	}

	if !strings.Contains(msg.Content, "<previous-summary>\nlatest summary content\n</previous-summary>") {
		t.Fatalf("expected latest summary in previous-summary block, got %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "new1") {
		t.Fatalf("expected prompt to include new messages since latest summary, got %q", msg.Content)
	}
	for _, unwanted := range []string{"old1", "old-asst1", "old2", "old-asst2", "mid1", "mid-asst1", "mid2", "mid-asst2", "first summary content"} {
		if strings.Contains(msg.Content, unwanted) {
			t.Fatalf("expected prompt not to include already-summarized content %q, got %q", unwanted, msg.Content)
		}
	}
}

func TestGetSummaryPromptEvent_UpdateIncludesActiveUserRequest(t *testing.T) {
	ctxStore, _, cleanup := setupContextSessionStore(t)
	defer cleanup()

	appendSummary(t, ctxStore, "ch-1", "latest summary content")
	appendTurn(t, ctxStore, "ch-1", "new1", "new-asst1")
	appendTurn(t, ctxStore, "ch-1", "new2", "new-asst2")

	cm := NewCompactionManager(ctxStore, 50000, 1000, 1, "system prompt")
	activeRequest := "finish the original app build request"
	event, err := cm.GetSummaryPromptEventWithActiveRequest("ch-1", "parent-1", 0, "", activeRequest)
	if err != nil {
		t.Fatalf("GetSummaryPromptEventWithActiveRequest failed: %v", err)
	}
	if event == nil {
		t.Fatal("expected summary prompt event")
	}
	msg, err := eventing.EventToLLMMessage(event)
	if err != nil {
		t.Fatalf("EventToLLMMessage failed: %v", err)
	}
	for _, want := range []string{"<active-user-request>", activeRequest, "<previous-summary>\nlatest summary content\n</previous-summary>", "current top-priority user request"} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("update summary prompt missing active request context %q: %q", want, msg.Content)
		}
	}
}

func TestExtractMessageText(t *testing.T) {
	tests := []struct {
		name string
		msg  llm.Message
		want string
	}{
		{
			name: "user message",
			msg:  llm.Message{Role: llm.RoleUser, Content: "hello world"},
			want: "hello world",
		},
		{
			name: "assistant with tool calls",
			msg: llm.Message{
				Role:      llm.RoleAssistant,
				Content:   "I will use the Read tool",
				ToolCalls: []llm.ToolCall{{ID: "1", Name: "Read", Arguments: "{}"}},
			},
			want: "I will use the Read tool Read {}",
		},
		{
			name: "tool message",
			msg:  llm.Message{Role: llm.RoleTool, Content: "file contents", ToolCallID: "1"},
			want: "file contents",
		},
		{
			name: "empty message",
			msg:  llm.Message{Role: llm.RoleUser, Content: ""},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMessageText(tt.msg)
			if got != tt.want {
				t.Errorf("extractMessageText() = %q, want %q", got, tt.want)
			}
		})
	}
}

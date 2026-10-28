package output

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/syntax"
	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stderr = w
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stderr pipe: %v", err)
	}
	os.Stderr = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return string(data)
}

func TestReplayMessagesRendersRestoredConversation(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "hello there"},
		{Role: llm.RoleAssistant, Content: "hi! **how** can I help?"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Bash", Arguments: `{"command":"ls"}`}}},
		{Role: llm.RoleTool, ToolCallID: "tc1", Content: "file1\nfile2"},
		{Role: llm.RoleAssistant, Content: "done."},
	}

	got := captureStderr(t, func() { out.ReplayMessages(messages) })

	for _, want := range []string{
		"Resumed session with 5 restored message(s).",
		"hello there",
		"how",
		"Bash",
		"file1",
		"done.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("replay output missing %q; got:\n%s", want, got)
		}
	}
}

func TestHandleLLMUserMessageCreatedRendersPrompt(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	event := eventing.NewLLMUserMessage("session", "parent", 1, llm.Message{
		Role:    llm.RoleUser,
		Content: "show me the repo",
	})

	got := captureStderr(t, func() {
		if err := out.HandleLLMUserMessageCreated(event); err != nil {
			t.Fatalf("HandleLLMUserMessageCreated() error = %v", err)
		}
	})

	if !strings.Contains(got, "You: show me the repo") {
		t.Fatalf("user prompt missing from stderr: %q", got)
	}
}

func TestRenderUserInputRendersSlashCommand(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	got := captureStderr(t, func() {
		out.RenderUserInput("/goal reconcile the statements")
	})

	if !strings.Contains(got, "You: /goal reconcile the statements") {
		t.Fatalf("slash command missing from stderr: %q", got)
	}
}

func TestUserPromptUsesInsetBackgroundInsteadOfCyan(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	out.stderrStyle = NewStyleEnabled(true)
	out.stderrWidth = func() int { return 20 }

	got := captureStderr(t, func() {
		out.renderMessage(llm.Message{Role: llm.RoleUser, Content: "hello"})
	})

	if !strings.Contains(got, "\x1b[48;2;56;59;70m  hello           \x1b[0m") {
		t.Fatalf("user prompt missing padded background row: %q", got)
	}
	if strings.Contains(got, "\x1b[36m") || strings.Contains(got, "You:") {
		t.Fatalf("styled user prompt should not use cyan or a label: %q", got)
	}
}

func TestUserPromptSanitizesPastedTerminalText(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	out.stderrStyle = NewStyleEnabled(true)
	out.stderrWidth = func() int { return 20 }

	got := captureStderr(t, func() {
		out.renderMessage(llm.Message{
			Role:    llm.RoleUser,
			Content: "\tdefer unix.Close(pidfd)\x1b[0m\r\x08",
		})
	})

	if strings.Contains(got, "\t") {
		t.Fatalf("rendered prompt contains a terminal-width-dependent tab: %q", got)
	}
	if strings.Count(got, "\x1b[48;2;56;59;70m") != strings.Count(got, "\x1b[0m") {
		t.Fatalf("pasted ANSI reset escaped the prompt background: %q", got)
	}
	for _, line := range strings.Split(strings.Trim(got, "\n"), "\n") {
		if width := ansi.VisibleWidth(line); width != 19 {
			t.Fatalf("rendered row width = %d, want 19: %q", width, line)
		}
	}
	if stripped := ansi.Strip(got); strings.ContainsAny(stripped, "\r\x08") {
		t.Fatalf("rendered prompt contains pasted terminal controls: %q", got)
	}
}

func TestCompactReasoningShowsActivityWithoutTokens(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	out.EnableReasoningStreaming()
	started := time.Unix(0, 0)
	clockCalls := 0
	out.now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return started
		}
		return started.Add(5200 * time.Millisecond)
	}
	got := captureStderr(t, func() {
		for _, event := range []*types.Event{
			eventing.NewLLMReasoningDelta("session", "parent", 1, "checking ", "", false),
			eventing.NewLLMReasoningDelta("session", "parent", 1, "files", "", false),
			eventing.NewLLMReasoningDelta("session", "parent", 1, "", "checking files", true),
		} {
			if err := out.HandleLLMReasoningDeltaCreated(event); err != nil {
				t.Fatalf("HandleLLMReasoningDeltaCreated() error = %v", err)
			}
		}
	})
	if strings.Count(got, "Thinking…") != 1 || !strings.Contains(got, "Thinking (5.2s)") || strings.Contains(got, "checking") || strings.Contains(got, "files") {
		t.Fatalf("reasoning stream output = %q", got)
	}
}

func TestDetailedReasoningStreamsDeltasAndIgnoresFinalSnapshot(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	out.EnableReasoningStreaming()
	started := time.Unix(0, 0)
	clockCalls := 0
	out.now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return started
		}
		return started.Add(5200 * time.Millisecond)
	}
	got := captureStderr(t, func() {
		for _, event := range []*types.Event{
			eventing.NewLLMReasoningDelta("session", "parent", 1, "checking ", "", false),
			eventing.NewLLMReasoningDelta("session", "parent", 1, "files", "", false),
			eventing.NewLLMReasoningDelta("session", "parent", 1, "", "checking files", true),
		} {
			if err := out.HandleLLMReasoningDeltaCreated(event); err != nil {
				t.Fatalf("HandleLLMReasoningDeltaCreated() error = %v", err)
			}
		}
	})
	if strings.Count(got, "Thinking: ") != 1 || strings.Count(got, "checking files") != 1 || !strings.Contains(got, "(5.2s)\n\n") {
		t.Fatalf("reasoning stream output = %q", got)
	}
}

func TestCompactModeShowsActionsAndCommentaryButHidesNoise(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	event := eventing.NewLLMAgentMessage("session", "parent", 1, llm.Message{
		Role:    llm.RoleAssistant,
		Content: "I’ll inspect the renderer.",
		ToolCalls: []llm.ToolCall{{
			ID:        "read-1",
			Name:      "Read",
			Arguments: `{"file_path":"internal/output/cli.go","offset":1,"limit":50}`,
		}},
	}, llm.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, 120)

	got := captureStderr(t, func() {
		if err := out.HandleLLMAgentMessageCreated(event); err != nil {
			t.Fatalf("HandleLLMAgentMessageCreated() error = %v", err)
		}
	})

	for _, want := range []string{"Read: internal/output/cli.go", "inspect the renderer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact output missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"file_path", "prompt=", "completion="} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("compact output contains noisy detail %q: %q", unwanted, got)
		}
	}
}

func TestCompactModeGroupsToolOnlyIterationsUnderLatestAgentCommentary(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	got := captureStderr(t, func() {
		out.renderMessage(llm.Message{Role: llm.RoleUser, Content: "inspect the output"})
		out.renderMessage(llm.Message{Role: llm.RoleAssistant, Content: "I’ll inspect it.", ToolCalls: []llm.ToolCall{
			{Name: "Read", Arguments: `{"file_path":"internal/output/cli.go"}`},
			{Name: "Grep", Arguments: `{"pattern":"renderCompact","path":"internal/output"}`},
		}})
		out.renderMessage(llm.Message{Role: llm.RoleTool, Content: "hidden result"})
		out.renderMessage(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{Name: "Edit", Arguments: `{"file_path":"internal/output/cli.go"}`},
			{Name: "Edit", Arguments: `{"file_path":"internal/output/cli_test.go"}`},
		}})
		out.renderMessage(llm.Message{Role: llm.RoleTool, Content: "error: edit target not found"})
		out.renderMessage(llm.Message{Role: llm.RoleAssistant, Content: "Now I’ll verify it.", ToolCalls: []llm.ToolCall{
			{Name: "Bash", Arguments: `{"command":"go test ./internal/output"}`},
		}})
	})

	for _, want := range []string{"You: inspect the output", "I’ll inspect it.", "Read: internal/output/cli.go", "Grep:", "Edit: internal/output/cli.go", "Edit: internal/output/cli_test.go", "error: edit target not found", "Now I’ll verify it.", "Bash: go test ./internal/output"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact grouped output missing %q: %q", want, got)
		}
	}
	if gotCount := strings.Count(got, strings.Repeat("─", 64)); gotCount != 2 {
		t.Fatalf("compact separator count = %d, want one before each agent iteration; output: %q", gotCount, got)
	}
	if strings.Contains(got, "hidden result") {
		t.Fatalf("compact output contains tool result: %q", got)
	}
}

func TestCompactModeKeepsInitialToolOnlyIterationUnderUserTurn(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	got := captureStderr(t, func() {
		out.renderMessage(llm.Message{Role: llm.RoleUser, Content: "inspect the output"})
		out.renderMessage(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{Name: "Read", Arguments: `{"file_path":"internal/output/cli.go"}`},
		}})
		out.renderMessage(llm.Message{Role: llm.RoleAssistant, Content: "I found the renderer."})
	})

	if gotCount := strings.Count(got, strings.Repeat("─", 64)); gotCount != 1 {
		t.Fatalf("compact separator count = %d, want one before assistant commentary; output: %q", gotCount, got)
	}
	if strings.Index(got, "Read: internal/output/cli.go") > strings.Index(got, strings.Repeat("─", 64)) {
		t.Fatalf("initial tool-only iteration was not grouped under the user turn: %q", got)
	}
}

func TestCompactModeRendersSemanticTaskProgress(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	got := captureStderr(t, func() {
		out.renderMessage(llm.Message{Role: llm.RoleUser, Content: "implement the feature"})
		out.renderMessage(llm.Message{Role: llm.RoleAssistant, Content: "I’ll work through the implementation."})
		for _, arguments := range []string{
			`{"op":"add","id":"T1","title":"Add compact task output"}`,
			`{"op":"start","id":"T1","next_action":"Update the renderer"}`,
			`{"op":"update","id":"T1","next_action":"Add regression tests"}`,
			`{"op":"update","id":"T1","validation":"Run focused tests"}`,
			`{"op":"done","id":"T1","outcome":"Task progress is visible"}`,
			`{"op":"block","id":"T2","next_action":"Wait for credentials"}`,
			`{"op":"prune_expired","ids":["T1","T2"]}`,
		} {
			out.renderMessage(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{Name: "UpdateTasks", Arguments: arguments}}})
		}
	})

	for _, want := range []string{
		"Task: ○ T1 — Add compact task output",
		"Task: ▶ T1 — Update the renderer",
		"Task: ↻ T1 — Add regression tests",
		"Task: ↻ T1 — Run focused tests",
		"Task: ✓ T1 — Task progress is visible",
		"Task: ■ T2 — Wait for credentials",
		"Task: ⌫ — T1, T2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact task output missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "UpdateTasks") || strings.Contains(got, `"op"`) {
		t.Fatalf("compact task output exposed raw tool details: %q", got)
	}
	if gotCount := strings.Count(got, strings.Repeat("─", 64)); gotCount != 1 {
		t.Fatalf("task updates should remain in the commentary group; separator count = %d, output: %q", gotCount, got)
	}
}

func TestFormatTaskStatsSortsToolsAndCompactsTokens(t *testing.T) {
	got := formatTaskStats(&tasks.Stats{TotalTokens: 12400, PromptTokens: 10800, CachedPromptTokens: 7100, CompletionTokens: 1600, ToolCalls: map[string]int{"Read": 2, "Edit": 3, "Bash": 4}})
	want := "↳ 12.4k tokens (prompt 10.8k, cached 7.1k, completion 1.6k) · tools: Bash×4, Edit×3, Read×2"
	if got != want {
		t.Fatalf("formatTaskStats() = %q, want %q", got, want)
	}
}

func TestCompactGoalCompletionLabelsWorkerUsageAndParentContext(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	event := eventing.NewTaskCompleted("parent", "parent", 0, tasks.Update{Op: tasks.OpDone, ID: "loop-1234-T1", Scope: "loop-1234"}, &tasks.Stats{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
	got := captureStderr(t, func() {
		if err := out.HandleTaskUpdatedCreated(event); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got, "↳ worker attempts: 12 tokens") || !strings.Contains(got, "[parent ctx 0]") || strings.Contains(got, "parent cache") {
		t.Fatalf("goal completion output = %q", got)
	}
}

func TestFormatCommandResultRendersMarkdown(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	out.stderrMarkdown = testRenderer()
	got := out.FormatCommandResult("## Goal awaiting approval\n\n- **T1 — Run benchmark**\n- Approve: `/goal:approve`")
	for _, raw := range []string{"## ", "**", "`"} {
		if strings.Contains(got, raw) {
			t.Fatalf("rendered command result contains %q: %q", raw, got)
		}
	}
	for _, want := range []string{"Goal awaiting approval", "T1 — Run benchmark", "/goal:approve"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered command result missing %q: %q", want, got)
		}
	}
}

func TestCompactModeHidesToolResultButReplaysRecentConversation(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	got := captureStderr(t, func() {
		out.renderMessage(llm.Message{Role: llm.RoleUser, Content: "secret prompt"})
		out.renderMessage(llm.Message{Role: llm.RoleTool, Content: "many noisy result lines"})
		out.ReplayMessages([]llm.Message{
			{Role: llm.RoleUser, Content: "old prompt"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{Name: "Read"}}, Content: "intermediate commentary"},
			{Role: llm.RoleTool, Content: "old noisy tool result"},
			{Role: llm.RoleAssistant, Content: "old final answer"},
		})
	})

	if !strings.Contains(got, "Resumed session with 4 restored message(s).") {
		t.Fatalf("compact replay notice missing: %q", got)
	}
	for _, want := range []string{"Restored conversation:", "You: old prompt", "Assistant:", "old final answer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact replay missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"noisy result", "intermediate commentary"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("compact output contains %q: %q", unwanted, got)
		}
	}
}

func TestMessageRenderersClaimUserAndToolMessages(t *testing.T) {
	render := func(w io.Writer, _ Style, _ int, role llm.Role, toolName, content string) bool {
		if (role == llm.RoleTool && toolName != "Ask") || !strings.HasPrefix(content, "<panel>") {
			return false
		}
		_, _ = io.WriteString(w, "PANEL("+string(role)+")\n")
		return true
	}
	ask := llm.ToolCall{ID: "c1", Name: "Ask"}
	for _, mode := range []Mode{ModeCompact, ModeDetailed} {
		out := NewCLIOutput(mode)
		out.SetMessageRenderers([]MessageRenderer{render})
		got := captureStderr(t, func() {
			out.renderMessage(llm.Message{Role: llm.RoleUser, Content: "<panel>asked"})
			out.renderMessageWithTool(llm.Message{Role: llm.RoleTool, Content: "<panel>answered"}, ask)
			out.renderMessageWithTool(llm.Message{Role: llm.RoleTool, Content: "<panel>spoofed"}, llm.ToolCall{Name: "Read"})
			out.ReplayMessages([]llm.Message{
				{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{ask}},
				{Role: llm.RoleTool, ToolCallID: "c1", Content: "<panel>replayed"},
			})
		})
		// A claimed tool message shows even in compact mode, and on replay.
		if strings.Count(got, "PANEL(user)") != 1 || strings.Count(got, "PANEL(tool)") != 2 {
			t.Fatalf("%s output = %q, want one user and two tool panels", mode, got)
		}
		if strings.Contains(got, "<panel>answered") || strings.Contains(got, "<panel>replayed") {
			t.Fatalf("%s output rendered claimed content itself: %q", mode, got)
		}
	}
}

func TestCompactReplayShowsAllSavedConversation(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "too old"},
		{Role: llm.RoleAssistant, Content: "answer one"},
		{Role: llm.RoleUser, Content: "question two"},
		{Role: llm.RoleAssistant, Content: "answer two"},
		{Role: llm.RoleUser, Content: "question three"},
	}
	got := captureStderr(t, func() { out.ReplayMessages(messages) })
	for _, want := range []string{"too old", "answer one", "question two", "answer two", "question three"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact replay missing %q: %q", want, got)
		}
	}
	if strings.Index(got, "too old") > strings.Index(got, "question three") {
		t.Fatalf("compact replay order changed: %q", got)
	}
}

func TestCompactReplayIncludesEarlierGoalOutcome(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	messages := []llm.Message{
		{Role: llm.RoleAssistant, Content: "Goal complete: 3/3 checks passed"},
		{Role: llm.RoleUser, Content: "what was the goal?"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{Name: "Read"}}},
		{Role: llm.RoleTool, Content: "large state file"},
		{Role: llm.RoleAssistant, Content: "The savings goal passed."},
		{Role: llm.RoleUser, Content: "thanks"},
		{Role: llm.RoleAssistant, Content: "you're welcome"},
	}
	got := captureStderr(t, func() { out.ReplayMessages(messages) })
	for _, want := range []string{"Goal complete: 3/3 checks passed", "what was the goal?", "The savings goal passed.", "thanks", "you're welcome"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact replay missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "large state file") || strings.Index(got, "Goal complete") > strings.Index(got, "what was the goal?") {
		t.Fatalf("compact replay included tool output or changed order: %q", got)
	}
}

func TestCompactModeKeepsToolErrorsAndShortCompactionNotice(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	compaction := eventing.NewCompactionCompleted("session", "manual", "private summary", 1000, 400)
	got := captureStderr(t, func() {
		out.renderMessage(llm.Message{Role: llm.RoleTool, Content: "error: command failed"})
		if err := out.HandleCompactionCompletedCreated(compaction); err != nil {
			t.Fatalf("HandleCompactionCompletedCreated() error = %v", err)
		}
	})

	for _, want := range []string{"error: command failed", "[Compacted context: 1.0k → 400 tokens (60% reduction)]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact output missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"private summary", "1000"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("compact compaction output contains %q: %q", unwanted, got)
		}
	}
}

func TestFormatUsageLineShowsCacheAndContext(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	got := out.formatUsageLine(llm.Usage{
		PromptTokens:       4250,
		CachedPromptTokens: 850,
		CompletionTokens:   319,
		TotalTokens:        4569,
	})

	for _, want := range []string{
		"cache 20%",
		"ctx 0",
		"prompt=4250",
		"cached=850",
		"completion=319",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage line missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"total=4569", "cumulative"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("usage line contains unwanted %q: %q", unwanted, got)
		}
	}
}

func TestFormatContextTokens(t *testing.T) {
	if got := formatContextTokens(5653, 200000); got != "5653/200000 (3%)" {
		t.Fatalf("formatContextTokens() = %q", got)
	}
}

func TestFormatCachePercentClamps(t *testing.T) {
	cases := []struct {
		usage llm.Usage
		want  string
	}{
		{llm.Usage{}, "0%"},
		{llm.Usage{PromptTokens: 100, CachedPromptTokens: 25}, "25%"},
		{llm.Usage{PromptTokens: 100, CachedPromptTokens: 200}, "100%"},
		{llm.Usage{PromptTokens: 100, CachedPromptTokens: -1}, "0%"},
	}
	for _, tc := range cases {
		if got := formatCachePercent(tc.usage); got != tc.want {
			t.Fatalf("formatCachePercent(%+v) = %q, want %q", tc.usage, got, tc.want)
		}
	}
}

func TestReplayMessagesSkipsCompactionSummary(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "original question"},
		{Role: llm.RoleUser, Content: "The conversation history before this point was compacted into the following summary:\n\n<summary>\nlong summary text\n</summary>"},
		{Role: llm.RoleAssistant, Content: "continuing"},
	}

	got := captureStderr(t, func() { out.ReplayMessages(messages) })

	if !strings.Contains(got, "[compacted history summary]") {
		t.Errorf("expected compacted summary marker; got:\n%s", got)
	}
	if strings.Contains(got, "long summary text") {
		t.Errorf("summary body should not be rendered; got:\n%s", got)
	}
	if !strings.Contains(got, "continuing") {
		t.Errorf("message after summary missing; got:\n%s", got)
	}
}

func TestReplayMessagesEmptyIsNoop(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	got := captureStderr(t, func() { out.ReplayMessages(nil) })
	if got != "" {
		t.Errorf("expected no output for empty replay; got:\n%s", got)
	}
}

func TestRenderMessageToolError(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	got := captureStderr(t, func() {
		out.renderMessage(llm.Message{Role: llm.RoleTool, Content: "error: command failed"})
	})
	if !strings.Contains(got, "error: command failed") {
		t.Errorf("expected tool error text; got:\n%s", got)
	}
}

func TestHandleEditResultHighlightsUnifiedDiff(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	out.stderrStyle = NewStyleEnabled(true)
	tool := llm.ToolCall{ID: "edit-1", Name: "Edit", Arguments: `{}`}
	content := "Successfully replaced 1 block(s) in main.go.\n--- original\n+++ modified\n@@ -1 +1 @@\n-old\n+new\n unchanged"
	event := eventing.NewLLMToolCallResult("session", "parent", 1, tool, "", "", llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: tool.ID,
		Content:    content,
	})

	got := captureStderr(t, func() {
		if err := out.HandleLLMToolCallResultCreated(event); err != nil {
			t.Fatalf("HandleLLMToolCallResultCreated() error = %v", err)
		}
	})

	for _, want := range []string{
		"\x1b[31m--- original\x1b[0m",
		"\x1b[32m+++ modified\x1b[0m",
		"\x1b[36m@@ -1 +1 @@\x1b[0m",
		"\x1b[31m-old\x1b[0m",
		"\x1b[32m+new\x1b[0m",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("edit result missing highlighted line %q: %q", want, got)
		}
	}
	if stripped := ansi.Strip(got); !strings.Contains(stripped, content) {
		t.Fatalf("highlighting changed copyable edit output: %q", stripped)
	}
}

func TestEditResultSyntaxHighlightsKnownLanguage(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	out := NewCLIOutput(ModeDetailed)
	out.stderrStyle = NewStyleEnabled(true)
	content := "Successfully replaced 1 block(s) in main.go.\n--- original\n+++ modified\n@@ -1,2 +1,2 @@\n func main() {\n-\tlimit := 10\n+\tlimit := 25\n }"
	tool := llm.ToolCall{ID: "edit-1", Name: "Edit", Arguments: `{"file_path":"cmd/main.go"}`}
	event := eventing.NewLLMToolCallResult("session", "parent", 1, tool, "", "", llm.Message{Role: llm.RoleTool, ToolCallID: tool.ID, Content: content})

	got := captureStderr(t, func() {
		if err := out.HandleLLMToolCallResultCreated(event); err != nil {
			t.Fatal(err)
		}
	})
	theme := syntax.NewTheme(syntax.DepthTrueColor)
	for _, want := range []string{
		"\x1b[31m--- original\x1b[0m",
		theme.AddedBg,
		theme.AddedEmphasis + "m25",
		theme.Foreground(syntax.Keyword) + "mfunc",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("edit result missing %q: %q", want, got)
		}
	}
	if stripped := ansi.Strip(got); !strings.Contains(stripped, strings.ReplaceAll(content, "\t", "    ")) {
		t.Fatalf("highlighting changed the edit text: %q", stripped)
	}
}

func TestReplayMessagesHighlightsEditResult(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	out.stderrStyle = NewStyleEnabled(true)
	messages := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "edit-1", Name: "Edit", Arguments: `{}`}}},
		{Role: llm.RoleTool, ToolCallID: "edit-1", Content: "-old\n+new"},
	}

	got := captureStderr(t, func() { out.ReplayMessages(messages) })
	if !strings.Contains(got, "\x1b[31m-old\x1b[0m") || !strings.Contains(got, "\x1b[32m+new\x1b[0m") {
		t.Fatalf("replayed edit diff was not highlighted: %q", got)
	}
}

func TestHandleCompactionCompletedRendersSummaryMarkdown(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	event := eventing.NewCompactionCompleted("session", "manual", "# Summary\n\n- kept **important** context", 1000, 400)

	got := captureStderr(t, func() {
		if err := out.HandleCompactionCompletedCreated(event); err != nil {
			t.Fatalf("HandleCompactionCompletedCreated() error = %v", err)
		}
	})

	if strings.Contains(got, "# Summary") {
		t.Fatalf("compaction summary was printed as raw markdown: %q", got)
	}
	for _, want := range []string{"Summary", "kept important context"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compaction summary missing rendered content %q: %q", want, got)
		}
	}
}

func TestHandleSubagentCompletedDoesNotPrintResult(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	event := eventing.NewSubagentInvocationCompleted("session", "reviewer", "child-123", "parent-456", "very noisy command result", 100, 200)

	got := captureStderr(t, func() {
		if err := out.HandleSubagentInvocation(event); err != nil {
			t.Fatalf("HandleSubagentInvocation() error = %v", err)
		}
	})

	if !strings.Contains(got, "[Subagent reviewer completed: child-123]") {
		t.Fatalf("subagent completion line missing: %q", got)
	}
	if strings.Contains(got, "very noisy command result") {
		t.Fatalf("subagent result should not be printed: %q", got)
	}
}

func TestReplaySubagentsSuppressesGoalInternals(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	got := captureStderr(t, func() {
		out.ReplaySubagents([]agent.SubagentReplay{{
			Name: "goal-discovery", Task: "Discover checks", Prompt: "inspect the repository\nGoal: verify PR 43\nStatic scan: very noisy details", Result: "checks found",
		}})
	})
	if got != "" {
		t.Fatalf("goal subagent internals were replayed: %q", got)
	}
}

func TestReplayGoalsRendersConciseTimeline(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	got := captureStderr(t, func() {
		out.ReplayGoals([]eventing.GoalEvent{
			{GoalID: "goal-1", Revision: 1, Stage: "created", Status: "planning", Summary: "Goal accepted"},
			{GoalID: "goal-1", Revision: 2, Stage: "planning", Status: "awaiting_approval", Summary: "Plan created with 2 tasks", NextAction: "/goal:approve"},
		})
	})
	for _, want := range []string{"Restored goal", "Setup", "Plan created with 2 tasks", "Status: awaiting_approval", "Next: /goal:approve"} {
		if !strings.Contains(got, want) {
			t.Fatalf("goal timeline missing %q: %q", want, got)
		}
	}
}

func TestReplayGoalsGroupsTaskAttemptsIntoRuns(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	task := func(stage, summary, status string) eventing.GoalEvent {
		return eventing.GoalEvent{GoalID: "goal-ab12", Stage: stage, Status: "running", Summary: summary, TaskID: "goal-ab12-T6", TaskTitle: "Build binary", TaskStatus: status}
	}
	got := captureStderr(t, func() {
		out.ReplayGoals([]eventing.GoalEvent{
			{GoalID: "goal-ab12", Stage: "created", Status: "planning", Goal: "ship it", Summary: "Goal accepted"},
			{Type: eventing.GoalRunStarted, GoalID: "goal-ab12", Stage: "running", Status: "running", Summary: "Goal execution started"},
			task("task", "Attempt 1 started", "running"),
			task("task", "built it", "attempted"),
			task("task-check", "exit 2: Run 'go help' for usage.", "pending"),
			task("task", "Attempt 2 started", "running"),
			task("task", "built it again", "attempted"),
			task("task-check", "exit 0", "done"),
			{GoalID: "goal-ab12", Stage: "replan", Status: "stalled", Summary: "replan cap reached: task goal-ab12-T6 failed", NextAction: "/goal:retry"},
		})
	})
	for _, want := range []string{"Restored goal ab12", "ship it", "Run 1", "T6 Build binary", "(2 attempts)", "#1 ✗ exit 2: Run 'go help' for usage.", "#2 ✓ exit 0", "built it again", "replan cap reached: task T6 failed", "Status: stalled"} {
		if !strings.Contains(got, want) {
			t.Fatalf("grouped timeline missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Attempt 1 started", "goal-ab12-T6", "Goal accepted"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("grouped timeline should fold %q:\n%s", unwanted, got)
		}
	}
}

func TestGoalSubagentCallsHaveDividersAndStats(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	stats := &eventing.SubagentStats{TotalTokens: 1234, PromptTokens: 1000, CachedPromptTokens: 500, CompletionTokens: 234, ToolCalls: map[string]int{"Read": 2}}
	events := []*types.Event{
		eventing.NewSubagentInvocationStarted("session", "goal-worker", "child-1", "parent", "Verify January", 60000),
		eventing.NewSubagentInvocationProgress("session", "goal-worker", "child-1", "parent", `subagent goal-worker tool call Read({"file_path":"jan.csv"})`, llm.Usage{}, 0, 100, 60000),
		eventing.NewSubagentInvocationCompletedWithStats("session", "goal-worker", "child-1", "parent", "private worker result", 100, 60000, stats),
		eventing.NewSubagentInvocationStarted("session", "goal-checker", "child-2", "parent", "Verify January", 60000),
		eventing.NewSubagentInvocationCompletedWithStats("session", "goal-checker", "child-2", "parent", "private checker result", 100, 60000, stats),
	}
	got := captureStderr(t, func() {
		for _, event := range events {
			if err := out.HandleSubagentInvocation(event); err != nil {
				t.Fatal(err)
			}
		}
	})
	for _, want := range []string{"goal-worker · Verify January", "[goal-worker] ▶ Read: jan.csv", "goal-checker · Verify January", "↳ 1.2k tokens", "tools: Read×2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("goal output missing %q: %q", want, got)
		}
	}
	if strings.Count(got, strings.Repeat("─", 48)) != 4 || strings.Count(got, "↳ 1.2k tokens") != 2 {
		t.Fatalf("goal call boundaries or stats missing: %q", got)
	}
	if strings.Contains(got, "private worker result") || strings.Contains(got, "private checker result") {
		t.Fatalf("raw subagent results leaked: %q", got)
	}
}

func TestGoalProgressShowsToolLifecycleWithoutResultContent(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	events := []*types.Event{
		eventing.NewSubagentInvocationProgress("session", "goal-planner", "child", "parent", `subagent goal-planner tool call Bash({"command":"go test ./..."})`, llm.Usage{}, 0, 100, 200),
		eventing.NewSubagentInvocationProgress("session", "goal-planner", "child", "parent", "subagent goal-planner tool result Bash: private test output", llm.Usage{}, 0, 100, 200),
		eventing.NewSubagentInvocationProgress("session", "goal-planner", "child", "parent", `subagent goal-planner tool call Read({"file_path":"broken.go"})`, llm.Usage{}, 0, 100, 200),
		eventing.NewSubagentInvocationProgress("session", "goal-planner", "child", "parent", "subagent goal-planner tool result Read: error: file not found", llm.Usage{}, 0, 100, 200),
	}

	got := captureStderr(t, func() {
		for _, event := range events {
			if err := out.HandleSubagentInvocation(event); err != nil {
				t.Fatal(err)
			}
		}
	})
	for _, want := range []string{
		"[goal-planner] ▶ Bash: go test ./...",
		"[goal-planner] ✓ Bash finished · thinking about the next step…",
		"[goal-planner] ▶ Read: broken.go",
		"[goal-planner] ✗ Read failed · reviewing the error…",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("goal lifecycle output missing %q: %q", want, got)
		}
	}
	for _, hidden := range []string{"private test output", "file not found"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("goal lifecycle leaked tool result %q: %q", hidden, got)
		}
	}
}

func TestSubagentStartShowsTaskLabel(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	event := eventing.NewSubagentInvocationStarted(
		"session", "work-worker", "child-1", "parent",
		"work-e6af53d3-T2: Add subprocess-level CLI integration tests", 60000,
	)
	got := captureStderr(t, func() {
		if err := out.HandleSubagentInvocation(event); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got, "[Subagent work-worker · work-e6af53d3-T2: Add subprocess-level CLI integration tests started]") {
		t.Fatalf("subagent task label missing: %q", got)
	}
}

func TestGoalTaskDoneDoesNotRepeatWorkerStats(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	event := eventing.NewTaskCompleted("session", "parent", 0, tasks.Update{Op: tasks.OpDone, Scope: "goal-123", ID: "task-1"}, &tasks.Stats{TotalTokens: 1234})
	got := captureStderr(t, func() {
		if err := out.HandleTaskUpdatedCreated(event); err != nil {
			t.Fatal(err)
		}
	})
	if got != "" {
		t.Fatalf("goal task stats shown twice: %q", got)
	}
}

func TestHandleSubagentProgressShowsToolCallsOnly(t *testing.T) {
	out := NewCLIOutput(ModeDetailed)
	events := []*types.Event{
		eventing.NewSubagentInvocationProgress("session", "reviewer", "child-123", "parent-456", `subagent reviewer reasoning: thinking through the task`, llm.Usage{}, 0, 100, 200),
		eventing.NewSubagentInvocationProgress("session", "reviewer", "child-123", "parent-456", `subagent reviewer tool call Ls({"path":"app/output"})`, llm.Usage{}, 0, 100, 200),
		eventing.NewSubagentInvocationProgress("session", "reviewer", "child-123", "parent-456", `subagent reviewer tool call Read({"file_path":"app/output/cli.go"})`, llm.Usage{}, 0, 100, 200),
		eventing.NewSubagentInvocationProgress("session", "reviewer", "child-123", "parent-456", `subagent reviewer tool call Bash({"command":"go test ./app/output"})`, llm.Usage{}, 0, 100, 200),
		eventing.NewSubagentInvocationProgress("session", "reviewer", "child-123", "parent-456", `subagent reviewer tool result Bash: noisy output`, llm.Usage{}, 0, 100, 200),
	}

	got := captureStderr(t, func() {
		for _, event := range events {
			if err := out.HandleSubagentInvocation(event); err != nil {
				t.Fatalf("HandleSubagentInvocation() error = %v", err)
			}
		}
	})

	for _, want := range []string{
		"[reviewer] Ls: app/output",
		"[reviewer] Read: app/output/cli.go",
		"[reviewer] Bash: go test ./app/output",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent tool call output missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"thinking through the task", "noisy output", "tool result"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("subagent noisy progress should be hidden (%q): %q", unwanted, got)
		}
	}
}

func TestHandleGoalWorkerProgressSummarizesLongArguments(t *testing.T) {
	out := NewCLIOutput(ModeCompact)
	name := "loop-cc62c8d1-task-6-attempt-6"
	command := "python3 " + strings.Repeat("a", 95)
	message := `subagent ` + name + ` tool call Bash({"command":"` + command + `","timeout":30000})`
	event := eventing.NewSubagentInvocationProgress("session", name, "child-123", "parent-456", message, llm.Usage{}, 0, 100, 200)
	got := captureStderr(t, func() {
		if err := out.HandleSubagentInvocation(event); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got, "[task 6, attempt 6] Bash: "+command) {
		t.Fatalf("worker command was not summarized: %q", got)
	}
	for _, unwanted := range []string{`{"command"`, `"timeout"`, name} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("worker progress includes %q: %q", unwanted, got)
		}
	}
}

func TestSubagentPrefixUsesGenericLabelWithoutSpecificName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "", want: "[subagent]"},
		{name: "subagent", want: "[subagent]"},
		{name: "code-researcher", want: "[code-researcher]"},
	} {
		if got := subagentPrefix(tc.name); got != tc.want {
			t.Errorf("subagentPrefix(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

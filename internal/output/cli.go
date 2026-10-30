package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/syntax"
	"github.com/makkalot/caretz/internal/tasks"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
	"golang.org/x/term"
)

type CLIOutput struct {
	session                  *agent.AgentSession
	mode                     Mode
	compactHasOutput         bool
	reasoningStreaming       bool
	reasoningStarted         bool
	reasoningLineOpen        bool
	reasoningStartedAt       time.Time
	suppressNextCompactBegin bool
	spinner                  *terminalSpinner
	now                      func() time.Time
	latestUsage              llm.Usage
	// Styles are derived per stream so each is enabled independently: the
	// final assistant response is styled for stdout, while activity (user
	// echo, tool calls, tool results, tokens, errors, replay) is styled for
	// stderr.
	stdoutStyle    Style
	stderrStyle    Style
	stdoutMarkdown *MarkdownRenderer
	stderrMarkdown *MarkdownRenderer
	stderrWidth    func() int
	renderers      []MessageRenderer
}

// MessageRenderer draws a user or tool message itself, for content an
// embedder recognises, such as a tagged block its tool returned. toolName is
// the tool that produced a tool message, and empty for a user message; a
// renderer should claim only its own tools' results, since any tool's output
// can contain its tag. It writes to w with style at width columns and reports
// whether it rendered content; otherwise the built-in rendering applies. A
// rendered tool message is shown even in compact mode, which normally hides
// tool output.
type MessageRenderer func(w io.Writer, style Style, width int, role llm.Role, toolName, content string) bool

func NewCLIOutput(mode Mode) *CLIOutput {
	if mode == "" {
		mode = ModeCompact
	}
	stdoutStyle := NewStyle(os.Stdout)
	stderrStyle := NewStyle(os.Stderr)
	return &CLIOutput{
		mode:           mode,
		stdoutStyle:    stdoutStyle,
		stderrStyle:    stderrStyle,
		stdoutMarkdown: NewMarkdownRendererWithWidth(stdoutStyle, terminalWidth(os.Stdout)),
		stderrMarkdown: NewMarkdownRendererWithWidth(stderrStyle, terminalWidth(os.Stderr)),
		stderrWidth:    terminalWidth(os.Stderr),
		now:            time.Now,
	}
}

// EnableReasoningStreaming makes provider reasoning updates visible as they arrive.
func (o *CLIOutput) EnableReasoningStreaming() { o.reasoningStreaming = true }

// SetMessageRenderers installs renderers consulted, in order, before the
// built-in rendering of user and tool messages.
func (o *CLIOutput) SetMessageRenderers(renderers []MessageRenderer) {
	o.renderers = append([]MessageRenderer(nil), renderers...)
}

// renderCustom offers msg to the installed renderers and reports whether one
// rendered it.
func (o *CLIOutput) renderCustom(msg llm.Message, toolName string) bool {
	rendered, ok := o.customRendering(msg, toolName)
	if ok {
		_, _ = fmt.Fprint(os.Stderr, rendered)
	}
	return ok
}

// customRendering returns what the first renderer that claims msg drew.
func (o *CLIOutput) customRendering(msg llm.Message, toolName string) (string, bool) {
	if len(o.renderers) == 0 || (msg.Role != llm.RoleUser && msg.Role != llm.RoleTool) {
		return "", false
	}
	width := 80
	if o.stderrWidth != nil {
		width = o.stderrWidth()
	}
	for _, render := range o.renderers {
		var buf bytes.Buffer
		if render(&buf, o.stderrStyle, width, msg.Role, toolName, msg.Content) {
			return buf.String(), true
		}
	}
	return "", false
}

// FormatCommandResult renders a slash-command result with the same Markdown
// rules and terminal width used for other interactive stderr output.
func (o *CLIOutput) FormatCommandResult(message string) string {
	return o.stderrMarkdown.Render(message)
}

func terminalWidth(file *os.File) func() int {
	return func() int {
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
			return width
		}
		return 80
	}
}

func (o *CLIOutput) RegisterHandlers(agentSession *agent.AgentSession) error {
	o.session = agentSession
	if err := agentSession.OnContextEvent(eventing.LLMUserMessageCreated, o.HandleLLMUserMessageCreated); err != nil {
		return err
	}

	if err := agentSession.OnContextEvent(eventing.LLMAgentMessageCreated, o.HandleLLMAgentMessageCreated); err != nil {
		return err
	}
	if err := agentSession.OnContextEvent(eventing.LLMReasoningDeltaCreated, o.HandleLLMReasoningDeltaCreated); err != nil {
		return err
	}

	if err := agentSession.OnContextEvent(eventing.LLMToolCallResultCreated, o.HandleLLMToolCallResultCreated); err != nil {
		return err
	}

	if err := agentSession.OnContextEvent(eventing.LLMAgentResponseCreated, o.HandleLLMAgentResponseCreated); err != nil {
		return err
	}

	if err := agentSession.OnContextEvent(eventing.LLMRequestRetryScheduled, o.HandleLLMRequestRetryScheduled); err != nil {
		return err
	}

	if err := agentSession.OnContextEvent(eventing.CompactionCompletedCreated, o.HandleCompactionCompletedCreated); err != nil {
		return err
	}
	if err := agentSession.OnContextEvent(eventing.TaskUpdatedCreated, o.HandleTaskUpdatedCreated); err != nil {
		return err
	}
	for _, et := range []string{
		eventing.SubagentInvocationStarted,
		eventing.SubagentInvocationProgress,
		eventing.SubagentInvocationCompleted,
		eventing.SubagentInvocationFailed,
	} {
		if err := agentSession.OnAuditEvent(et, o.HandleSubagentInvocation); err != nil {
			return err
		}
	}
	if err := agentSession.OnAuditEvent(eventing.StatusUpdated, o.HandleStatusUpdated); err != nil {
		return err
	}
	if err := agentSession.OnAuditEvent(agent.LoopGuardJudgedEvent, o.HandleLoopGuardJudged); err != nil {
		return err
	}

	return nil
}

func (o *CLIOutput) HandleStatusUpdated(e *types.Event) error {
	var payload eventing.Status
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", o.stderrStyle.Agent("["+payload.Source+"]"), o.stderrStyle.Muted(truncateLine(payload.Message, 160)))
	return nil
}

func (o *CLIOutput) HandleLLMUserMessageCreated(e *types.Event) error {
	var payload eventing.LLMUserMessage
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}

	payload.Message.Content = agent.StripModeNotice(payload.Message.Content)
	o.renderMessage(payload.Message)
	return nil
}

// RenderUserInput renders submitted input that does not create an LLM user
// message, such as a slash command handled directly by the interactive UI.
func (o *CLIOutput) RenderUserInput(content string) {
	o.renderMessage(llm.Message{Role: llm.RoleUser, Content: content})
}

// RenderCommandProgress shows slash-command milestones while the command runs.
func (o *CLIOutput) RenderCommandProgress(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	first, rest, _ := strings.Cut(message, "\n")
	_, _ = fmt.Fprintln(os.Stderr, o.stderrStyle.Muted(GlyphEllipsis+" "+first))
	if rest != "" {
		_, _ = fmt.Fprintln(os.Stderr, o.progressDetail(rest))
	}
}

// progressDetail colors the detail lines of a multi-line progress message by
// their leading mark, so outcomes stand out from the muted milestone line.
func (o *CLIOutput) progressDetail(detail string) string {
	s := o.stderrStyle
	lines := strings.Split(detail, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, glyph := range []string{GlyphSuccess, GlyphFailure, GlyphWarning, GlyphQuestion, GlyphStopped, GlyphRetry} {
			if strings.HasPrefix(trimmed, glyph) {
				lines[i] = s.MarkText(glyph, line)
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// renderMessage renders one conversation message using the same layout as the
// live event handlers. It is shared by the handlers and by session replay so
// resumed sessions look identical to fresh ones.
func (o *CLIOutput) renderMessage(msg llm.Message) {
	o.renderMessageWithTool(msg, llm.ToolCall{})
}

// renderMessageWithTool renders msg; tool is the call a tool result answers.
func (o *CLIOutput) renderMessageWithTool(msg llm.Message, tool llm.ToolCall) {
	if o.mode == ModeCompact {
		o.renderCompactMessage(msg, tool.Name)
		return
	}
	o.renderDetailedMessage(msg, tool)
}

func (o *CLIOutput) renderDetailedMessage(msg llm.Message, tool llm.ToolCall) {
	toolName := tool.Name
	switch msg.Role {
	case llm.RoleUser:
		if isCompactionSummary(msg.Content) {
			_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted("[compacted history summary]"))
			return
		}
		if strings.TrimSpace(msg.Content) == "" || o.renderCustom(msg, "") {
			return
		}
		o.renderUserPrompt(msg.Content)
	case llm.RoleAssistant:
		_, _ = fmt.Fprintln(os.Stderr)
		for _, tc := range msg.ToolCalls {
			_, _ = fmt.Fprintf(os.Stderr, "%s %s(%s)\n", GlyphTool, o.stderrStyle.ToolName(tc.Name), o.stderrStyle.Muted(truncateRunes(tc.Arguments, 80)))
		}
		if strings.TrimSpace(msg.Content) != "" {
			_, _ = fmt.Fprint(os.Stderr, o.stderrMarkdown.Render(msg.Content))
			_, _ = fmt.Fprintln(os.Stderr)
		}
	case llm.RoleTool:
		if strings.HasPrefix(msg.Content, "error: ") {
			_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", GlyphResult, o.stderrStyle.Error(msg.Content))
			return
		}
		if o.renderCustom(msg, toolName) {
			return
		}
		preview := msg.Content
		previewLines := strings.Split(preview, "\n")
		if len(previewLines) > 20 {
			preview = strings.Join(previewLines[:20], "\n") + "\n... [truncated]"
		}
		if strings.EqualFold(toolName, "Edit") {
			preview = o.renderEditResult(preview, editedPath(tool))
		} else {
			preview = o.stderrStyle.Muted(preview)
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", GlyphResult, preview)
	}
}

func (o *CLIOutput) renderCompactMessage(msg llm.Message, toolName string) {
	switch msg.Role {
	case llm.RoleUser:
		if isCompactionSummary(msg.Content) || strings.TrimSpace(msg.Content) == "" {
			return
		}
		o.beginCompactBlock()
		if o.renderCustom(msg, "") {
			return
		}
		o.renderCompactUser(msg.Content)
	case llm.RoleAssistant:
		if len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Content) == "" {
			return
		}
		if strings.TrimSpace(msg.Content) != "" {
			o.beginCompactBlock()
			_, _ = fmt.Fprint(os.Stderr, o.stderrMarkdown.Render(msg.Content))
			_, _ = fmt.Fprintln(os.Stderr)
		}
		for _, tc := range msg.ToolCalls {
			o.compactHasOutput = true
			name := compactToolName(tc.Name)
			detail := summarizeToolArgs(tc.Name, tc.Arguments)
			detail = truncateLine(detail, 120)
			if detail == "" {
				_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", GlyphTool, o.stderrStyle.ToolName(name))
				continue
			}
			_, _ = fmt.Fprintf(os.Stderr, "%s %s: %s\n", GlyphTool, o.stderrStyle.ToolName(name), o.stderrStyle.Muted(detail))
		}
	case llm.RoleTool:
		if strings.HasPrefix(msg.Content, "error: ") {
			_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", GlyphResult, o.stderrStyle.Error(msg.Content))
			return
		}
		// Compact mode hides tool output, except what a renderer claims.
		o.renderCustom(msg, toolName)
	}
}

func (o *CLIOutput) beginCompactBlock() {
	if o.suppressNextCompactBegin {
		o.suppressNextCompactBegin = false
		o.compactHasOutput = true
		return
	}
	if o.compactHasOutput {
		_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Rule(RuleWidth))
	}
	o.compactHasOutput = true
}

func (o *CLIOutput) renderCompactUser(content string) {
	o.renderUserPrompt(content)
}

func (o *CLIOutput) renderUserPrompt(content string) {
	if !o.stderrStyle.Enabled() {
		_, _ = fmt.Fprintf(os.Stderr, "You: %s\n", content)
		return
	}
	content = terminalSafePromptText(content)

	width := 80
	if o.stderrWidth != nil {
		width = o.stderrWidth()
	}
	blockWidth := max(width-2, 8)
	innerWidth := max(blockWidth-4, 1)
	blank := o.stderrStyle.PromptCard(strings.Repeat(" ", blockWidth))
	_, _ = fmt.Fprintf(os.Stderr, "\n %s\n", blank)
	for _, sourceLine := range strings.Split(content, "\n") {
		for _, line := range wrapTableCell(sourceLine, innerWidth) {
			row := "  " + line + strings.Repeat(" ", innerWidth-ansi.VisibleWidth(line)) + "  "
			_, _ = fmt.Fprintf(os.Stderr, " %s\n", o.stderrStyle.PromptCard(row))
		}
	}
	_, _ = fmt.Fprintf(os.Stderr, " %s\n", blank)
}

func terminalSafePromptText(content string) string {
	content = ansi.Strip(content)
	var rendered strings.Builder
	for _, r := range content {
		switch r {
		case '\n':
			rendered.WriteRune(r)
		case '\t':
			// Tabs expand relative to the terminal cursor, so keeping them would
			// make the card wider than the width used for wrapping and padding.
			rendered.WriteString("    ")
		default:
			if !unicode.IsControl(r) {
				rendered.WriteRune(r)
			}
		}
	}
	return rendered.String()
}

// truncateLine collapses whitespace and truncates value to max runes.
func truncateLine(value string, max int) string {
	return truncateRunes(strings.Join(strings.Fields(value), " "), max)
}

// truncateRunes shortens value to at most max runes, ending in "...", without
// splitting a multi-byte character.
func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-3]) + "..."
}

// renderEditResult colors the Edit tool's diff. When the edited file's
// language is known, hunk lines are syntax highlighted like /review shows
// them; otherwise they are plain green and red.
func (o *CLIOutput) renderEditResult(result, path string) string {
	lines := strings.Split(result, "\n")
	lang := syntax.Detect(path, "")
	if lang == nil || !o.stderrStyle.Enabled() {
		for i, line := range lines {
			lines[i] = o.renderEditLine(line)
		}
		return strings.Join(lines, "\n")
	}
	theme := syntax.NewTheme(syntax.DepthFromEnv(os.Getenv))
	for i := 0; i < len(lines); {
		inHunk := strings.HasPrefix(lines[i], "@@")
		lines[i] = o.renderEditLine(lines[i])
		i++
		if !inHunk {
			continue
		}
		start := i
		var body []syntax.DiffLine
		for ; i < len(lines) && lines[i] != ""; i++ {
			kind, ok := map[byte]syntax.DiffKind{' ': syntax.DiffContext, '+': syntax.DiffAdded, '-': syntax.DiffRemoved}[lines[i][0]]
			if !ok {
				break
			}
			body = append(body, syntax.DiffLine{Kind: kind, Text: lines[i][1:]})
		}
		toks, spans := syntax.HighlightHunk(lang, body)
		for k, line := range body {
			if line.Kind == syntax.DiffContext {
				lines[start+k] = " " + syntax.RenderLine(toks[k], theme, syntax.RenderOptions{Dim: true})
				continue
			}
			lines[start+k] = syntax.RenderDiffLine(theme, line.Kind, toks[k], spans[k], 0)
		}
	}
	return strings.Join(lines, "\n")
}

func (o *CLIOutput) renderEditLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+"):
		return o.stderrStyle.DiffAdd(line)
	case strings.HasPrefix(line, "-"):
		return o.stderrStyle.DiffDel(line)
	case strings.HasPrefix(line, "@@"):
		return o.stderrStyle.DiffHunk(line)
	default:
		return o.stderrStyle.Muted(line)
	}
}

// editedPath is the file an Edit tool call changed, empty when unknown.
func editedPath(tool llm.ToolCall) string {
	var args struct {
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal([]byte(tool.Arguments), &args) != nil {
		return ""
	}
	return args.FilePath
}

func isCompactionSummary(content string) bool {
	return strings.Contains(content, "was compacted into the following summary:")
}

// ReplayMessages renders restored session context once, so opening a session
// with -s shows the existing conversation before the first prompt.
func (o *CLIOutput) ReplayMessages(messages []llm.Message) {
	if len(messages) == 0 {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted(fmt.Sprintf("Resumed session with %d restored message(s).", len(messages))))
	messages = append([]llm.Message(nil), messages...)
	for i := range messages {
		if messages[i].Role == llm.RoleUser {
			messages[i].Content = agent.StripModeNotice(messages[i].Content)
		}
	}
	if o.mode == ModeCompact {
		o.replayCompactConversation(messages)
		return
	}
	toolCalls := make(map[string]llm.ToolCall)
	for _, msg := range messages {
		for _, toolCall := range msg.ToolCalls {
			toolCalls[toolCall.ID] = toolCall
		}
		o.renderMessageWithTool(msg, toolCalls[msg.ToolCallID])
	}
}

// ReplaySubagents restores only the durable endpoints of prior subagent runs.
// Live reasoning, tool calls, and tool results intentionally remain ephemeral.
func (o *CLIOutput) ReplaySubagents(runs []agent.SubagentReplay) {
	visible := make([]agent.SubagentReplay, 0, len(runs))
	for _, run := range runs {
		if !strings.HasPrefix(run.Name, "goal-") && !strings.HasPrefix(run.Name, "work-") {
			visible = append(visible, run)
		}
	}
	if len(visible) == 0 {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted("Restored subagent activity:"))
	for i, run := range visible {
		if i > 0 {
			_, _ = fmt.Fprintln(os.Stderr, o.stderrStyle.Rule(RuleWidth))
		}
		label := run.Name
		if strings.TrimSpace(run.Task) != "" {
			label += " · " + truncateLine(run.Task, 100)
		}
		_, _ = fmt.Fprintln(os.Stderr, o.stderrStyle.Heading(label))
		if prompt := replaySubagentPrompt(run); prompt != "" {
			_, _ = fmt.Fprintln(os.Stderr, o.stderrStyle.Muted("Prompt:"))
			_, _ = fmt.Fprint(os.Stderr, o.stderrMarkdown.Render(prompt))
			_, _ = fmt.Fprintln(os.Stderr)
		}
		if result := strings.TrimSpace(run.Result); result != "" {
			_, _ = fmt.Fprintln(os.Stderr, o.stderrStyle.Muted("Final response:"))
			_, _ = fmt.Fprint(os.Stderr, o.stderrMarkdown.Render(boundedReplayText(result, 1200)))
			_, _ = fmt.Fprintln(os.Stderr)
		} else if failure := strings.TrimSpace(run.Error); failure != "" {
			_, _ = fmt.Fprintln(os.Stderr, o.stderrStyle.Error(subagentFailure(failure)))
		}
	}
}

func replaySubagentPrompt(run agent.SubagentReplay) string {
	prompt := strings.TrimSpace(run.Prompt)
	if !strings.HasPrefix(run.Name, "goal-") && !strings.HasPrefix(run.Name, "work-") {
		return boundedReplayText(prompt, 1200)
	}
	fields := make([]string, 0, 4)
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"Goal:", "Task:", "Next action:", "Validation criteria:"} {
			if strings.HasPrefix(line, prefix) {
				fields = append(fields, line)
				break
			}
		}
	}
	if len(fields) > 0 {
		return boundedReplayText(strings.Join(fields, "\n"), 1200)
	}
	if task := strings.TrimSpace(run.Task); task != "" {
		return task
	}
	return boundedReplayText(prompt, 1200)
}

func boundedReplayText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "\n\n… truncated; inspect the subagent session for full details"
}

func (o *CLIOutput) replayCompactConversation(messages []llm.Message) {
	type replayed struct {
		msg      llm.Message
		rendered string
		custom   bool
	}
	visible := make([]replayed, 0, len(messages))
	toolNames := make(map[string]string)
	for _, msg := range messages {
		for _, toolCall := range msg.ToolCalls {
			toolNames[toolCall.ID] = toolCall.Name
		}
		if strings.TrimSpace(msg.Content) == "" || isCompactionSummary(msg.Content) {
			continue
		}
		if rendered, ok := o.customRendering(msg, toolNames[msg.ToolCallID]); ok {
			visible = append(visible, replayed{msg: msg, rendered: rendered, custom: true})
			continue
		}
		if (msg.Role == llm.RoleAssistant && len(msg.ToolCalls) == 0) || msg.Role == llm.RoleUser {
			visible = append(visible, replayed{msg: msg})
		}
	}
	if len(visible) == 0 {
		return
	}

	_, _ = fmt.Fprintf(os.Stderr, "%s\n", o.stderrStyle.Muted("Restored conversation:"))
	for i, item := range visible {
		if i != 0 {
			_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Rule(RuleWidth))
		}
		if item.custom {
			_, _ = fmt.Fprint(os.Stderr, item.rendered)
			continue
		}
		msg := item.msg
		switch msg.Role {
		case llm.RoleUser:
			_, _ = fmt.Fprintln(os.Stderr)
			o.renderCompactUser(msg.Content)
		case llm.RoleAssistant:
			_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted("Assistant:"))
			_, _ = fmt.Fprint(os.Stderr, o.stderrMarkdown.Render(msg.Content))
			_, _ = fmt.Fprintln(os.Stderr)
		}
	}
	o.compactHasOutput = true
}

func (o *CLIOutput) HandleLLMAgentMessageCreated(e *types.Event) error {
	var payload eventing.LLMAgentMessage
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	o.latestUsage = payload.Usage

	if o.mode == ModeDetailed && payload.Usage.TotalTokens > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted("["+o.formatUsageLine(payload.Usage)+"]"))
	}

	o.finishReasoningStream()
	o.renderMessage(payload.Message)
	return nil
}

func (o *CLIOutput) HandleLLMReasoningDeltaCreated(e *types.Event) error {
	var payload eventing.LLMReasoningDelta
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	if !o.reasoningStreaming {
		return nil
	}
	if payload.Started {
		o.beginReasoningStream()
	}
	if payload.Delta != "" {
		o.beginReasoningStream()
		if o.mode == ModeDetailed {
			o.stopThinkingSpinner()
			if !o.reasoningLineOpen {
				_, _ = fmt.Fprint(os.Stderr, o.stderrStyle.Muted("Thinking: "))
				o.reasoningLineOpen = true
			}
			_, _ = fmt.Fprint(os.Stderr, o.stderrStyle.Muted(payload.Delta))
		}
	}
	if payload.Done {
		if o.mode == ModeCompact && !o.reasoningStarted && payload.Text != "" {
			o.beginReasoningStream()
		}
		if o.mode == ModeDetailed && !o.reasoningLineOpen && payload.Text != "" {
			o.beginReasoningStream()
			o.stopThinkingSpinner()
			_, _ = fmt.Fprint(os.Stderr, o.stderrStyle.Muted("Thinking: "))
			o.reasoningLineOpen = true
			_, _ = fmt.Fprint(os.Stderr, o.stderrStyle.Muted(payload.Text))
		}
		o.finishReasoningStream()
	}
	return nil
}

func (o *CLIOutput) beginReasoningStream() {
	if o.reasoningStarted {
		return
	}
	if o.mode == ModeCompact {
		o.beginCompactBlock()
	}
	o.reasoningStarted = true
	o.reasoningStartedAt = o.now()
	_, _ = fmt.Fprintln(os.Stderr)
	if o.stderrStyle.Enabled() {
		o.spinner = startTerminalSpinner(os.Stderr, o.stderrStyle)
	} else {
		_, _ = fmt.Fprintln(os.Stderr, "Thinking…")
	}
}

func (o *CLIOutput) finishReasoningStream() bool {
	if !o.reasoningStarted {
		return false
	}
	o.stopThinkingSpinner()
	duration := formatThinkingDuration(o.now().Sub(o.reasoningStartedAt))
	if o.reasoningLineOpen {
		_, _ = fmt.Fprintf(os.Stderr, " %s\n\n", o.stderrStyle.Muted("("+duration+")"))
	} else {
		_, _ = fmt.Fprintf(os.Stderr, "%s\n\n", o.stderrStyle.Muted("Thinking ("+duration+")"))
	}
	o.reasoningStarted = false
	o.reasoningLineOpen = false
	if o.mode == ModeCompact {
		o.suppressNextCompactBegin = true
	}
	return true
}

func formatThinkingDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	return duration.Round(100 * time.Millisecond).String()
}

func (o *CLIOutput) stopThinkingSpinner() {
	if o.spinner == nil {
		return
	}
	o.spinner.Stop(os.Stderr)
	o.spinner = nil
}

func (o *CLIOutput) HandleLLMToolCallResultCreated(e *types.Event) error {
	var payload eventing.LLMToolCallResult
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}

	o.renderMessageWithTool(payload.Message, payload.Tool)
	return nil
}

func (o *CLIOutput) HandleLLMAgentResponseCreated(e *types.Event) error {
	var payload eventing.LLMAgentResponse
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	o.latestUsage = payload.Usage
	o.finishReasoningStream()

	if o.mode == ModeDetailed && payload.Usage.TotalTokens > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted("["+o.formatUsageLine(payload.Usage)+"]"))
	}
	if o.mode == ModeCompact {
		o.beginCompactBlock()
	}

	fmt.Print(o.stdoutMarkdown.Render(payload.Message.Content))
	fmt.Println()
	if o.mode == ModeCompact && payload.CommandOutcomeKey == "" {
		o.renderContextStatus(payload.Usage)
	}

	return nil
}

func (o *CLIOutput) HandleTaskUpdatedCreated(e *types.Event) error {
	if o.mode != ModeCompact {
		return nil
	}
	var payload eventing.TaskUpdated
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	if payload.Update.Op != "done" {
		return nil
	}
	if strings.HasPrefix(payload.Update.Scope, "goal-") {
		// Goal shows each child's stats when that call finishes, before batch checks.
		return nil
	}
	if payload.Stats != nil {
		line := formatTaskStats(payload.Stats)
		if strings.HasPrefix(payload.Update.Scope, "loop-") {
			line = strings.Replace(line, "↳ ", "↳ worker attempts: ", 1)
		}
		_, _ = fmt.Fprintf(os.Stderr, "  %s\n", o.stderrStyle.Muted(line))
	}
	if strings.HasPrefix(payload.Update.Scope, "loop-") {
		o.renderParentContextStatus()
	} else {
		o.renderContextStatus(o.latestUsage)
	}
	return nil
}

func (o *CLIOutput) renderContextStatus(usage llm.Usage) {
	current, window := 0, 0
	if o.session != nil {
		current, window = o.session.EstimatedContextTokens(), o.session.ContextWindow()
	}
	_, _ = fmt.Fprintf(os.Stderr, "  %s\n", o.stderrStyle.Muted(fmt.Sprintf("[ctx %s | cache %s]", formatContextTokens(current, window), formatCachePercent(usage))))
}

func (o *CLIOutput) renderParentContextStatus() {
	current, window := 0, 0
	if o.session != nil {
		current, window = o.session.EstimatedContextTokens(), o.session.ContextWindow()
	}
	_, _ = fmt.Fprintf(os.Stderr, "  %s\n", o.stderrStyle.Muted(fmt.Sprintf("[parent ctx %s]", formatContextTokens(current, window))))
}

func formatTaskStats(stats *tasks.Stats) string {
	line := fmt.Sprintf("↳ %s tokens (prompt %s, cached %s, completion %s)", compactTokenCount(stats.TotalTokens), compactTokenCount(stats.PromptTokens), compactTokenCount(stats.CachedPromptTokens), compactTokenCount(stats.CompletionTokens))
	names := make([]string, 0, len(stats.ToolCalls))
	for name := range stats.ToolCalls {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if stats.ToolCalls[names[i]] == stats.ToolCalls[names[j]] {
			return names[i] < names[j]
		}
		return stats.ToolCalls[names[i]] > stats.ToolCalls[names[j]]
	})
	if len(names) > 0 {
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s×%d", name, stats.ToolCalls[name]))
		}
		line += " · tools: " + strings.Join(parts, ", ")
	}
	return line
}

// CompactTokenCount formats a token count as 950 or 42.0k.
func CompactTokenCount(tokens int64) string {
	return compactTokenCount(tokens)
}

// CachePercent formats the cached share of a response's prompt tokens.
func CachePercent(usage llm.Usage) string {
	return formatCachePercent(usage)
}

// LatestUsage returns the usage of the most recent agent response, or zero
// before the first response.
func (o *CLIOutput) LatestUsage() llm.Usage {
	return o.latestUsage
}

func compactTokenCount(tokens int64) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	return fmt.Sprintf("%.1fk", float64(tokens)/1000)
}

func (o *CLIOutput) HandleLLMRequestRetryScheduled(e *types.Event) error {
	var payload eventing.LLMRequestRetry
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted("["+payload.Message+"]"))
	return nil
}

func (o *CLIOutput) HandleCompactionCompletedCreated(e *types.Event) error {
	var payload eventing.CompactionCompleted
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}

	ctxTokens := payload.AfterTokens
	if o.mode == ModeCompact {
		notice := fmt.Sprintf("[Compacted context: %s → %s tokens]", compactTokenCount(int64(payload.BeforeTokens)), compactTokenCount(int64(payload.AfterTokens)))
		if payload.BeforeTokens > 0 && payload.AfterTokens <= payload.BeforeTokens {
			reduction := float64(payload.BeforeTokens-payload.AfterTokens) / float64(payload.BeforeTokens) * 100
			notice = fmt.Sprintf("[Compacted context: %s → %s tokens (%.0f%% reduction)]", compactTokenCount(int64(payload.BeforeTokens)), compactTokenCount(int64(payload.AfterTokens)), reduction)
		}
		_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted(notice))
		return nil
	}
	if o.session != nil {
		ctxTokens = o.session.EstimatedContextTokens()
	}
	_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Muted(fmt.Sprintf("[Compacted context: %d %s %d tokens | context: %d]", payload.BeforeTokens, GlyphArrow, payload.AfterTokens, ctxTokens)))
	if payload.Summary != "" {
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", o.stderrMarkdown.Render(payload.Summary))
	}
	return nil
}

func (o *CLIOutput) formatUsageLine(usage llm.Usage) string {
	ctxTokens := 0
	ctxWindow := 0
	if o.session != nil {
		ctxTokens = o.session.EstimatedContextTokens()
		ctxWindow = o.session.ContextWindow()
	}
	return fmt.Sprintf("cache %s | ctx %s | turn prompt=%d cached=%d completion=%d",
		formatCachePercent(usage),
		formatContextTokens(ctxTokens, ctxWindow),
		usage.PromptTokens,
		usage.CachedPromptTokens,
		usage.CompletionTokens)
}

func formatContextTokens(current, max int) string {
	if max <= 0 {
		return fmt.Sprintf("%d", current)
	}
	pct := float64(current) / float64(max) * 100
	return fmt.Sprintf("%d/%d (%.0f%%)", current, max, pct)
}

func formatCachePercent(usage llm.Usage) string {
	if usage.PromptTokens <= 0 {
		return "0%"
	}
	pct := float64(usage.CachedPromptTokens) / float64(usage.PromptTokens) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%.0f%%", pct)
}

func (o *CLIOutput) HandleSubagentInvocation(e *types.Event) error {
	var payload eventing.SubagentInvocation
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	if strings.HasPrefix(payload.Name, "goal-") && e.EventType != eventing.SubagentInvocationProgress {
		o.renderGoalInvocation(e.EventType, payload)
		return nil
	}
	if strings.HasPrefix(payload.Name, "goal-") && e.EventType == eventing.SubagentInvocationProgress {
		o.renderGoalProgress(payload)
		return nil
	}
	switch e.EventType {
	case eventing.SubagentInvocationStarted:
		label := payload.Name
		if task := strings.TrimSpace(payload.Task); task != "" {
			label += " · " + truncateLine(task, 100)
		}
		if o.mode == ModeCompact {
			_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Accent(fmt.Sprintf("[Subagent %s started]", label)))
			return nil
		}
		_, _ = fmt.Fprintf(os.Stderr, "\n%s\n", o.stderrStyle.Accent(fmt.Sprintf("[Subagent %s started: %s]", label, payload.ChildSessionID)))
	case eventing.SubagentInvocationProgress:
		if toolName, detail, ok := formatSubagentToolCallProgress(payload.Name, payload.Message); ok {
			prefix := o.stderrStyle.Agent(subagentPrefix(payload.Name))
			styledName := o.stderrStyle.ToolName(compactToolName(toolName))
			if detail == "" {
				_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", prefix, styledName)
				return nil
			}
			_, _ = fmt.Fprintf(os.Stderr, "%s %s: %s\n", prefix, styledName, o.stderrStyle.Muted(truncateLine(detail, 120)))
		}
	case eventing.SubagentInvocationCompleted:
		if o.mode == ModeCompact {
			_, _ = fmt.Fprintf(os.Stderr, "%s\n", o.stderrStyle.Accent(fmt.Sprintf("[Subagent %s completed]", payload.Name)))
			return nil
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", o.stderrStyle.Accent(fmt.Sprintf("[Subagent %s completed: %s]", payload.Name, payload.ChildSessionID)))
	case eventing.SubagentInvocationFailed:
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", o.stderrStyle.Error(fmt.Sprintf("[Subagent %s failed: %s]", payload.Name, payload.Error)))
	}
	return nil
}

// subagentFailure is the one wording for a failed subagent run in block
// layouts (goal children and replayed subagents).
func subagentFailure(reason string) string {
	return GlyphFailure + " failed: " + reason
}

func (o *CLIOutput) renderGoalProgress(payload eventing.SubagentInvocation) {
	prefix := o.stderrStyle.Agent(subagentPrefix(payload.Name))
	if toolName, detail, ok := formatSubagentToolCallProgress(payload.Name, payload.Message); ok {
		styledName := o.stderrStyle.ToolName(compactToolName(toolName))
		running := o.stderrStyle.Mark(GlyphRunning)
		if detail == "" {
			_, _ = fmt.Fprintf(os.Stderr, "%s %s %s\n", prefix, running, styledName)
			return
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s %s %s: %s\n", prefix, running, styledName, o.stderrStyle.Muted(truncateLine(detail, 120)))
		return
	}
	toolName, failed, ok := formatSubagentToolResultProgress(payload.Name, payload.Message)
	if !ok {
		return
	}
	styledName := o.stderrStyle.Bold(compactToolName(toolName))
	if failed {
		_, _ = fmt.Fprintf(os.Stderr, "%s %s %s %s\n", prefix, o.stderrStyle.Mark(GlyphFailure), styledName, o.stderrStyle.Muted("failed · reviewing the error"+GlyphEllipsis))
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s %s %s %s\n", prefix, o.stderrStyle.Mark(GlyphSuccess), styledName, o.stderrStyle.Muted("finished · thinking about the next step"+GlyphEllipsis))
}

func (o *CLIOutput) renderGoalInvocation(eventType string, payload eventing.SubagentInvocation) {
	rule := o.stderrStyle.Rule(RuleWidth)
	switch eventType {
	case eventing.SubagentInvocationStarted:
		label := strings.TrimSpace(payload.Task)
		if label == "" {
			label = payload.Name
		} else {
			label = payload.Name + " · " + truncateLine(label, 80)
		}
		_, _ = fmt.Fprintf(os.Stderr, "\n%s\n%s\n", rule, o.stderrStyle.Heading(label))
	case eventing.SubagentInvocationCompleted, eventing.SubagentInvocationFailed:
		if payload.Stats != nil {
			stats := &tasks.Stats{
				PromptTokens: payload.Stats.PromptTokens, CachedPromptTokens: payload.Stats.CachedPromptTokens,
				CompletionTokens: payload.Stats.CompletionTokens, TotalTokens: payload.Stats.TotalTokens,
				ToolCalls: payload.Stats.ToolCalls,
			}
			_, _ = fmt.Fprintf(os.Stderr, "  %s\n", o.stderrStyle.Muted(formatTaskStats(stats)))
		}
		if eventType == eventing.SubagentInvocationFailed {
			_, _ = fmt.Fprintln(os.Stderr, "  "+o.stderrStyle.Error(subagentFailure(truncateLine(strings.TrimSpace(payload.Error), 160))))
		}
		_, _ = fmt.Fprintln(os.Stderr, rule)
	}
}

func subagentPrefix(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "subagent") {
		return "[subagent]"
	}
	if strings.HasPrefix(name, "loop-") {
		if _, taskAttempt, ok := strings.Cut(name, "-task-"); ok {
			if task, attempt, ok := strings.Cut(taskAttempt, "-attempt-"); ok && task != "" && attempt != "" {
				return "[task " + task + ", attempt " + attempt + "]"
			}
		}
	}
	return "[" + name + "]"
}

func formatSubagentToolCallProgress(name, message string) (toolName, detail string, ok bool) {
	const marker = " tool call "
	rest, ok := strings.CutPrefix(message, "subagent "+name+marker)
	if !ok {
		return "", "", false
	}

	open := strings.Index(rest, "(")
	close := strings.LastIndex(rest, ")")
	if open <= 0 || close < open {
		return strings.TrimSpace(rest), "", true
	}

	toolName = strings.TrimSpace(rest[:open])
	args := strings.TrimSpace(rest[open+1 : close])
	detail = summarizeToolArgs(toolName, args)
	return toolName, detail, true
}

func formatSubagentToolResultProgress(name, message string) (toolName string, failed, ok bool) {
	const marker = " tool result "
	rest, ok := strings.CutPrefix(message, "subagent "+name+marker)
	if !ok {
		return "", false, false
	}
	toolName, detail, found := strings.Cut(rest, ":")
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return "", false, false
	}
	if !found {
		return toolName, false, true
	}
	return toolName, strings.HasPrefix(strings.TrimSpace(detail), "error:"), true
}

func summarizeToolArgs(toolName, args string) string {
	fields := map[string]any{}
	if err := json.Unmarshal([]byte(args), &fields); err != nil {
		return args
	}

	switch toolName {
	case "Bash":
		return jsonString(fields, "command")
	case "Read":
		return jsonString(fields, "file_path")
	case "Ls":
		if path := jsonString(fields, "path"); path != "" {
			return path
		}
		return "."
	case "Grep":
		pattern := jsonString(fields, "pattern")
		path := jsonString(fields, "path")
		glob := jsonString(fields, "glob")
		switch {
		case pattern != "" && path != "" && glob != "":
			return fmt.Sprintf("%q in %s (%s)", pattern, path, glob)
		case pattern != "" && path != "":
			return fmt.Sprintf("%q in %s", pattern, path)
		case pattern != "" && glob != "":
			return fmt.Sprintf("%q in %s", pattern, glob)
		default:
			return pattern
		}
	case "Find":
		pattern := jsonString(fields, "pattern")
		path := jsonString(fields, "path")
		if pattern != "" && path != "" {
			return fmt.Sprintf("%s in %s", pattern, path)
		}
		return pattern
	case "Edit", "Write", "SendFile", "LocalImage":
		return jsonString(fields, "file_path")
	case "WebFetch":
		return jsonString(fields, "url")
	case "WebSearch":
		return jsonString(fields, "query")
	case "UpdateTasks":
		return summarizeTaskUpdate(fields)
	default:
		return args
	}
}

func compactToolName(toolName string) string {
	if toolName == "UpdateTasks" {
		return "Task"
	}
	return toolName
}

func summarizeTaskUpdate(fields map[string]any) string {
	op := jsonString(fields, "op")
	id := jsonString(fields, "id")
	var marker, detail string
	switch op {
	case "add":
		marker, detail = GlyphPending, jsonString(fields, "title")
	case "start":
		marker, detail = GlyphRunning, jsonString(fields, "next_action")
	case "update":
		marker, detail = GlyphRetry, jsonString(fields, "next_action")
		if detail == "" {
			detail = jsonString(fields, "validation")
		}
	case "done":
		marker, detail = GlyphSuccess, jsonString(fields, "outcome")
	case "block":
		marker, detail = GlyphStopped, jsonString(fields, "next_action")
	case "prune_expired":
		marker, detail = "⌫", strings.Join(jsonStrings(fields, "ids"), ", ")
	default:
		return ""
	}

	summary := strings.TrimSpace(strings.Join([]string{marker, id}, " "))
	if detail != "" {
		summary += " — " + detail
	}
	return summary
}

func jsonString(fields map[string]any, key string) string {
	value, ok := fields[key]
	if !ok {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func jsonStrings(fields map[string]any, key string) []string {
	values, ok := fields[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			result = append(result, strings.TrimSpace(s))
		}
	}
	return result
}

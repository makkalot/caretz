package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/turnscope"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/progress"
	"github.com/makkalot/caretz/lib/types"
)

type acpMockProvider struct {
	mu            sync.Mutex
	calls         int
	systemPrompts []string
}

func (p *acpMockProvider) Chat(systemPrompt string, messages []llm.Message, defs []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	return p.ChatContext(context.Background(), systemPrompt, messages, defs, onDelta)
}

func (p *acpMockProvider) ChatContext(ctx context.Context, systemPrompt string, messages []llm.Message, defs []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.systemPrompts = append(p.systemPrompts, systemPrompt)
	p.mu.Unlock()
	if call == 1 {
		if handler := llm.ReasoningHandlerFromContext(ctx); handler != nil {
			handler(llm.ReasoningUpdate{Delta: "checking tools"})
			handler(llm.ReasoningUpdate{Text: "checking tools", Done: true})
		}
		return llm.Response{
			ToolCalls:        []llm.ToolCall{{ID: "tool-1", Name: "Echo", Arguments: `{"text":"hello"}`}},
			Message:          llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "tool-1", Name: "Echo", Arguments: `{"text":"hello"}`}}},
			Usage:            llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
			ReasoningSummary: "checking tools",
		}, nil
	}
	if onDelta != nil {
		onDelta("do")
		onDelta("ne")
	}
	return llm.Response{
		Content: "done",
		Message: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Usage:   llm.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15},
	}, nil
}

func (p *acpMockProvider) GetModel() string   { return "mock" }
func (p *acpMockProvider) GetBaseURL() string { return "mock://test" }

func (p *acpMockProvider) SystemPrompts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.systemPrompts...)
}

type blockingACPProvider struct {
	entered chan struct{}
}

func (p blockingACPProvider) Chat(systemPrompt string, messages []llm.Message, defs []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	return p.ChatContext(context.Background(), systemPrompt, messages, defs, onDelta)
}

func (p blockingACPProvider) ChatContext(ctx context.Context, systemPrompt string, messages []llm.Message, defs []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	close(p.entered)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func (p blockingACPProvider) GetModel() string   { return "mock" }
func (p blockingACPProvider) GetBaseURL() string { return "mock://test" }

func TestACPInitializePromptAndToolUpdates(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"sessionId":"s1","cwd":` + quoteJSON(tmp) + `}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"hi"}]}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertHasResponseResult(t, lines, float64(1), "agentCapabilities")
	assertHasResponseResult(t, lines, float64(2), "sessionId")
	assertHasPromptStop(t, lines, "end_turn")
	assertHasAvailableCommand(t, lines, "compact")
	assertUpdateAfterResponse(t, lines, float64(2), "available_commands_update")
	assertHasUpdate(t, lines, "user_message_chunk")
	assertHasUpdate(t, lines, "agent_thought_chunk")
	assertHasUpdate(t, lines, "tool_call")
	assertHasUpdate(t, lines, "tool_call_update")
	assertHasUpdate(t, lines, "agent_message_chunk")
	assertHasUpdate(t, lines, "caretz/provisional_agent_message_chunk")
	assertHasTextUpdate(t, lines, "agent_thought_chunk", "checking tools")
	if got := countUpdates(t, lines, "agent_thought_chunk"); got != 1 {
		t.Fatalf("agent_thought_chunk updates = %d, want 1", got)
	}

	if _, err := os.Stat(filepath.Join(tmp, ".caretz", "sessions", acpAdapterType, "s1", "events.jsonpl")); err != nil {
		t.Fatalf("ACP session was not persisted: %v", err)
	}
}

func TestACPPreparesTextAndImageAttachment(t *testing.T) {
	root := t.TempDir()
	rt := newTestACPRuntime(t, root, &acpMockProvider{})
	server := &acpServer{rt: rt, sessions: map[string]*acpSession{}}
	session := &acpSession{id: "photo-session", cwd: root}
	image := []byte("small image bytes")
	prompt, display, attachments, cleanup, err := server.prepareACPPrompt(session, acpPromptParams{Prompt: []acpContentBlock{
		{Type: "text", Text: "What is in this photo?"},
		{Type: "image", Data: base64.StdEncoding.EncodeToString(image), MimeType: "image/jpeg", URI: "../camera photo.jpg"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if display != "What is in this photo?" || !strings.Contains(prompt, display) || !strings.Contains(prompt, "[ACP attachment]") {
		t.Fatalf("prompt = %q, display = %q", prompt, display)
	}
	if len(attachments) != 1 || attachments[0].Name != "camera_photo.jpg" || attachments[0].Kind != "image" {
		t.Fatalf("attachments = %#v", attachments)
	}
	path := filepath.Join(acpAttachmentsDir(rt.SessionsDir, session.id), attachments[0].ID, attachments[0].Name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, image) || !strings.Contains(prompt, "path: "+path) {
		t.Fatalf("stored data/path mismatch: data=%q prompt=%q", data, prompt)
	}
	content := acpUserContent(session.id, eventing.LogMessage{Content: prompt, DisplayContent: display, Attachments: attachments})
	encoded, _ := json.Marshal(content)
	if strings.Contains(string(encoded), path) || strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(image)) || !strings.Contains(string(encoded), attachments[0].SHA256) {
		t.Fatalf("client content leaks data or omits metadata: %s", encoded)
	}
}

func TestACPAttachmentFailureRollsBackEarlierFiles(t *testing.T) {
	root := t.TempDir()
	rt := newTestACPRuntime(t, root, &acpMockProvider{})
	server := &acpServer{rt: rt, sessions: map[string]*acpSession{}}
	session := &acpSession{id: "rollback", cwd: root}
	_, _, _, _, err := server.prepareACPPrompt(session, acpPromptParams{Prompt: []acpContentBlock{
		{Type: "image", Data: base64.StdEncoding.EncodeToString([]byte("valid")), MimeType: "image/png", URI: "one.png"},
		{Type: "image", Data: "not base64", MimeType: "image/png", URI: "two.png"},
	}})
	if err == nil {
		t.Fatal("invalid base64 was accepted")
	}
	entries, readErr := os.ReadDir(acpAttachmentsDir(rt.SessionsDir, session.id))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("partial attachments remain: %#v", entries)
	}
}

func TestACPAttachmentPromptEmitsClientSafeMetadata(t *testing.T) {
	root := t.TempDir()
	rt := newTestACPRuntime(t, root, &acpMockProvider{})
	encoded := base64.StdEncoding.EncodeToString([]byte("photo bytes"))
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"photo","cwd":` + quoteJSON(root) + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"photo","prompt":[{"type":"text","text":"inspect this"},{"type":"image","data":` + quoteJSON(encoded) + `,"mimeType":"image/jpeg","uri":"photo.jpg"}]}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	if code := runACPServer(context.Background(), rt, strings.NewReader(input), &out, io.Discard); code != 0 {
		t.Fatalf("runACPServer code = %d", code)
	}
	lines := decodeJSONLines(t, out.String())
	found := false
	for _, line := range lines {
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] != "user_message_chunk" {
			continue
		}
		content := update["content"].(map[string]any)
		metadata := content["_meta"].(map[string]any)["caretz"].(map[string]any)
		attachments := metadata["attachments"].([]any)
		attachment := attachments[0].(map[string]any)
		if content["text"] != "inspect this" || attachment["name"] != "photo.jpg" || strings.Contains(fmt.Sprint(content), encoded) || strings.Contains(fmt.Sprint(content), root) {
			t.Fatalf("unsafe or incomplete user content: %#v", content)
		}
		found = true
	}
	if !found {
		t.Fatalf("missing attachment user update: %#v", lines)
	}
}

func TestACPSendFilePersistsAndReplaysDownloadMetadata(t *testing.T) {
	root := t.TempDir()
	rt := newTestACPRuntime(t, root, &acpMockProvider{})
	var out bytes.Buffer
	server := &acpServer{rt: rt, out: &out, sessions: map[string]*acpSession{}}
	session, err := server.openSession("sent-files", root)
	if err != nil {
		t.Fatalf("openSession() error = %v", err)
	}
	defer func() { _ = session.session.Close() }()

	foundSendFile := false
	for _, definition := range session.session.ToolRegistry().ToolDefinitions() {
		foundSendFile = foundSendFile || definition.Name == "SendFile"
	}
	if !foundSendFile {
		t.Fatal("ACP build session does not advertise SendFile")
	}

	source := filepath.Join(root, "report.txt")
	if err := os.WriteFile(source, []byte("report contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := session.fileSender.SendFile(context.Background(), source, "Latest report", "user-message-1")
	if err != nil {
		t.Fatalf("SendFile() error = %v", err)
	}
	if result != "sent file report.txt" {
		t.Fatalf("SendFile() result = %q", result)
	}
	lines := decodeJSONLines(t, out.String())
	file := findSentFileUpdate(t, lines)
	if file["name"] != "report.txt" || file["caption"] != "Latest report" || file["inReplyTo"] != "user-message-1" || file["mimeType"] != "text/plain" {
		t.Fatalf("sent file metadata = %#v", file)
	}
	id := fmt.Sprint(file["id"])
	stored := filepath.Join(acpAttachmentsDir(rt.SessionsDir, session.id), id, "report.txt")
	if data, err := os.ReadFile(stored); err != nil || string(data) != "report contents" {
		t.Fatalf("stored sent file = %q, %v", data, err)
	}

	out.Reset()
	server.notifyMu.Lock()
	if err := server.replaySessionLocked(session); err != nil {
		server.notifyMu.Unlock()
		t.Fatalf("replaySessionLocked() error = %v", err)
	}
	server.notifyMu.Unlock()
	replayed := findSentFileUpdate(t, decodeJSONLines(t, out.String()))
	if replayed["id"] != file["id"] || replayed["sha256"] != file["sha256"] {
		t.Fatalf("replayed metadata = %#v, want %#v", replayed, file)
	}
}

func findSentFileUpdate(t *testing.T, lines []map[string]any) map[string]any {
	t.Helper()
	for _, line := range lines {
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == "caretz/file" {
			file, _ := update["file"].(map[string]any)
			return file
		}
	}
	t.Fatalf("missing caretz/file update in %#v", lines)
	return nil
}

func TestACPRejectsUnsafeSessionIDs(t *testing.T) {
	rt := newTestACPRuntime(t, t.TempDir(), &acpMockProvider{})
	server := &acpServer{rt: rt, sessions: map[string]*acpSession{}}
	for _, id := range []string{"../escape", "a/b", "a\\b", "", ".", ".."} {
		params, _ := json.Marshal(map[string]any{"sessionId": id, "cwd": rt.CWD})
		if _, rpcErr := server.handleSessionLoad("session/load", params); rpcErr == nil || rpcErr.Code != -32602 {
			t.Fatalf("session/load id %q error = %#v, want invalid params", id, rpcErr)
		}
	}
}

func TestACPSessionListFiltersWorkspaceAndResumeReusesOpenSession(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	rt := newTestACPRuntime(t, root, &acpMockProvider{})
	server := &acpServer{rt: rt, out: io.Discard, sessions: map[string]*acpSession{}}
	newParams := func(id, cwd string) json.RawMessage {
		data, _ := json.Marshal(map[string]any{"sessionId": id, "cwd": cwd})
		return data
	}
	if _, rpcErr := server.handleSessionNew(newParams("root-session", root)); rpcErr != nil {
		t.Fatalf("new root session: %v", rpcErr)
	}
	if _, rpcErr := server.handleSessionNew(newParams("root-session", root)); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("duplicate session/new error = %#v, want invalid params", rpcErr)
	}
	if _, rpcErr := server.handleSessionNew(newParams("other-session", other)); rpcErr != nil {
		t.Fatalf("new other session: %v", rpcErr)
	}
	original := server.getSession("root-session")
	if _, rpcErr := server.handleSessionLoad("session/resume", newParams("root-session", root)); rpcErr != nil {
		t.Fatalf("resume root session: %v", rpcErr)
	}
	if server.getSession("root-session") != original {
		t.Fatal("session/resume replaced the already open session")
	}
	result, rpcErr := server.handleSessionList(newParams("ignored", root))
	if rpcErr != nil {
		t.Fatalf("session/list: %v", rpcErr)
	}
	items := result.(map[string]any)["sessions"].([]map[string]any)
	if len(items) != 1 || items[0]["sessionId"] != "root-session" {
		t.Fatalf("session/list = %#v, want only root-session", items)
	}
}

func TestACPSessionLoadReplaysHistoryBeforeResponse(t *testing.T) {
	root := t.TempDir()
	rt := newTestACPRuntime(t, root, &acpMockProvider{})
	firstInput := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"history","cwd":` + quoteJSON(root) + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"history","prompt":[{"type":"text","text":"remember this"}]}}`,
		"",
	}, "\n")
	if code := runACPServer(context.Background(), rt, strings.NewReader(firstInput), io.Discard, io.Discard); code != 0 {
		t.Fatalf("first ACP run code = %d", code)
	}
	var output bytes.Buffer
	loadInput := `{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"history","cwd":` + quoteJSON(root) + `}}` + "\n"
	if code := runACPServer(context.Background(), rt, strings.NewReader(loadInput), &output, io.Discard); code != 0 {
		t.Fatalf("load ACP run code = %d", code)
	}
	lines := decodeJSONLines(t, output.String())
	responseIndex := -1
	historyIndexes := []int{}
	for i, line := range lines {
		if line["id"] == float64(3) {
			responseIndex = i
		}
		if line["method"] == "session/update" {
			params, _ := line["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			if update["sessionUpdate"] == "user_message_chunk" || update["sessionUpdate"] == "agent_message_chunk" || update["sessionUpdate"] == "tool_call" {
				historyIndexes = append(historyIndexes, i)
			}
		}
	}
	if responseIndex < 0 || len(historyIndexes) < 3 {
		t.Fatalf("load output missing response or complete history: %#v", lines)
	}
	for _, index := range historyIndexes {
		if index > responseIndex {
			t.Fatalf("history update index %d follows load response index %d", index, responseIndex)
		}
	}
}

func TestACPVerboseSessionDebugLog(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	rt.Logger.SetVerbosity(1)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s-debug","cwd":` + quoteJSON(tmp) + `}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	path := filepath.Join(tmp, ".caretz", "sessions", acpAdapterType, "s-debug", "debug.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ACP debug log: %v", err)
	}
	text := string(data)
	for _, want := range []string{"openSession start", "openSession complete", "session/new ready"} {
		if !strings.Contains(text, want) {
			t.Fatalf("debug log missing %q:\n%s", want, text)
		}
	}
}

func TestACPVerboseLogsProtocolLifecycleWithoutRequestContent(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	rt.Logger.SetVerbosity(1)
	var logs bytes.Buffer
	rt.Logger.SetSender(func(prefix, content string) {
		fmt.Fprintf(&logs, "%s %s\n", prefix, content)
	})

	const secret = "do-not-log-this-prompt"
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"secret":"` + secret + `"}}`,
		`{"jsonrpc":"2.0","method":"unknown/notification","params":{"secret":"` + secret + `"}}`,
		`not-json`,
		"",
	}, "\n")
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: io.Discard, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	got := logs.String()
	for _, want := range []string{
		"ACP server started",
		"ACP request: method=initialize id=1",
		"ACP request complete: method=initialize id=1",
		"ACP request: method=unknown/notification id=notification",
		"ACP request failed: method=unknown/notification id=notification code=-32601",
		"ACP request rejected: invalid JSON",
		"ACP server stopped",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("verbose ACP log missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, secret) {
		t.Fatalf("verbose ACP log contains request content:\n%s", got)
	}
}

func TestACPQuietDoesNotLogProtocolLifecycle(t *testing.T) {
	rt := newTestACPRuntime(t, t.TempDir(), &acpMockProvider{})
	var logs bytes.Buffer
	rt.Logger.SetSender(func(prefix, content string) {
		fmt.Fprintf(&logs, "%s %s\n", prefix, content)
	})
	server := &acpServer{
		rt: rt, in: strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n"),
		out: io.Discard, sessions: map[string]*acpSession{},
	}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := logs.String(); got != "" {
		t.Fatalf("quiet ACP logs = %q, want empty", got)
	}
}

func TestACPDoesNotWriteSessionDebugLogWithoutVerbosity(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s-quiet","cwd":` + quoteJSON(tmp) + `}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	path := filepath.Join(tmp, ".caretz", "sessions", acpAdapterType, "s-quiet", "debug.log")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("debug log exists without verbosity: stat err=%v", err)
	}
}

func TestACPCloseSession(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s-close","cwd":` + quoteJSON(tmp) + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/close","params":{"sessionId":"s-close"}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if session := server.getSession("s-close"); session != nil {
		t.Fatal("session/close did not remove session")
	}
	lines := decodeJSONLines(t, out.String())
	found := false
	for _, line := range lines {
		if line["id"] == float64(2) && line["error"] == nil {
			if _, ok := line["result"].(map[string]any); ok {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("missing successful session/close response in %#v", lines)
	}
}

func TestACPSlashCommandDiscoveryAndExecution(t *testing.T) {
	tmp := t.TempDir()
	provider := &acpMockProvider{}
	rt := newTestACPRuntime(t, tmp, provider)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"sessionId":"s1","cwd":` + quoteJSON(tmp) + `}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"/context"}]}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertHasAvailableCommand(t, lines, "compact")
	assertHasAvailableCommand(t, lines, "context")
	assertUpdateAfterResponse(t, lines, float64(2), "available_commands_update")
	assertHasPromptStop(t, lines, "end_turn")
	assertHasAgentTextContaining(t, lines, "context:")
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("provider calls = %d, want slash command to execute without LLM call", calls)
	}
}

func TestACPAdvertisesConfiguredModelSelector(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	baseURL := "http://localhost:11434/v1"
	rt.ProjectConfigPath = filepath.Join(tmp, ".caretz", "config.json")
	rt.FileConfig = config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Name:    "Local",
				Options: config.ProviderOptions{BaseURL: &baseURL},
				Models: map[string]config.Model{
					"qwen": {Name: "Qwen Code", Model: "qwen-code"},
					"kimi": {Name: "Kimi Code", Model: "kimi-code"},
				},
			},
		},
	}
	rt.Settings.ActiveModelRef = "local/qwen"

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":` + quoteJSON(tmp) + `}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertHasModelConfigOption(t, lines, float64(1), "local/qwen", []string{"local/kimi", "local/qwen"})
}

func TestACPAdvertisesReasoningAndModeConfigOptions(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	baseURL := "http://localhost:11434/v1"
	rt.FileConfig = config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Name:    "Local",
				Options: config.ProviderOptions{BaseURL: &baseURL},
				Models: map[string]config.Model{
					"qwen": {Name: "Qwen Code", Model: "qwen-code", Reasoning: "medium", ReasoningValues: []string{"low", "medium", "high"}},
				},
			},
		},
	}
	rt.Settings.ActiveModelRef = "local/qwen"
	rt.Settings.ReasoningEffort = "high"

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":` + quoteJSON(tmp) + `}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertHasSelectConfigOption(t, lines, float64(1), "reasoning_effort", "thought_level", "high", []string{"low", "medium", "high"})
	assertHasSelectConfigOption(t, lines, float64(1), "mode", "mode", "build", []string{"build", "plan"})
}

func TestACPOmitsReasoningConfigOptionForNonReasoningModel(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	baseURL := "http://localhost:11434/v1"
	rt.FileConfig = config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: &baseURL},
				Models:  map[string]config.Model{"qwen": {Name: "Qwen Code", Model: "qwen-code"}},
			},
		},
	}
	rt.Settings.ActiveModelRef = "local/qwen"

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":` + quoteJSON(tmp) + `}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertNoConfigOption(t, lines, float64(1), "reasoning_effort")
	assertHasSelectConfigOption(t, lines, float64(1), "mode", "mode", "build", []string{"build", "plan"})
}

func TestACPSetConfigOptionSwitchesModel(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	baseURL := "http://localhost:11434/v1"
	apiKey := "test-key"
	rt.ProjectConfigPath = filepath.Join(tmp, ".caretz", "config.json")
	rt.FileConfig = config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Name:    "Local",
				Options: config.ProviderOptions{BaseURL: &baseURL, APIKey: &apiKey},
				Models: map[string]config.Model{
					"qwen": {Name: "Qwen Code", Model: "qwen-code"},
					"kimi": {Name: "Kimi Code", Model: "kimi-code"},
				},
			},
		},
	}
	rt.Settings.ActiveModelRef = "local/qwen"

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":` + quoteJSON(tmp) + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/set_config_option","params":{"sessionId":"s1","configId":"model","value":"local/kimi"}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertHasModelConfigOption(t, lines, float64(2), "local/kimi", []string{"local/kimi", "local/qwen"})
	data, err := os.ReadFile(rt.ProjectConfigPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if !strings.Contains(string(data), `"activeModel": "local/kimi"`) {
		t.Fatalf("project config did not persist active model:\n%s", data)
	}
}

func TestACPAllowedModelsFilterChoicesAndRejectSwitch(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	rt.FileConfig = config.Config{Provider: map[string]config.Provider{
		"local": {Models: map[string]config.Model{"qwen": {}, "kimi": {}}},
	}}
	server := &acpServer{rt: rt, allowedModels: map[string]bool{"local/qwen": true}}

	choices := server.modelChoices()
	if len(choices) != 1 || choices[0].Ref != "local/qwen" {
		t.Fatalf("modelChoices() = %#v, want only local/qwen", choices)
	}
	if _, err := server.switchACPAgentSessionModel(context.Background(), nil, "local/kimi", ""); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("disallowed model switch error = %v", err)
	}
}

func TestNewACPServerDefersAllowedModelFallbackToSession(t *testing.T) {
	rt := newTestACPRuntime(t, t.TempDir(), &acpMockProvider{})
	rt.FileConfig = config.Config{Provider: map[string]config.Provider{
		"local": {Models: map[string]config.Model{"qwen": {}, "kimi": {}}},
	}}
	rt.Settings.ActiveModelRef = "local/kimi"
	if _, err := NewACPServer(rt, []string{"local/qwen"}); err != nil {
		t.Fatalf("NewACPServer() error = %v", err)
	}
	rt.Settings.ActiveModelRef = "local/qwen"
	if _, err := NewACPServer(rt, []string{"local/qwen"}); err != nil {
		t.Fatalf("NewACPServer() allowed model error = %v", err)
	}
}

func TestACPServeACPClosesOpenSessions(t *testing.T) {
	root := t.TempDir()
	rt := newTestACPRuntime(t, root, &acpMockProvider{})
	constructed, err := NewACPServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := constructed.(*acpServer)
	input := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s-close","cwd":` + quoteJSON(root) + `}}` + "\n"
	if err := server.ServeACP(context.Background(), strings.NewReader(input), io.Discard); err != nil {
		t.Fatalf("ServeACP() error = %v", err)
	}
	server.sessionsMu.RLock()
	defer server.sessionsMu.RUnlock()
	if len(server.sessions) != 0 {
		t.Fatalf("open sessions after ServeACP = %d, want 0", len(server.sessions))
	}
}

func TestACPSetReasoningConfigOption(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	baseURL := "http://localhost:11434/v1"
	apiKey := "test-key"
	rt.ProjectConfigPath = filepath.Join(tmp, ".caretz", "config.json")
	rt.FileConfig = config.Config{
		ActiveModel: "local/qwen",
		Provider: map[string]config.Provider{
			"local": {
				Options: config.ProviderOptions{BaseURL: &baseURL, APIKey: &apiKey},
				Models: map[string]config.Model{
					"qwen": {Name: "Qwen Code", Model: "qwen-code", Reasoning: "medium", ReasoningValues: []string{"low", "medium", "high"}},
				},
			},
		},
	}
	rt.Settings.ActiveModelRef = "local/qwen"
	rt.Settings.ReasoningEffort = "medium"

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":` + quoteJSON(tmp) + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/set_config_option","params":{"sessionId":"s1","configId":"reasoning_effort","value":"high"}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertHasSelectConfigOption(t, lines, float64(2), "reasoning_effort", "thought_level", "high", []string{"low", "medium", "high"})
}

func TestACPSetModeConfigOptionSwitchesToolPolicy(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":` + quoteJSON(tmp) + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/set_config_option","params":{"sessionId":"s1","configId":"mode","value":"plan"}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertHasSelectConfigOption(t, lines, float64(2), "mode", "mode", "plan", []string{"build", "plan"})
	session := server.getSession("s1")
	if session == nil {
		t.Fatal("missing ACP session")
	}
	registry := session.session.ToolRegistry()
	if !registry.PlanMode() {
		t.Fatalf("registry PlanMode = %v, want true", registry.PlanMode())
	}
	if _, err := registry.HandleToolCall(llm.ToolCall{ID: "w", Name: "Write", Arguments: `{"file_path":"x","content":"y"}`}); err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("Write after switching ACP mode to plan: err = %v, want plan mode refusal", err)
	}
}

func TestACPSubagentInvocationEventsAreForwarded(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	var out bytes.Buffer
	server := &acpServer{rt: rt, out: &out, sessions: map[string]*acpSession{}}
	session, err := server.openSession("s1", tmp)
	if err != nil {
		t.Fatalf("openSession() error = %v", err)
	}
	server.sessions["s1"] = session

	if err := session.session.AppendAuditEvent(eventing.NewSubagentInvocationStarted("s1", "reviewer", "child-1", "s1", "check the diff", 100000)); err != nil {
		t.Fatalf("AppendAuditEvent(started) error = %v", err)
	}
	if err := session.session.PublishAuditEvent(eventing.NewSubagentInvocationProgress("s1", "reviewer", "child-1", "s1", "subagent reviewer assistant: looks good", llm.Usage{}, 0, 1200, 100000)); err != nil {
		t.Fatalf("PublishAuditEvent(progress) error = %v", err)
	}
	if err := session.session.AppendAuditEvent(eventing.NewSubagentInvocationCompleted("s1", "reviewer", "child-1", "s1", "PASS", 1400, 100000)); err != nil {
		t.Fatalf("AppendAuditEvent(completed) error = %v", err)
	}

	lines := decodeJSONLines(t, out.String())
	assertHasTextUpdate(t, lines, "agent_thought_chunk", "subagent reviewer started: child-1\ncheck the diff")
	assertNoThoughtTextContaining(t, lines, "looks good")
	assertHasTextUpdate(t, lines, "agent_thought_chunk", "subagent reviewer completed: PASS")
}

func TestACPSubagentProgressIsNotForwardedAsThoughtChunks(t *testing.T) {
	tmp := t.TempDir()
	rt := newTestACPRuntime(t, tmp, &acpMockProvider{})
	var out bytes.Buffer
	server := &acpServer{rt: rt, out: &out, sessions: map[string]*acpSession{}}
	session, err := server.openSession("s1", tmp)
	if err != nil {
		t.Fatalf("openSession() error = %v", err)
	}

	events := []*types.Event{
		eventing.NewSubagentInvocationProgress("s1", "code-researcher", "child-1", "s1", "subagent code-researcher tool result Read: [File: llmctl.go]", llm.Usage{}, 0, 0, 0),
		eventing.NewSubagentInvocationProgress("s1", "code-researcher", "child-1", "s1", "subagent code-researcher reasoning: I", llm.Usage{}, 0, 0, 0),
		eventing.NewSubagentInvocationProgress("s1", "code-researcher", "child-1", "s1", "subagent code-researcher reasoning: 'm", llm.Usage{}, 0, 0, 0),
		eventing.NewSubagentInvocationProgress("s1", "code-researcher", "child-1", "s1", "subagent code-researcher tool call Read({})", llm.Usage{}, 0, 0, 0),
		eventing.NewSubagentInvocationProgress("s1", "code-researcher", "child-1", "s1", "subagent code-researcher tool result Bash: ok", llm.Usage{}, 0, 0, 0),
	}
	for _, event := range events {
		if err := session.session.PublishAuditEvent(event); err != nil {
			t.Fatalf("PublishAuditEvent(%s) error = %v", event.EventType, err)
		}
	}

	lines := decodeJSONLines(t, out.String())
	assertNoThoughtTextContaining(t, lines, "Read: [File: llmctl.go]")
	assertNoThoughtTextContaining(t, lines, "I")
	assertNoThoughtTextContaining(t, lines, "'m")
	assertNoThoughtTextContaining(t, lines, "Read({})")
	assertNoThoughtTextContaining(t, lines, "Bash: ok")
	assertNoThoughtTextContaining(t, lines, "subagent code-researcher reasoning:")
}

func TestACPCallSubagentToolKind(t *testing.T) {
	if got := acpToolKind("CallSubagent"); got != "subagent" {
		t.Fatalf("acpToolKind(CallSubagent) = %q, want subagent", got)
	}
}

func TestACPWorkspaceSkillsAreAdvertisedInSessionPrompt(t *testing.T) {
	serverRoot := t.TempDir()
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".caretz", "skills", "workspace-review")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: workspace-review\ndescription: Review code in this ACP workspace\n---\n\nUse workspace-specific review rules.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolvedWorkspace, err := resolveRootDir(workspace)
	if err != nil {
		t.Fatal(err)
	}

	provider := &acpMockProvider{}
	rt := newTestACPRuntime(t, serverRoot, provider)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s-skills","cwd":` + quoteJSON(workspace) + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s-skills","prompt":[{"type":"text","text":"use the workspace-review skill"}]}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: strings.NewReader(input), out: &out, sessions: map[string]*acpSession{}}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	prompts := provider.SystemPrompts()
	if len(prompts) == 0 {
		t.Fatal("provider did not receive a prompt")
	}
	systemPrompt := prompts[0]
	for _, want := range []string{
		"<available_skills>",
		"workspace-review",
		"Review code in this ACP workspace",
		filepath.Join(resolvedWorkspace, ".caretz", "skills", "workspace-review", "SKILL.md"),
		"Working directory: " + resolvedWorkspace,
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("ACP system prompt missing %q:\n%s", want, systemPrompt)
		}
	}
}

func TestACPWorkspaceSubagentsAreAvailableToCommands(t *testing.T) {
	serverRoot := t.TempDir()
	workspace := t.TempDir()
	agentsDir := filepath.Join(workspace, ".caretz", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte("---\ndescription: Review code in this ACP workspace\nmode: subagent\n---\n\nReview carefully.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rt := newTestACPRuntime(t, serverRoot, &acpMockProvider{})
	session, err := (&acpServer{rt: rt}).openSession("s-subagents", workspace)
	if err != nil {
		t.Fatalf("openSession() error = %v", err)
	}

	if !session.slashCommands.Handles("@reviewer check the diff") {
		t.Fatal("ACP command registry did not discover workspace-local subagent shorthand")
	}
	agentsDirs := session.session.AgentsDirs()
	want := filepath.Join(session.cwd, ".caretz", "agents")
	if len(agentsDirs) == 0 || agentsDirs[len(agentsDirs)-1] != want {
		t.Fatalf("ACP session AgentsDirs() = %#v, want final workspace agents dir %q", agentsDirs, want)
	}
}

func TestACPCancelActivePrompt(t *testing.T) {
	tmp := t.TempDir()
	entered := make(chan struct{})
	rt := newTestACPRuntime(t, tmp, blockingACPProvider{entered: entered})
	reader, writer := io.Pipe()
	var out bytes.Buffer
	server := &acpServer{rt: rt, in: reader, out: &out, sessions: map[string]*acpSession{}}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background())
	}()

	writeRPC(t, writer, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":`+quoteJSON(tmp)+`}}`)
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s1","message":"wait"}}`)
	<-entered
	writeRPC(t, writer, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`)
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	lines := decodeJSONLines(t, out.String())
	assertHasPromptStop(t, lines, "cancelled")
}

func TestACPCancelWaitingPromptPersistsNothing(t *testing.T) {
	cwd := t.TempDir()
	entered := make(chan struct{})
	server := &acpServer{rt: newTestACPRuntime(t, cwd, blockingACPProvider{entered: entered}), out: io.Discard, sessions: map[string]*acpSession{}}
	session, err := server.openSession("waiting", cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.session.Close() }()
	server.sessions[session.id] = session
	request := eventing.AsyncRunRequest{RequestID: "earlier-report", Mode: eventing.AsyncRunForeground, Prompt: "earlier report"}
	if err := session.session.PublishAsyncEvent(t.Context(), eventing.NewAsyncRunRequested(session.id, request)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background report did not start")
	}
	waiting := make(chan struct{})
	ctx := progress.With(t.Context(), func(string) { close(waiting) })
	done := make(chan *acpError, 1)
	go func() {
		_, err := server.handlePrompt(ctx, json.RawMessage(`{"sessionId":"waiting","message":"abandoned waiting prompt"}`))
		done <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("prompt did not wait for session")
	}
	if _, err := server.handleCancel(json.RawMessage(`{"sessionId":"waiting"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("waiting ACP prompt ignored cancellation")
	}
	events, err := session.session.GetEvents()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType == eventing.LogMessageUserCreated {
			message, err := eventing.DecodeLogMessage(event)
			if err != nil {
				t.Fatal(err)
			}
			if message.Content == "abandoned waiting prompt" {
				t.Fatal("cancelled waiting prompt was persisted")
			}
		}
	}
}

type ownedJobsACPProvider struct {
	blockingACPProvider
	calls int
}

func (p *ownedJobsACPProvider) ChatContext(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	p.calls++
	if p.calls == 1 || p.calls == 3 {
		tc := llm.ToolCall{ID: fmt.Sprintf("job-%d", p.calls), Name: "OwnedJob", Arguments: `{}`}
		return llm.Response{ToolCalls: []llm.ToolCall{tc}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{tc}}}, nil
	}
	if p.calls == 2 {
		return llm.Response{Content: "first turn complete", Message: llm.Message{Role: llm.RoleAssistant, Content: "first turn complete"}}, nil
	}
	close(p.entered)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestACPCancelStopsOnlyCurrentTurnOwnedWork(t *testing.T) {
	cwd := t.TempDir()
	provider := &ownedJobsACPProvider{blockingACPProvider: blockingACPProvider{entered: make(chan struct{})}}
	rt := newTestACPRuntime(t, cwd, provider)
	stopped := []chan struct{}{make(chan struct{}), make(chan struct{})}
	jobs := 0
	if err := rt.ToolRegistry.RegisterTool(tools.Tool{Definition: llm.ToolDefinition{Name: "OwnedJob", Description: "Test owned work", Parameters: map[string]any{"type": "object"}}, Handler: func(ctx context.Context, _ llm.ToolCall) (string, error) {
		done := stopped[jobs]
		jobs++
		turnscope.From(ctx).Register(func() (string, error) { close(done); return "Stopped background job.", nil })
		return "Background command started.", nil
	}}); err != nil {
		t.Fatal(err)
	}
	server := &acpServer{rt: rt, out: io.Discard, sessions: map[string]*acpSession{}}
	session, err := server.openSession("owned", cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.session.Close() }()
	server.sessions[session.id] = session
	params := json.RawMessage(`{"sessionId":"owned","message":"launch background work"}`)
	if _, rpcErr := server.handlePrompt(t.Context(), params); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	done := make(chan *acpError, 1)
	go func() { _, err := server.handlePrompt(t.Context(), params); done <- err }()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("second turn did not start")
	}
	if _, err := server.handleCancel(json.RawMessage(`{"sessionId":"owned"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ACP cancellation did not finish")
	}
	select {
	case <-stopped[1]:
	default:
		t.Fatal("current turn's job survived")
	}
	select {
	case <-stopped[0]:
		t.Fatal("earlier turn's job was stopped")
	default:
	}
}

func newTestACPRuntime(t *testing.T, cwd string, provider llm.Provider) *Runtime {
	t.Helper()
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: cwd}
	if err := registry.RegisterTool(tools.Tool{
		Definition: llm.ToolDefinition{Name: "Echo", Description: "Echo test tool", Parameters: map[string]any{"type": "object"}},
		Handler: func(ctx context.Context, call llm.ToolCall) (string, error) {
			return "echoed", nil
		},
	}); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}
	return &Runtime{
		Ctx:          context.Background(),
		CWD:          cwd,
		ConfigRoot:   filepath.Join(cwd, ".caretz"),
		SessionsDir:  filepath.Join(cwd, ".caretz", "sessions"),
		Logger:       log.New(),
		Provider:     provider,
		ToolRegistry: registry,
		SystemPrompt: "test prompt",
		Settings: runtimeSettings{
			Model:            "mock",
			BaseURL:          "http://127.0.0.1:8080/v1",
			ContextWindow:    100000,
			ReservedTokens:   8192,
			KeepRecentTokens: 8192,
			SummaryTokens:    2000,
		},
		DisableTasks: true,
	}
}

func writeRPC(t *testing.T, writer *io.PipeWriter, line string) {
	t.Helper()
	if _, err := writer.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func decodeJSONLines(t *testing.T, text string) []map[string]any {
	t.Helper()
	var result []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("invalid JSON line %q: %v", scanner.Text(), err)
		}
		result = append(result, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan output: %v", err)
	}
	return result
}

func assertHasResponseResult(t *testing.T, lines []map[string]any, id float64, key string) {
	t.Helper()
	for _, line := range lines {
		if line["id"] != id {
			continue
		}
		result, _ := line["result"].(map[string]any)
		if _, ok := result[key]; ok {
			return
		}
	}
	t.Fatalf("missing response id=%v with result key %q in %#v", id, key, lines)
}

func assertHasPromptStop(t *testing.T, lines []map[string]any, reason string) {
	t.Helper()
	for _, line := range lines {
		result, _ := line["result"].(map[string]any)
		if result["stopReason"] == reason {
			return
		}
	}
	t.Fatalf("missing prompt stopReason %q in %#v", reason, lines)
}

func assertHasUpdate(t *testing.T, lines []map[string]any, updateType string) {
	t.Helper()
	for _, line := range lines {
		if line["method"] != "session/update" {
			continue
		}
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == updateType {
			return
		}
	}
	t.Fatalf("missing session/update %q in %#v", updateType, lines)
}

func assertHasAvailableCommand(t *testing.T, lines []map[string]any, name string) {
	t.Helper()
	for _, line := range lines {
		if line["method"] != "session/update" {
			continue
		}
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] != "available_commands_update" {
			continue
		}
		commands, _ := update["availableCommands"].([]any)
		for _, raw := range commands {
			cmd, _ := raw.(map[string]any)
			if cmd["name"] == name {
				return
			}
		}
	}
	t.Fatalf("missing available command %q in %#v", name, lines)
}

func assertUpdateAfterResponse(t *testing.T, lines []map[string]any, id float64, updateType string) {
	t.Helper()
	responseIndex := -1
	updateIndex := -1
	for i, line := range lines {
		if line["id"] == id {
			responseIndex = i
		}
		if line["method"] != "session/update" {
			continue
		}
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == updateType && updateIndex == -1 {
			updateIndex = i
		}
	}
	if responseIndex == -1 {
		t.Fatalf("missing response id=%v in %#v", id, lines)
	}
	if updateIndex == -1 {
		t.Fatalf("missing update %q in %#v", updateType, lines)
	}
	if updateIndex < responseIndex {
		t.Fatalf("update %q arrived before response id=%v: update index %d response index %d", updateType, id, updateIndex, responseIndex)
	}
}

func assertHasAgentTextContaining(t *testing.T, lines []map[string]any, text string) {
	t.Helper()
	for _, line := range lines {
		if line["method"] != "session/update" {
			continue
		}
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] != "agent_message_chunk" {
			continue
		}
		content, _ := update["content"].(map[string]any)
		if strings.Contains(fmt.Sprint(content["text"]), text) {
			return
		}
	}
	t.Fatalf("missing agent text containing %q in %#v", text, lines)
}

func assertHasTextUpdate(t *testing.T, lines []map[string]any, updateType, text string) {
	t.Helper()
	for _, line := range lines {
		if line["method"] != "session/update" {
			continue
		}
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] != updateType {
			continue
		}
		content, _ := update["content"].(map[string]any)
		if content["type"] == "text" && content["text"] == text && strings.TrimSpace(fmt.Sprint(update["messageId"])) != "" {
			return
		}
	}
	t.Fatalf("missing session/update %q with text %q and messageId in %#v", updateType, text, lines)
}

func assertNoThoughtTextContaining(t *testing.T, lines []map[string]any, text string) {
	t.Helper()
	for _, line := range lines {
		if line["method"] != "session/update" {
			continue
		}
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] != "agent_thought_chunk" {
			continue
		}
		content, _ := update["content"].(map[string]any)
		if strings.Contains(fmt.Sprint(content["text"]), text) {
			t.Fatalf("unexpected thought text containing %q in %#v", text, lines)
		}
	}
}

func assertHasModelConfigOption(t *testing.T, lines []map[string]any, id float64, current string, refs []string) {
	t.Helper()
	option := findResponseConfigOption(t, lines, id, "model")
	if option == nil {
		t.Fatalf("missing model config option in response id=%v: %#v", id, lines)
	}
	if option["category"] != "model" || option["type"] != "select" {
		t.Fatalf("model config option category/type = %v/%v, want model/select", option["category"], option["type"])
	}
	if option["currentValue"] != current {
		t.Fatalf("model currentValue = %q, want %q", option["currentValue"], current)
	}
	values := map[string]bool{}
	selectOptions, _ := option["options"].([]any)
	for _, rawSelectOption := range selectOptions {
		selectOption, _ := rawSelectOption.(map[string]any)
		values[fmt.Sprint(selectOption["value"])] = true
	}
	for _, ref := range refs {
		if !values[ref] {
			t.Fatalf("missing model option %q in %#v", ref, option["options"])
		}
	}
}

func assertHasSelectConfigOption(t *testing.T, lines []map[string]any, id float64, configID, category, current string, values []string) {
	t.Helper()
	option := findResponseConfigOption(t, lines, id, configID)
	if option == nil {
		t.Fatalf("missing config option %q in response id=%v: %#v", configID, id, lines)
	}
	if option["category"] != category || option["type"] != "select" {
		t.Fatalf("%s config option category/type = %v/%v, want %s/select", configID, option["category"], option["type"], category)
	}
	if option["currentValue"] != current {
		t.Fatalf("%s currentValue = %q, want %q", configID, option["currentValue"], current)
	}
	gotValues := map[string]bool{}
	selectOptions, _ := option["options"].([]any)
	for _, rawSelectOption := range selectOptions {
		selectOption, _ := rawSelectOption.(map[string]any)
		gotValues[fmt.Sprint(selectOption["value"])] = true
	}
	for _, value := range values {
		if !gotValues[value] {
			t.Fatalf("missing %s option %q in %#v", configID, value, option["options"])
		}
	}
}

func assertNoConfigOption(t *testing.T, lines []map[string]any, id float64, configID string) {
	t.Helper()
	if option := findResponseConfigOption(t, lines, id, configID); option != nil {
		t.Fatalf("unexpected config option %q in response id=%v: %#v", configID, id, option)
	}
}

func findResponseConfigOption(t *testing.T, lines []map[string]any, id float64, configID string) map[string]any {
	t.Helper()
	for _, line := range lines {
		if line["id"] != id {
			continue
		}
		result, _ := line["result"].(map[string]any)
		options, _ := result["configOptions"].([]any)
		for _, rawOption := range options {
			option, _ := rawOption.(map[string]any)
			if option["id"] != configID {
				continue
			}
			return option
		}
	}
	return nil
}

func countUpdates(t *testing.T, lines []map[string]any, updateType string) int {
	t.Helper()
	var count int
	for _, line := range lines {
		if line["method"] != "session/update" {
			continue
		}
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == updateType {
			count++
		}
	}
	return count
}

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

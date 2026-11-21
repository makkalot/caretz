//go:build unix

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/peers"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

// peerACPProvider answers every turn with "noted" and reports the last user
// message of each call, so a test can wait for a turn it did not prompt.
type peerACPProvider struct {
	mu        sync.Mutex
	toolNames []string
	prompts   chan string
}

func newPeerACPProvider() *peerACPProvider {
	return &peerACPProvider{prompts: make(chan string, 16)}
}

func (p *peerACPProvider) Chat(systemPrompt string, messages []llm.Message, defs []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	return p.ChatContext(context.Background(), systemPrompt, messages, defs, onDelta)
}

func (p *peerACPProvider) ChatContext(_ context.Context, _ string, messages []llm.Message, defs []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	p.mu.Lock()
	p.toolNames = p.toolNames[:0]
	for _, def := range defs {
		p.toolNames = append(p.toolNames, def.Name)
	}
	p.mu.Unlock()
	last := ""
	for _, message := range messages {
		if message.Role == llm.RoleUser {
			last = message.Content
		}
	}
	p.prompts <- last
	return llm.Response{Content: "noted", Message: llm.Message{Role: llm.RoleAssistant, Content: "noted"}}, nil
}

func (p *peerACPProvider) GetModel() string   { return "mock" }
func (p *peerACPProvider) GetBaseURL() string { return "mock://test" }

func (p *peerACPProvider) offered(tool string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.toolNames, tool)
}

func (p *peerACPProvider) waitForPrompt(t *testing.T, containing string) {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case prompt := <-p.prompts:
			if strings.Contains(prompt, containing) {
				return
			}
		case <-timeout:
			t.Fatalf("no turn ran with a prompt containing %q", containing)
		}
	}
}

func writeACPPeerWorkspace(t *testing.T) string {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(workspace, ".caretz", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "reviewer.md"), []byte("---\ndescription: Reviews diffs\nmode: peer\n---\nReview."), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspace
}

// spoolFromReviewer delivers content from the reviewer bot to ACP session id.
func spoolFromReviewer(t *testing.T, rt *Runtime, workspace, sessionID, content string) {
	t.Helper()
	runDir, err := peers.OpenRunDir(rt.ConfigRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	to := peers.SessionEndpoint(acpAdapterType, sessionID)
	if err := runDir.Spool(peers.Message{ID: peers.NewMessageID(), From: peers.BotEndpoint("reviewer"), FromLabel: "reviewer", To: to, Content: content, Hop: 1}); err != nil {
		t.Fatal(err)
	}
	runDir.Poke(to)
}

// serveACPInBackground runs an ACP server until the returned stop function
// closes its input, then returns everything it wrote.
func serveACPInBackground(t *testing.T, rt *Runtime) (*io.PipeWriter, func() []map[string]any) {
	t.Helper()
	server, err := NewACPServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	return serveInBackground(t, server.(*acpServer))
}

func serveInBackground(t *testing.T, server *acpServer) (*io.PipeWriter, func() []map[string]any) {
	t.Helper()
	reader, writer := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- server.ServeACP(context.Background(), reader, &out) }()
	return writer, func() []map[string]any {
		t.Helper()
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("ServeACP() error = %v", err)
		}
		return decodeJSONLines(t, out.String())
	}
}

// userChunkIndexes returns the output positions of user_message_chunk
// updates whose text contains text.
func userChunkIndexes(lines []map[string]any, text string) []int {
	var indexes []int
	for i, line := range lines {
		if line["method"] != "session/update" {
			continue
		}
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		content, _ := update["content"].(map[string]any)
		if update["sessionUpdate"] == "user_message_chunk" && strings.Contains(fmt.Sprint(content["text"]), text) {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func responseIndex(lines []map[string]any, id float64) int {
	for i, line := range lines {
		if line["id"] == id {
			return i
		}
	}
	return -1
}

func TestACPSessionSendsAndReceivesPeerMessages(t *testing.T) {
	workspace := writeACPPeerWorkspace(t)
	provider := newPeerACPProvider()
	rt := newTestACPRuntime(t, workspace, provider)
	writer, stop := serveACPInBackground(t, rt)

	writeRPC(t, writer, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":`+quoteJSON(workspace)+`}}`)
	spoolFromReviewer(t, rt, workspace, "s1", "please check spool.go")
	provider.waitForPrompt(t, "please check spool.go")
	if !provider.offered(peers.ToolName) {
		t.Errorf("%s was not offered to the model: %v", peers.ToolName, provider.toolNames)
	}
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"client prompt"}]}}`)
	provider.waitForPrompt(t, "client prompt")
	lines := stop()

	peerChunks := userChunkIndexes(lines, "please check spool.go")
	if len(peerChunks) != 1 {
		t.Fatalf("peer message echoed %d times, want 1: %#v", len(peerChunks), lines)
	}
	if created := responseIndex(lines, 1); created < 0 || peerChunks[0] < created {
		t.Fatalf("peer message (index %d) streamed before the session/new response (index %d)", peerChunks[0], created)
	}
	if got := len(userChunkIndexes(lines, "client prompt")); got != 1 {
		t.Fatalf("client prompt echoed %d times, want 1", got)
	}
	assertHasAgentTextContaining(t, lines, "noted")
	assertHasPromptStop(t, lines, "end_turn")

	// Clients see the peer's message itself, tagged with who sent it, not
	// the frame the model reads.
	peerContent := updateContent(lines[peerChunks[0]])
	if text := fmt.Sprint(peerContent["text"]); text != "please check spool.go" {
		t.Fatalf("peer message text = %q, want the bare message", text)
	}
	meta, _ := peerContent["_meta"].(map[string]any)
	caretzMeta, _ := meta["caretz"].(map[string]any)
	origin, _ := caretzMeta["origin"].(map[string]any)
	if origin["source"] != "peer" || origin["from"] != "reviewer" || origin["messageId"] == nil || origin["messageId"] != origin["sourceId"] {
		t.Fatalf("peer message origin = %#v", origin)
	}

	// Each turn, prompted or not, is framed: the peer turn, then the prompt.
	frames := turnFrames(lines)
	if len(frames) != 4 {
		t.Fatalf("turn frames = %+v, want two started/ended pairs", frames)
	}
	for i, want := range []struct{ kind, source string }{
		{"caretz/turn_started", "peer"}, {"caretz/turn_ended", "peer"},
		{"caretz/turn_started", "prompt"}, {"caretz/turn_ended", "prompt"},
	} {
		if frames[i].kind != want.kind || frames[i].update["source"] != want.source || frames[i].update["startedAt"] == nil {
			t.Fatalf("turn frame %d = %+v, want %s from %s", i, frames[i], want.kind, want.source)
		}
	}
	started, ended := frames[0], frames[1]
	if started.update["turnId"] == nil || started.update["turnId"] != ended.update["turnId"] || ended.update["stopReason"] != "end_turn" || started.update["promptRequestId"] != nil {
		t.Fatalf("peer turn frames = %+v / %+v", started.update, ended.update)
	}
	if started.index > peerChunks[0] || ended.index < peerChunks[0] {
		t.Fatalf("peer message (index %d) is outside its turn frame (%d..%d)", peerChunks[0], started.index, ended.index)
	}
	prompt := userChunkIndexes(lines, "client prompt")[0]
	if prompt < ended.index {
		t.Fatalf("client prompt (index %d) streamed inside the peer turn (ended %d)", prompt, ended.index)
	}
	// The prompted turn starts after its user message, carries the prompt's
	// request id, and ends just before the prompt response.
	promptStarted, promptEnded := frames[2], frames[3]
	if promptStarted.index < prompt || promptStarted.update["promptRequestId"] != float64(2) || promptEnded.update["stopReason"] != "end_turn" {
		t.Fatalf("prompt turn frames = %+v / %+v (prompt at %d)", promptStarted, promptEnded, prompt)
	}
	if response := responseIndex(lines, 2); response != promptEnded.index+1 {
		t.Fatalf("prompt response at %d, want right after turn_ended at %d", response, promptEnded.index)
	}
	created := lines[responseIndex(lines, 1)]["result"].(map[string]any)
	if active, ok := created["activeTurn"]; !ok || active != nil {
		t.Fatalf("session/new activeTurn = %#v (present %t), want null", active, ok)
	}

	// History replays the peer message with the same text and origin.
	writer, stop = serveACPInBackground(t, rt)
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"s1","cwd":`+quoteJSON(workspace)+`}}`)
	replayed := stop()
	chunks := userChunkIndexes(replayed, "please check spool.go")
	if len(chunks) != 1 {
		t.Fatalf("replayed peer message %d times, want 1: %#v", len(chunks), replayed)
	}
	replayedContent := updateContent(replayed[chunks[0]])
	replayedMeta, _ := replayedContent["_meta"].(map[string]any)
	if fmt.Sprint(replayedContent["text"]) != "please check spool.go" || !reflect.DeepEqual(replayedMeta, meta) {
		t.Fatalf("replayed peer message = %#v, want text and _meta %#v", replayedContent, meta)
	}
}

func TestACPSessionSetupReportsActiveTurn(t *testing.T) {
	session := &acpSession{id: "s1"}
	if got := session.activeTurn(); got != nil {
		t.Fatalf("idle activeTurn = %#v, want nil", got)
	}
	started := time.Date(2026, 11, 21, 10, 0, 0, 0, time.FixedZone("x", 3600))
	session.turn = &acpTurnInfo{id: "t1", source: acpTurnSourcePrompt, startedAt: started, promptRequestID: json.RawMessage(`"018f1591"`)}
	want := map[string]any{"turnId": "t1", "source": "prompt", "startedAt": "2026-11-21T09:00:00Z", "promptRequestId": json.RawMessage(`"018f1591"`)}
	if got := session.activeTurn(); !reflect.DeepEqual(got, want) {
		t.Fatalf("activeTurn = %#v, want %#v", got, want)
	}
	session.turn = &acpTurnInfo{id: "t2", source: "peer", startedAt: started}
	if got := session.activeTurn().(map[string]any); got["promptRequestId"] != nil || got["source"] != "peer" {
		t.Fatalf("background activeTurn = %#v, want peer turn without promptRequestId", got)
	}
}

func updateContent(line map[string]any) map[string]any {
	params, _ := line["params"].(map[string]any)
	update, _ := params["update"].(map[string]any)
	content, _ := update["content"].(map[string]any)
	return content
}

type turnFrame struct {
	index  int
	kind   string
	update map[string]any
}

func turnFrames(lines []map[string]any) []turnFrame {
	var frames []turnFrame
	for i, line := range lines {
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if kind, _ := update["sessionUpdate"].(string); strings.HasPrefix(kind, "caretz/turn_") {
			frames = append(frames, turnFrame{index: i, kind: kind, update: update})
		}
	}
	return frames
}

func TestACPCancelStopsBackgroundTurnWhenNoPromptRuns(t *testing.T) {
	cwd := t.TempDir()
	entered := make(chan struct{})
	var out bytes.Buffer
	server := &acpServer{rt: newTestACPRuntime(t, cwd, blockingACPProvider{entered: entered}), out: &out, sessions: map[string]*acpSession{}}
	session, err := server.openSession("background", cwd)
	if err != nil {
		t.Fatal(err)
	}
	server.sessions[session.id] = session
	completed, err := session.session.SubscribeAuditEvents(eventing.AsyncRunCompleted)
	if err != nil {
		t.Fatal(err)
	}
	defer completed.Close()
	request := eventing.AsyncRunRequest{RequestID: "scheduled", Source: "schedule", Mode: eventing.AsyncRunForeground, Prompt: "scheduled check"}
	if err := session.session.PublishAsyncEvent(t.Context(), eventing.NewAsyncRunRequested(session.id, request)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("background turn did not start")
	}
	if _, rpcErr := server.handleCancel(json.RawMessage(`{"sessionId":"background"}`)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	select {
	case event := <-completed.Events():
		if result, _ := eventing.DecodeAsyncRunResult(event); result.Status != "cancelled" {
			t.Fatalf("background turn result = %+v, want cancelled", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session/cancel did not stop the background turn")
	}
	if err := session.session.Close(); err != nil {
		t.Fatal(err)
	}
	frames := turnFrames(decodeJSONLines(t, out.String()))
	if len(frames) != 2 || frames[1].update["stopReason"] != "cancelled" || frames[1].update["source"] != "schedule" {
		t.Fatalf("turn frames = %+v, want a cancelled schedule turn", frames)
	}
}

func TestACPPeerServerRunsBotAndAttachesClientOnLoad(t *testing.T) {
	workspace := writeACPPeerWorkspace(t)
	definition := "---\ndescription: Reviews diffs\nmode: peer\nworkspace: out/reviewer\n---\nReview."
	if err := os.WriteFile(filepath.Join(workspace, ".caretz", "agents", "reviewer.md"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := newPeerACPProvider()
	rt := newTestACPRuntime(t, workspace, provider)
	rt.AgentsDirs = []string{filepath.Join(workspace, ".caretz", "agents")}
	launch, err := preparePeerLaunch(rt, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = launch.lock.Release() }()
	if _, err := preparePeerLaunch(rt, "reviewer"); err == nil {
		t.Fatal("a second instance of the bot started")
	}
	server, err := NewACPServer(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.(*acpServer).peer = launch
	writer, stop := serveInBackground(t, server.(*acpServer))

	// A root session asks the bot for a review before any client attaches.
	runDir, err := peers.OpenRunDir(rt.ConfigRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	root := peers.SessionEndpoint(acpAdapterType, "root-session")
	request := peers.Message{ID: peers.NewMessageID(), From: root, FromLabel: peers.RootLabel, To: peers.BotEndpoint("reviewer"), Content: "review internal/peers", Hop: 1, ExpectsReply: true}
	if err := runDir.Spool(request); err != nil {
		t.Fatal(err)
	}
	runDir.Poke(request.To)
	provider.waitForPrompt(t, "review internal/peers")
	reply := waitForReply(t, runDir, root, request.ID)
	if reply.Content != "noted" || reply.Status != "succeeded" {
		t.Fatalf("reply = %+v", reply)
	}

	writeRPC(t, writer, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":`+quoteJSON(workspace)+`}}`)
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":2,"method":"session/list","params":{}}`)
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"reviewer","cwd":`+quoteJSON(workspace)+`}}`)
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"reviewer","prompt":[{"type":"text","text":"direct question"}]}}`)
	provider.waitForPrompt(t, "direct question")
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":5,"method":"session/close","params":{"sessionId":"reviewer"}}`)
	session := server.(*acpServer).getSession("reviewer")
	lines := stop()

	// The bot's turn ran before the client sent anything, so a live update
	// leaked while detached would precede the first response. Only the
	// content-free turn boundaries are sent, so a supervisor sees it busy.
	loaded := responseIndex(lines, 3)
	detachedFrames := turnFrames(lines[:responseIndex(lines, 1)])
	for _, line := range lines[:responseIndex(lines, 1)] {
		params, _ := line["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if kind, _ := update["sessionUpdate"].(string); line["method"] == "session/update" && !strings.HasPrefix(kind, "caretz/turn_") {
			t.Fatalf("detached bot sent a live update: %#v", line)
		}
	}
	if len(detachedFrames) != 2 || detachedFrames[0].update["source"] != "peer" || detachedFrames[1].update["stopReason"] != "end_turn" {
		t.Fatalf("detached bot turn frames = %+v, want one peer turn", detachedFrames)
	}
	if line := lines[responseIndex(lines, 1)]; line["error"] == nil {
		t.Fatalf("session/new on a bot server = %#v, want an error", line)
	}
	sessions, _ := lines[responseIndex(lines, 2)]["result"].(map[string]any)["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["sessionId"] != "reviewer" {
		t.Fatalf("session/list = %#v, want only the bot", sessions)
	}
	if history := userChunkIndexes(lines, "review internal/peers"); len(history) != 1 || history[0] > loaded {
		t.Fatalf("bot history not replayed before the load response: %v (load %d)", history, loaded)
	}
	if got := len(userChunkIndexes(lines, "direct question")); got != 1 {
		t.Fatalf("direct prompt echoed %d times", got)
	}
	assertHasPromptStop(t, lines, "end_turn")
	if _, err := os.Stat(filepath.Join(rt.SessionsDir, peerAdapter, "reviewer", "events.jsonpl")); err != nil {
		t.Fatalf("bot history is not in its peer session: %v", err)
	}
	if got, want := session.session.ToolRegistry().WorkspaceDir, filepath.Join(workspace, "out", "reviewer"); got != want {
		t.Fatalf("bot works in %s, want its confined workspace %s", got, want)
	}
	if !session.detached.Load() {
		t.Fatal("session/close did not detach the client")
	}
}

func waitForReply(t *testing.T, runDir *peers.RunDir, endpoint, inReplyTo string) peers.Message {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		messages, _, err := runDir.Pending(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if message.InReplyTo == inReplyTo {
				return message
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no reply to %s reached %s", inReplyTo, endpoint)
	return peers.Message{}
}

func TestACPSessionLoadDeliversWaitingPeerMessageAfterReplay(t *testing.T) {
	workspace := writeACPPeerWorkspace(t)
	provider := newPeerACPProvider()
	rt := newTestACPRuntime(t, workspace, provider)

	writer, stop := serveACPInBackground(t, rt)
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"sessionId":"s1","cwd":`+quoteJSON(workspace)+`}}`)
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"remember this"}]}}`)
	provider.waitForPrompt(t, "remember this")
	stop()

	// The session is closed, so the message waits in its inbox.
	spoolFromReviewer(t, rt, workspace, "s1", "arrived while you were away")

	writer, stop = serveACPInBackground(t, rt)
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"s1","cwd":`+quoteJSON(workspace)+`}}`)
	provider.waitForPrompt(t, "arrived while you were away")
	// A prompt waits for the peer turn, so its stop proves that turn ended.
	writeRPC(t, writer, `{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"next"}]}}`)
	provider.waitForPrompt(t, "next")
	lines := stop()

	loaded := responseIndex(lines, 3)
	history := userChunkIndexes(lines, "remember this")
	if loaded < 0 || len(history) != 1 || history[0] > loaded {
		t.Fatalf("replay missing or after load response (response %d, history %v): %#v", loaded, history, lines)
	}
	waiting := userChunkIndexes(lines, "arrived while you were away")
	if len(waiting) != 1 || waiting[0] < loaded {
		t.Fatalf("waiting peer message should stream once after the load response (response %d, chunks %v)", loaded, waiting)
	}
}

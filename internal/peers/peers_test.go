//go:build unix

package peers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type fakeGateway struct {
	id     string
	mu     sync.Mutex
	events []*types.Event
	audit  *eventing.SyncEventLoop
	ctx    *eventing.SyncEventLoop
}

func newFakeGateway(id string, initial ...*types.Event) *fakeGateway {
	return &fakeGateway{
		id:     id,
		events: append([]*types.Event(nil), initial...),
		audit:  eventing.NewSyncEventLoop(context.Background()),
		ctx:    eventing.NewSyncEventLoop(context.Background()),
	}
}

func (g *fakeGateway) PublishAsyncEvent(_ context.Context, event *types.Event) error {
	g.mu.Lock()
	g.events = append(g.events, event)
	g.mu.Unlock()
	return g.audit.RunEvent(event)
}

func (g *fakeGateway) SubscribeAuditEvents(types ...string) (eventing.Subscription, error) {
	return g.audit.Subscribe(types...)
}

func (g *fakeGateway) SubscribeContextEvents(types ...string) (eventing.Subscription, error) {
	return g.ctx.Subscribe(types...)
}

func (g *fakeGateway) AuditEvents() ([]*types.Event, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*types.Event(nil), g.events...), nil
}

func (g *fakeGateway) ChannelID() string { return g.id }

func (g *fakeGateway) ofType(eventType string) []*types.Event {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*types.Event
	for _, event := range g.events {
		if event.EventType == eventType {
			out = append(out, event)
		}
	}
	return out
}

func (g *fakeGateway) requests(t *testing.T) []eventing.AsyncRunRequest {
	t.Helper()
	var out []eventing.AsyncRunRequest
	for _, event := range g.ofType(eventing.AsyncRunRequested) {
		request, err := eventing.DecodeAsyncRunRequest(event)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, request)
	}
	return out
}

// finish simulates the session's async executor running a requested turn.
func (g *fakeGateway) finish(t *testing.T, request eventing.AsyncRunRequest, response string) {
	t.Helper()
	if err := g.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunStarted(g.id, request)); err != nil {
		t.Fatal(err)
	}
	result := eventing.AsyncRunResult{RequestID: request.RequestID, Source: request.Source, SourceID: request.SourceID, Status: "succeeded", Response: response}
	if err := g.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunCompleted(g.id, result)); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func testPeers(defs ...subagents.Definition) func() ([]subagents.Definition, error) {
	if len(defs) == 0 {
		defs = []subagents.Definition{{Name: "reviewer", Description: "Reviews code", Mode: subagents.ModePeer}}
	}
	return func() ([]subagents.Definition, error) { return defs, nil }
}

func openTestRunDir(t *testing.T) *RunDir {
	t.Helper()
	runDir, err := OpenRunDir(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return runDir
}

func startRoot(t *testing.T, runDir *RunDir, gateway *fakeGateway, peers func() ([]subagents.Definition, error)) *integration {
	t.Helper()
	i, err := newIntegration(Config{
		RunDir:       runDir,
		Endpoint:     func(id string) string { return SessionEndpoint("tui", id) },
		Label:        RootLabel,
		Peers:        peers,
		LockEndpoint: true,
		PollInterval: 50 * time.Millisecond,
	}, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = i.Close() })
	return i
}

func startBot(t *testing.T, runDir *RunDir, gateway *fakeGateway, name string, acceptFrom []string, peers func() ([]subagents.Definition, error)) *integration {
	t.Helper()
	i, err := newIntegration(Config{
		RunDir:       runDir,
		Endpoint:     func(string) string { return BotEndpoint(name) },
		Label:        name,
		Self:         name,
		AcceptFrom:   acceptFrom,
		Peers:        peers,
		PollInterval: 50 * time.Millisecond,
	}, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = i.Close() })
	return i
}

func send(t *testing.T, ctx context.Context, i *integration, to, content string) (sendResult, error) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"to": to, "content": content})
	out, err := i.sendMessage(ctx, llm.ToolCall{Arguments: string(args)})
	if err != nil {
		return sendResult{}, err
	}
	var result sendResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func TestRoundTripDeliversRequestAndRoutesReplyOnce(t *testing.T) {
	runDir := openTestRunDir(t)
	rootGW := newFakeGateway("r1")
	botGW := newFakeGateway("reviewer")
	root := startRoot(t, runDir, rootGW, testPeers())
	// Bot processes hold their endpoint lock for their whole life.
	botLock, err := runDir.Lock(BotEndpoint("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = botLock.Release() }()
	startBot(t, runDir, botGW, "reviewer", nil, testPeers())

	result, err := send(t, context.Background(), root, "reviewer", "review spool.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "queued" || !result.Online || result.MessageID == "" {
		t.Fatalf("send result = %+v", result)
	}
	waitFor(t, "bot turn request", func() bool { return len(botGW.ofType(eventing.AsyncRunRequested)) == 1 })
	request := botGW.requests(t)[0]
	if request.Source != eventing.AsyncRunPeer || request.SourceID != result.MessageID || !strings.Contains(request.Prompt, "review spool.go") || !strings.Contains(request.Prompt, "from root") {
		t.Fatalf("bot request = %+v", request)
	}
	if n := runDir.PendingCount(BotEndpoint("reviewer")); n != 0 {
		t.Fatalf("bot inbox still holds %d messages", n)
	}

	botGW.finish(t, request, "looks good")
	waitFor(t, "root reply turn", func() bool { return len(rootGW.ofType(eventing.AsyncRunRequested)) == 1 })
	reply := rootGW.requests(t)[0]
	if !strings.Contains(reply.Prompt, "looks good") || !strings.Contains(reply.Prompt, "Reply from peer bot reviewer") {
		t.Fatalf("root reply prompt = %q", reply.Prompt)
	}
	if n := len(botGW.ofType(eventing.PeerReplyForwarded)); n != 1 {
		t.Fatalf("reply forwarded %d times", n)
	}

	// The root's handling of a reply never bounces back to the bot.
	rootGW.finish(t, reply, "noted")
	time.Sleep(150 * time.Millisecond)
	if n := runDir.PendingCount(BotEndpoint("reviewer")); n != 0 {
		t.Fatalf("root reply bounced %d messages back to the bot", n)
	}
	if n := len(botGW.ofType(eventing.AsyncRunRequested)); n != 1 {
		t.Fatalf("bot got %d turns, want 1", n)
	}
}

func TestOfflineBotReceivesSpooledMessageOnStart(t *testing.T) {
	runDir := openTestRunDir(t)
	rootGW := newFakeGateway("r1")
	root := startRoot(t, runDir, rootGW, testPeers())

	result, err := send(t, context.Background(), root, "reviewer", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.Online || result.Note == "" {
		t.Fatalf("offline send result = %+v", result)
	}
	if n := runDir.PendingCount(BotEndpoint("reviewer")); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}

	botGW := newFakeGateway("reviewer")
	startBot(t, runDir, botGW, "reviewer", nil, testPeers())
	waitFor(t, "spooled delivery", func() bool { return len(botGW.ofType(eventing.AsyncRunRequested)) == 1 })
}

func TestSendMessageRejectsInvalidTargetsWithoutSideEffects(t *testing.T) {
	runDir := openTestRunDir(t)
	peers := testPeers(
		subagents.Definition{Name: "reviewer", Mode: subagents.ModePeer},
		subagents.Definition{Name: "private", Mode: subagents.ModePeer, AcceptFrom: []string{"planner"}},
	)
	botGW := newFakeGateway("reviewer")
	bot := startBot(t, runDir, botGW, "reviewer", nil, peers)

	hopCtx := sessiontools.WithAsyncRequest(context.Background(), eventing.AsyncRunRequest{Source: eventing.AsyncRunPeer, SourceID: "deep"})
	bot.mu.Lock()
	bot.received["deep"] = eventing.PeerMessageReceivedPayload{MessageID: "deep", Hop: DefaultMaxHops}
	bot.mu.Unlock()

	cases := []struct {
		name, to, content string
		ctx               context.Context
		want              string
	}{
		{"unknown", "nobody", "x", context.Background(), "unknown peer bot"},
		{"self", "reviewer", "x", context.Background(), "another peer bot"},
		{"empty", "private", " ", context.Background(), "must not be empty"},
		{"accept_from", "private", "x", context.Background(), "does not accept"},
		{"hop limit", "private", "x", hopCtx, "chain limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "hop limit" {
				bot.cfg.Label = "planner"
				defer func() { bot.cfg.Label = "reviewer" }()
			}
			_, err := send(t, tc.ctx, bot, tc.to, tc.content)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if n := len(botGW.ofType(eventing.PeerMessageSent)); n != 0 {
		t.Fatalf("rejected sends recorded %d events", n)
	}
	for _, name := range []string{"nobody", "private"} {
		if n := runDir.PendingCount(BotEndpoint(name)); n != 0 {
			t.Fatalf("rejected send spooled %d messages for %s", n, name)
		}
	}
}

func TestBotToBotChainCarriesHops(t *testing.T) {
	runDir := openTestRunDir(t)
	peers := testPeers(
		subagents.Definition{Name: "a", Mode: subagents.ModePeer},
		subagents.Definition{Name: "b", Mode: subagents.ModePeer},
	)
	aGW, bGW := newFakeGateway("a"), newFakeGateway("b")
	a := startBot(t, runDir, aGW, "a", nil, peers)
	startBot(t, runDir, bGW, "b", nil, peers)

	ctx := context.Background()
	for hop := 1; hop <= DefaultMaxHops; hop++ {
		if _, err := send(t, ctx, a, "b", "ping"); err != nil {
			t.Fatalf("hop %d: %v", hop, err)
		}
		waitFor(t, "b delivery", func() bool { return len(bGW.ofType(eventing.AsyncRunRequested)) == hop })
		request := bGW.requests(t)[hop-1]
		bGW.finish(t, request, "pong")
		waitFor(t, "a reply", func() bool { return len(aGW.ofType(eventing.AsyncRunRequested)) == hop })
		reply := aGW.requests(t)[hop-1]
		ctx = sessiontools.WithAsyncRequest(context.Background(), reply)
	}
	if _, err := send(t, ctx, a, "b", "ping"); err == nil || !strings.Contains(err.Error(), "chain limit") {
		t.Fatalf("send past hop limit err = %v", err)
	}
}

func TestReceiverRejectsDisallowedSenderWithReply(t *testing.T) {
	runDir := openTestRunDir(t)
	rootGW := newFakeGateway("r1")
	open := testPeers(subagents.Definition{Name: "private", Mode: subagents.ModePeer})
	root := startRoot(t, runDir, rootGW, open)
	botGW := newFakeGateway("private")
	startBot(t, runDir, botGW, "private", []string{"planner"}, open)

	if _, err := send(t, context.Background(), root, "private", "let me in"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "rejection reply", func() bool { return len(rootGW.ofType(eventing.AsyncRunRequested)) == 1 })
	if prompt := rootGW.requests(t)[0].Prompt; !strings.Contains(prompt, "rejected") {
		t.Fatalf("rejection prompt = %q", prompt)
	}
	if n := len(botGW.ofType(eventing.AsyncRunRequested)); n != 0 {
		t.Fatalf("disallowed sender started %d bot turns", n)
	}
}

func TestTamperedAndUnsignedMessagesAreRejected(t *testing.T) {
	runDir := openTestRunDir(t)
	endpoint := BotEndpoint("reviewer")
	if err := runDir.Spool(Message{ID: "1-a", From: "session.tui.x", FromLabel: RootLabel, To: endpoint, Content: "ok", ExpectsReply: true}); err != nil {
		t.Fatal(err)
	}
	dir := runDir.inboxDir(endpoint)
	data, err := os.ReadFile(filepath.Join(dir, "1-a.json"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"content":"ok"`, `"content":"rm -rf"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "2-b.json"), []byte(strings.Replace(tampered, `"id":"1-a"`, `"id":"2-b"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	unsigned, _ := json.Marshal(Message{ID: "3-c", To: endpoint, Content: "forged"})
	if err := os.WriteFile(filepath.Join(dir, "3-c.json"), unsigned, 0o600); err != nil {
		t.Fatal(err)
	}

	messages, rejected, err := runDir.Pending(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "1-a" {
		t.Fatalf("messages = %+v", messages)
	}
	if len(rejected) != 2 {
		t.Fatalf("rejected = %v", rejected)
	}
	if _, err := os.Stat(filepath.Join(dir, "2-b.json.rejected")); err != nil {
		t.Fatalf("tampered file not set aside: %v", err)
	}
}

func TestReconcileForwardsUnforwardedReplyExactlyOnce(t *testing.T) {
	runDir := openTestRunDir(t)
	record := eventing.PeerMessageReceivedPayload{MessageID: "m1", From: SessionEndpoint("tui", "r1"), FromLabel: RootLabel, Content: "q", Hop: 1, ExpectsReply: true}
	request := eventing.AsyncRunRequest{RequestID: "peer-m1", Source: eventing.AsyncRunPeer, SourceID: "m1", Prompt: "q"}
	history := []*types.Event{
		eventing.NewPeerMessageReceived("reviewer", record),
		eventing.NewAsyncRunRequested("reviewer", request),
		eventing.NewAsyncRunStarted("reviewer", request),
		eventing.NewAsyncRunCompleted("reviewer", eventing.AsyncRunResult{RequestID: "peer-m1", Source: eventing.AsyncRunPeer, SourceID: "m1", Status: "succeeded", Response: "answer"}),
	}
	botGW := newFakeGateway("reviewer", history...)
	first := startBot(t, runDir, botGW, "reviewer", nil, testPeers())
	if n := runDir.PendingCount(record.From); n != 1 {
		t.Fatalf("reply spooled %d times, want 1", n)
	}
	_ = first.Close()

	all, _ := botGW.AuditEvents()
	restarted := newFakeGateway("reviewer", all...)
	startBot(t, runDir, restarted, "reviewer", nil, testPeers())
	if n := runDir.PendingCount(record.From); n != 1 {
		t.Fatalf("restart re-forwarded: pending = %d, want 1", n)
	}
	if n := len(restarted.ofType(eventing.AsyncRunRequested)); n != 1 {
		t.Fatalf("restart re-ran a completed turn: %d requests", n)
	}
}

func TestReconcileRequestsUnrunAndReportsInterruptedTurns(t *testing.T) {
	runDir := openTestRunDir(t)
	from := SessionEndpoint("tui", "r1")
	pending := eventing.PeerMessageReceivedPayload{MessageID: "m1", From: from, FromLabel: RootLabel, Content: "never ran", Hop: 1, ExpectsReply: true}
	interrupted := eventing.PeerMessageReceivedPayload{MessageID: "m2", From: from, FromLabel: RootLabel, Content: "crashed", Hop: 1, ExpectsReply: true}
	started := eventing.AsyncRunRequest{RequestID: "peer-m2", Source: eventing.AsyncRunPeer, SourceID: "m2", Prompt: "crashed"}
	botGW := newFakeGateway("reviewer",
		eventing.NewPeerMessageReceived("reviewer", pending),
		eventing.NewPeerMessageReceived("reviewer", interrupted),
		eventing.NewAsyncRunRequested("reviewer", started),
		eventing.NewAsyncRunStarted("reviewer", started),
	)
	startBot(t, runDir, botGW, "reviewer", nil, testPeers())

	requests := botGW.requests(t)
	if len(requests) != 2 || requests[1].SourceID != "m1" {
		t.Fatalf("requests = %+v, want m1 re-requested", requests)
	}
	messages, _, err := runDir.Pending(from)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].InReplyTo != "m2" || messages[0].Status != "interrupted" {
		t.Fatalf("interrupted reply = %+v", messages)
	}
}

func TestLockAllowsOneOwner(t *testing.T) {
	runDir := openTestRunDir(t)
	endpoint := BotEndpoint("reviewer")
	if _, live := runDir.Live(endpoint); live {
		t.Fatal("endpoint live before lock")
	}
	lock, err := runDir.Lock(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	pid, live := runDir.Live(endpoint)
	if !live || pid != os.Getpid() {
		t.Fatalf("Live = %d, %v", pid, live)
	}
	var locked *ErrLocked
	if _, err := runDir.Lock(endpoint); !errors.As(err, &locked) || locked.PID != os.Getpid() {
		t.Fatalf("second lock err = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, live := runDir.Live(endpoint); live {
		t.Fatal("endpoint live after release")
	}
	again, err := runDir.Lock(endpoint)
	if err != nil {
		t.Fatalf("relock: %v", err)
	}
	_ = again.Release()
}

func TestOpenRunDirRefusesSharedPermissions(t *testing.T) {
	configRoot, workspace := t.TempDir(), t.TempDir()
	runDir, err := OpenRunDir(configRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runDir.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRunDir(configRoot, workspace); err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("OpenRunDir err = %v", err)
	}
}

func TestSocketIgnoresMalformedPokes(t *testing.T) {
	runDir := openTestRunDir(t)
	endpoint := BotEndpoint("reviewer")
	listener, err := runDir.Listen(endpoint)
	if err != nil {
		// Sandboxed test runs may deny every socket location; delivery then
		// falls back to polling, which TestDeliveryFallsBackToPolling covers.
		t.Skipf("cannot bind a wake socket in this environment: %v", err)
	}
	defer func() { _ = listener.Close() }()
	info, err := os.Stat(runDir.SocketPath(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket mode = %v", info.Mode().Perm())
	}
	if !runDir.Poke(endpoint) {
		t.Fatal("poke failed")
	}
	select {
	case <-listener.Wake():
	case <-time.After(time.Second):
		t.Fatal("no wake after poke")
	}
	if runDir.Poke(BotEndpoint("absent")) {
		t.Fatal("poke to absent endpoint succeeded")
	}
}

func TestWorkspaceAliasesShareEndpoints(t *testing.T) {
	configRoot, real := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	direct, err := OpenRunDir(configRoot, real)
	if err != nil {
		t.Fatal(err)
	}
	viaAlias, err := OpenRunDir(configRoot, alias)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Path() != viaAlias.Path() {
		t.Fatalf("run dirs differ: %s vs %s", direct.Path(), viaAlias.Path())
	}
}

func TestDeliveryFallsBackToPollingWithoutSocket(t *testing.T) {
	runDir := openTestRunDir(t)
	// A non-empty directory where the socket belongs makes binding fail.
	for _, endpoint := range []string{BotEndpoint("reviewer"), SessionEndpoint("tui", "r1")} {
		blocker := filepath.Join(runDir.SocketPath(endpoint), "x")
		if err := os.MkdirAll(blocker, 0o700); err != nil {
			t.Skipf("cannot create socket blocker: %v", err)
		}
		if _, err := runDir.Listen(endpoint); err == nil {
			t.Fatal("Listen succeeded over a directory")
		}
	}
	rootGW, botGW := newFakeGateway("r1"), newFakeGateway("reviewer")
	root := startRoot(t, runDir, rootGW, testPeers())
	startBot(t, runDir, botGW, "reviewer", nil, testPeers())

	if _, err := send(t, context.Background(), root, "reviewer", "poll me"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "polled bot delivery", func() bool { return len(botGW.ofType(eventing.AsyncRunRequested)) == 1 })
	botGW.finish(t, botGW.requests(t)[0], "polled answer")
	waitFor(t, "polled reply", func() bool { return len(rootGW.ofType(eventing.AsyncRunRequested)) == 1 })
}

func TestSocketPathIgnoresEnvironmentAndFitsSunPath(t *testing.T) {
	deep := &RunDir{path: "/" + strings.Repeat("very-long-directory-name/", 8) + "run/abcd1234"}
	endpoint := SessionEndpoint("tui", "0123456789abcdef0123456789abcdef")
	first := deep.SocketPath(endpoint)
	t.Setenv("TMPDIR", "/"+strings.Repeat("x", 120))
	if second := deep.SocketPath(endpoint); second != first {
		t.Fatalf("socket path depends on TMPDIR: %s vs %s", first, second)
	}
	if len(first) > maxSocketPath {
		t.Fatalf("socket path is %d bytes: %s", len(first), first)
	}
	if other := deep.SocketPath(BotEndpoint("reviewer")); other == first {
		t.Fatal("endpoints share a socket path")
	}
}

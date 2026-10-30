package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/herdr"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/peers"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
)

func writePeerWorkspace(t *testing.T, agentFile string) (workspace, configRoot string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	workspace, configRoot = t.TempDir(), t.TempDir()
	agentsDir := filepath.Join(workspace, ".caretz", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte(agentFile), 0o644); err != nil {
		t.Fatal(err)
	}
	config := `{"activeModel": "local/m", "provider": {"local": {"options": {"baseURL": "http://127.0.0.1:1/v1"}, "models": {"m": {}}}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspace, configRoot
}

const confinedReviewer = `---
description: Reviews changes
mode: peer
workspace: out/reviewer
---
Review carefully.`

func peerArgs(workspace, configRoot string) []string {
	return []string{"peer", "reviewer", "--workspace", workspace, "--config-root", configRoot, "--no-model-discovery"}
}

func TestPeerCommandRunsBotOnStableSession(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	_, stderrBuf, restore := setupTUIStreams(t, "/peers\n/exit\n")
	defer restore()

	var out, errOut strings.Builder
	if code := Run(context.Background(), peerArgs(workspace, configRoot), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q / %q", code, errOut.String(), stderrBuf.String())
	}
	if !strings.Contains(stderrBuf.String(), "reviewer: this session (pid") {
		t.Fatalf("/peers output does not mark the bot's own session: %q", stderrBuf.String())
	}
	if _, err := os.Stat(filepath.Join(configRoot, "sessions", peerAdapter, "reviewer")); err != nil {
		t.Fatalf("bot session not stored under the peer adapter: %v", err)
	}
	if info, err := os.Stat(filepath.Join(workspace, "out", "reviewer")); err != nil || !info.IsDir() {
		t.Fatalf("bot workspace not created: %v", err)
	}
	runDir, err := peers.OpenRunDir(configRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, live := runDir.Live(peers.BotEndpoint("reviewer")); live {
		t.Fatal("bot endpoint still locked after exit")
	}
}

func TestPeerCommandRefusesSecondInstance(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	runDir, err := peers.OpenRunDir(configRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := runDir.Lock(peers.BotEndpoint("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	_, _, restore := setupTUIStreams(t, "/exit\n")
	defer restore()

	var out, errOut strings.Builder
	if code := Run(context.Background(), peerArgs(workspace, configRoot), &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "already running (pid") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestSessionFlagWithBotNameOpensBot(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	rootArgs := []string{"--workspace", workspace, "--config-root", configRoot, "--no-model-discovery"}
	var out, errOut strings.Builder

	_, stderrBuf, restore := setupTUIStreams(t, "/peers\n/exit\n")
	if code := Run(context.Background(), append(rootArgs, "-s", "reviewer"), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q / %q", code, errOut.String(), stderrBuf.String())
	}
	restore()
	if !strings.Contains(stderrBuf.String(), "reviewer: this session (pid") {
		t.Fatalf("-s reviewer did not open the bot: %q", stderrBuf.String())
	}
	if _, err := os.Stat(filepath.Join(configRoot, "sessions", "tui", "reviewer")); !os.IsNotExist(err) {
		t.Fatalf("root session named after the bot was created: %v", err)
	}

	// A one-shot prompt can't run as the bot.
	errOut.Reset()
	if code := Run(context.Background(), append(rootArgs, "-s", "reviewer", "-p", "hi"), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "is a peer bot") {
		t.Fatalf("-s reviewer -p: exit = %d, stderr = %q", code, errOut.String())
	}

	// --adapter still reaches a root session that shares the bot's name.
	_, stderrBuf, restore = setupTUIStreams(t, "/peers\n/exit\n")
	errOut.Reset()
	if code := Run(context.Background(), append(rootArgs, "-s", "reviewer", "--adapter", "tui"), &out, &errOut); code != 0 {
		t.Fatalf("--adapter tui: exit = %d, stderr = %q / %q", code, errOut.String(), stderrBuf.String())
	}
	restore()
	if !strings.Contains(stderrBuf.String(), "reviewer: offline") {
		t.Fatalf("--adapter tui opened the bot: %q", stderrBuf.String())
	}
}

func TestMainPeerRunsBotInPlaceOfRoot(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	cfg := `{"activeModel": "local/m", "provider": {"local": {"options": {"baseURL": "http://127.0.0.1:1/v1"}, "models": {"m": {}}}}, "profiles": {"general": {"peers": {"main": "reviewer"}}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir, err := peers.OpenRunDir(configRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	rootArgs := []string{"--workspace", workspace, "--config-root", configRoot, "--no-model-discovery"}
	var out, errOut strings.Builder

	// A plain caretz ignores peers.main and opens a root session.
	_, stderrBuf, restore := setupTUIStreams(t, "/peers\n/exit\n")
	if code := Run(context.Background(), rootArgs, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q / %q", code, errOut.String(), stderrBuf.String())
	}
	restore()
	if !strings.Contains(stderrBuf.String(), "reviewer: offline") {
		t.Fatalf("plain caretz ran as the main bot: %q", stderrBuf.String())
	}

	// --start-peers runs the main bot in place of the root session.
	_, stderrBuf, restore = setupTUIStreams(t, "/peers\n/exit\n")
	errOut.Reset()
	if code := Run(context.Background(), append(rootArgs, "--start-peers"), &out, &errOut); code != 0 {
		t.Fatalf("--start-peers: exit = %d, stderr = %q / %q", code, errOut.String(), stderrBuf.String())
	}
	restore()
	if !strings.Contains(stderrBuf.String(), "reviewer: this session (pid") {
		t.Fatalf("--start-peers did not run as the main bot: %q", stderrBuf.String())
	}

	// With the bot already running, the error points at --main-peer root.
	lock, err := runDir.Lock(peers.BotEndpoint("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, restore = setupTUIStreams(t, "/exit\n")
	errOut.Reset()
	if code := Run(context.Background(), append(rootArgs, "--start-peers"), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "--main-peer root") {
		t.Fatalf("busy main bot: exit = %d, stderr = %q", code, errOut.String())
	}
	restore()

	// --main-peer root overrides the config and opens a root session.
	_, stderrBuf, restore = setupTUIStreams(t, "/peers\n/exit\n")
	errOut.Reset()
	if code := Run(context.Background(), append(rootArgs, "--start-peers", "--main-peer", "root"), &out, &errOut); code != 0 {
		t.Fatalf("--main-peer root: exit = %d, stderr = %q / %q", code, errOut.String(), stderrBuf.String())
	}
	restore()
	_ = lock.Release()
	// The plain run and the --main-peer root run each opened a root session.
	if entries, _ := os.ReadDir(filepath.Join(configRoot, "sessions", "tui")); len(entries) != 2 {
		t.Fatalf("root sessions = %v, want two", entries)
	}

	// An explicit bot can't be combined with flags that pick a session.
	errOut.Reset()
	if code := Run(context.Background(), append(rootArgs, "--main-peer", "reviewer", "-p", "hi"), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "--main-peer") {
		t.Fatalf("--main-peer with -p: exit = %d, stderr = %q", code, errOut.String())
	}
	errOut.Reset()
	if code := Run(context.Background(), append(peerArgs(workspace, configRoot), "--main-peer", "root"), &out, &errOut); code != 2 {
		t.Fatalf("caretz peer --main-peer: exit = %d, stderr = %q", code, errOut.String())
	}
}

func TestPeerCommandRejectsSubagentsAndPrompts(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, "---\ndescription: Reviews\nmode: subagent\n---\nReview.")
	_, _, restore := setupTUIStreams(t, "/exit\n")
	defer restore()

	var out, errOut strings.Builder
	if code := Run(context.Background(), peerArgs(workspace, configRoot), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "mode: peer") {
		t.Fatalf("subagent: exit = %d, stderr = %q", code, errOut.String())
	}
	errOut.Reset()
	if code := Run(context.Background(), append(peerArgs(workspace, configRoot), "--incognito"), &out, &errOut); code != 2 {
		t.Fatalf("incognito bot: exit = %d, stderr = %q", code, errOut.String())
	}
}

func TestConfinedBotWritesOnlyItsWorkspace(t *testing.T) {
	mainDir := mustEvalSymlinks(t, t.TempDir())
	botDir := filepath.Join(mainDir, "out", "reviewer")
	if err := os.MkdirAll(botDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session, err := agent.NewAgentSession(peerAdapter, "reviewer", agent.SessionParams{
		BaseDir:              t.TempDir(),
		WorkspaceDir:         botDir,
		Logger:               log.New(),
		Provider:             &replayMockProvider{},
		ToolRegistry:         &tools.Registry{WorkspaceDir: botDir},
		SystemPrompt:         "system",
		ContextWindow:        128000,
		ReservedTokens:       16384,
		KeepRecentTokens:     20000,
		InMemorySessionStore: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	launch := &peerLaunch{
		def:     subagents.Definition{Name: "reviewer", Mode: subagents.ModePeer, SystemPrompt: "Review carefully."},
		mainDir: mainDir,
		workDir: botDir,
	}
	if err := launch.configureSession(session); err != nil {
		t.Fatal(err)
	}
	if prompt := launch.systemPrompt(); !strings.Contains(prompt, "read-only for you") || !strings.Contains(prompt, "Review carefully.") {
		t.Fatalf("system prompt = %q", prompt)
	}

	call := func(name string, args map[string]string) string {
		t.Helper()
		data, _ := json.Marshal(args)
		message, err := session.ToolRegistry().HandleToolCallContext(context.Background(), llm.ToolCall{ID: "1", Name: name, Arguments: string(data)})
		if err != nil {
			return "error: " + err.Error()
		}
		return message.Content
	}
	if got := call("Read", map[string]string{"file_path": filepath.Join(mainDir, "main.go")}); !strings.Contains(got, "package main") {
		t.Fatalf("reading the main workspace failed: %q", got)
	}
	call("Write", map[string]string{"file_path": filepath.Join(mainDir, "main.go"), "content": "clobbered"})
	if data, _ := os.ReadFile(filepath.Join(mainDir, "main.go")); string(data) != "package main\n" {
		t.Fatalf("confined bot wrote to the main workspace: %q", data)
	}
	call("Write", map[string]string{"file_path": filepath.Join(botDir, "notes.md"), "content": "ok"})
	if data, err := os.ReadFile(filepath.Join(botDir, "notes.md")); err != nil || string(data) != "ok" {
		t.Fatalf("confined bot could not write its workspace: %v %q", err, data)
	}
}

func TestRuntimeHidesPeerRunDirectoryFromTools(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	t.Setenv("OPENAI_API_MODEL", "m")
	t.Setenv("OPENAI_API_BASE_URL", "http://127.0.0.1:1/v1")
	flags := baseFlagValues{Workspace: workspace, ConfigRoot: configRoot, DisableModelDiscovery: true, Changed: map[string]bool{"config-root": true, "no-model-discovery": true}}
	prepareBaseFlags(&flags)
	var errOut strings.Builder
	rt, code := buildRuntimeFromFlags(context.Background(), flags, log.New(), &errOut, Options{})
	if code != 0 {
		t.Fatalf("buildRuntimeFromFlags exit = %d: %s", code, errOut.String())
	}
	runRoot := peers.RunRoot(rt.ConfigRoot)
	found := false
	for _, hidden := range rt.SandboxSettings.HiddenPaths {
		if hidden == runRoot {
			found = true
		}
	}
	if !found {
		t.Fatalf("hidden paths %v do not include %s", rt.SandboxSettings.HiddenPaths, runRoot)
	}
	if _, err := peers.OpenRunDir(rt.ConfigRoot, workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "probe"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	message, err := rt.ToolRegistry.HandleToolCallContext(context.Background(), llm.ToolCall{ID: "1", Name: "Read", Arguments: `{"file_path": "` + filepath.Join(runRoot, "probe") + `"}`})
	if err == nil && strings.TrimSpace(message.Content) == "x" {
		t.Fatal("Read tool can see the peer run directory")
	}
}

func TestSandboxedBashCannotReachPeerRunDirectory(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	t.Setenv("OPENAI_API_MODEL", "m")
	t.Setenv("OPENAI_API_BASE_URL", "http://127.0.0.1:1/v1")
	flags := baseFlagValues{Workspace: workspace, ConfigRoot: configRoot, DisableModelDiscovery: true, Changed: map[string]bool{"config-root": true, "no-model-discovery": true}}
	prepareBaseFlags(&flags)
	var errOut strings.Builder
	rt, code := buildRuntimeFromFlags(context.Background(), flags, log.New(), &errOut, Options{})
	if code != 0 {
		t.Fatalf("buildRuntimeFromFlags exit = %d: %s", code, errOut.String())
	}
	if rt.SandboxSettings.Disabled {
		t.Skip("sandbox disabled in this environment")
	}
	runDir, err := peers.OpenRunDir(rt.ConfigRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(runDir.Path(), "key")
	spool := filepath.Join(runDir.Path(), "inbox", "forged.json")
	command := "cat " + key + "; echo forged > " + spool
	data, _ := json.Marshal(map[string]string{"command": command})
	registry, _ := rt.ToolRegistry.BindSessionExecution()
	t.Cleanup(func() { _ = registry.CloseSessionExecution() })
	message, err := registry.HandleToolCallContext(context.Background(), llm.ToolCall{ID: "1", Name: "Bash", Arguments: string(data)})
	t.Logf("sandboxed Bash output: %q (err %v)", message.Content, err)
	if err == nil && strings.Contains(message.Content, string(runDirKey(t, key))) {
		t.Fatal("sandboxed Bash read the peer signing key")
	}
	if _, err := os.Stat(spool); err == nil {
		t.Fatal("sandboxed Bash wrote into the peer spool")
	}
}

func runDirKey(t *testing.T, path string) []byte {
	t.Helper()
	key, err := os.ReadFile(path)
	if err != nil || len(key) == 0 {
		t.Fatalf("read key: %v", err)
	}
	return key
}

func TestPeerCommandInheritsRootFlagsAndDefinitionProfile(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, "---\ndescription: Reviews\nmode: peer\nprofile: review\n---\nReview.")
	config := `{"activeModel": "local/m", "provider": {"local": {"options": {"baseURL": "http://127.0.0.1:1/v1"}, "models": {"m": {}}}}, "profiles": {"review": {"plan": true}, "other": {}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionsDir := filepath.Join(configRoot, "sessions")

	run := func(extra ...string) sessionRuntimeState {
		t.Helper()
		_, stderrBuf, restore := setupTUIStreams(t, "/exit\n")
		defer restore()
		var out, errOut strings.Builder
		args := append(peerArgs(workspace, configRoot), extra...)
		if code := Run(context.Background(), args, &out, &errOut); code != 0 {
			t.Fatalf("exit = %d, stderr = %q / %q", code, errOut.String(), stderrBuf.String())
		}
		state, found, err := loadSessionRuntime(sessionRuntimePath(sessionsDir, peerAdapter, "reviewer"))
		if err != nil || !found {
			t.Fatalf("session runtime: found=%v err=%v", found, err)
		}
		return state
	}
	// Root flags such as --unrestricted-network parse on `caretz peer`.
	if state := run("--unrestricted-network", "-v"); state.Profile != "review" {
		t.Fatalf("profile = %q, want the definition's review", state.Profile)
	}
	// A flag typed on `caretz peer` wins over the definition.
	if state := run("--profile", "other"); state.Profile != "other" {
		t.Fatalf("profile = %q, want explicit other", state.Profile)
	}
}

func TestPeerCommandRejectsSessionSelectionFlags(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	_, _, restore := setupTUIStreams(t, "/exit\n")
	defer restore()
	for _, extra := range [][]string{{"-p", "hi"}, {"-s", "abc"}, {"--adapter", "cli"}} {
		var out, errOut strings.Builder
		if code := Run(context.Background(), append(peerArgs(workspace, configRoot), extra...), &out, &errOut); code != 2 {
			t.Fatalf("%v: exit = %d, stderr = %q", extra, code, errOut.String())
		}
	}
}

type fakeBotSpawner struct{}

func (fakeBotSpawner) SpawnTab(context.Context, string, string, herdr.Layout, []herdr.PaneCommand) ([]string, error) {
	return nil, nil
}

func stubBotSpawner(t *testing.T, spawner botSpawner) {
	t.Helper()
	previous := spawnerFromEnvironment
	spawnerFromEnvironment = func() botSpawner { return spawner }
	t.Cleanup(func() { spawnerFromEnvironment = previous })
}

func peerRuntime(workspace, configRoot string) *Runtime {
	return &Runtime{CWD: workspace, ConfigRoot: configRoot, AgentsDirs: []string{filepath.Join(workspace, ".caretz", "agents")}}
}

func TestRootStartsBotsOnlyWithStartPeers(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	rt := peerRuntime(workspace, configRoot)
	var errOut strings.Builder
	plain := rootPeerOptions(rt, log.New(), &errOut, nil)
	if plain.Integration == nil || plain.OnStart != nil {
		t.Fatalf("plain root: integration set = %v, starts bots = %v", plain.Integration != nil, plain.OnStart != nil)
	}
	stubBotSpawner(t, fakeBotSpawner{})
	start, err := prepareBotStart(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if started := rootPeerOptions(rt, log.New(), &errOut, start); started.Integration == nil || started.OnStart == nil {
		t.Fatalf("--start-peers root: integration set = %v, starts bots = %v", started.Integration != nil, started.OnStart != nil)
	}
	// Bots never inherit the flag, so a bot cannot start its siblings.
	if !slices.Contains(sessionSelectionFlags, "start-peers") {
		t.Fatalf("sessionSelectionFlags = %v, want start-peers excluded from bot flags", sessionSelectionFlags)
	}
}

func TestStartPeersFailsWithoutMultiplexer(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	stubBotSpawner(t, nil)
	_, err := prepareBotStart(peerRuntime(workspace, configRoot), []string{"--profile=work"})
	if err == nil || !strings.Contains(err.Error(), "Herdr or tmux") || !strings.Contains(err.Error(), "\n  caretz peer reviewer --profile=work") {
		t.Fatalf("err = %v", err)
	}
	// The check runs before any session is created.
	var out, errOut strings.Builder
	args := []string{"--workspace", workspace, "--config-root", configRoot, "--no-model-discovery", "--start-peers"}
	if code := Run(context.Background(), args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "caretz peer reviewer") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(configRoot, "sessions")); len(entries) != 0 {
		t.Fatalf("session files created: %v", entries)
	}
}

func TestStartPeersNeedsBotsAndInteractiveSession(t *testing.T) {
	workspace, configRoot := writePeerWorkspace(t, confinedReviewer)
	stubBotSpawner(t, fakeBotSpawner{})
	if err := os.Remove(filepath.Join(workspace, ".caretz", "agents", "reviewer.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareBotStart(peerRuntime(workspace, configRoot), nil); err == nil || !strings.Contains(err.Error(), "no peer bots are configured") {
		t.Fatalf("err = %v", err)
	}
	var out, errOut strings.Builder
	args := []string{"--workspace", workspace, "--config-root", configRoot, "--start-peers", "-p", "hi"}
	if code := Run(context.Background(), args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "cannot be combined with -p") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
}

func TestBotCommandForwardsRootFlags(t *testing.T) {
	rootFlags := []string{"--profile=foo", "--v=true", "--read-root=a", "--read-root=b", "--model=big", "--unrestricted-network=true"}
	plain := botCommand("/bin/caretz", subagents.Definition{Name: "reviewer"}, rootFlags, "/cfg", false)
	if got, want := strings.Join(plain, " "), "/bin/caretz peer reviewer --profile=foo --v=true --read-root=a --read-root=b --model=big --unrestricted-network=true"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	// The definition's own model and profile win over the root's.
	owned := botCommand("/bin/caretz", subagents.Definition{Name: "reviewer", Model: "local/m", Profile: "review"}, rootFlags, "/cfg", false)
	if got := strings.Join(owned, " "); strings.Contains(got, "--model=") || strings.Contains(got, "--profile=") || !strings.Contains(got, "--read-root=b") {
		t.Fatalf("command = %q", got)
	}
	// A config root found through CARETZ_CONFIG_ROOT is passed explicitly.
	if got := strings.Join(botCommand("caretz", subagents.Definition{Name: "r"}, nil, "/cfg", true), " "); got != "caretz peer r --config-root /cfg" {
		t.Fatalf("command = %q", got)
	}
	if got := strings.Join(botCommand("caretz", subagents.Definition{Name: "r"}, []string{"--config-root=x"}, "/cfg", true), " "); got != "caretz peer r --config-root=x" {
		t.Fatalf("command = %q", got)
	}
}

func TestBotTabsFollowPeersLayout(t *testing.T) {
	offline := []subagents.Definition{{Name: "coordinator"}, {Name: "impl-a"}, {Name: "impl-b"}, {Name: "reviewer"}}
	layout := &config.Peers{Tabs: []config.PeerTab{
		{Name: "coordinator", Bots: []string{"coordinator"}},
		{Name: "implementers", Bots: []string{"impl-a", "impl-b"}, Layout: "rows"},
		{Name: "idle", Bots: []string{"missing"}},
	}}
	summary := func(tabs []botTab) string {
		var parts []string
		for _, tab := range tabs {
			var names []string
			for _, def := range tab.defs {
				names = append(names, def.Name)
			}
			parts = append(parts, fmt.Sprintf("%s(%s):%s", tab.label, tab.layout, strings.Join(names, ",")))
		}
		return strings.Join(parts, " ")
	}
	if got, want := summary(botTabs(nil, offline)), "bots():coordinator,impl-a,impl-b,reviewer"; got != want {
		t.Fatalf("no layout: %s, want %s", got, want)
	}
	// Unlisted bots go to a trailing bots tab; tabs with no offline bot are skipped.
	if got, want := summary(botTabs(layout, offline)), "coordinator():coordinator implementers(rows):impl-a,impl-b bots():reviewer"; got != want {
		t.Fatalf("layout: %s, want %s", got, want)
	}
	// A configured bots tab collects the unlisted bots in its position.
	layout.Tabs = append([]config.PeerTab{{Name: "bots", Layout: "columns"}}, layout.Tabs...)
	if got, want := summary(botTabs(layout, offline[1:])), "bots(columns):reviewer implementers(rows):impl-a,impl-b"; got != want {
		t.Fatalf("bots tab: %s, want %s", got, want)
	}
}

func TestPeerResumeCommandKeepsFlags(t *testing.T) {
	launch := &peerLaunch{def: subagents.Definition{Name: "reviewer"}, flags: []string{"--profile=foo", "--plan=true"}}
	got := launch.resumeCommand()
	if len(got) != 5 || got[0] != "caretz" || got[1] != peerAdapter || got[2] != "reviewer" || got[3] != "--profile=foo" || got[4] != "--plan=true" {
		t.Fatalf("resume command = %q", got)
	}
}

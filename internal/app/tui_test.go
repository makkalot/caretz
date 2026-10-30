package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

func TestReadPermissionDecisionDefaultsToDenyAndAcceptsShortcut(t *testing.T) {
	request := eventing.PermissionRequest{ToolName: "Read", Capability: "path_read", Target: "/outside"}
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "default deny", input: "\n", want: eventing.PermissionDecisionDeny},
		{name: "allow shortcut", input: "y", want: eventing.PermissionDecisionAllow},
		{name: "session shortcut", input: "s", want: eventing.PermissionDecisionAllowSession},
		{name: "select session", input: "\x1b[D\n", want: eventing.PermissionDecisionAllowSession},
		{name: "select allow", input: "\x1b[D\x1b[D\n", want: eventing.PermissionDecisionAllow},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			terminal := ansi.NewTerminal(strings.NewReader(test.input), &output)
			decision, err := readPermissionDecision(terminal, request)
			if err != nil {
				t.Fatal(err)
			}
			if decision != test.want {
				t.Fatalf("decision = %q, want %q", decision, test.want)
			}
			if !strings.Contains(output.String(), "[ Allow once ]") || !strings.Contains(output.String(), "[ Allow for session ]") || !strings.Contains(output.String(), "[>Deny<]") {
				t.Fatalf("approval controls missing: %q", output.String())
			}
		})
	}
}

// A request that releases content shows it before the user decides, and one
// that can't be granted for the session doesn't offer that choice.
func TestReadPermissionDecisionShowsPreviewAndHonorsNoSessionGrant(t *testing.T) {
	request := eventing.PermissionRequest{ToolName: "ask_expert", Capability: "custom_send", Target: "expert", Preview: "line one\n\x1b[2Kline two", NoSessionGrant: true}
	var output bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader("s\x1b[D\n"), &output)
	decision, err := readPermissionDecision(terminal, request)
	if err != nil {
		t.Fatal(err)
	}
	// 's' is not a shortcut here, so Left then Enter selects Allow once.
	if decision != eventing.PermissionDecisionAllow {
		t.Fatalf("decision = %q, want allow", decision)
	}
	got := output.String()
	if !strings.Contains(got, "line one\n    line two") || strings.Contains(got, "\x1b[2K") {
		t.Fatalf("preview missing or not stripped: %q", got)
	}
	if strings.Contains(got, "Allow for session") {
		t.Fatalf("session grant offered for NoSessionGrant request: %q", got)
	}
}

// The button row is redrawn in place with \r; a row wider than the terminal
// wraps and every redraw then leaves a stale copy behind.
func TestSandboxOffPromptRowFitsNarrowTerminals(t *testing.T) {
	request := eventing.PermissionRequest{ToolName: "Bash", Capability: tools.PermissionSandboxOff, Target: "sudo true", Command: "sudo true", Reason: "sandbox blocked: os sudo cannot gain privileges inside the sandbox", MayHaveRun: true}
	var output bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader("\t\t\t\n"), &output)
	if _, err := readPermissionDecision(terminal, request); err != nil {
		t.Fatal(err)
	}
	ansiCodes := regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	redraws := strings.Split(output.String(), "\r")[1:]
	if len(redraws) != 4 {
		t.Fatalf("redraws = %d, want 4: %q", len(redraws), output.String())
	}
	for _, row := range redraws {
		row = strings.TrimRight(ansiCodes.ReplaceAllString(row, ""), "\n")
		if len(row) > 60 {
			t.Fatalf("button row is %d columns, wraps on narrow terminals: %q", len(row), row)
		}
	}
	if !strings.Contains(output.String(), "\x1b[K") {
		t.Fatal("redraw does not clear the rest of the line")
	}
}

// tuiMockProvider is a minimal llm.Provider that returns a fixed assistant
// response with no tool calls, so an agent session can run a single prompt
// without any network or tooling.
type tuiMockProvider struct {
	content string
}

func (m *tuiMockProvider) Chat(string, []llm.Message, []llm.ToolDefinition, func(string)) (llm.Response, error) {
	return llm.Response{Content: m.content, Message: llm.Message{Role: llm.RoleAssistant, Content: m.content}}, nil
}

func (m *tuiMockProvider) GetModel() string   { return "tui-mock" }
func (m *tuiMockProvider) GetBaseURL() string { return "http://mock" }

// newTUISession builds an in-memory agent session backed by the mock provider.
func newTUISession(t *testing.T, channelID string) *agent.AgentSession {
	t.Helper()
	session, err := agent.NewAgentSession("tui", channelID, agent.SessionParams{
		InMemorySessionStore: true,
		Provider:             &tuiMockProvider{content: "assistant reply"},
	})
	if err != nil {
		t.Fatalf("NewAgentSession() error = %v", err)
	}
	return session
}

// newTestRegistry builds a command registry containing the real /clear command
// so session switching can be exercised end to end.
func newTestRegistry() *slashcmd.Registry {
	registry := slashcmd.NewRegistry()
	if err := registry.Register(slashcmd.ClearCommand{}); err != nil {
		panic(err)
	}
	return registry
}

type lifecycleCall struct {
	state, message, sessionID string
}

type fakeLifecycleReporter struct {
	calls  []lifecycleCall
	closed bool
}

func (r *fakeLifecycleReporter) Report(state, message, sessionID string) {
	r.calls = append(r.calls, lifecycleCall{state: state, message: message, sessionID: sessionID})
}

func (r *fakeLifecycleReporter) Close() { r.closed = true }

// setupTUIStreams swaps the package-level stdin/stdout/stderr with fakes and
// redirects the real OS stdout/stderr (used by the output handlers) to
// /dev/null so the test run stays quiet. It returns a restore function.
func setupTUIStreams(t *testing.T, input string) (*bytes.Buffer, *bytes.Buffer, func()) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}

	oldStdin, oldStdout, oldStderr := stdin, stdout, stderr
	stdoutBuf := &bytes.Buffer{}
	stderrBuf := &bytes.Buffer{}

	stdin = strings.NewReader(input)
	stdout = stdoutBuf
	stderr = stderrBuf

	realStdout, realStderr := os.Stdout, os.Stderr
	os.Stdout = devNull
	os.Stderr = devNull

	return stdoutBuf, stderrBuf, func() {
		stdin = oldStdin
		stdout = oldStdout
		stderr = oldStderr
		os.Stdout = realStdout
		os.Stderr = realStderr
		_ = devNull.Close()
	}
}

func TestStartTUIExitCommand(t *testing.T) {
	session := newTUISession(t, "tui-exit")
	_, stderrBuf, restore := setupTUIStreams(t, "/exit\n")
	defer restore()

	resources := startupResources{
		Context: []string{"AGENTS.md"},
		Skills:  []string{"add-llm-provider"},
		Agents:  []string{"review", "research"},
	}
	if err := startTUI(session, log.New(), "model", "", "simple", t.TempDir(), nil, newTestRegistry(), nil, false, output.ModeCompact, true, "", false, resources, scheduleDefaults(nil), tuiHooks{}, tuiPeerOptions{}); err != nil {
		t.Fatalf("startTUI() error = %v", err)
	}

	if !strings.Contains(stderrBuf.String(), "caretz · profile=simple · /help for commands, /exit to quit\n") {
		t.Fatalf("banner missing from stderr: %q", stderrBuf.String())
	}
	// Model, mode and context usage belong to the prompt footer.
	for _, duplicate := range []string{"interactive mode", "[model]", "% used)", "Shift+Tab"} {
		if strings.Contains(stderrBuf.String(), duplicate) {
			t.Fatalf("banner repeats footer detail %q: %q", duplicate, stderrBuf.String())
		}
	}
	for _, want := range []string{"\nContext\n", "AGENTS.md", "\nSkills\n", "add-llm-provider", "\nAgents\n", "review", "research"} {
		if !strings.Contains(stderrBuf.String(), want) {
			t.Fatalf("startup resources missing %q: %q", want, stderrBuf.String())
		}
	}
	for _, unwanted := range []string{"reasoning=", "integrations/tui", "Charm UI"} {
		if strings.Contains(stderrBuf.String(), unwanted) {
			t.Fatalf("banner contains obsolete text %q: %q", unwanted, stderrBuf.String())
		}
	}
}

func TestStartTUICommandExecution(t *testing.T) {
	session := newTUISession(t, "tui-cmd")
	registry := slashcmd.NewRegistry()
	if err := registry.Register(slashcmd.HelpCommand{Registry: registry}); err != nil {
		t.Fatal(err)
	}
	_, stderrBuf, restore := setupTUIStreams(t, "/help\n/exit\n")
	defer restore()

	if err := startTUI(session, log.New(), "model", "", "general", t.TempDir(), nil, registry, nil, false, output.ModeCompact, true, "", false, startupResources{}, scheduleDefaults(nil), tuiHooks{}, tuiPeerOptions{}); err != nil {
		t.Fatalf("startTUI() error = %v", err)
	}

	if !strings.Contains(stderrBuf.String(), "Available commands:") {
		t.Fatalf("command message missing from stderr: %q", stderrBuf.String())
	}
}

func TestTUIEchoesHandledSlashCommand(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTUISession(t, "tui-command-echo")
	defer func() { _ = session.Close() }()
	registry := slashcmd.NewRegistry()
	if err := registry.Register(slashcmd.HelpCommand{Registry: registry}); err != nil {
		t.Fatal(err)
	}

	oldOSStderr, oldStderr := os.Stderr, stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	stderr = &bytes.Buffer{}
	t.Cleanup(func() {
		os.Stderr = oldOSStderr
		stderr = oldStderr
		_ = r.Close()
		_ = w.Close()
	})

	state := &tuiState{session: session, cliOutput: output.NewCLIOutput(output.ModeDetailed), registry: registry}
	if _, err := state.processLineWith("/help", func(*agent.AgentSession, string) error {
		t.Fatal("slash command reached prompt runner")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "You: /help") {
		t.Fatalf("slash command echo missing: %q", got)
	}
}

func TestTUIReportsPromptLifecycle(t *testing.T) {
	session := newTUISession(t, "tui-herdr")
	defer func() { _ = session.Close() }()
	reporter := &fakeLifecycleReporter{}
	state := &tuiState{session: session, cliOutput: output.NewCLIOutput(output.ModeCompact), registry: slashcmd.NewRegistry(), lifecycle: reporter}
	if _, err := state.processLineWith("hello", func(*agent.AgentSession, string) error { return errors.New("turn failed") }); err != nil {
		t.Fatal(err)
	}
	want := []lifecycleCall{
		{state: "working", sessionID: "tui-herdr"},
		{state: "idle", sessionID: "tui-herdr"},
	}
	if !reflect.DeepEqual(reporter.calls, want) {
		t.Fatalf("lifecycle calls = %#v, want %#v", reporter.calls, want)
	}
}

func TestTUIReportsNewSessionAfterClear(t *testing.T) {
	session := newTUISession(t, "tui-herdr-clear")
	reporter := &fakeLifecycleReporter{}
	state := &tuiState{session: session, cliOutput: output.NewCLIOutput(output.ModeCompact), registry: newTestRegistry(), lifecycle: reporter}
	t.Cleanup(func() { state.close() })
	if _, err := state.processLineWith("/clear", func(*agent.AgentSession, string) error {
		t.Fatal("clear reached prompt runner")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(reporter.calls) != 2 || reporter.calls[0].state != "working" || reporter.calls[0].sessionID != "tui-herdr-clear" {
		t.Fatalf("unexpected lifecycle calls: %#v", reporter.calls)
	}
	if reporter.calls[1].state != "idle" || reporter.calls[1].sessionID == "tui-herdr-clear" || reporter.calls[1].sessionID == "" {
		t.Fatalf("new session was not reported idle: %#v", reporter.calls)
	}
}

func TestPermissionLifecycleReportsBlockedThenWorking(t *testing.T) {
	session := newTUISession(t, "tui-herdr-permission")
	defer func() { _ = session.Close() }()
	reporter := &fakeLifecycleReporter{}
	lifecycle := permissionLifecycle(reporter, session)
	lifecycle.blocked(eventing.PermissionRequest{ToolName: "Bash"})
	lifecycle.working()
	want := []lifecycleCall{
		{state: "blocked", message: "Permission required by Bash", sessionID: "tui-herdr-permission"},
		{state: "working", sessionID: "tui-herdr-permission"},
	}
	if !reflect.DeepEqual(reporter.calls, want) {
		t.Fatalf("permission lifecycle = %#v, want %#v", reporter.calls, want)
	}
}

func TestTUIRunsBangPromptWithoutCallingModel(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	workspace := t.TempDir()
	var gotCommand string
	toolRegistry := &tools.Registry{
		WorkspaceDir: workspace,
		RunSandboxed: func(_ string, command string) (string, string, error) {
			gotCommand = command
			return "file.txt\n", "", nil
		},
	}
	session, err := agent.NewAgentSession("tui", "tui-bang", agent.SessionParams{
		InMemorySessionStore: true,
		WorkspaceDir:         workspace,
		Provider:             &tuiMockProvider{content: "assistant reply"},
		ToolRegistry:         toolRegistry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	oldStdout, oldStderr := stdout, stderr
	var stdoutBuf, stderrBuf bytes.Buffer
	stdout, stderr = &stdoutBuf, &stderrBuf
	t.Cleanup(func() { stdout, stderr = oldStdout, oldStderr })

	state := &tuiState{session: session, cliOutput: output.NewCLIOutput(output.ModeCompact), registry: slashcmd.NewRegistry()}
	if _, err := state.processLineWith("!ls", func(*agent.AgentSession, string) error {
		t.Fatal("bang prompt reached model prompt runner")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if gotCommand != "ls" || stdoutBuf.String() != "file.txt\n" {
		t.Fatalf("command/output = %q/%q, want ls/file.txt", gotCommand, stdoutBuf.String())
	}
}

type tuiProgressCommand struct{}

func (tuiProgressCommand) Name() string        { return "progress-test" }
func (tuiProgressCommand) Description() string { return "test command progress" }
func (tuiProgressCommand) Usage() string       { return "/progress-test" }
func (tuiProgressCommand) Aliases() []string   { return nil }
func (tuiProgressCommand) Run(ctx context.Context, _ *agent.AgentSession, _ string) (slashcmd.Result, error) {
	slashcmd.EmitProgress(ctx, "solve evidence planning started")
	return slashcmd.Result{Message: "evidence ready"}, nil
}

func TestTUIShowsSlashCommandProgress(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTUISession(t, "tui-command-progress")
	defer func() { _ = session.Close() }()
	registry := slashcmd.NewRegistry()
	if err := registry.Register(tuiProgressCommand{}); err != nil {
		t.Fatal(err)
	}
	oldOSStderr, oldStderr := os.Stderr, stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	resultBuf := &bytes.Buffer{}
	stderr = resultBuf
	t.Cleanup(func() {
		os.Stderr = oldOSStderr
		stderr = oldStderr
		_ = r.Close()
		_ = w.Close()
	})

	state := &tuiState{session: session, cliOutput: output.NewCLIOutput(output.ModeCompact), registry: registry}
	if _, err := state.processLineWith("/progress-test", func(*agent.AgentSession, string) error {
		t.Fatal("slash command reached prompt runner")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	progress, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(progress), "solve evidence planning started") {
		t.Fatalf("command progress missing: %q", progress)
	}
	if !strings.Contains(resultBuf.String(), "evidence ready") {
		t.Fatalf("command result missing: %q", resultBuf.String())
	}
}

type tuiPublishedCommand struct{}

func (tuiPublishedCommand) Name() string        { return "published-test" }
func (tuiPublishedCommand) Description() string { return "publish a command result" }
func (tuiPublishedCommand) Usage() string       { return "/published-test" }
func (tuiPublishedCommand) Aliases() []string   { return nil }
func (tuiPublishedCommand) Run(_ context.Context, session *agent.AgentSession, _ string) (slashcmd.Result, error) {
	const message = "Finished once"
	published, err := session.RecordCommandOutcome("published-test", message)
	return slashcmd.Result{Message: message, MessagePublished: published}, err
}

func TestTUIPublishedCommandResultAppearsOnce(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTUISession(t, "tui-published-command")
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close TUI session: %v", err)
		}
	}()
	registry := slashcmd.NewRegistry()
	if err := registry.Register(tuiPublishedCommand{}); err != nil {
		t.Fatal(err)
	}
	cliOutput := output.NewCLIOutput(output.ModeCompact)
	if err := cliOutput.RegisterHandlers(session); err != nil {
		t.Fatal(err)
	}
	oldStdout, oldStderr := os.Stdout, stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	stderrBuf := &bytes.Buffer{}
	stderr = stderrBuf
	t.Cleanup(func() {
		os.Stdout = oldStdout
		stderr = oldStderr
		_ = r.Close()
		_ = w.Close()
	})
	state := &tuiState{session: session, cliOutput: cliOutput, registry: registry}
	if _, err := state.processLineWith("/published-test", func(*agent.AgentSession, string) error {
		t.Fatal("slash command reached prompt runner")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	printed, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(printed), "Finished once") != 1 || strings.Contains(stderrBuf.String(), "Finished once") {
		t.Fatalf("published command result duplicated: stdout=%q stderr=%q", printed, stderrBuf.String())
	}
}

func TestStartTUIBlankInputIgnored(t *testing.T) {
	session := newTUISession(t, "tui-blank")
	_, _, restore := setupTUIStreams(t, "\n   \n\t\n/exit\n")
	defer restore()

	// Blank and whitespace-only lines must be ignored, not sent to the agent.
	if err := startTUI(session, log.New(), "model", "", "general", t.TempDir(), nil, newTestRegistry(), nil, false, output.ModeCompact, true, "", false, startupResources{}, scheduleDefaults(nil), tuiHooks{}, tuiPeerOptions{}); err != nil {
		t.Fatalf("startTUI() error = %v", err)
	}

	if got := session.ActiveMessageCount(); got != 0 {
		t.Fatalf("blank input created %d messages, want 0", got)
	}
}

func TestStartTUISessionSwitching(t *testing.T) {
	session := newTUISession(t, "tui-switch")
	// Seed the original session so the fresh sibling is observably different.
	if _, err := session.Call(context.Background(), "seed"); err != nil {
		t.Fatalf("seed Call() error = %v", err)
	}
	if session.ActiveMessageCount() == 0 {
		t.Fatal("seeded session has no messages")
	}

	_, stderrBuf, restore := setupTUIStreams(t, "/clear\n/exit\n")
	defer restore()

	if err := startTUI(session, log.New(), "model", "", "general", t.TempDir(), nil, newTestRegistry(), nil, false, output.ModeCompact, true, "", false, startupResources{}, scheduleDefaults(nil), tuiHooks{}, tuiPeerOptions{}); err != nil {
		t.Fatalf("startTUI() error = %v", err)
	}

	if !strings.Contains(stderrBuf.String(), "cleared session: tui-switch → ") {
		t.Fatalf("session switch message missing from stderr: %q", stderrBuf.String())
	}
}

func TestStartTUIScannerFallback(t *testing.T) {
	session := newTUISession(t, "tui-scan")
	_, _, restore := setupTUIStreams(t, "hello\n/exit\n")
	defer restore()

	// stdin is a *strings.Reader (not an *os.File), so startTUI must take the
	// scanner fallback path rather than the raw terminal editor. The prompt
	// "hello" should reach the agent and produce a live context.
	if err := startTUI(session, log.New(), "model", "", "general", t.TempDir(), nil, newTestRegistry(), nil, false, output.ModeCompact, true, "", false, startupResources{}, scheduleDefaults(nil), tuiHooks{}, tuiPeerOptions{}); err != nil {
		t.Fatalf("startTUI() error = %v", err)
	}

	if got := session.ActiveMessageCount(); got == 0 {
		t.Fatal("scanner fallback did not process the prompt; session has no messages")
	}
}

func TestRunRawTUISeparatesPrompts(t *testing.T) {
	session := newTUISession(t, "tui-raw-spacing")
	_, stderrBuf, restore := setupTUIStreams(t, "\n/exit\n")
	defer restore()

	state := &tuiState{
		session:    session,
		cliOutput:  output.NewCLIOutput(output.ModeCompact),
		outputMode: output.ModeCompact,
		registry:   newTestRegistry(),
	}

	if err := runRawTUI(state, t.TempDir(), nil); err != nil {
		t.Fatalf("runRawTUI() error = %v", err)
	}

	// Frame rows end in \r\n; a bare \n before the top rule is the blank
	// separator line.
	if !strings.Contains(strings.ReplaceAll(ansi.Strip(stderrBuf.String()), "\r\n", "\r"), "\n─") {
		t.Fatalf("second prompt was not separated by a blank line: %q", stderrBuf.String())
	}
}

func TestRunRawTUIClearCommandClearsScreenAndStartsNewSession(t *testing.T) {
	session := newTUISession(t, "before-clear")
	_, stderrBuf, restore := setupTUIStreams(t, "/clear\n/exit\n")
	defer restore()

	state := &tuiState{
		session:    session,
		cliOutput:  output.NewCLIOutput(output.ModeCompact),
		outputMode: output.ModeCompact,
		registry:   newTestRegistry(),
	}
	defer state.close()

	if err := runRawTUI(state, t.TempDir(), nil); err != nil {
		t.Fatalf("runRawTUI() error = %v", err)
	}

	if state.session.ChannelID() == "before-clear" {
		t.Fatal("/clear did not start a new session")
	}
	if !strings.Contains(stderrBuf.String(), "\x1b[2J\x1b[H") {
		t.Fatalf("full-screen clear sequence missing from stderr: %q", stderrBuf.String())
	}
	if !strings.Contains(ansi.Strip(stderrBuf.String()), "cleared session: before-clear → "+state.session.ChannelID()) {
		t.Fatalf("session switch message missing from stderr: %q", stderrBuf.String())
	}
}

func TestRunRawTUICtrlCShowsResumeCommand(t *testing.T) {
	session := newTUISession(t, "resume-me")
	_, stderrBuf, restore := setupTUIStreams(t, "\x03")
	defer restore()

	state := &tuiState{
		session:    session,
		cliOutput:  output.NewCLIOutput(output.ModeCompact),
		outputMode: output.ModeCompact,
		registry:   newTestRegistry(),
		resumable:  true,
	}

	if err := runRawTUI(state, t.TempDir(), nil); err != nil {
		t.Fatalf("runRawTUI() error = %v", err)
	}

	if !strings.Contains(ansi.Strip(stderrBuf.String()), "Resume this session with: caretz -s resume-me") {
		t.Fatalf("resume command missing from stderr: %q", stderrBuf.String())
	}
}

func TestRunRawTUICtrlCDoesNotOfferIncognitoResume(t *testing.T) {
	session := newTUISession(t, "incognito")
	_, stderrBuf, restore := setupTUIStreams(t, "\x03")
	defer restore()

	state := &tuiState{
		session:    session,
		cliOutput:  output.NewCLIOutput(output.ModeCompact),
		outputMode: output.ModeCompact,
		registry:   newTestRegistry(),
		resumable:  false,
	}

	if err := runRawTUI(state, t.TempDir(), nil); err != nil {
		t.Fatalf("runRawTUI() error = %v", err)
	}

	if strings.Contains(stderrBuf.String(), "caretz -s") {
		t.Fatalf("incognito session offered a resume command: %q", stderrBuf.String())
	}
}

func TestRunInteractiveRawEventLoopEscapeCancelsImmediately(t *testing.T) {
	var output bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader("\x1b "), &output)
	var stopped atomic.Bool

	err := runInteractiveRawEventLoop(terminal, nil, func(ctx context.Context, stop <-chan struct{}) error {
		select {
		case <-ctx.Done():
			stopped.Store(true)
			return ctx.Err()
		case <-time.After(time.Second):
			return errors.New("timed out waiting for graceful stop")
		}
	}, nil, permissionStateReporter{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runInteractiveRawEventLoop() error = %v, want context.Canceled", err)
	}
	if !stopped.Load() {
		t.Fatal("ESC did not cancel the context")
	}
	if !strings.Contains(output.String(), "stopping…") {
		t.Fatalf("output = %q, want stopping status", output.String())
	}
}

func TestRunInteractiveRawEventLoopCtrlCCancelsImmediately(t *testing.T) {
	terminal := ansi.NewTerminal(strings.NewReader("\x03"), &bytes.Buffer{})
	err := runInteractiveRawEventLoop(terminal, nil, func(ctx context.Context, _ <-chan struct{}) error {
		<-ctx.Done()
		return ctx.Err()
	}, nil, permissionStateReporter{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runInteractiveRawEventLoop() error = %v, want context.Canceled", err)
	}
}

func TestRunInteractiveRawEventLoopPermissionEscapeCancelsWithoutResolve(t *testing.T) {
	for _, capability := range []string{"path_read", cloudModelCapability, modelOverrideCapability} {
		t.Run(capability, func(t *testing.T) {
			events := make(chan *types.Event, 2)
			request := eventing.PermissionRequest{RequestID: "pending", ToolName: "Read", Capability: capability, Target: "/outside"}
			events <- eventing.NewPermissionRequested("test", request)
			events <- eventing.NewPermissionRequested("test", request)
			var output bytes.Buffer
			terminal := ansi.NewTerminal(strings.NewReader("\x1b "), &output)
			finalized := false
			err := runInteractiveRawEventLoop(terminal, events, func(ctx context.Context, _ <-chan struct{}) error { <-ctx.Done(); finalized = true; return ctx.Err() }, func(eventing.PermissionResolution) error {
				t.Error("cancelled permission was resolved as a decision")
				return nil
			}, permissionStateReporter{}, nil)
			if !errors.Is(err, context.Canceled) || !finalized {
				t.Fatalf("error = %v, finalized = %v", err, finalized)
			}
		})
	}
}

func TestRunInteractiveRawEventLoopErrorsAwaitFinalization(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	for _, mode := range []string{"read", "decode", "prompt", "resolve"} {
		t.Run(mode, func(t *testing.T) {
			events := make(chan *types.Event, 1)
			input := ""
			if mode != "read" {
				event := eventing.NewPermissionRequested("test", eventing.PermissionRequest{RequestID: "pending", ToolName: "Read", Capability: "path_read", Target: "/outside"})
				if mode == "decode" {
					event.Payload = "{"
				}
				events <- event
			}
			if mode == "resolve" {
				input = "y"
			}
			terminal := ansi.NewTerminal(strings.NewReader(input), io.Discard)
			finalized := false
			err := runInteractiveRawEventLoop(terminal, events, func(ctx context.Context, _ <-chan struct{}) error {
				<-ctx.Done()
				finalized = true
				return errors.Join(ctx.Err(), cleanupErr)
			}, func(eventing.PermissionResolution) error { return errors.New("resolve failed") }, permissionStateReporter{}, nil)
			if !finalized || !errors.Is(err, cleanupErr) {
				t.Fatalf("error = %v, finalized = %v", err, finalized)
			}
		})
	}
}

func TestRunInteractiveRawEventLoopSkipsStalePermission(t *testing.T) {
	events := make(chan *types.Event, 1)
	events <- eventing.NewPermissionRequested("test", eventing.PermissionRequest{RequestID: "stale", ToolName: "Read", Capability: "path_read", Target: "/stale-path"})
	var output bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader("\x1b "), &output)
	err := runInteractiveRawEventLoop(terminal, events, func(ctx context.Context, _ <-chan struct{}) error { <-ctx.Done(); return ctx.Err() }, nil, permissionStateReporter{pending: func(string) bool { return false }}, nil)
	if !errors.Is(err, context.Canceled) || strings.Contains(output.String(), "/stale-path") {
		t.Fatalf("error = %v, output = %q", err, output.String())
	}
}

func TestRunRawCommandEscapeCancelsCommandContext(t *testing.T) {
	session := newTUISession(t, "tui-command-escape")
	defer func() { _ = session.Close() }()
	var terminalOutput bytes.Buffer
	terminal := ansi.NewTerminal(strings.NewReader("\x1b"), &terminalOutput)

	_, handled, err := runRawCommand(terminal, session, func(ctx context.Context) (slashcmd.Result, bool, error) {
		<-ctx.Done()
		return slashcmd.Result{}, true, ctx.Err()
	}, nil, nil)

	if !handled {
		t.Fatal("runRawCommand() handled = false, want true")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runRawCommand() error = %v, want context.Canceled", err)
	}
	if !strings.Contains(terminalOutput.String(), "stopping…") {
		t.Fatalf("output = %q, want stopping status", terminalOutput.String())
	}
}

func TestPromptPermissionUsesRegisteredPrompt(t *testing.T) {
	terminal := ansi.NewTerminal(strings.NewReader(""), io.Discard)
	var prompted eventing.PermissionRequest
	prompts := map[string]slashcmd.PermissionPrompt{
		"custom_send": func(_ *ansi.Terminal, request eventing.PermissionRequest) (eventing.PermissionResolution, error) {
			prompted = request
			return eventing.PermissionResolution{
				Decision: eventing.PermissionDecisionAllow,
				Metadata: map[string]string{eventing.PermissionMetadataPayload: "edited"},
			}, nil
		},
	}

	resolution, err := promptPermission(terminal, eventing.PermissionRequest{RequestID: "r1", Capability: "custom_send", Preview: "payload"}, prompts)

	if err != nil {
		t.Fatalf("promptPermission() error = %v", err)
	}
	if prompted.Preview != "payload" {
		t.Fatalf("prompt got preview %q, want payload", prompted.Preview)
	}
	// The request ID comes from the request, so a prompt need not copy it.
	if resolution.RequestID != "r1" || resolution.Decision != eventing.PermissionDecisionAllow || resolution.Metadata[eventing.PermissionMetadataPayload] != "edited" {
		t.Fatalf("resolution = %+v, want r1 allow with edited payload", resolution)
	}
}

// TestIsTerminalStream guards the TTY-vs-fallback branch used by startTUI.
func TestIsTerminalStream(t *testing.T) {
	if isTerminalStream(strings.NewReader("x")) {
		t.Fatal("isTerminalStream(*strings.Reader) = true, want false")
	}
	if isTerminalStream(&bytes.Buffer{}) {
		t.Fatal("isTerminalStream(*bytes.Buffer) = true, want false")
	}
	if isTerminalStream(nil) {
		t.Fatal("isTerminalStream(nil) = true, want false")
	}
}

func newAsyncTurnState(t *testing.T) *tuiState {
	t.Helper()
	return &tuiState{
		session:    newTUISession(t, "async-turns"),
		cliOutput:  output.NewCLIOutput(output.ModeCompact),
		outputMode: output.ModeCompact,
		registry:   newTestRegistry(),
		asyncWake:  make(chan struct{}, 1),
		asyncTurns: make(chan *asyncTurn, 4),
	}
}

func TestBackgroundTurnCtrlCCancelsTurnNotTUI(t *testing.T) {
	state := newAsyncTurnState(t)
	result := make(chan error, 1)
	go func() {
		result <- state.queueAsyncTurn(context.Background(), func(ctx context.Context, _ <-chan struct{}) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-state.asyncWake
	state.runQueuedAsyncTurns(ansi.NewTerminal(strings.NewReader("\x03"), &bytes.Buffer{}))
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("turn error = %v, want context.Canceled", err)
	}
}

func TestBackgroundTurnEscapeCancelsTurn(t *testing.T) {
	state := newAsyncTurnState(t)
	result := make(chan error, 1)
	go func() {
		result <- state.queueAsyncTurn(context.Background(), func(ctx context.Context, _ <-chan struct{}) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-state.asyncWake
	var terminalOutput bytes.Buffer
	state.runQueuedAsyncTurns(ansi.NewTerminal(strings.NewReader("\x1b "), &terminalOutput))
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("turn error = %v, want context.Canceled", err)
	}
	if !strings.Contains(terminalOutput.String(), "stopping…") {
		t.Fatalf("terminal output = %q", terminalOutput.String())
	}
}

func TestBackgroundTurnQueuedAtExitFails(t *testing.T) {
	state := newAsyncTurnState(t)
	result := make(chan error, 1)
	go func() {
		result <- state.queueAsyncTurn(context.Background(), func(context.Context, <-chan struct{}) error {
			t.Error("turn ran after the TUI stopped")
			return nil
		})
	}()
	<-state.asyncWake
	state.stopAsyncTurns()
	if err := <-result; !errors.Is(err, errTUIClosed) {
		t.Fatalf("turn error = %v, want errTUIClosed", err)
	}
}

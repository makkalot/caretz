package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
)

// A background Bash job that ends on its own starts a report turn, so the
// model learns its result without polling for it.
func TestBackgroundBashJobExitStartsReportTurn(t *testing.T) {
	workspace := t.TempDir()
	runner := sandbox.NewRunner(sandbox.Config{})
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: workspace, SessionExec: true, SandboxDisabled: true, NewSandboxSession: func() *sandbox.Session { return runner.NewSession(true) }}
	start := llm.ToolCall{ID: "bg", Name: "Bash", Arguments: `{"command":"sleep 0.2; echo build-finished; exit 2","background":true}`}
	parent := newRecordingParent(llm.Response{ToolCalls: []llm.ToolCall{start}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{start}}})
	session, err := NewAgentSession("test", "bg-job", SessionParams{BaseDir: t.TempDir(), WorkspaceDir: workspace, Logger: log.New(), Provider: parent, ToolRegistry: registry, SystemPrompt: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := session.Call(t.Context(), "build it in the background"); err != nil {
		t.Fatal(err)
	}
	calls := parent.calls()
	if len(calls) < 2 || !strings.Contains(calls[1], "do not poll") {
		t.Fatalf("start result does not promise a report: %q", calls)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(parent.calls()) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("no report turn; calls=%q", parent.calls())
		}
		time.Sleep(20 * time.Millisecond)
	}
	report := parent.calls()[2]
	for _, want := range []string{"Background command finished.", "echo build-finished", "Exit code: 2", "build-finished\n", "React if this needs action"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report lacks %q:\n%s", want, report)
		}
	}
}

func TestBackgroundJobReportNamesSignal(t *testing.T) {
	report := backgroundJobReport(sandbox.BackgroundExit{PID: 42, Command: "npm run dev", ExitCode: -1, Signal: "killed", LogPath: "$TMPDIR/caretz/processes/42.log"})
	for _, want := range []string{"Killed by signal: killed", "PID: 42", "Log: $TMPDIR/caretz/processes/42.log", "End of output:\n(empty)"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "Exit code") {
		t.Fatalf("signal report shows an exit code:\n%s", report)
	}
}

package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

// recordingRunners records which runner a command reached.
type recordingRunners struct {
	mu          sync.Mutex
	sandboxed   []sandbox.RunOptions
	unsandboxed int
}

func (r *recordingRunners) registry(logger *log.Logger, workspace string) *tools.Registry {
	return &tools.Registry{
		Logger:       logger,
		WorkspaceDir: workspace,
		RunSandboxedOptions: func(_ context.Context, _, command string, opts sandbox.RunOptions) (string, string, error) {
			r.mu.Lock()
			r.sandboxed = append(r.sandboxed, opts)
			r.mu.Unlock()
			return "3 tests failed\n", "boom\n", errors.New("exit status 2")
		},
		RunUnsandboxed: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
			r.mu.Lock()
			r.unsandboxed++
			r.mu.Unlock()
			return "", "", nil
		},
	}
}

// runAsyncCommandForTest runs one scheduled command and returns its result
// and the report the parent model read, if any.
func runAsyncCommandForTest(t *testing.T, registry *tools.Registry, provider *subagentModelTestProvider, setup func(*AgentSession)) (eventing.AsyncRunResult, string) {
	t.Helper()
	request := eventing.AsyncRunRequest{RequestID: "request-1", Source: "test", SourceID: "sched_1", Prompt: "go test ./...", Mode: eventing.AsyncRunCommand, Timeout: 2 * time.Second}
	return runAsyncRequestForTest(t, registry, provider, setup, request)
}

// runAsyncRequestForTest runs one async request and returns its result and
// the first message the parent model read, if any.
func runAsyncRequestForTest(t *testing.T, registry *tools.Registry, provider llm.Provider, setup func(*AgentSession), request eventing.AsyncRunRequest) (eventing.AsyncRunResult, string) {
	t.Helper()
	session, err := NewAgentSession("test", "parent", SessionParams{BaseDir: filepath.Join(t.TempDir(), "sessions"), WorkspaceDir: registry.WorkspaceDir, Logger: registry.Logger, Provider: provider, ToolRegistry: registry, SystemPrompt: "parent prompt", ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	if setup != nil {
		setup(session)
	}
	completed, err := session.SubscribeAuditEvents(eventing.AsyncRunCompleted)
	if err != nil {
		t.Fatal(err)
	}
	defer completed.Close()
	if err := session.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunRequested(session.ChannelID(), request)); err != nil {
		t.Fatalf("PublishAsyncEvent() error = %v", err)
	}
	select {
	case event := <-completed.Events():
		result, err := eventing.DecodeAsyncRunResult(event)
		if err != nil {
			t.Fatal(err)
		}
		report := ""
		if messages := session.GetContextMessages(); len(messages) > 0 {
			report = messages[0].Content
		}
		return result, report
	case <-time.After(3 * time.Second):
		t.Fatal("async command did not complete")
	}
	return eventing.AsyncRunResult{}, ""
}

func TestAsyncCommandRunsInSandboxAndWakesParent(t *testing.T) {
	runners := &recordingRunners{}
	logger := log.New()
	registry := runners.registry(logger, t.TempDir())
	provider := &subagentModelTestProvider{model: "parent-model", responses: []llm.Response{
		{Content: "Tests are failing.", Message: llm.Message{Role: llm.RoleAssistant, Content: "Tests are failing."}},
	}}
	// A sandbox-off grant for this exact command must not move it out of
	// the sandbox.
	result, report := runAsyncCommandForTest(t, registry, provider, func(session *AgentSession) {
		session.addSessionPermissionGrant(eventing.PermissionRequest{Capability: tools.PermissionSandboxOff, Target: "go test ./..."})
	})

	if result.Status != "succeeded" {
		t.Fatalf("result = %#v, want succeeded: a failing command is a result, not a failed run", result)
	}
	if len(runners.sandboxed) != 1 || runners.unsandboxed != 0 {
		t.Fatalf("runners: sandboxed=%d unsandboxed=%d, want the sandbox only", len(runners.sandboxed), runners.unsandboxed)
	}
	if provider.requests != 1 {
		t.Fatalf("provider requests = %d, want one parent turn and no subagent", provider.requests)
	}
	for _, want := range []string{"Schedule: sched_1", "go test ./...", "Exit code: 2", "3 tests failed", "boom"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report = %q, want %q", report, want)
		}
	}
}

func TestAsyncCommandTimeoutNamesCommandLimitAndPreviousRun(t *testing.T) {
	registry := &tools.Registry{
		Logger:       log.New(),
		WorkspaceDir: t.TempDir(),
		RunSandboxedOptions: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
			return "partial\n", "", errors.New("command timed out after 7 seconds")
		},
	}
	provider := &subagentModelTestProvider{model: "parent-model"}
	request := eventing.AsyncRunRequest{RequestID: "request-1", Source: "test", SourceID: "sched_1", Prompt: "sleep 60", Mode: eventing.AsyncRunCommand, Timeout: 2 * time.Second, CommandTimeoutSeconds: 7, PreviousRun: "Status: failed\nError: boom"}
	result, report := runAsyncRequestForTest(t, registry, provider, nil, request)

	if result.Status != "timed_out" || result.Error != "command timeout of 7s exceeded" {
		t.Fatalf("result = %#v, want a command timeout", result)
	}
	for _, want := range []string{"(command timeout of 7s exceeded)", "partial", "Previous run did not succeed:\nStatus: failed\nError: boom"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report = %q, want %q", report, want)
		}
	}
}

// slowProvider answers after a delay, so a model phase can outlast its limit.
type slowProvider struct {
	subagentModelTestProvider
	delay time.Duration
}

func (p *slowProvider) Chat(systemPrompt string, messages []llm.Message, tools []llm.ToolDefinition, onDelta func(string)) (llm.Response, error) {
	time.Sleep(p.delay)
	return p.subagentModelTestProvider.Chat(systemPrompt, messages, tools, onDelta)
}

func TestAsyncCommandReportTurnTimeoutNamesModelLimit(t *testing.T) {
	registry := (&recordingRunners{}).registry(log.New(), t.TempDir())
	provider := &slowProvider{subagentModelTestProvider: subagentModelTestProvider{model: "parent-model"}, delay: 300 * time.Millisecond}
	request := eventing.AsyncRunRequest{RequestID: "request-1", Source: "test", SourceID: "sched_1", Prompt: "go test ./...", Mode: eventing.AsyncRunCommand, Timeout: 2 * time.Second, ModelTimeout: 50 * time.Millisecond}
	result, _ := runAsyncRequestForTest(t, registry, provider, nil, request)

	if result.Status != "timed_out" || result.Error != "model timeout of 50ms exceeded" {
		t.Fatalf("result = %#v, want a model timeout", result)
	}
}

func TestTimeoutErrorNamesRunLimit(t *testing.T) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Nanosecond, asyncTimeout{"run", 30 * time.Minute})
	defer cancel()
	<-ctx.Done()
	err := timeoutError(ctx, fmt.Errorf("llm request: %w", context.DeadlineExceeded))
	if err.Error() != "run timeout of 30m exceeded" || asyncStatus(err, ctx) != "timed_out" {
		t.Fatalf("error = %v, status = %s", err, asyncStatus(err, ctx))
	}
	// An error the deadline did not cause keeps its own text.
	other := errors.New("model overloaded")
	if got := timeoutError(context.Background(), other); got != other {
		t.Fatalf("error = %v, want it unchanged", got)
	}
}

func TestAsyncCommandRefusedWhenSandboxDisabled(t *testing.T) {
	runners := &recordingRunners{}
	registry := runners.registry(log.New(), t.TempDir())
	registry.SandboxDisabled = true
	provider := &subagentModelTestProvider{model: "parent-model"}

	result, _ := runAsyncCommandForTest(t, registry, provider, nil)

	if result.Status != "refused" || !strings.Contains(result.Error, "need the sandbox") {
		t.Fatalf("result = %#v, want refused", result)
	}
	if len(runners.sandboxed) != 0 || runners.unsandboxed != 0 || provider.requests != 0 {
		t.Fatalf("sandboxed=%d unsandboxed=%d provider=%d, want nothing run", len(runners.sandboxed), runners.unsandboxed, provider.requests)
	}
}

func TestAsyncCommandDoesNotBlockUserTurns(t *testing.T) {
	logger := log.New()
	started, release := make(chan struct{}), make(chan struct{})
	registry := &tools.Registry{Logger: logger, WorkspaceDir: t.TempDir(), RunSandboxedOptions: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
		close(started)
		<-release
		return "done\n", "", nil
	}}
	provider := &subagentModelTestProvider{model: "parent-model"}

	done := make(chan eventing.AsyncRunResult, 1)
	go func() {
		result, _ := runAsyncCommandForTest(t, registry, provider, func(session *AgentSession) {
			go func() {
				<-started
				// The command is still running; a user turn must not wait for it.
				turn := make(chan error, 1)
				go func() {
					_, err := session.Call(context.Background(), "hello")
					turn <- err
				}()
				select {
				case err := <-turn:
					if err != nil {
						t.Errorf("user turn error = %v", err)
					}
				case <-time.After(time.Second):
					t.Error("user turn blocked by a running scheduled command")
				}
				close(release)
			}()
		})
		done <- result
	}()
	if result := <-done; result.Status != "succeeded" {
		t.Fatalf("result = %#v", result)
	}
}

// Run with -race: a scheduled command reads the registry and its writable
// roots outside the execution lock while turns approve paths and switch the
// runtime.
func TestAsyncCommandRacesSafelyWithApprovalsAndRuntimeSwitch(t *testing.T) {
	workspace := t.TempDir()
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: workspace, RunSandboxedOptions: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
		return "", "mkdir /outside/cache: operation not permitted", errors.New("exit status 1")
	}}
	provider := &subagentModelTestProvider{model: "parent-model"}
	stop, stopped := make(chan struct{}), make(chan struct{})
	defer func() { close(stop); <-stopped }()

	result, report := runAsyncCommandForTest(t, registry, provider, func(session *AgentSession) {
		go func() {
			defer close(stopped)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if err := registry.AddPermissionGrant(tools.PermissionPathWrite, filepath.Join(workspace, fmt.Sprint("approved-", i)), false); err != nil {
					t.Error(err)
					return
				}
				_ = session.withExecution(func() error { return session.SwitchRuntime(registry, "parent prompt") })
			}
		}()
	})

	if result.Status != "succeeded" || !strings.Contains(report, "Blocked by the sandbox") {
		t.Fatalf("result = %#v, report = %q", result, report)
	}
}

func TestAsyncCommandIsReadOnlyInPlanMode(t *testing.T) {
	runners := &recordingRunners{}
	registry := runners.registry(log.New(), t.TempDir())
	registry.SetPlanMode(true)

	result, _ := runAsyncCommandForTest(t, registry, &subagentModelTestProvider{model: "parent-model"}, nil)

	if result.Status != "succeeded" || len(runners.sandboxed) != 1 || !runners.sandboxed[0].ReadOnly {
		t.Fatalf("result = %#v, runs = %#v, want one read-only sandboxed run", result, runners.sandboxed)
	}
}

package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

func TestTruncateBashOutput(t *testing.T) {
	tests := []struct {
		name            string
		output          string
		maxBytes        int
		wantUnchanged   bool
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:          "short output unchanged",
			output:        strings.Repeat("x", 100),
			maxBytes:      30 * 1024,
			wantUnchanged: true,
		},
		{
			name:          "exactly at limit",
			output:        strings.Repeat("x", 30*1024),
			maxBytes:      30 * 1024,
			wantUnchanged: true,
		},
		{
			name:         "over limit",
			output:       strings.Repeat("x", 60*1024),
			maxBytes:     30 * 1024,
			wantContains: []string{"output truncated", "61440 bytes total"},
		},
		{
			name:         "preserves head content",
			output:       "HEADER\n" + strings.Repeat("x", 60*1024),
			maxBytes:     30 * 1024,
			wantContains: []string{"HEADER"},
		},
		{
			name:         "preserves tail content",
			output:       strings.Repeat("x", 60*1024) + "\nFOOTER",
			maxBytes:     30 * 1024,
			wantContains: []string{"FOOTER"},
		},
		{
			name:         "very large output",
			output:       strings.Repeat("x", 1024*1024),
			maxBytes:     30 * 1024,
			wantContains: []string{"output truncated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateBashOutput(tt.output, tt.maxBytes)

			if tt.wantUnchanged {
				if got != tt.output {
					t.Errorf("expected output unchanged, got different output (len %d vs %d)", len(got), len(tt.output))
				}
				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("output missing expected %q", want)
				}
			}
		})
	}
}

func TestHandleBashToolCallTimeoutOptions(t *testing.T) {
	tests := []struct {
		name        string
		args        string
		wantTimeout int
		wantErr     string
	}{
		{
			name:        "default",
			args:        `{"command":"sleep 1"}`,
			wantTimeout: sandbox.DefaultTimeoutSeconds,
		},
		{
			name:        "custom",
			args:        `{"command":"sleep 1","timeout_seconds":300}`,
			wantTimeout: 300,
		},
		{
			name:        "capped",
			args:        `{"command":"sleep 1","timeout_seconds":999}`,
			wantTimeout: sandbox.MaxTimeoutSeconds,
		},
		{
			name:    "negative",
			args:    `{"command":"sleep 1","timeout_seconds":-1}`,
			wantErr: "timeout_seconds must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotTimeout int
			registry := &Registry{
				RunSandboxedOptions: func(ctx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error) {
					gotTimeout = opts.TimeoutSeconds
					return "ok", "", nil
				},
			}

			msg, err := registry.HandleToolCall(llm.ToolCall{
				ID:        "bash-timeout",
				Name:      "Bash",
				Arguments: tt.args,
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotTimeout != tt.wantTimeout {
				t.Fatalf("timeout = %d, want %d", gotTimeout, tt.wantTimeout)
			}
			if msg.Content != "ok" {
				t.Fatalf("content = %q, want ok", msg.Content)
			}
		})
	}
}

func TestHandleBashToolCallProvidesLiveNetworkAuthorizer(t *testing.T) {
	call := llm.ToolCall{ID: "bash-network", Name: "Bash", Arguments: `{"command":"run tests"}`}
	registry := &Registry{RunSandboxedOptions: func(ctx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error) {
		if opts.AuthorizeNetwork == nil {
			t.Fatal("live network authorizer is nil")
		}
		allowed, err := opts.AuthorizeNetwork(context.Background(), "127.0.0.1:43127")
		if err != nil || !allowed {
			t.Fatalf("authorization = (%t, %v), want allowed", allowed, err)
		}
		return "ok", "", nil
	}}
	ctx := WithPermissionRequester(context.Background(), func(_ context.Context, got llm.ToolCall, denial *PermissionError) (bool, error) {
		if got.ID != call.ID || denial.Capability != PermissionNetwork || denial.Target != "127.0.0.1:43127" || !denial.MayHaveRun {
			t.Fatalf("permission request = call %#v denial %#v", got, denial)
		}
		return true, nil
	})
	msg, err := registry.HandleToolCallContext(ctx, call)
	if err != nil || msg.Content != "ok" {
		t.Fatalf("result = %#v, %v", msg, err)
	}
}

func TestBashBackgroundPolicy(t *testing.T) {
	registry := &Registry{}
	call := llm.ToolCall{ID: "bg", Name: "Bash", Arguments: `{"command":"sleep 1","background":true}`}
	if _, err := registry.HandleToolCall(call); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("disabled background error = %v", err)
	}
	registry.SessionExec = true
	registry.SetPlanMode(true)
	if _, err := registry.HandleToolCall(call); err == nil || !strings.Contains(err.Error(), "not available in plan mode") {
		t.Fatalf("plan background error = %v", err)
	}
	registry.SetPlanMode(false)
	call.Arguments = `{"command":"sleep 1","background":true,"timeout_seconds":0}`
	if _, err := registry.HandleToolCall(call); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("background timeout error = %v", err)
	}
}

func TestBashBackgroundSchemaIsConditional(t *testing.T) {
	property := func(r *Registry) any {
		for _, def := range r.ToolDefinitions() {
			if def.Name == "Bash" {
				props := def.Parameters["properties"].(map[string]any)
				return props["background"]
			}
		}
		return nil
	}
	if got := property(&Registry{}); got != nil {
		t.Fatalf("default background schema=%v", got)
	}
	if got := property(&Registry{SessionExec: true}); got == nil {
		t.Fatal("enabled schema omits background")
	}
	if got := property((&Registry{SessionExec: true}).WithPlanMode(true)); got == nil {
		t.Fatal("plan mode must keep the tool schema unchanged")
	}
}

func TestPlanModeBashRunsReadOnlyOutsideLiveSession(t *testing.T) {
	var got sandbox.RunOptions
	registry := &Registry{
		SessionExec: true,
		RunSandboxedOptions: func(_ context.Context, _, _ string, opts sandbox.RunOptions) (string, string, error) {
			got = opts
			return "ok", "", nil
		},
	}
	registry.SetPlanMode(true)
	result, err := registry.HandleToolCall(llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"ls"}`})
	if err != nil || result.Content != "ok" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !got.ReadOnly {
		t.Fatal("plan mode Bash should run in a read-only sandbox")
	}
	if _, err := registry.HandleToolCall(llm.ToolCall{ID: "2", Name: "Bash", Arguments: `{"command":"sleep 1","background":true}`}); err == nil {
		t.Fatal("background Bash should fail in plan mode")
	}
}

func TestPlanModeSwitchReachesBashButNotClones(t *testing.T) {
	var readOnly atomic.Bool
	registry := &Registry{
		RunSandboxedOptions: func(_ context.Context, _, _ string, opts sandbox.RunOptions) (string, string, error) {
			readOnly.Store(opts.ReadOnly)
			return "ok", "", nil
		},
	}
	registry.SetPlanMode(true)
	subagent := registry.WithDisabledTools(map[string]bool{"CallSubagent": true})
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"go build ./..."}`}

	registry.SetPlanMode(false)
	if !subagent.PlanMode() {
		t.Fatal("a running subagent must keep the mode it was spawned with")
	}
	if _, err := registry.HandleToolCall(call); err != nil || readOnly.Load() {
		t.Fatalf("build mode Bash ran read-only=%v err=%v", readOnly.Load(), err)
	}
	if _, err := subagent.HandleToolCall(call); err != nil || !readOnly.Load() {
		t.Fatalf("plan mode subagent Bash ran read-only=%v err=%v", readOnly.Load(), err)
	}

	registry.SetPlanMode(true)
	if _, err := registry.RunVerificationCommand(context.Background(), "true"); err != nil || !readOnly.Load() {
		t.Fatalf("plan mode verification ran read-only=%v err=%v", readOnly.Load(), err)
	}
	if _, err := registry.WithPlanMode(false).RunVerificationCommand(context.Background(), "true"); err != nil || readOnly.Load() {
		t.Fatalf("build mode clone verification ran read-only=%v err=%v", readOnly.Load(), err)
	}
}

func TestPlanModeRefusesRunnersWithoutOptions(t *testing.T) {
	registry := &Registry{
		RunSandboxedContext: func(context.Context, string, string) (string, string, error) {
			t.Fatal("plan mode ran a command through a runner that cannot make it read-only")
			return "", "", nil
		},
	}
	registry.SetPlanMode(true)
	result, err := registry.HandleToolCall(llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"touch x"}`})
	if err != nil || !strings.Contains(result.Content, "read-only") {
		t.Fatalf("result = %#v, err = %v, want read-only refusal", result, err)
	}
}

func TestPlanModeInteractiveCommandRunsReadOnly(t *testing.T) {
	var got sandbox.RunOptions
	registry, _ := (&Registry{
		SessionExec:       true,
		NewSandboxSession: func() *sandbox.Session { t.Fatal("plan mode used the writable live session"); return nil },
		RunSandboxedOptions: func(_ context.Context, _, _ string, opts sandbox.RunOptions) (string, string, error) {
			got = opts
			return "", "touch: x: Read-only file system", fmt.Errorf("exit status 1")
		},
	}).BindSessionExecution()
	registry.SetPlanMode(true)
	result, err := registry.RunInteractiveCommand(context.Background(), "touch x")
	if !got.ReadOnly {
		t.Fatal("plan mode ! command did not run read-only")
	}
	if err == nil && result.ExitCode == 0 {
		t.Fatalf("write in plan mode succeeded: %#v", result)
	}
	if err != nil && strings.Contains(err.Error(), "verification") {
		t.Fatalf("err = %v, want interactive command wording", err)
	}
}

func TestPlanModeSwitchIsRaceFree(t *testing.T) {
	registry := &Registry{RunSandboxedOptions: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
		return "ok", "", nil
	}}
	registry.SetPlanMode(false)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 100 {
			registry.SetPlanMode(i%2 == 0)
		}
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			_, _ = registry.HandleToolCall(llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"ls"}`})
		}
	}()
	wg.Wait()
}

func TestSessionExecutionBindingScopesAgentTrees(t *testing.T) {
	defaultRegistry := &Registry{NewSandboxSession: func() *sandbox.Session { t.Fatal("default path created supervisor"); return nil }}
	if bound, owned := defaultRegistry.BindSessionExecution(); owned || bound.executionState != nil {
		t.Fatal("default path bound session execution")
	}
	created := 0
	base := &Registry{SessionExec: true, NewSandboxSession: func() *sandbox.Session { created++; return sandbox.NewRunner(sandbox.Config{}).NewSession(true) }}
	parent, owned := base.BindSessionExecution()
	if !owned || parent.executionState == nil {
		t.Fatal("top-level registry did not bind execution state")
	}
	if created != 0 {
		t.Fatalf("supervisor factory called eagerly: %d", created)
	}
	child := parent.WithoutSessionTools()
	if rebound, childOwned := child.BindSessionExecution(); childOwned || rebound.executionState != parent.executionState {
		t.Fatal("child did not share parent execution state")
	}
	sibling, siblingOwned := parent.ForNewTopLevelSession().BindSessionExecution()
	if !siblingOwned || sibling.executionState == parent.executionState {
		t.Fatal("sibling shared parent execution state")
	}
}

func TestHandleBashToolCallAddsActorMetadataEnv(t *testing.T) {
	var gotEnv map[string]string
	registry := &Registry{
		RunSandboxedOptions: func(ctx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error) {
			gotEnv = opts.Env
			return "ok", "", nil
		},
	}

	ctx := WithActorMetadata(context.Background(), ActorMetadata{
		Adapter:   "telegram",
		ChannelID: "123",
		UserID:    "42",
		MessageID: "telegram-message",
	})
	msg, err := registry.HandleToolCallContext(ctx, llm.ToolCall{
		ID:        "bash-actor-env",
		Name:      "Bash",
		Arguments: `{"command":"env"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content != "ok" {
		t.Fatalf("content = %q, want ok", msg.Content)
	}
	want := map[string]string{
		"CARETZ_ADAPTER":    "telegram",
		"CARETZ_CHANNEL_ID": "123",
		"CARETZ_USER_ID":    "42",
		"CARETZ_MESSAGE_ID": "telegram-message",
	}
	for key, value := range want {
		if gotEnv[key] != value {
			t.Fatalf("env[%s] = %q, want %q; env=%#v", key, gotEnv[key], value, gotEnv)
		}
	}
}

func TestTruncateBashOutput_DefaultLimit(t *testing.T) {
	bigOutput := strings.Repeat("x", 1024*1024)
	got := TruncateBashOutput(bigOutput, MaxBashOutputBytes)
	if len(got) > MaxBashOutputBytes+200 {
		t.Errorf("truncated output too large: %d bytes (limit %d)", len(got), MaxBashOutputBytes)
	}
}

func TestHandleBashToolCallUsesWorkspaceDir(t *testing.T) {
	workspaceDir := t.TempDir()
	var gotCwd string
	registry := &Registry{
		WorkspaceDir: workspaceDir,
		RunSandboxedContext: func(ctx context.Context, cwd, command string) (string, string, error) {
			gotCwd = cwd
			if command != "pwd" {
				t.Fatalf("command = %q, want pwd", command)
			}
			return "ok", "", nil
		},
	}

	msg, err := registry.HandleToolCall(llm.ToolCall{
		ID:        "bash-workspace",
		Name:      "Bash",
		Arguments: `{"command":"pwd"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCwd != workspaceDir {
		t.Fatalf("cwd = %q, want %q", gotCwd, workspaceDir)
	}
	if msg.Content != "ok" {
		t.Fatalf("content = %q, want ok", msg.Content)
	}
}

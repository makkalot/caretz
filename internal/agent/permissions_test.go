package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
)

func TestPermissionAllowRetriesToolAndPersistsOneResult(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	path := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(path, []byte("approved contents"), 0600); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]string{"file_path": path})
	call := llm.ToolCall{ID: "read-outside", Name: "Read", Arguments: string(arguments)}
	provider := &mockProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
		{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	session, err := NewAgentSession("test", "permission-retry", SessionParams{
		InMemorySessionStore: true,
		WorkspaceDir:         workspace,
		Provider:             provider,
		ToolRegistry:         &tools.Registry{WorkspaceDir: workspace},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	done := make(chan error, 1)
	go func() {
		_, callErr := session.CallUntilStopWithPermissions(context.Background(), "read it", nil)
		done <- callErr
	}()
	request := waitForPendingPermission(t, session)
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if request.Target != resolvedPath || request.Capability != tools.PermissionPathRead {
		t.Fatalf("request = %+v", request)
	}
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: request.RequestID, Decision: eventing.PermissionDecisionAllow}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	contextEvents, err := session.GetContextEvents()
	if err != nil {
		t.Fatal(err)
	}
	toolResults := 0
	for _, event := range contextEvents {
		if event.EventType != eventing.LLMToolCallResultCreated {
			continue
		}
		toolResults++
		var payload eventing.LLMToolCallResult
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Error != "" || payload.Message.Content == "" {
			t.Fatalf("tool result = %+v", payload)
		}
	}
	if toolResults != 1 {
		t.Fatalf("tool results = %d, want 1", toolResults)
	}
}

func waitForPendingPermission(t *testing.T, session *AgentSession) eventing.PermissionRequest {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pending := session.PendingPermissions()
		if len(pending) == 1 {
			return pending[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("permission request did not become pending")
	return eventing.PermissionRequest{}
}

func TestPermissionRequestCanBeResolvedConcurrently(t *testing.T) {
	session, err := NewAgentSession("test", "permission-session", SessionParams{
		InMemorySessionStore: true,
		Provider:             &mockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	result := make(chan bool, 1)
	go func() {
		allowed, requestErr := session.requestPermission(context.Background(), llm.ToolCall{ID: "call-1", Name: "Read", Arguments: `{"file_path":"/outside"}`}, &tools.PermissionError{Capability: tools.PermissionPathRead, Target: "/outside", Reason: "path outside allowed directory"})
		if requestErr != nil {
			result <- false
			return
		}
		result <- allowed
	}()

	request := waitForPendingPermission(t, session)
	if request.RequestID == "" {
		t.Fatal("permission request did not become pending")
	}
	resolution := eventing.PermissionResolution{RequestID: request.RequestID, Decision: eventing.PermissionDecisionAllow, Resolver: "test"}
	if err := session.ResolvePermission(resolution); err != nil {
		t.Fatal(err)
	}
	if err := session.ResolvePermission(resolution); err != nil {
		t.Fatalf("idempotent resolution failed: %v", err)
	}
	if !<-result {
		t.Fatal("request was not allowed")
	}

	events, err := session.AuditEvents()
	if err != nil {
		t.Fatal(err)
	}
	var requested, resolved bool
	for _, event := range events {
		requested = requested || event.EventType == eventing.PermissionRequested
		resolved = resolved || event.EventType == eventing.PermissionResolved
	}
	if !requested || !resolved {
		t.Fatalf("permission audit lifecycle missing: requested=%v resolved=%v", requested, resolved)
	}
}

func TestPermissionAllowSessionApprovesMatchingLaterRequest(t *testing.T) {
	session, err := NewAgentSession("test", "permission-allow-session", SessionParams{
		InMemorySessionStore: true,
		Provider:             &mockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	denial := &tools.PermissionError{Capability: tools.PermissionNetwork, Target: "superdoc.bg:443", Reason: "network access denied"}
	first := make(chan bool, 1)
	go func() {
		allowed, _ := session.requestPermission(context.Background(), llm.ToolCall{ID: "call-1", Name: "Bash"}, denial)
		first <- allowed
	}()
	request := waitForPendingPermission(t, session)
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: request.RequestID, Decision: eventing.PermissionDecisionAllowSession}); err != nil {
		t.Fatal(err)
	}
	if !<-first {
		t.Fatal("session permission did not allow the initial request")
	}

	allowed, err := session.requestPermission(context.Background(), llm.ToolCall{ID: "call-2", Name: "Bash"}, denial)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("matching later request was not allowed")
	}
	if pending := session.PendingPermissions(); len(pending) != 0 {
		t.Fatalf("matching later request prompted again: %+v", pending)
	}
}

func TestAskPermissionNoSessionGrantPromptsEveryTime(t *testing.T) {
	session, err := NewAgentSession("test", "permission-no-session-grant", SessionParams{
		InMemorySessionStore: true,
		Provider:             &mockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	request := eventing.PermissionRequest{Capability: "payload_send", Target: "expert", NoSessionGrant: true}
	for i := range 2 {
		result := make(chan eventing.PermissionResolution, 1)
		go func() {
			resolution, _ := session.AskPermission(context.Background(), request)
			result <- resolution
		}()
		pending := waitForPendingPermission(t, session)
		if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: pending.RequestID, Decision: eventing.PermissionDecisionAllowSession}); err != nil {
			t.Fatal(err)
		}
		if got := <-result; got.Resolver == "session-grant" {
			t.Fatalf("request %d was answered by a session grant", i)
		}
	}
}

func TestPermissionRequestTimesOutAsDeny(t *testing.T) {
	session, err := NewAgentSession("test", "permission-timeout", SessionParams{
		InMemorySessionStore: true,
		Provider:             &mockProvider{},
		PermissionTimeout:    10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	allowed, err := session.requestPermission(context.Background(), llm.ToolCall{ID: "call-timeout", Name: "Bash"}, &tools.PermissionError{
		Capability: tools.PermissionNetwork,
		Target:     "example.com:443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("timed-out permission was allowed")
	}
	if pending := session.PendingPermissions(); len(pending) != 0 {
		t.Fatalf("timed-out permission remains pending: %+v", pending)
	}

	events, err := session.AuditEvents()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType != eventing.PermissionResolved {
			continue
		}
		resolution, err := eventing.DecodePermissionResolution(event)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Decision == eventing.PermissionDecisionDeny && resolution.Resolver == "permission-timeout" {
			return
		}
	}
	t.Fatal("timeout denial was not recorded")
}

func TestPermissionRequestWithoutTimeoutWaitsForAnswer(t *testing.T) {
	session, err := NewAgentSession("test", "permission-no-timeout", SessionParams{
		InMemorySessionStore: true,
		Provider:             &mockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	type result struct {
		allowed bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		allowed, err := session.requestPermission(context.Background(), llm.ToolCall{ID: "call-wait", Name: "Bash"}, &tools.PermissionError{
			Capability: tools.PermissionNetwork,
			Target:     "example.com:443",
		})
		done <- result{allowed: allowed, err: err}
	}()

	var pending []eventing.PermissionRequest
	for deadline := time.Now().Add(time.Second); len(pending) == 0 && time.Now().Before(deadline); {
		pending = session.PendingPermissions()
		time.Sleep(time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("pending permissions = %+v, want one", pending)
	}
	if !pending[0].ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v, want zero without a timeout", pending[0].ExpiresAt)
	}
	select {
	case got := <-done:
		t.Fatalf("permission resolved without an answer: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}

	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: pending[0].RequestID, Decision: eventing.PermissionDecisionAllow}); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || !got.allowed {
		t.Fatalf("result = %+v, want allowed", got)
	}
}

func TestPermissionResolutionRejectsConflict(t *testing.T) {
	session := &AgentSession{
		channelId:             "permission-conflict",
		pendingPermissions:    map[string]*pendingPermission{},
		permissionResolutions: map[string]eventing.PermissionResolution{"request-1": {RequestID: "request-1", Decision: eventing.PermissionDecisionAllow}},
	}
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: "request-1", Decision: eventing.PermissionDecisionDeny}); err == nil {
		t.Fatal("expected conflicting resolution error")
	}
}

func TestSessionRestoreExpiresUnresolvedPermission(t *testing.T) {
	baseDir := t.TempDir()
	params := SessionParams{BaseDir: baseDir, WorkspaceDir: t.TempDir(), Provider: &mockProvider{}}
	first, err := NewAgentSession("test", "permission-restore", params)
	if err != nil {
		t.Fatal(err)
	}
	request := eventing.PermissionRequest{RequestID: "stale-request", SessionID: "permission-restore", ToolCallID: "call", ToolName: "Read", Capability: tools.PermissionPathRead, Target: "/outside", Reason: "test"}
	if err := first.AppendAuditEvent(eventing.NewPermissionRequested(first.ChannelID(), request)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewAgentSession("test", "permission-restore", params)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	events, err := second.AuditEvents()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType != eventing.PermissionResolved {
			continue
		}
		resolution, err := eventing.DecodePermissionResolution(event)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.RequestID == request.RequestID && resolution.Decision == eventing.PermissionDecisionExpired {
			return
		}
	}
	t.Fatal("stale permission was not expired")
}

func TestSandboxOffAllowSessionCoversOnlyThatExactCommand(t *testing.T) {
	workspace := t.TempDir()
	bash := func(id, command string) llm.ToolCall {
		arguments, _ := json.Marshal(map[string]any{"command": command})
		return llm.ToolCall{ID: id, Name: "Bash", Arguments: string(arguments)}
	}
	calls := []llm.ToolCall{bash("1", "kubectl get pods"), bash("2", "kubectl get pods"), bash("3", "kubectl delete pod x")}
	var responses []llm.Response
	for _, call := range calls {
		responses = append(responses, llm.Response{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}})
	}
	responses = append(responses, llm.Response{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}})
	var ran, sandboxed []string
	session, err := NewAgentSession("test", "sandbox-off-session", SessionParams{
		InMemorySessionStore: true,
		WorkspaceDir:         workspace,
		Provider:             &mockProvider{responses: responses},
		ToolRegistry: &tools.Registry{WorkspaceDir: workspace, AskSandboxOff: true, RunSandboxedOptions: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
			sandboxed = append(sandboxed, command)
			return "", "error: open /Users/me/.kube/config: operation not permitted", errors.New("exit status 1")
		}, RunUnsandboxed: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
			ran = append(ran, command)
			return "ok", "", nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	done := make(chan error, 1)
	go func() {
		_, callErr := session.CallUntilStopWithPermissions(context.Background(), "check pods", nil)
		done <- callErr
	}()
	first := waitForPendingPermission(t, session)
	if first.Capability != tools.PermissionSandboxOff || first.Target != "kubectl get pods" {
		t.Fatalf("first request = %+v", first)
	}
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: first.RequestID, Decision: eventing.PermissionDecisionAllowSession}); err != nil {
		t.Fatal(err)
	}
	second := waitForPendingPermission(t, session)
	if second.Target != "kubectl delete pod x" {
		t.Fatalf("identical command prompted again or wrong request: %+v", second)
	}
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: second.RequestID, Decision: eventing.PermissionDecisionDeny}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(ran) != 2 || ran[0] != "kubectl get pods" || ran[1] != "kubectl get pods" {
		t.Fatalf("unsandboxed runs = %v", ran)
	}
	// The session-approved repeat goes straight to the host.
	if strings.Join(sandboxed, ",") != "kubectl get pods,kubectl delete pod x" {
		t.Fatalf("sandboxed runs = %v", sandboxed)
	}
	contextEvents, err := session.GetContextEvents()
	if err != nil {
		t.Fatal(err)
	}
	// The model sees the declined command's ordinary sandboxed result, with
	// no sign that it could have left the sandbox.
	var declined llm.Message
	for _, event := range contextEvents {
		var payload eventing.LLMToolCallResult
		if event.EventType == eventing.LLMToolCallResultCreated && json.Unmarshal([]byte(event.Payload), &payload) == nil && payload.Tool.ID == "3" {
			if payload.Error != "" {
				t.Fatalf("declined command returned an error: %q", payload.Error)
			}
			declined = payload.Message
		}
	}
	if declined.Content != "sandbox error: error: open /Users/me/.kube/config: operation not permitted" {
		t.Fatalf("declined result = %q", declined.Content)
	}
}

// Approving a command blocked by a hidden host service for the session covers
// that exact command again, straight on the host. A different command blocked
// by the same service, such as tail on a log quoting the refusal, asks again.
func TestSandboxOffAllowSessionForHostServiceCoversOnlyThatCommand(t *testing.T) {
	workspace := t.TempDir()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bash := func(id, command string) llm.ToolCall {
		arguments, _ := json.Marshal(map[string]any{"command": command})
		return llm.ToolCall{ID: id, Name: "Bash", Arguments: string(arguments)}
	}
	calls := []llm.ToolCall{bash("1", "docker images"), bash("2", "docker images"), bash("3", "tail build.log"), bash("4", "ls")}
	var responses []llm.Response
	for _, call := range calls {
		responses = append(responses, llm.Response{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}})
	}
	responses = append(responses, llm.Response{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}})
	var ran, sandboxed []string
	session, err := NewAgentSession("test", "sandbox-off-service", SessionParams{
		InMemorySessionStore: true,
		WorkspaceDir:         workspace,
		Provider:             &mockProvider{responses: responses},
		ToolRegistry: &tools.Registry{WorkspaceDir: workspace, AskSandboxOff: true, RunSandboxedOptions: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
			sandboxed = append(sandboxed, command)
			if command == "ls" {
				return "file", "", nil
			}
			return "Cannot connect to the Docker daemon at unix://" + socket + ". Is the docker daemon running?\n", "", nil
		}, RunUnsandboxed: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
			ran = append(ran, command)
			return "ok", "", nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	done := make(chan error, 1)
	go func() {
		_, callErr := session.CallUntilStopWithPermissions(context.Background(), "clean images", nil)
		done <- callErr
	}()
	first := waitForPendingPermission(t, session)
	if first.Target != "docker images" || first.Command != "docker images" {
		t.Fatalf("first request = %+v", first)
	}
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: first.RequestID, Decision: eventing.PermissionDecisionAllowSession}); err != nil {
		t.Fatal(err)
	}
	second := waitForPendingPermission(t, session)
	if second.Target != "tail build.log" {
		t.Fatalf("second request = %+v", second)
	}
	if err := session.ResolvePermission(eventing.PermissionResolution{RequestID: second.RequestID, Decision: eventing.PermissionDecisionDeny}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if pending := session.PendingPermissions(); len(pending) != 0 {
		t.Fatalf("pending = %+v", pending)
	}
	if strings.Join(ran, ",") != "docker images,docker images" {
		t.Fatalf("unsandboxed runs = %v", ran)
	}
	if strings.Join(sandboxed, ",") != "docker images,tail build.log,ls" {
		t.Fatalf("sandboxed runs = %v", sandboxed)
	}
}

// Without a way to ask, as in prompt mode or a subagent, a refusal keeps the
// command's ordinary result instead of "permission approval unavailable".
func TestSandboxOffWithoutRequesterKeepsSandboxResult(t *testing.T) {
	workspace := t.TempDir()
	arguments, _ := json.Marshal(map[string]any{"command": "sudo true"})
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: string(arguments)}
	session, err := NewAgentSession("test", "sandbox-off-headless", SessionParams{
		InMemorySessionStore: true,
		WorkspaceDir:         workspace,
		Provider: &mockProvider{responses: []llm.Response{
			{ToolCalls: []llm.ToolCall{call}, Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
			{Content: "done", Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
		}},
		ToolRegistry: &tools.Registry{WorkspaceDir: workspace, AskSandboxOff: true, RunSandboxedOptions: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
			return "", `sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`, errors.New("exit status 1")
		}, RunUnsandboxed: func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
			t.Fatal("ran outside the sandbox without approval")
			return "", "", nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.Call(context.Background(), "check sudo"); err != nil {
		t.Fatal(err)
	}
	contextEvents, err := session.GetContextEvents()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range contextEvents {
		var payload eventing.LLMToolCallResult
		if event.EventType == eventing.LLMToolCallResultCreated && json.Unmarshal([]byte(event.Payload), &payload) == nil {
			if payload.Error != "" || !strings.HasPrefix(payload.Message.Content, "sandbox error: sudo:") {
				t.Fatalf("result = %+v", payload)
			}
			return
		}
	}
	t.Fatal("no tool result")
}

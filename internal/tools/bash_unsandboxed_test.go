package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

// unsandboxedTestRegistry fails every sandboxed command with stderr and
// records which runner each command reached.
func unsandboxedTestRegistry(t *testing.T, calls *[]string, stderr string) *Registry {
	t.Helper()
	return &Registry{
		WorkspaceDir:  t.TempDir(),
		AskSandboxOff: true,
		RunSandboxedOptions: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
			*calls = append(*calls, "sandboxed:"+command)
			return "", stderr, errors.New("exit status 1")
		},
		RunUnsandboxed: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
			*calls = append(*calls, "unsandboxed:"+command)
			return "host", "", nil
		},
	}
}

const sandboxRefusal = "touch: /etc/caretz-test: Operation not permitted"

func TestBashSandboxDenialEscalatesToUser(t *testing.T) {
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, sandboxRefusal)
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"touch /etc/caretz-test"}`}
	_, err := registry.HandleToolCallContext(context.Background(), call)
	permissionErr, ok := AsPermissionError(err)
	if !ok || permissionErr.Capability != PermissionSandboxOff || permissionErr.Target != "touch /etc/caretz-test" || !permissionErr.MayHaveRun {
		t.Fatalf("error = %#v", err)
	}
	if !strings.Contains(permissionErr.Reason, "/etc/caretz-test") {
		t.Fatalf("reason = %q", permissionErr.Reason)
	}
	if permissionErr.Fallback == nil || permissionErr.Fallback.Content != "sandbox error: "+sandboxRefusal || permissionErr.Fallback.ToolCallID != "1" {
		t.Fatalf("fallback = %#v", permissionErr.Fallback)
	}

	other := WithPermissionGrant(context.Background(), PermissionSandboxOff, "touch /etc/other", true)
	if _, err := registry.HandleToolCallContext(other, call); err == nil {
		t.Fatal("grant for a different command was accepted")
	}
	granted := WithPermissionGrant(context.Background(), PermissionSandboxOff, "touch /etc/caretz-test", false)
	msg, err := registry.HandleToolCallContext(granted, call)
	if err != nil || msg.Content != unsandboxedResultNote+"host" {
		t.Fatalf("result = %#v, %v", msg, err)
	}
	want := "sandboxed:touch /etc/caretz-test,sandboxed:touch /etc/caretz-test,unsandboxed:touch /etc/caretz-test"
	if strings.Join(calls, ",") != want {
		t.Fatalf("calls = %v", calls)
	}
}

func TestBashSandboxDenialWithoutFlagKeepsFailing(t *testing.T) {
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, sandboxRefusal)
	registry.AskSandboxOff = false
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"touch /etc/caretz-test"}`}
	granted := WithPermissionGrant(context.Background(), PermissionSandboxOff, "touch /etc/caretz-test", false)
	msg, err := registry.HandleToolCallContext(granted, call)
	if err != nil || !strings.Contains(msg.Content, "Operation not permitted") {
		t.Fatalf("result = %#v, %v", msg, err)
	}
	registry.AskSandboxOff = true
	registry.SetPlanMode(true)
	if _, err := registry.HandleToolCallContext(granted, call); err != nil {
		t.Fatalf("plan mode escalated: %v", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "unsandboxed:") {
			t.Fatalf("commands ran unsandboxed: %v", calls)
		}
	}
}

func TestBashOrdinaryFailureDoesNotEscalate(t *testing.T) {
	workspace := t.TempDir()
	for name, stderr := range map[string]string{
		"not a refusal":     "ls: /nope: No such file or directory",
		"inside workspace":  "touch: " + filepath.Join(workspace, "x") + ": Operation not permitted",
		"no output at all":  "",
		"generic exit code": "make: *** [test] Error 2",
	} {
		t.Run(name, func(t *testing.T) {
			var calls []string
			registry := unsandboxedTestRegistry(t, &calls, stderr)
			registry.WorkspaceDir = workspace
			call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"false"}`}
			if _, err := registry.HandleToolCallContext(context.Background(), call); err != nil {
				t.Fatalf("escalated: %v", err)
			}
		})
	}
}

func TestSandboxOffApprovalBypassesSessionSupervisor(t *testing.T) {
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, sandboxRefusal)
	registry.SessionExec = true
	registry.NewSandboxSession = func() *sandbox.Session { t.Fatal("approved command started the session supervisor"); return nil }
	registry, _ = registry.BindSessionExecution()
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"launchctl list"}`}
	ctx := WithPermissionGrant(context.Background(), PermissionSandboxOff, "launchctl list", false)
	if msg, err := registry.HandleToolCallContext(ctx, call); err != nil || msg.Content != unsandboxedResultNote+"host" {
		t.Fatalf("result = %#v, %v", msg, err)
	}
	if escalation := registry.sandboxOffEscalation(registry.WorkspaceDir, "launchctl list", "", sandboxRefusal, true, llm.Message{}, false); escalation != nil {
		t.Fatalf("background command escalated: %v", escalation)
	}
}

func TestBashSchemaHasNoSandboxOption(t *testing.T) {
	for _, def := range (&Registry{AskSandboxOff: true}).ToolDefinitions() {
		if def.Name == "Bash" {
			if props := def.Parameters["properties"].(map[string]any); props["unsandboxed"] != nil {
				t.Fatal("model can choose to leave the sandbox")
			}
		}
	}
}

func TestSandboxOffIsNotARegistryGrant(t *testing.T) {
	if err := (&Registry{}).AddPermissionGrant(PermissionSandboxOff, "ls", false); err == nil {
		t.Fatal("sandbox_off accepted as a registry grant")
	}
}

func TestBashFailureKeepsStdoutForModel(t *testing.T) {
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, "")
	registry.AskSandboxOff = false
	registry.RunSandboxedOptions = func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
		return "failed to connect to local tailscaled\n", "", errors.New("exit status 1")
	}
	msg, err := registry.HandleToolCall(llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"tailscale status 2>&1"}`})
	if err != nil || msg.Content != "failed to connect to local tailscaled\nerror: exit status 1" {
		t.Fatalf("result = %#v, %v", msg, err)
	}
}

func TestSandboxOffApprovalPrefersTerminalRunner(t *testing.T) {
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, sandboxRefusal)
	ctx := WithPermissionGrant(context.Background(), PermissionSandboxOff, "sudo systemctl restart ollama", false)
	ctx = WithForegroundRunner(ctx, func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		calls = append(calls, "terminal:"+command)
		return "restarted", "", nil
	})
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"sudo systemctl restart ollama"}`}
	if msg, err := registry.HandleToolCallContext(ctx, call); err != nil || msg.Content != unsandboxedResultNote+"restarted" {
		t.Fatalf("result = %#v, %v", msg, err)
	}
	if strings.Join(calls, ",") != "terminal:sudo systemctl restart ollama" {
		t.Fatalf("calls = %v", calls)
	}
}

// A successful command that prints refusal text, such as a log search or a
// command that hides its exit status, keeps its result.
func TestSuccessfulCommandPrintingRefusalDoesNotEscalate(t *testing.T) {
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, "")
	registry.RunSandboxedOptions = func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
		return "sudo: The \"no new privileges\" flag is set, which prevents sudo from running as root.\nopen /etc/shadow: operation not permitted\n", "", nil
	}
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"grep -h 'not permitted' /var/log/syslog"}`}
	msg, err := registry.HandleToolCallContext(context.Background(), call)
	if err != nil || !strings.Contains(msg.Content, "/etc/shadow") {
		t.Fatalf("result = %#v, %v", msg, err)
	}
}

func TestSessionApprovedCommandSkipsTheSandbox(t *testing.T) {
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, sandboxRefusal)
	registry.SandboxEnvironment = func() (map[string]string, error) { return map[string]string{"GH_CONFIG_DIR": "/gh"}, nil }
	var env map[string]string
	registry.RunUnsandboxed = func(_ context.Context, _, command string, opts sandbox.RunOptions) (string, string, error) {
		calls, env = append(calls, "unsandboxed:"+command), opts.Env
		return "host", "", nil
	}
	ctx := WithSessionPermissions(context.Background(), func(capability, target string) bool {
		return capability == PermissionSandboxOff && target == "echo >> log; sudo true"
	})
	approved := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"echo >> log; sudo true"}`}
	if msg, err := registry.HandleToolCallContext(ctx, approved); err != nil || msg.Content != unsandboxedResultNote+"host" {
		t.Fatalf("result = %#v, %v", msg, err)
	}
	if strings.Join(calls, ",") != "unsandboxed:echo >> log; sudo true" {
		t.Fatalf("session-approved command ran in the sandbox first: %v", calls)
	}
	if env["GH_CONFIG_DIR"] != "/gh" {
		t.Fatalf("rerun lost the sandbox environment: %v", env)
	}
	other := llm.ToolCall{ID: "2", Name: "Bash", Arguments: `{"command":"sudo true"}`}
	if _, err := registry.HandleToolCallContext(ctx, other); err == nil {
		t.Fatal("a different command skipped the sandbox")
	}
}

func TestApprovedRerunReportsWhatHappened(t *testing.T) {
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, sandboxRefusal)
	command := "sudo tee /etc/x > /dev/null <<'EOF'\nok\nEOF"
	args, _ := json.Marshal(map[string]string{"command": command})
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: string(args)}
	ctx := WithPermissionGrant(context.Background(), PermissionSandboxOff, command, false)

	registry.RunUnsandboxed = func(context.Context, string, string, sandbox.RunOptions) (string, string, error) { return "", "", nil }
	if msg, err := registry.HandleToolCallContext(ctx, call); err != nil || msg.Content != unsandboxedResultNote+"(no output)" {
		t.Fatalf("silent success = %#v, %v", msg, err)
	}
	registry.RunUnsandboxed = func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
		return "", "sudo: a terminal is required to read the password\n", errors.New("exit status 1")
	}
	if msg, err := registry.HandleToolCallContext(ctx, call); err != nil || msg.Content != unsandboxedResultNote+"sudo: a terminal is required to read the password\nerror: exit status 1" {
		t.Fatalf("failure = %#v, %v", msg, err)
	}
}

// "; echo $?" or a pipe hides a failure, so a hidden host service counts
// even when the command succeeded. The approval names the command, not the
// service.
func TestHiddenHostServiceEscalatesEvenWhenCommandSucceeds(t *testing.T) {
	// The refusal counts only when the socket exists on the host.
	socket := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dockerRefusal := "Cannot connect to the Docker daemon at unix://" + socket + ". Is the docker daemon running?"
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, "")
	registry.RunSandboxedOptions = func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		calls = append(calls, "sandboxed:"+command)
		return dockerRefusal + "\nexit=1\n", "", nil
	}
	command := `docker rmi app:v1; echo "exit=$?"`
	call := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"docker rmi app:v1; echo \"exit=$?\""}`}
	_, err := registry.HandleToolCallContext(context.Background(), call)
	permissionErr, ok := AsPermissionError(err)
	if !ok || permissionErr.Capability != PermissionSandboxOff || permissionErr.Target != command || permissionErr.Command != command || !strings.Contains(permissionErr.Reason, "docker daemon "+socket) {
		t.Fatalf("error = %#v", err)
	}
	if permissionErr.Fallback == nil || !strings.Contains(permissionErr.Fallback.Content, "exit=1") {
		t.Fatalf("fallback = %#v", permissionErr.Fallback)
	}
	granted := WithPermissionGrant(context.Background(), PermissionSandboxOff, command, false)
	if msg, err := registry.HandleToolCallContext(granted, call); err != nil || msg.Content != unsandboxedResultNote+"host" {
		t.Fatalf("result = %#v, %v", msg, err)
	}
	if got := strings.Join(calls, ","); got != "sandboxed:"+command+",unsandboxed:"+command {
		t.Fatalf("calls = %v", calls)
	}
}

// Models probe sudo with "; echo $?", which hides the failure. sudo's own
// refusal from a command that runs sudo still asks the user, while text that
// only quotes the refusal does not.
func TestSudoRefusalEscalatesEvenWhenCommandSucceeds(t *testing.T) {
	const refusal = "sudo: /etc/sudo.conf is owned by uid 65534, should be 0\n" +
		"sudo: The \"no new privileges\" flag is set, which prevents sudo from running as root.\n"
	for _, tc := range []struct {
		name, command, output string
		escalates             bool
	}{
		{"probe", `sudo -n true 2>&1; echo "exit=$?"`, refusal + "exit=1\n", true},
		{"after another command", `cd /tmp && sudo systemctl status ollama | head`, refusal, true},
		{"log search", `grep -r "no new privileges" /var/log`, "/var/log/caretz.log:" + refusal, false},
		{"quoted without sudo", `cat notes.txt`, refusal, false},
		{"sudo inside a word", `pseudo-tool --check`, refusal, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			registry := unsandboxedTestRegistry(t, &calls, "")
			registry.RunSandboxedOptions = func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
				return tc.output, "", nil
			}
			args, _ := json.Marshal(map[string]string{"command": tc.command})
			_, err := registry.HandleToolCallContext(context.Background(), llm.ToolCall{ID: "1", Name: "Bash", Arguments: string(args)})
			permissionErr, ok := AsPermissionError(err)
			if ok != tc.escalates {
				t.Fatalf("escalated = %v, want %v (err %v)", ok, tc.escalates, err)
			}
			if ok && (permissionErr.Target != tc.command || !strings.Contains(permissionErr.Reason, sandbox.SudoDenial.Target)) {
				t.Fatalf("request = %#v", permissionErr)
			}
		})
	}
}

// A session approval covers only the approved command. Another command that
// prints the same refusal, which the model controls, still asks.
func TestSessionApprovalDoesNotCoverOtherCommandsWithTheSameRefusal(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	registry := unsandboxedTestRegistry(t, &calls, "")
	registry.RunSandboxedOptions = func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		calls = append(calls, "sandboxed:"+command)
		return "Cannot connect to the Docker daemon at unix://" + socket + ". Is the docker daemon running?\n", "", nil
	}
	ctx := WithSessionPermissions(context.Background(), func(capability, target string) bool {
		return capability == PermissionSandboxOff && target == "docker images"
	})
	approved := llm.ToolCall{ID: "1", Name: "Bash", Arguments: `{"command":"docker images"}`}
	if msg, err := registry.HandleToolCallContext(ctx, approved); err != nil || msg.Content != unsandboxedResultNote+"host" {
		t.Fatalf("approved result = %#v, %v", msg, err)
	}
	other := llm.ToolCall{ID: "2", Name: "Bash", Arguments: `{"command":"tail build.log"}`}
	_, err := registry.HandleToolCallContext(ctx, other)
	if permissionErr, ok := AsPermissionError(err); !ok || permissionErr.Target != "tail build.log" {
		t.Fatalf("other command error = %#v", err)
	}
	if got := strings.Join(calls, ","); got != "unsandboxed:docker images,sandboxed:tail build.log" {
		t.Fatalf("calls = %v", calls)
	}
}

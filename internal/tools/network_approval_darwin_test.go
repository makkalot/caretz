//go:build darwin

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/lib/llm"
)

func TestBashLiveNetworkApprovalIntegration(t *testing.T) {
	if os.Getenv("CARETZ_RUN_SANDBOX_INTEGRATION") != "1" {
		t.Skip("native sandbox integration disabled")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("approved")) }))
	defer upstream.Close()
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-shot", true: "persistent"}[persistent], func(t *testing.T) {
			runner := sandbox.NewRunner(sandbox.Config{})
			registry := &Registry{WorkspaceDir: t.TempDir(), SessionExec: persistent, RunSandboxedOptions: runner.RunContextOptions, NewSandboxSession: func() *sandbox.Session { return runner.NewSession(false) }}
			registry, _ = registry.BindSessionExecution()
			defer func() { _ = registry.CloseSessionExecution() }()
			var approvals atomic.Int32
			ctx := WithPermissionRequester(t.Context(), func(_ context.Context, call llm.ToolCall, permission *PermissionError) (bool, error) {
				if call.ID != "live-network" || permission.Target != strings.TrimPrefix(upstream.URL, "http://") || !permission.MayHaveRun {
					t.Errorf("incorrect permission: %#v %#v", call, permission)
				}
				approvals.Add(1)
				return true, nil
			})
			args, _ := json.Marshal(BashToolArguments{Command: "curl -fsS --max-time 5 " + upstream.URL})
			msg, err := registry.HandleToolCallContext(ctx, llm.ToolCall{ID: "live-network", Name: "Bash", Arguments: string(args)})
			if err != nil || msg.Content != "approved" || approvals.Load() != 1 {
				t.Fatalf("result=%#v err=%v approvals=%d", msg, err, approvals.Load())
			}
		})
	}
}

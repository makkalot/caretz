package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/makkalot/caretz/acp"
	"github.com/makkalot/caretz/cli"
)

func TestRuntimeHostsMultiplexedACPSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	host := acp.NewHost(context.Background())
	t.Cleanup(func() { _ = host.Close() })
	if err := host.RegisterWorkspace("repo", acp.WorkspaceConfig{Runtime: func(ctx context.Context) (acp.Runtime, error) {
		var stderr bytes.Buffer
		runtime, code := cli.BuildRuntime(ctx, []string{
			"--workspace", workspace,
			"--base-url", "http://127.0.0.1:1/v1",
			"--api-key", "test-key",
			"--model", "test-model",
			"--no-model-discovery",
			"--tools", "Read",
		}, &stderr, cli.Options{})
		if code != 0 || runtime == nil {
			return nil, fmt.Errorf("BuildRuntime() code = %d, runtime = %v, stderr = %q", code, runtime, stderr.String())
		}
		return runtime, nil
	}}); err != nil {
		t.Fatal(err)
	}
	endpoint, err := host.Endpoint(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"sessionId":"one"}}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":3,"method":"session/new","params":{"sessionId":"two"}}`),
	} {
		if err := endpoint.Send(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}

	want := map[float64]bool{1: false, 2: false, 3: false}
	deadline := time.After(5 * time.Second)
	for {
		complete := true
		for _, found := range want {
			complete = complete && found
		}
		if complete {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for ACP responses: %#v", want)
		case message := <-endpoint.Messages():
			var response map[string]any
			if err := json.Unmarshal(message, &response); err != nil {
				t.Fatalf("invalid ACP output %q: %v", message, err)
			}
			if id, ok := response["id"].(float64); ok {
				if _, tracked := want[id]; tracked {
					want[id] = true
				}
			}
		}
	}
}

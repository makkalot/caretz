//go:build unix

package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/makkalot/caretz/cli"
	"github.com/makkalot/caretz/internal/peers"
)

func TestRuntimePeerBotsListsBotsWithState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARETZ_CONFIG_ROOT", "")
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(workspace, ".caretz", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"reviewer.md": "---\ndescription: Reviews diffs\nmode: peer\n---\nReview.",
		"writer.md":   "---\ndescription: Writes docs\nmode: peer\n---\nWrite.",
		"helper.md":   "---\ndescription: One-off helper\nmode: subagent\n---\nHelp.",
	} {
		if err := os.WriteFile(filepath.Join(agents, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stderr bytes.Buffer
	runtime, code := cli.BuildRuntime(context.Background(), []string{
		"--workspace", workspace,
		"--base-url", "http://127.0.0.1:1/v1",
		"--model", "test-model",
		"--no-model-discovery",
	}, &stderr, cli.Options{})
	if code != 0 || runtime == nil {
		t.Fatalf("BuildRuntime() code = %d, stderr = %q", code, stderr.String())
	}

	// writer is running and has a message waiting.
	runDir, err := peers.OpenRunDir(filepath.Join(workspace, ".caretz"), workspace)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := runDir.Lock(peers.BotEndpoint("writer"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if err := runDir.Spool(peers.Message{ID: peers.NewMessageID(), From: peers.BotEndpoint("reviewer"), FromLabel: "reviewer", To: peers.BotEndpoint("writer"), Content: "draft notes", Hop: 1}); err != nil {
		t.Fatal(err)
	}

	bots, err := runtime.PeerBots()
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 2 {
		t.Fatalf("PeerBots() = %+v, want reviewer and writer only", bots)
	}
	reviewer, writer := bots[0], bots[1]
	if reviewer.Name != "reviewer" || reviewer.Description != "Reviews diffs" || reviewer.Running || reviewer.Queued != 0 {
		t.Errorf("reviewer = %+v, want an idle offline bot", reviewer)
	}
	if writer.Name != "writer" || !writer.Running || writer.PID != os.Getpid() || writer.Queued != 1 {
		t.Errorf("writer = %+v, want running in this process with 1 queued message", writer)
	}
	if want := filepath.Join(agents, "reviewer.md"); reviewer.Path != want {
		t.Errorf("reviewer.Path = %q, want %q", reviewer.Path, want)
	}
	if dirs := runtime.AgentsDirs(); !slices.Contains(dirs, agents) {
		t.Errorf("AgentsDirs() = %v, want it to include %s", dirs, agents)
	}

	// Editing a definition changes its revision; the other bot's stays.
	if reviewer.Revision == "" || reviewer.Revision == writer.Revision {
		t.Fatalf("revisions = %q / %q, want distinct non-empty", reviewer.Revision, writer.Revision)
	}
	if err := os.WriteFile(filepath.Join(agents, "reviewer.md"), []byte("---\ndescription: Reviews diffs\nmode: peer\n---\nReview harder."), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, err := runtime.PeerBots()
	if err != nil {
		t.Fatal(err)
	}
	if edited[0].Revision == reviewer.Revision || edited[1].Revision != writer.Revision {
		t.Errorf("revisions after editing reviewer = %q / %q, was %q / %q", edited[0].Revision, edited[1].Revision, reviewer.Revision, writer.Revision)
	}
}

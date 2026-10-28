package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/schedules"
)

func TestAdapterSessionSchedulesUseProfileDefaults(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(home, ".config/caretz/config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	data := `{"activeModel": "local/a", "provider": {"local": {"options": {"baseURL": "http://127.0.0.1:8080/v1"}, "models": {"a": {"model": "qwen-a"}, "b": {"model": "qwen-b"}}}},
		"profiles": {"general": {"schedules": {"model": "local/b", "reasoning": "low"}}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	rt, code := BuildRuntime(t.Context(), []string{"--workspace", root, "--no-model-discovery", "--incognito"}, &stderr)
	if code != 0 {
		t.Fatalf("runtime startup=%d: %s", code, stderr.String())
	}
	session, err := rt.NewAdapterSession("test", "schedules", AdapterSessionOptions{WorkspaceDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	integration, ok := session.AgentSession().Integration(schedules.IntegrationName)
	if !ok {
		t.Fatal("adapter session has no scheduler")
	}
	if got := integration.(*schedules.Integration).Defaults(); got.Model != "local/b" || got.Reasoning != "low" {
		t.Fatalf("defaults = %#v, want local/b low", got)
	}
	// A scheduler an adapter installs afterwards must not replace the one
	// carrying the profile's defaults.
	if err := session.AgentSession().InstallIntegration(schedules.NewIntegration); err != nil {
		t.Fatal(err)
	}
	integration, _ = session.AgentSession().Integration(schedules.IntegrationName)
	if got := integration.(*schedules.Integration).Defaults(); got.Model != "local/b" {
		t.Fatalf("defaults after reinstall = %#v, want local/b", got)
	}
}

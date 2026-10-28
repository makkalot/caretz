package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/config"
)

func TestAuthCommandSetListRemove(t *testing.T) {
	t.Setenv(config.CredentialsFileEnv, "")
	dir := t.TempDir()
	homePath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(homePath, []byte(`{"provider":{"openrouter":{"options":{"baseURL":"https://openrouter.ai/api/v1"}},"local":{"options":{"baseURL":"http://127.0.0.1:8080/v1"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(action func(authCommand, []string) int, input string, args ...string) (int, string, string) {
		var out, errOut strings.Builder
		cmd := authCommand{homePath: homePath, in: strings.NewReader(input), out: &out, errOut: &errOut}
		return action(cmd, args), out.String(), errOut.String()
	}

	if code, _, errOut := run(authCommand.set, "sk-or-v1-abcdefgh1234\n", "openrouter"); code != 0 {
		t.Fatalf("set: %d %s", code, errOut)
	}
	if code, _, errOut := run(authCommand.set, "tvly-secret-9876\n", "search/tavily"); code != 0 {
		t.Fatalf("set search: %d %s", code, errOut)
	}
	if code, _, _ := run(authCommand.set, "x\n", "search/unknown"); code == 0 {
		t.Fatal("accepted an unknown search service")
	}
	if code, _, _ := run(authCommand.set, "\n", "openrouter"); code == 0 {
		t.Fatal("accepted an empty key")
	}

	code, out, errOut := run(authCommand.list, "")
	if code != 0 {
		t.Fatalf("list: %d %s", code, errOut)
	}
	if strings.Contains(out, "abcdefgh") || strings.Contains(out, "tvly-secret") {
		t.Fatalf("list printed a key: %s", out)
	}
	for _, want := range []string{"credentials.json …1234", "search/tavily", "local", "not set"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}

	cfg, err := config.Load(homePath, "")
	if err != nil {
		t.Fatal(err)
	}
	if key := cfg.ProviderAPIKey("openrouter"); key != "sk-or-v1-abcdefgh1234" {
		t.Fatalf("stored key = %q", key)
	}

	if code, _, errOut := run(authCommand.rm, "", "openrouter"); code != 0 {
		t.Fatalf("rm: %d %s", code, errOut)
	}
	if code, _, _ := run(authCommand.rm, "", "openrouter"); code == 0 {
		t.Fatal("removing a missing key succeeded")
	}
	creds, err := config.LoadCredentials(config.CredentialsPath(homePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := creds.Provider["openrouter"]; ok || creds.Search["tavily"] == "" {
		t.Fatalf("credentials after rm = %+v", creds)
	}
}

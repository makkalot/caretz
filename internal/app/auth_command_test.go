package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/config"
)

func TestAuthLoginNamesTheServiceAndLogoutTheProvider(t *testing.T) {
	t.Setenv(config.CredentialsFileEnv, "")
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"auth", "login"}, {"auth", "logout"}, {"auth", "login", "chatgpt", "a", "b"}} {
		var stderr strings.Builder
		if code := Run(context.Background(), args, io.Discard, &stderr); code != 2 {
			t.Errorf("%v: code=%d stderr=%q, want a usage error", args, code, stderr.String())
		}
	}
	var stderr strings.Builder
	if code := Run(context.Background(), []string{"auth", "login", "copilot"}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), `unknown sign-in service "copilot"; available: chatgpt`) {
		t.Fatalf("unknown service: code=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := Run(context.Background(), []string{"auth", "logout", "chatgpt"}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "chatgpt is not signed in") {
		t.Fatalf("logout: code=%d stderr=%q", code, stderr.String())
	}
	if got := config.SignInCommand("chatgpt", "work"); got != "caretz auth login chatgpt work" {
		t.Fatalf("SignInCommand = %q", got)
	}
}

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

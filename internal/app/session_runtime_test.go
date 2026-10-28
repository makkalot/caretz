package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	libruntime "github.com/makkalot/caretz/lib/runtime"
)

func TestRunResumeRestoresSessionModelInsteadOfProjectDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configRoot := t.TempDir()
	configPath := filepath.Join(configRoot, "config.json")
	writeModelConfig := func(active string) {
		t.Helper()
		data := fmt.Sprintf(`{
  "activeModel": %q,
  "provider": {
    "local": {
      "options": {"baseURL": "http://localhost:8000/v1"},
      "models": {
        "small": {"model": "small-model", "limit": {"context": 32000}},
        "big": {"model": "big-model", "limit": {"context": 128000}}
      }
    }
  }
}
`, active)
		if err := os.WriteFile(configPath, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}

	type selectedModel struct {
		ref     string
		model   string
		context int
	}
	var selected []selectedModel
	runner := func(params libruntime.InteractiveParams) error {
		selected = append(selected, selectedModel{ref: params.ModelRef, model: params.Model, context: params.Session.ContextWindow()})
		return nil
	}
	run := func(sessionID string) {
		t.Helper()
		var stderr byteWriter
		code := RunWithOptions(context.Background(), []string{"--config-root", configRoot, "--no-model-discovery", "-s", sessionID}, io.Discard, &stderr, Options{InteractiveRunner: runner})
		if code != 0 {
			t.Fatalf("RunWithOptions(%s) code = %d, stderr = %q", sessionID, code, stderr.String())
		}
	}

	writeModelConfig("local/small")
	run("session-a")
	writeModelConfig("local/big")
	run("session-b")
	run("session-a")

	want := []selectedModel{
		{ref: "local/small", model: "small-model", context: 32000},
		{ref: "local/big", model: "big-model", context: 128000},
		{ref: "local/small", model: "small-model", context: 32000},
	}
	if fmt.Sprint(selected) != fmt.Sprint(want) {
		t.Fatalf("selected models = %#v, want %#v", selected, want)
	}
}

func TestRunResumeRestoresSessionProfileUnlessExplicitlyOverridden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARETZ_PROFILE", "")
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(`{
  "activeModel": "local/test",
  "provider": {
    "local": {
      "options": {"baseURL": "http://localhost:8000/v1"},
      "models": {"test": {"model": "test-model", "limit": {"context": 32000}}}
    }
  }
}`), 0644); err != nil {
		t.Fatal(err)
	}

	var profiles []string
	runner := func(params libruntime.InteractiveParams) error {
		profiles = append(profiles, params.Profile)
		return nil
	}
	run := func(extra ...string) {
		t.Helper()
		args := []string{"--config-root", configRoot, "--no-model-discovery", "-s", "profile-session"}
		args = append(args, extra...)
		var stderr byteWriter
		if code := RunWithOptions(context.Background(), args, io.Discard, &stderr, Options{InteractiveRunner: runner}); code != 0 {
			t.Fatalf("RunWithOptions(%v) code = %d, stderr = %q", extra, code, stderr.String())
		}
	}

	run("--profile", "coding")
	run()
	run("--profile", "general")

	if got, want := fmt.Sprint(profiles), "[coding coding general]"; got != want {
		t.Fatalf("profiles = %s, want %s", got, want)
	}
	state, found, err := loadSessionRuntime(sessionRuntimePath(filepath.Join(configRoot, "sessions"), "tui", "profile-session"))
	if err != nil || !found {
		t.Fatalf("loadSessionRuntime() = (%+v, %t, %v)", state, found, err)
	}
	if state.Profile != "general" {
		t.Fatalf("saved profile = %q, want general", state.Profile)
	}
}

type byteWriter []byte

func (w *byteWriter) Write(p []byte) (int, error) {
	*w = append(*w, p...)
	return len(p), nil
}

func (w *byteWriter) String() string { return string(*w) }

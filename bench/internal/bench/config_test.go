package bench

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadModelResolvesNamedYAML(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "models"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "models", "qwen-q6.yaml")
	if err := os.WriteFile(path, []byte(`
name: qwen-27b-q6
endpoint: http://localhost:8081/v1
model: qwen-27b
context_size: 65536
temperature: 0
metadata:
  quantization: Q6_K
`), 0644); err != nil {
		t.Fatal(err)
	}

	model, err := LoadModel(root, "qwen-q6")
	if err != nil {
		t.Fatalf("LoadModel() error = %v", err)
	}
	if model.Name != "qwen-27b-q6" {
		t.Fatalf("Name = %q, want qwen-27b-q6", model.Name)
	}
	if model.Endpoint != "http://localhost:8081/v1" {
		t.Fatalf("Endpoint = %q", model.Endpoint)
	}
	if model.ContextSize != 65536 {
		t.Fatalf("ContextSize = %d, want 65536", model.ContextSize)
	}
	if model.Temperature == nil || *model.Temperature != 0 {
		t.Fatalf("Temperature = %#v, want 0", model.Temperature)
	}
}

func TestLoadModelParsesLLMTimeout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "models"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "models", "slow.yaml")
	if err := os.WriteFile(path, []byte(`
name: slow-model
endpoint: http://localhost:8081/v1
model: slow
context_size: 65536
llm_timeout: 12m
`), 0644); err != nil {
		t.Fatal(err)
	}

	model, err := LoadModel(root, "slow")
	if err != nil {
		t.Fatalf("LoadModel() error = %v", err)
	}
	if !model.LLMTimeout.Set {
		t.Fatal("LLMTimeout.Set = false, want true")
	}
	if model.LLMTimeout.Duration != 12*time.Minute {
		t.Fatalf("LLMTimeout.Duration = %s, want 12m", model.LLMTimeout.Duration)
	}
}

func TestAppendCaretzModelArgs(t *testing.T) {
	t.Run("base omits optional flags", func(t *testing.T) {
		model := ModelConfig{Endpoint: "http://localhost:8081/v1", Model: "qwen-27b"}
		got := appendCaretzModelArgs(nil, model)
		want := []string{"--base-url", "http://localhost:8081/v1", "--model", "qwen-27b"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("appendCaretzModelArgs() = %#v, want %#v", got, want)
		}
	})

	t.Run("full includes api key and context window", func(t *testing.T) {
		model := ModelConfig{Endpoint: "http://localhost:8081/v1", Model: "qwen-27b", APIKey: "test-key", ContextSize: 65536}
		got := appendCaretzModelArgs(nil, model)
		want := []string{"--base-url", "http://localhost:8081/v1", "--model", "qwen-27b", "--api-key", "test-key", "--context-window", "65536"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("appendCaretzModelArgs() = %#v, want %#v", got, want)
		}
	})
}

func TestEffectiveLLMTimeoutPrefersRunOptionOverModel(t *testing.T) {
	model := ModelConfig{LLMTimeout: DurationOption{Duration: 12 * time.Minute, Set: true}}
	timeout, ok := effectiveLLMTimeout(model, RunOptions{LLMTimeout: 0, LLMTimeoutSet: true})
	if !ok {
		t.Fatal("effectiveLLMTimeout ok = false, want true")
	}
	if timeout != 0 {
		t.Fatalf("effectiveLLMTimeout = %s, want disabled timeout 0", timeout)
	}
}

func TestLoadSuiteDefaultsTaskBenchmarkAndTimeout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "suites"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "suites", "swe-smoke-v1.yaml")
	if err := os.WriteFile(path, []byte(`
name: swe-smoke-v1
benchmark: swebench
tasks:
  - id: django__django-12345
    workspace: /tmp/work
    prompt: fix it
`), 0644); err != nil {
		t.Fatal(err)
	}

	suite, err := LoadSuite(root, "swe-smoke-v1")
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	if suite.TimeoutSeconds != defaultTimeoutSeconds {
		t.Fatalf("TimeoutSeconds = %d, want default %d", suite.TimeoutSeconds, defaultTimeoutSeconds)
	}
	if suite.Tasks[0].Benchmark != "swebench" {
		t.Fatalf("task benchmark = %q, want swebench", suite.Tasks[0].Benchmark)
	}
}

func TestLoadSuiteParsesSandboxDisable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "suites"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "suites", "terminal.yaml")
	if err := os.WriteFile(path, []byte(`
name: terminal
benchmark: terminal-bench
tasks:
  - id: task-1
    task_dir: /tmp/task-1
  - id: task-2
    task_dir: /tmp/task-2
    sandbox_disable: true
`), 0644); err != nil {
		t.Fatal(err)
	}

	suite, err := LoadSuite(root, "terminal")
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	if suite.Tasks[0].SandboxDisable {
		t.Fatalf("task-1 SandboxDisable = true, want default false")
	}
	if !suite.Tasks[1].SandboxDisable {
		t.Fatalf("task-2 SandboxDisable = false, want true")
	}
}

func TestFilterSuiteTasksSelectsOneTask(t *testing.T) {
	tasks := []TaskSpec{
		{ID: "task-1"},
		{ID: "task-2"},
	}

	filtered, err := filterSuiteTasks(tasks, "task-2")
	if err != nil {
		t.Fatalf("filterSuiteTasks() error = %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "task-2" {
		t.Fatalf("filtered = %#v, want only task-2", filtered)
	}
}

func TestLoadSuiteParsesTier(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "suites", "daily.yaml"), `
name: daily
benchmark: local
tasks:
  - id: task-1
    tier: daily-simple
  - id: task-2
`)

	suite, err := LoadSuite(root, "daily")
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	if suite.Tasks[0].Tier != "daily-simple" || suite.Tasks[1].Tier != "" {
		t.Fatalf("tiers = %q, %q; want daily-simple and empty", suite.Tasks[0].Tier, suite.Tasks[1].Tier)
	}
}

func TestFilterSuiteTierSelectsMatchingTasks(t *testing.T) {
	tasks := []TaskSpec{
		{ID: "task-1", Tier: "daily-simple"},
		{ID: "task-2", Tier: "daily-harder"},
		{ID: "task-3", Tier: "daily-simple"},
	}

	filtered, err := filterSuiteTier(tasks, "daily-simple")
	if err != nil {
		t.Fatalf("filterSuiteTier() error = %v", err)
	}
	if len(filtered) != 2 || filtered[0].ID != "task-1" || filtered[1].ID != "task-3" {
		t.Fatalf("filtered = %#v, want task-1 and task-3", filtered)
	}
}

func TestFilterSuiteTierReportsAvailableTiers(t *testing.T) {
	tasks := []TaskSpec{
		{ID: "task-1", Tier: "daily-simple"},
		{ID: "task-2", Tier: "daily-harder"},
		{ID: "task-3", Tier: "daily-simple"},
	}

	_, err := filterSuiteTier(tasks, "missing")
	want := `tier "missing" not found in suite; available tiers: daily-simple, daily-harder`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}

	_, err = filterSuiteTier([]TaskSpec{{ID: "task-1"}}, "daily-simple")
	want = `tier "daily-simple" not found in suite; suite tasks have no tiers`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestFilterSuiteTasksReportsAvailableTasks(t *testing.T) {
	tasks := []TaskSpec{
		{ID: "task-1"},
		{ID: "task-2"},
	}

	_, err := filterSuiteTasks(tasks, "missing")
	if err == nil {
		t.Fatal("filterSuiteTasks() error is nil, want missing task error")
	}
	want := `task "missing" not found in suite; available tasks: task-1, task-2`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

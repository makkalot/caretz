package bench

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunnerRunCreatesArtifactsWithFakeCaretz(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	workspace := t.TempDir()
	runCmd(t, workspace, "git", "init", "--quiet")
	writeFile(t, filepath.Join(workspace, "file.txt"), "before\n")
	runCmd(t, workspace, "git", "add", "file.txt")
	runCmd(t, workspace, "git", "-c", "user.name=Bench Test", "-c", "user.email=bench@example.com", "commit", "--quiet", "-m", "seed")
	writeFile(t, filepath.Join(root, "suites", "smoke.yaml"), `
name: smoke
benchmark: swebench
timeout_seconds: 30
tasks:
  - id: task-1
    workspace: `+workspace+`
    prompt: fix file
    evaluator_command: test "$(cat file.txt)" = "after"
`)
	caretz := filepath.Join(root, "fake-caretz.sh")
	writeFile(t, caretz, `#!/bin/sh
config_root=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --config-root) config_root="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf "after\n" > file.txt
mkdir -p "$config_root/sessions/cli/task-1"
cat > "$config_root/sessions/cli/task-1/context.jsonpl" <<'EOF'
{"event_type":"LLMAgentResponse.Created","payload":{"usage":{"PromptTokens":3,"CompletionTokens":2,"TotalTokens":5}}}
EOF
`)
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	runDir, results, err := NewRunner().Run(nil, RunOptions{
		Root:       root,
		ModelName:  "mock",
		SuiteName:  "smoke",
		CaretzBin:  relativePathFromCwd(t, caretz),
		OutputRoot: filepath.Join(root, "runs"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed || results[0].Status != "PASS" {
		t.Fatalf("results = %#v, want one PASS", results)
	}
	for _, path := range []string{
		filepath.Join(runDir, "run.json"),
		filepath.Join(runDir, "results", "task-1.json"),
		filepath.Join(runDir, "logs", "task-1.log"),
		filepath.Join(runDir, "patches", "task-1.diff"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing artifact %s: %v", path, err)
		}
	}
	logData, err := os.ReadFile(filepath.Join(runDir, "logs", "task-1.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "[bench] starting task task-1") || !strings.Contains(string(logData), "[bench] running CARETZ") {
		t.Fatalf("task log missing bench progress markers:\n%s", logData)
	}
}

func TestRunnerCanEvaluatePatchAfterCaretzError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	workspace := t.TempDir()
	runCmd(t, workspace, "git", "init", "--quiet")
	writeFile(t, filepath.Join(workspace, "file.txt"), "before\n")
	runCmd(t, workspace, "git", "add", "file.txt")
	runCmd(t, workspace, "git", "-c", "user.name=Bench Test", "-c", "user.email=bench@example.com", "commit", "--quiet", "-m", "seed")
	writeFile(t, filepath.Join(root, "suites", "smoke.yaml"), `
name: smoke
benchmark: swebench
timeout_seconds: 30
tasks:
  - id: task-1
    workspace: `+workspace+`
    prompt: fix file
    evaluator_command: test "$(cat file.txt)" = "after"
`)
	caretz := filepath.Join(root, "fake-caretz.sh")
	writeFile(t, caretz, `#!/bin/sh
config_root=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --config-root) config_root="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf "after\n" > file.txt
mkdir -p "$config_root/sessions/cli/task-1"
cat > "$config_root/sessions/cli/task-1/context.jsonpl" <<'EOF'
{"event_type":"LLMAgentResponse.Created","payload":{"usage":{"PromptTokens":3,"CompletionTokens":2,"TotalTokens":5}}}
EOF
echo "error: failed to run llm loop: LLM returned empty response 3 consecutive times" >&2
exit 1
`)
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	_, results, err := NewRunner().Run(nil, RunOptions{
		Root:                  root,
		ModelName:             "mock",
		SuiteName:             "smoke",
		CaretzBin:             relativePathFromCwd(t, caretz),
		OutputRoot:            filepath.Join(root, "runs"),
		EvaluateOnCaretzError: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed || results[0].Status != "PASS" {
		t.Fatalf("results = %#v, want one PASS", results)
	}
	if results[0].ExitReason != "caretz_error" {
		t.Fatalf("ExitReason = %q, want caretz_error", results[0].ExitReason)
	}
	if results[0].Error == "" {
		t.Fatalf("Error is empty, want preserved CARETZ failure detail")
	}
}

func TestRunnerVerbosePassesVerboseFlagToCaretz(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	workspace := t.TempDir()
	runCmd(t, workspace, "git", "init", "--quiet")
	writeFile(t, filepath.Join(workspace, "file.txt"), "before\n")
	runCmd(t, workspace, "git", "add", "file.txt")
	runCmd(t, workspace, "git", "-c", "user.name=Bench Test", "-c", "user.email=bench@example.com", "commit", "--quiet", "-m", "seed")
	writeFile(t, filepath.Join(root, "suites", "smoke.yaml"), `
name: smoke
benchmark: local
timeout_seconds: 30
tasks:
  - id: task-1
    workspace: `+workspace+`
    prompt: inspect file
    evaluator_command: test -f caretz-args.txt
`)
	caretz := filepath.Join(root, "fake-caretz.sh")
	writeFile(t, caretz, `#!/bin/sh
printf '%s\n' "$@" > caretz-args.txt
`)
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	_, results, err := NewRunner().Run(nil, RunOptions{
		Root:       root,
		ModelName:  "mock",
		SuiteName:  "smoke",
		CaretzBin:  relativePathFromCwd(t, caretz),
		OutputRoot: filepath.Join(root, "runs"),
		Verbose:    true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed || results[0].Status != "PASS" {
		t.Fatalf("results = %#v, want one PASS", results)
	}
	runArgsPath := filepath.Join(root, "runs")
	matches, err := filepath.Glob(filepath.Join(runArgsPath, "*", "workspaces", "task-1", "caretz-args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("caretz args files = %v, want one", matches)
	}
	args, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "\n-v\n") {
		t.Fatalf("caretz args missing -v:\n%s", args)
	}
}

func TestRunnerRunsOnlySelectedTask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	workspace1 := t.TempDir()
	runCmd(t, workspace1, "git", "init", "--quiet")
	writeFile(t, filepath.Join(workspace1, "file.txt"), "task-1\n")
	runCmd(t, workspace1, "git", "add", "file.txt")
	runCmd(t, workspace1, "git", "-c", "user.name=Bench Test", "-c", "user.email=bench@example.com", "commit", "--quiet", "-m", "seed")
	workspace2 := t.TempDir()
	runCmd(t, workspace2, "git", "init", "--quiet")
	writeFile(t, filepath.Join(workspace2, "file.txt"), "task-2\n")
	runCmd(t, workspace2, "git", "add", "file.txt")
	runCmd(t, workspace2, "git", "-c", "user.name=Bench Test", "-c", "user.email=bench@example.com", "commit", "--quiet", "-m", "seed")
	writeFile(t, filepath.Join(root, "suites", "smoke.yaml"), `
name: smoke
benchmark: local
timeout_seconds: 30
tasks:
  - id: task-1
    workspace: `+workspace1+`
    prompt: inspect file
    evaluator_command: test "$(cat file.txt)" = "task-1"
  - id: task-2
    workspace: `+workspace2+`
    prompt: inspect file
    evaluator_command: test "$(cat file.txt)" = "task-2"
`)
	caretz := filepath.Join(root, "fake-caretz.sh")
	writeFile(t, caretz, `#!/bin/sh
:
`)
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	runDir, results, err := NewRunner().Run(nil, RunOptions{
		Root:       root,
		ModelName:  "mock",
		SuiteName:  "smoke",
		TaskID:     "task-2",
		CaretzBin:  relativePathFromCwd(t, caretz),
		OutputRoot: filepath.Join(root, "runs"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || results[0].TaskID != "task-2" || !results[0].Passed {
		t.Fatalf("results = %#v, want one PASS for task-2", results)
	}
	if _, err := os.Stat(filepath.Join(runDir, "results", "task-1.json")); !os.IsNotExist(err) {
		t.Fatalf("task-1 result exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "results", "task-2.json")); err != nil {
		t.Fatalf("task-2 result missing: %v", err)
	}
}

func TestRunnerRunsOnlySelectedTierAndRecordsIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
`)
	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, "answer.txt"), "42\n")
	writeFile(t, filepath.Join(root, "suites", "daily.yaml"), `
name: daily
benchmark: local
timeout_seconds: 30
tasks:
  - id: simple-1
    tier: daily-simple
    workspace: `+workspace+`
    prompt: answer
    evaluator_command: test "$(cat answer.txt)" = "42"
  - id: harder-1
    tier: daily-harder
    workspace: `+workspace+`
    prompt: answer
    evaluator_command: "true"
  - id: simple-2
    tier: daily-simple
    workspace: `+workspace+`
    prompt: answer
    evaluator_command: "false"
`)
	caretz := filepath.Join(root, "fake-caretz.sh")
	writeFile(t, caretz, `#!/bin/sh
:
`)
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	opts := RunOptions{
		Root:       root,
		ModelName:  "mock",
		SuiteName:  "daily",
		Tier:       "daily-simple",
		CaretzBin:  relativePathFromCwd(t, caretz),
		OutputRoot: filepath.Join(root, "runs"),
	}
	runDir, results, err := NewRunner().Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 2 || results[0].TaskID != "simple-1" || results[1].TaskID != "simple-2" {
		t.Fatalf("results = %#v, want simple-1 and simple-2", results)
	}
	if !results[0].Passed || results[1].Passed {
		t.Fatalf("passed = %v/%v, want true/false", results[0].Passed, results[1].Passed)
	}
	loaded, err := LoadResults(runDir)
	if err != nil {
		t.Fatalf("LoadResults() error = %v", err)
	}
	for _, result := range loaded {
		if result.Tier != "daily-simple" {
			t.Fatalf("stored result %s tier = %q, want daily-simple", result.TaskID, result.Tier)
		}
	}

	opts.TaskID = "harder-1"
	if _, _, err := NewRunner().Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), `task "harder-1" not found`) {
		t.Fatalf("Run() with task outside tier error = %v, want task not found", err)
	}
}

func TestRunnerClonesSeedWorkspaceWithoutDirtyChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	workspace := t.TempDir()
	runCmd(t, workspace, "git", "init", "--quiet")
	writeFile(t, filepath.Join(workspace, "file.txt"), "before\n")
	runCmd(t, workspace, "git", "add", "file.txt")
	runCmd(t, workspace, "git", "-c", "user.name=Bench Test", "-c", "user.email=bench@example.com", "commit", "--quiet", "-m", "seed")
	writeFile(t, filepath.Join(workspace, "file.txt"), "dirty\n")
	writeFile(t, filepath.Join(root, "suites", "smoke.yaml"), `
name: smoke
benchmark: swebench
timeout_seconds: 30
tasks:
  - id: task-1
    workspace: `+workspace+`
    prompt: inspect file
    evaluator_command: test "$(cat file.txt)" = "before"
`)
	caretz := filepath.Join(root, "fake-caretz.sh")
	writeFile(t, caretz, `#!/bin/sh
config_root=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --config-root) config_root="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$config_root/sessions/cli/task-1"
cat > "$config_root/sessions/cli/task-1/context.jsonpl" <<'EOF'
{"event_type":"LLMAgentResponse.Created","payload":{"usage":{"PromptTokens":3,"CompletionTokens":2,"TotalTokens":5}}}
EOF
`)
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	runDir, results, err := NewRunner().Run(nil, RunOptions{
		Root:       root,
		ModelName:  "mock",
		SuiteName:  "smoke",
		CaretzBin:  relativePathFromCwd(t, caretz),
		OutputRoot: filepath.Join(root, "runs"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed || results[0].Status != "PASS" {
		t.Fatalf("results = %#v, want one PASS", results)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "workspaces", "task-1", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\n" {
		t.Fatalf("run workspace file = %q, want committed seed content", data)
	}
	seedData, err := os.ReadFile(filepath.Join(workspace, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(seedData) != "dirty\n" {
		t.Fatalf("seed workspace changed to %q, want dirty content preserved", seedData)
	}
}

func TestRunnerCopiesNestedGitWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	repo := t.TempDir()
	runCmd(t, repo, "git", "init", "--quiet")
	workspace := filepath.Join(repo, "bench", "workspaces", "terminal", "task-1")
	writeFile(t, filepath.Join(workspace, "input.txt"), "source\n")
	writeFile(t, filepath.Join(root, "verifiers", "task-1", "answer.txt"), "done\n")
	writeFile(t, filepath.Join(root, "verifiers", "task-1", "run.sh"), `#!/bin/sh
test ! -e answer.txt
cmp output.txt "$BENCH_ROOT/verifiers/task-1/answer.txt"
`)
	writeFile(t, filepath.Join(root, "suites", "smoke.yaml"), `
name: smoke
benchmark: local
timeout_seconds: 30
tasks:
  - id: task-1
    workspace: `+workspace+`
    prompt: write output
    evaluator_command: sh "$BENCH_ROOT/verifiers/task-1/run.sh"
`)
	caretz := filepath.Join(root, "fake-caretz.sh")
	writeFile(t, caretz, `#!/bin/sh
printf "done\n" > output.txt
`)
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	runDir, results, err := NewRunner().Run(nil, RunOptions{
		Root:       root,
		ModelName:  "mock",
		SuiteName:  "smoke",
		CaretzBin:  relativePathFromCwd(t, caretz),
		OutputRoot: filepath.Join(root, "runs"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed || results[0].Status != "PASS" {
		t.Fatalf("results = %#v, want one PASS", results)
	}
	if _, err := os.Stat(filepath.Join(workspace, "output.txt")); !os.IsNotExist(err) {
		t.Fatalf("source workspace output exists or stat failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "workspaces", "task-1", "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "done\n" {
		t.Fatalf("run workspace output = %q, want done", data)
	}
}

func TestRunnerRunsTerminalBenchTaskWithDockerVerifier(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	taskDir := filepath.Join(root, "terminal-tasks", "task-1")
	writeFile(t, filepath.Join(taskDir, "instruction.md"), "fix the app\n")
	writeFile(t, filepath.Join(taskDir, "task.toml"), `
[environment]
docker_image = "example/task:latest"
allow_internet = true
`)
	writeFile(t, filepath.Join(taskDir, "tests", "test.sh"), `#!/bin/sh
echo 1 > /logs/verifier/reward.txt
`)
	writeFile(t, filepath.Join(root, "suites", "terminal.yaml"), `
name: terminal
benchmark: terminal-bench
timeout_seconds: 30
tasks:
  - id: task-1
    task_dir: `+taskDir+`
`)
	caretz := filepath.Join(root, "caretz-linux")
	writeFile(t, caretz, "#!/bin/sh\n")
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "fake-bin")
	logPath := filepath.Join(root, "docker-args.log")
	writeFile(t, filepath.Join(fakeBin, "docker"), `#!/bin/sh
printf '%s\n' "$*" >> "`+logPath+`"
case "$1" in
  run)
    echo fake-container-id
    ;;
  cp)
    case "$2" in
      *:/tmp/caretz-state/.)
        mkdir -p "$3/sessions/cli/task-1"
        printf '%s\n' '{"event_type":"LLMAgentResponse.Created","payload":{"usage":{"PromptTokens":7,"CompletionTokens":3,"TotalTokens":10}}}' > "$3/sessions/cli/task-1/context.jsonpl"
        ;;
      *:/logs/verifier/.)
        mkdir -p "$3"
        printf '1\n' > "$3/reward.txt"
        ;;
    esac
    ;;
  exec)
    if [ "$6" = "git" ] || [ "$5" = "git" ]; then
      exit 0
    fi
    ;;
esac
exit 0
`)
	if err := os.Chmod(filepath.Join(fakeBin, "docker"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := Runner{now: func() time.Time {
		return time.Date(2026, 8, 20, 2, 45, 0, 0, time.UTC)
	}}
	runDir, results, err := runner.Run(nil, RunOptions{
		Root:          root,
		ModelName:     "mock",
		SuiteName:     "terminal",
		CaretzBin:     relativePathFromCwd(t, caretz),
		OutputRoot:    filepath.Join(root, "runs"),
		LLMTimeout:    12 * time.Minute,
		LLMTimeoutSet: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed || results[0].Status != "PASS" {
		t.Fatalf("results = %#v, want one PASS", results)
	}
	if results[0].Usage.TotalTokens != 10 {
		t.Fatalf("usage = %#v, want copied container usage", results[0].Usage)
	}
	promptData, err := os.ReadFile(filepath.Join(runDir, "prompts", "task-1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(promptData) != "fix the app\n" {
		t.Fatalf("prompt = %q, want instruction.md", promptData)
	}
	argsData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsData)
	expectedContainer := "caretz-bench-2026-08-20-024500-mock-model-terminal-task-1"
	for _, want := range []string{
		"run -d --name " + expectedContainer + " --cap-add SYS_ADMIN --cap-add NET_ADMIN --security-opt seccomp=unconfined example/task:latest tail -f /dev/null",
		"exec " + expectedContainer + " sh -lc test ! -e /tests && test ! -e /solution && test ! -e /logs/verifier/reward.txt",
		"exec " + expectedContainer + " sh -lc command -v bwrap >/dev/null 2>&1 || (apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y bubblewrap)",
		"exec -w /app " + expectedContainer + " /tmp/caretz-bench/caretz --workspace /app",
		"--llm-timeout 12m0s",
		"--unrestricted-network",
		"cp " + taskDir + "/tests " + expectedContainer + ":/tests",
		"exec -w /app " + expectedContainer + " bash /tests/test.sh",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("docker args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "CARETZ_DISABLE_SANDBOX=1") {
		t.Fatalf("terminal-bench CARETZ run should keep sandbox enabled:\n%s", args)
	}
	if strings.Contains(args, "CARETZ_DISABLE_SECCOMP=1") {
		t.Fatalf("terminal-bench CARETZ run should not pass seccomp env flag:\n%s", args)
	}
	if strings.Count(args, "--unrestricted-network") != 1 {
		t.Fatalf("terminal-bench CARETZ run should pass --unrestricted-network once:\n%s", args)
	}
	if strings.Contains(args, "--privileged") {
		t.Fatalf("terminal-bench container should not run privileged:\n%s", args)
	}
	caretzIndex := strings.Index(args, "exec -w /app "+expectedContainer+" /tmp/caretz-bench/caretz")
	testsIndex := strings.Index(args, "cp "+taskDir+"/tests "+expectedContainer+":/tests")
	if caretzIndex < 0 || testsIndex < 0 || testsIndex < caretzIndex {
		t.Fatalf("tests copied before CARETZ ran:\n%s", args)
	}
	if strings.Count(args, "rm -f "+expectedContainer) < 2 {
		t.Fatalf("container cleanup was not called before and after run:\n%s", args)
	}
}

func TestRunnerEvaluatesTerminalBenchAfterCaretzErrorByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	taskDir := filepath.Join(root, "terminal-tasks", "task-1")
	writeFile(t, filepath.Join(taskDir, "instruction.md"), "fix the app\n")
	writeFile(t, filepath.Join(taskDir, "task.toml"), `
[environment]
docker_image = "example/task:latest"
`)
	writeFile(t, filepath.Join(taskDir, "tests", "test.sh"), `#!/bin/sh
echo 1 > /logs/verifier/reward.txt
`)
	writeFile(t, filepath.Join(root, "suites", "terminal.yaml"), `
name: terminal
benchmark: terminal-bench
timeout_seconds: 30
tasks:
  - id: task-1
    task_dir: `+taskDir+`
`)
	caretz := filepath.Join(root, "caretz-linux")
	writeFile(t, caretz, "#!/bin/sh\n")
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "fake-bin")
	logPath := filepath.Join(root, "docker-args.log")
	writeFile(t, filepath.Join(fakeBin, "docker"), `#!/bin/sh
printf '%s\n' "$*" >> "`+logPath+`"
case "$1" in
  run)
    echo fake-container-id
    ;;
  cp)
    case "$2" in
      *:/tmp/caretz-state/.)
        mkdir -p "$3/sessions/cli/task-1"
        printf '%s\n' '{"event_type":"LLMAgentResponse.Created","payload":{"usage":{"PromptTokens":7,"CompletionTokens":3,"TotalTokens":10}}}' > "$3/sessions/cli/task-1/context.jsonpl"
        ;;
      *:/logs/verifier/.)
        mkdir -p "$3"
        printf '1\n' > "$3/reward.txt"
        ;;
    esac
    ;;
  exec)
    case "$*" in
      *"/tmp/caretz-bench/caretz --workspace"*)
        echo "error: failed to run llm loop: LLM returned empty response 3 consecutive times" >&2
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`)
	if err := os.Chmod(filepath.Join(fakeBin, "docker"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, results, err := NewRunner().Run(nil, RunOptions{
		Root:       root,
		ModelName:  "mock",
		SuiteName:  "terminal",
		CaretzBin:  relativePathFromCwd(t, caretz),
		OutputRoot: filepath.Join(root, "runs"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed || results[0].Status != "PASS" {
		t.Fatalf("results = %#v, want one PASS", results)
	}
	if results[0].ExitReason != "caretz_error" {
		t.Fatalf("ExitReason = %q, want caretz_error", results[0].ExitReason)
	}
	if !strings.Contains(results[0].Error, "caretz exited before evaluation") {
		t.Fatalf("Error = %q, want preserved CARETZ failure detail", results[0].Error)
	}
	argsData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argsData), "bash /tests/test.sh") {
		t.Fatalf("verifier was not run after CARETZ error:\n%s", argsData)
	}
}

func TestTerminalBenchEnvironmentParsesDockerImageAndAllowInternet(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "task.toml")
	writeFile(t, path, `
[environment]
docker_image = "example/task:latest"
allow_internet = false
`)

	env, err := terminalBenchEnvironment(path)
	if err != nil {
		t.Fatalf("terminalBenchEnvironment() error = %v", err)
	}
	if env.DockerImage != "example/task:latest" {
		t.Fatalf("DockerImage = %q, want example/task:latest", env.DockerImage)
	}
	if env.AllowInternet {
		t.Fatalf("AllowInternet = true, want false")
	}
}

func TestRunnerPassesDisableSandboxEnvForTerminalBenchTask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker uses POSIX sh")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "mock.yaml"), `
name: mock-model
endpoint: http://localhost:9999/v1
model: mock
context_size: 32768
`)
	taskDir := filepath.Join(root, "terminal-tasks", "task-1")
	writeFile(t, filepath.Join(taskDir, "instruction.md"), "fix the app\n")
	writeFile(t, filepath.Join(taskDir, "task.toml"), `
[environment]
docker_image = "example/task:latest"
`)
	writeFile(t, filepath.Join(taskDir, "tests", "test.sh"), `#!/bin/sh
echo 1 > /logs/verifier/reward.txt
`)
	writeFile(t, filepath.Join(root, "suites", "terminal.yaml"), `
name: terminal
benchmark: terminal-bench
timeout_seconds: 30
tasks:
  - id: task-1
    task_dir: `+taskDir+`
    sandbox_disable: true
`)
	caretz := filepath.Join(root, "caretz-linux")
	writeFile(t, caretz, "#!/bin/sh\n")
	if err := os.Chmod(caretz, 0755); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "fake-bin")
	logPath := filepath.Join(root, "docker-args.log")
	writeFile(t, filepath.Join(fakeBin, "docker"), `#!/bin/sh
printf '%s\n' "$*" >> "`+logPath+`"
case "$1" in
  cp)
    case "$2" in
      *:/tmp/caretz-state/.)
        mkdir -p "$3/sessions/cli/task-1"
        ;;
      *:/logs/verifier/.)
        mkdir -p "$3"
        printf '1\n' > "$3/reward.txt"
        ;;
    esac
    ;;
esac
exit 0
`)
	if err := os.Chmod(filepath.Join(fakeBin, "docker"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, results, err := NewRunner().Run(nil, RunOptions{
		Root:       root,
		ModelName:  "mock",
		SuiteName:  "terminal",
		CaretzBin:  relativePathFromCwd(t, caretz),
		OutputRoot: filepath.Join(root, "runs"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed {
		t.Fatalf("results = %#v, want one PASS", results)
	}
	argsData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsData)
	wantExec := "exec -w /app -e CARETZ_DISABLE_SANDBOX=1 "
	if strings.Count(args, wantExec) != 1 {
		t.Fatalf("terminal-bench CARETZ run should pass CARETZ_DISABLE_SANDBOX before the container exactly once:\n%s", args)
	}
	if strings.Contains(args, "--unrestricted-network") {
		t.Fatalf("sandbox_disable should not imply unrestricted network:\n%s", args)
	}
}

func TestDockerRunWithOutputStreamsSingleConsoleWriter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker uses POSIX sh")
	}
	root := t.TempDir()
	fakeBin := filepath.Join(root, "fake-bin")
	writeFile(t, filepath.Join(fakeBin, "docker"), `#!/bin/sh
printf 'out-line\n'
printf 'err-line\n' >&2
`)
	if err := os.Chmod(filepath.Join(fakeBin, "docker"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logBuf, outBuf, combined bytes.Buffer
	err := dockerRunWithOutput(nil, &logBuf, &combined, &outBuf, nil, true, "exec", "container", "cmd")
	if err != nil {
		t.Fatalf("dockerRunWithOutput() error = %v", err)
	}
	if strings.Contains(outBuf.String(), "err-line") {
		t.Fatalf("stderr was streamed to console writer:\n%s", outBuf.String())
	}
	if !strings.Contains(logBuf.String(), "out-line") || !strings.Contains(logBuf.String(), "err-line") {
		t.Fatalf("log missing stdout/stderr:\n%s", logBuf.String())
	}
	if !strings.Contains(combined.String(), "out-line") || !strings.Contains(combined.String(), "err-line") {
		t.Fatalf("combined output missing stdout/stderr:\n%s", combined.String())
	}
}

func TestTerminalBenchContainerNameIncludesRunAndRepeat(t *testing.T) {
	runDir := filepath.Join("/tmp", "2026-08-20-024500-mock-model-terminal")
	first := terminalBenchContainerName(runDir, "task-1")
	second := terminalBenchContainerName(runDir, "task-1-r2")
	if first == second {
		t.Fatalf("container names should differ for repeats: %q", first)
	}
	if !strings.Contains(first, "2026-08-20-024500-mock-model-terminal-task-1") {
		t.Fatalf("first container name = %q", first)
	}
	if !strings.Contains(second, "task-1-r2") {
		t.Fatalf("second container name = %q", second)
	}
}

func TestClassifyCaretzErrorEmptyResponseIsModelError(t *testing.T) {
	got := classifyCaretzError("LLM returned empty response 3 consecutive times")
	if got != "MODEL_ERROR" {
		t.Fatalf("classifyCaretzError() = %q, want MODEL_ERROR", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func relativePathFromCwd(t *testing.T, path string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

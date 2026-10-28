package bench

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (r Runner) runTerminalBenchTask(ctx context.Context, runDir, caretzBin string, model ModelConfig, suite SuiteConfig, task TaskSpec, repeat int, timeout time.Duration, commit string, opts RunOptions) (result TaskResult) {
	start := r.now()
	result = TaskResult{
		TaskID:       task.ID,
		Tier:         task.Tier,
		Benchmark:    task.Benchmark,
		Suite:        suite.Name,
		Model:        model.Name,
		CaretzCommit: commit,
		RunnerCommit: commit,
		Status:       "BENCHMARK_ERROR",
		ExitReason:   "not_started",
	}
	defer func() {
		result.RuntimeSeconds = int64(r.now().Sub(start).Seconds())
	}()

	if strings.TrimSpace(task.TaskDir) == "" {
		result.Error = "terminal-bench task_dir is required"
		return result
	}
	taskDir, err := filepath.Abs(task.TaskDir)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	prompt, err := terminalBenchPrompt(task, taskDir)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	taskName := taskResultName(task.ID, repeat)
	promptPath := filepath.Join(runDir, "prompts", taskName+".md")
	configRoot := filepath.Join(runDir, "caretz-state", taskName)
	sessionID := taskName
	logPath := filepath.Join(runDir, "logs", taskName+".log")
	evalLogPath := filepath.Join(runDir, "logs", taskName+"-verifier.log")
	patchPath := filepath.Join(runDir, "patches", taskName+".diff")
	evalDir := filepath.Join(runDir, "evaluations", taskName)

	logFile, err := openLogFile(logPath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer logFile.Close()
	taskLog := runLogWriter(logFile, opts.Stdout, opts.Verbose)
	_, _ = fmt.Fprintf(taskLog, "[bench] starting terminal-bench task %s\n", task.ID)

	if err := os.MkdirAll(filepath.Dir(promptPath), 0755); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := os.WriteFile(promptPath, []byte(prompt), 0644); err != nil {
		result.Error = err.Error()
		return result
	}

	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	env, err := terminalBenchEnvironment(filepath.Join(taskDir, "task.toml"))
	if err != nil {
		result.Error = err.Error()
		return result
	}

	image, err := terminalBenchImage(taskCtx, taskDir, taskName, taskLog, env)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	container := terminalBenchContainerName(runDir, taskName)
	_ = dockerRun(ctx, taskLog, "rm", "-f", container)
	if err := dockerRun(taskCtx, taskLog, "run", "-d", "--name", container, "--cap-add", "SYS_ADMIN", "--cap-add", "NET_ADMIN", "--security-opt", "seccomp=unconfined", image, "tail", "-f", "/dev/null"); err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() {
		_ = dockerRun(context.Background(), taskLog, "rm", "-f", container)
	}()

	if err := dockerRun(taskCtx, taskLog, "exec", container, "mkdir", "-p", "/tmp/caretz-bench", "/tmp/caretz-state", "/logs/verifier"); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := dockerRun(taskCtx, taskLog, "exec", container, "sh", "-lc", "test ! -e /tests && test ! -e /solution && test ! -e /logs/verifier/reward.txt"); err != nil {
		result.Error = "terminal-bench image exposes verifier or solution files before agent run"
		return result
	}
	if err := ensureTerminalBenchSandbox(taskCtx, taskLog, container); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := dockerRun(taskCtx, taskLog, "cp", caretzBin, container+":/tmp/caretz-bench/caretz"); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := dockerRun(taskCtx, taskLog, "cp", promptPath, container+":/tmp/caretz-bench/prompt.md"); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := dockerRun(taskCtx, taskLog, "exec", container, "chmod", "+x", "/tmp/caretz-bench/caretz"); err != nil {
		result.Error = err.Error()
		return result
	}

	caretzArgs := []string{
		"exec", "-w", "/app",
	}
	if task.SandboxDisable {
		caretzArgs = append(caretzArgs, "-e", "CARETZ_DISABLE_SANDBOX=1")
	}
	caretzArgs = append(caretzArgs,
		container,
		"/tmp/caretz-bench/caretz",
		"--workspace", "/app",
		"--config-root", "/tmp/caretz-state",
		"-s", sessionID,
		"--prompt-file", "/tmp/caretz-bench/prompt.md",
	)
	caretzArgs = appendCaretzModelArgs(caretzArgs, model)
	caretzArgs = appendLLMTimeoutArg(caretzArgs, model, opts)
	if opts.Verbose {
		caretzArgs = append(caretzArgs, "-v")
	}
	if env.AllowInternet {
		caretzArgs = append(caretzArgs, "--unrestricted-network")
	}
	var output bytes.Buffer
	_, _ = fmt.Fprintf(taskLog, "[bench] running CARETZ in docker container %s\n", container)
	err = dockerRunWithOutput(taskCtx, taskLog, &output, opts.Stdout, nil, opts.Verbose, caretzArgs...)
	_, _ = fmt.Fprintf(taskLog, "[bench] CARETZ exited: %v\n", err)

	_ = os.MkdirAll(configRoot, 0755)
	_ = dockerRun(ctx, taskLog, "cp", container+":/tmp/caretz-state/.", configRoot)
	_ = writeDockerGitDiff(ctx, container, patchPath)

	sessionDir := filepath.Join(configRoot, "sessions", "cli", sessionID)
	if usage, usageErr := ExtractUsage(sessionDir); usageErr == nil {
		result.Usage = usage
	}

	if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
		result.Status = "TIMEOUT"
		result.ExitReason = "timeout"
		return result
	}
	if errors.Is(taskCtx.Err(), context.Canceled) {
		result.Status = "CANCELED"
		result.ExitReason = "canceled"
		return result
	}
	if err != nil {
		result.Status = classifyCaretzError(output.String())
		result.ExitReason = "caretz_error"
		result.Error = err.Error()
		// Terminal-bench scores the final container state. CARETZ can solve the
		// task and still exit non-zero later, for example after a near-context
		// LLM empty-response loop, so evaluate by default instead of discarding
		// the completed state.
		result.Error = "caretz exited before evaluation: " + err.Error()
	} else {
		result.ExitReason = "completed"
	}

	passed, evalErr := runTerminalBenchVerifier(taskCtx, taskDir, container, evalDir, evalLogPath, opts)
	if evalErr != nil {
		result.Status = "BENCHMARK_ERROR"
		result.Error = evalErr.Error()
		return result
	}
	result.Passed = passed
	if passed {
		result.Status = "PASS"
	} else {
		result.Status = "FAIL"
	}
	return result
}

func terminalBenchPrompt(task TaskSpec, taskDir string) (string, error) {
	if task.Prompt != "" || task.PromptFile != "" {
		return taskPrompt(task)
	}
	path := filepath.Join(taskDir, "instruction.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read terminal-bench instruction %s: %w", path, err)
	}
	return string(data), nil
}

func terminalBenchImage(ctx context.Context, taskDir, taskName string, log io.Writer, env terminalBenchEnvironmentConfig) (string, error) {
	if env.DockerImage != "" {
		_, _ = fmt.Fprintf(log, "[bench] using terminal-bench docker image %s\n", env.DockerImage)
		return env.DockerImage, nil
	}
	dockerfile := filepath.Join(taskDir, "environment", "Dockerfile")
	if _, err := os.Stat(dockerfile); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("terminal-bench task %s has no task.toml docker_image or environment/Dockerfile", taskDir)
		}
		return "", err
	}
	image := "caretz-bench-terminal-" + safeName(taskName)
	_, _ = fmt.Fprintf(log, "[bench] building terminal-bench docker image %s\n", image)
	if err := dockerRun(ctx, log, "build", "-t", image, "-f", dockerfile, filepath.Join(taskDir, "environment")); err != nil {
		return "", err
	}
	return image, nil
}

type terminalBenchEnvironmentConfig struct {
	DockerImage   string
	AllowInternet bool
}

func terminalBenchEnvironment(path string) (terminalBenchEnvironmentConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return terminalBenchEnvironmentConfig{}, nil
		}
		return terminalBenchEnvironmentConfig{}, err
	}
	defer file.Close()

	var config terminalBenchEnvironmentConfig
	inEnvironment := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inEnvironment = line == "[environment]"
			continue
		}
		if !inEnvironment {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "docker_image":
			config.DockerImage = strings.Trim(value, `"`)
		case "allow_internet":
			config.AllowInternet = strings.EqualFold(value, "true")
		}
	}
	if err := scanner.Err(); err != nil {
		return terminalBenchEnvironmentConfig{}, err
	}
	return config, nil
}

func runTerminalBenchVerifier(ctx context.Context, taskDir, container, evalDir, logPath string, opts RunOptions) (bool, error) {
	testsDir := filepath.Join(taskDir, "tests")
	testScript := filepath.Join(testsDir, "test.sh")
	if _, err := os.Stat(testScript); err != nil {
		return false, fmt.Errorf("terminal-bench verifier test.sh is required: %w", err)
	}
	if err := os.MkdirAll(evalDir, 0755); err != nil {
		return false, err
	}
	logFile, err := openLogFile(logPath)
	if err != nil {
		return false, err
	}
	defer logFile.Close()

	if err := dockerRun(ctx, logFile, "exec", container, "rm", "-rf", "/tests"); err != nil {
		return false, err
	}
	if err := dockerRun(ctx, logFile, "cp", testsDir, container+":/tests"); err != nil {
		return false, err
	}
	var output bytes.Buffer
	err = dockerRunWithOutput(ctx, logFile, &output, opts.Stdout, nil, opts.Verbose, "exec", "-w", "/app", container, "bash", "/tests/test.sh")
	_ = dockerRun(context.Background(), logFile, "cp", container+":/logs/verifier/.", evalDir)

	rewardPath := filepath.Join(evalDir, "reward.txt")
	if reward, readErr := os.ReadFile(rewardPath); readErr == nil {
		return strings.TrimSpace(string(reward)) == "1", nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func ensureTerminalBenchSandbox(ctx context.Context, log io.Writer, container string) error {
	check := "command -v bwrap >/dev/null 2>&1 || (apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y bubblewrap)"
	if err := dockerRun(ctx, log, "exec", container, "sh", "-lc", check); err != nil {
		return fmt.Errorf("install bubblewrap in terminal-bench container: %w", err)
	}
	if err := dockerRun(ctx, log, "exec", container, "sh", "-lc", "command -v bwrap >/dev/null 2>&1"); err != nil {
		return fmt.Errorf("terminal-bench container does not provide bwrap after install")
	}
	return nil
}

func terminalBenchContainerName(runDir, taskName string) string {
	name := "caretz-bench-" + safeName(filepath.Base(runDir)+"-"+taskName)
	if len(name) <= 120 {
		return name
	}
	return name[:120]
}

func writeDockerGitDiff(ctx context.Context, container, patchPath string) error {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", "exec", "-w", "/app", container, "git", "diff", "--binary", "HEAD")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		cmd = exec.CommandContext(ctx, "docker", "exec", "-w", "/app", container, "git", "diff", "--binary")
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(patchPath, out.Bytes(), 0644)
}

func dockerRun(ctx context.Context, log io.Writer, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = log
	cmd.Stderr = log
	return cmd.Run()
}

func dockerRunWithOutput(ctx context.Context, logFile io.Writer, output *bytes.Buffer, stdout, stderr io.Writer, verbose bool, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = commandOutputWriters(output, logFile, stdout, stderr, verbose)
	return cmd.Run()
}

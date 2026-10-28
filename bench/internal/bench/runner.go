package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type RunOptions struct {
	Root                  string
	ModelName             string
	SuiteName             string
	TaskID                string
	Tier                  string
	CaretzBin             string
	OutputRoot            string
	Repeat                int
	Timeout               time.Duration
	LLMTimeout            time.Duration
	LLMTimeoutSet         bool
	Verbose               bool
	EvaluateOnCaretzError bool
	Stdout                io.Writer
	Stderr                io.Writer
}

type Runner struct {
	now func() time.Time
}

func NewRunner() Runner {
	return Runner{now: time.Now}
}

func (r Runner) Run(ctx context.Context, opts RunOptions) (string, []TaskResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Root == "" {
		opts.Root = "bench"
	}
	if opts.OutputRoot == "" {
		opts.OutputRoot = filepath.Join(opts.Root, "runs")
	}
	outputRoot, err := filepath.Abs(opts.OutputRoot)
	if err != nil {
		return "", nil, err
	}
	if opts.Repeat <= 0 {
		opts.Repeat = 1
	}
	model, err := LoadModel(opts.Root, opts.ModelName)
	if err != nil {
		return "", nil, err
	}
	suite, err := LoadSuite(opts.Root, opts.SuiteName)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(opts.Tier) != "" {
		filtered, err := filterSuiteTier(suite.Tasks, opts.Tier)
		if err != nil {
			return "", nil, err
		}
		suite.Tasks = filtered
	}
	if strings.TrimSpace(opts.TaskID) != "" {
		filtered, err := filterSuiteTasks(suite.Tasks, opts.TaskID)
		if err != nil {
			return "", nil, err
		}
		suite.Tasks = filtered
	}
	caretzBin := opts.CaretzBin
	if strings.TrimSpace(caretzBin) == "" {
		caretzBin = defaultCaretzBin()
	}
	caretzBin, err = filepath.Abs(caretzBin)
	if err != nil {
		return "", nil, err
	}
	started := r.now()
	runDir := filepath.Join(outputRoot, fmt.Sprintf("%s-%s-%s", started.Format("2006-01-02-150405"), safeName(model.Name), safeName(suite.Name)))
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return "", nil, err
	}
	commit := gitCommit()
	manifest := RunManifest{
		Model:        model.Name,
		Suite:        suite.Name,
		CaretzCommit: commit,
		RunnerCommit: commit,
		StartedAt:    started,
		Settings: map[string]any{
			"context_size":    model.ContextSize,
			"temperature":     model.Temperature,
			"timeout_seconds": int(timeoutDuration(suite, opts.Timeout).Seconds()),
		},
	}
	if llmTimeout, ok := effectiveLLMTimeout(model, opts); ok {
		manifest.Settings["llm_timeout"] = llmTimeout.String()
	}
	if err := WriteJSON(filepath.Join(runDir, "run.json"), manifest); err != nil {
		return "", nil, err
	}

	var results []TaskResult
	for repeat := 1; repeat <= opts.Repeat; repeat++ {
		for _, task := range suite.Tasks {
			if err := ctx.Err(); err != nil {
				return runDir, results, err
			}
			result := r.runTask(ctx, runDir, caretzBin, model, suite, task, repeat, timeoutDuration(suite, opts.Timeout), commit, opts)
			results = append(results, result)
			if err := WriteJSON(filepath.Join(runDir, "results", taskResultFile(task.ID, repeat, opts.Repeat)), result); err != nil {
				return runDir, results, err
			}
			if err := ctx.Err(); err != nil {
				return runDir, results, err
			}
		}
	}
	return runDir, results, nil
}

func filterSuiteTasks(tasks []TaskSpec, taskID string) ([]TaskSpec, error) {
	taskID = strings.TrimSpace(taskID)
	for _, task := range tasks {
		if task.ID == taskID {
			return []TaskSpec{task}, nil
		}
	}
	var ids []string
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return nil, fmt.Errorf("task %q not found in suite; available tasks: %s", taskID, strings.Join(ids, ", "))
}

func filterSuiteTier(tasks []TaskSpec, tier string) ([]TaskSpec, error) {
	tier = strings.TrimSpace(tier)
	var filtered []TaskSpec
	var tiers []string
	seen := map[string]bool{}
	for _, task := range tasks {
		if task.Tier == tier {
			filtered = append(filtered, task)
		}
		if task.Tier != "" && !seen[task.Tier] {
			seen[task.Tier] = true
			tiers = append(tiers, task.Tier)
		}
	}
	if len(filtered) > 0 {
		return filtered, nil
	}
	if len(tiers) == 0 {
		return nil, fmt.Errorf("tier %q not found in suite; suite tasks have no tiers", tier)
	}
	return nil, fmt.Errorf("tier %q not found in suite; available tiers: %s", tier, strings.Join(tiers, ", "))
}

func (r Runner) runTask(ctx context.Context, runDir, caretzBin string, model ModelConfig, suite SuiteConfig, task TaskSpec, repeat int, timeout time.Duration, commit string, opts RunOptions) (result TaskResult) {
	if task.Benchmark == "terminal-bench" {
		return r.runTerminalBenchTask(ctx, runDir, caretzBin, model, suite, task, repeat, timeout, commit, opts)
	}

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

	workspace := task.Workspace
	if workspace == "" {
		result.Error = "task workspace is required for the initial manual SWE smoke adapter"
		return result
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	prompt, err := taskPrompt(task)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	taskName := taskResultName(task.ID, repeat)
	promptPath := filepath.Join(runDir, "prompts", taskName+".md")
	configRoot := filepath.Join(runDir, "caretz-state", taskName)
	sessionID := taskName
	logPath := filepath.Join(runDir, "logs", taskName+".log")
	patchPath := filepath.Join(runDir, "patches", taskName+".diff")
	runWorkspace := filepath.Join(runDir, "workspaces", taskName)
	logFile, err := openLogFile(logPath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer logFile.Close()
	taskLog := runLogWriter(logFile, opts.Stdout, opts.Verbose)
	_, _ = fmt.Fprintf(taskLog, "[bench] starting task %s\n", task.ID)
	if err := os.MkdirAll(filepath.Dir(promptPath), 0755); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := os.WriteFile(promptPath, []byte(prompt), 0644); err != nil {
		result.Error = err.Error()
		return result
	}
	workspaceAbs, err = prepareTaskWorkspace(workspaceAbs, runWorkspace, taskLog)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	caretzArgs := []string{
		"--workspace", workspaceAbs,
		"--config-root", configRoot,
		"-s", sessionID,
		"--prompt-file", promptPath,
	}
	caretzArgs = appendCaretzModelArgs(caretzArgs, model)
	caretzArgs = appendLLMTimeoutArg(caretzArgs, model, opts)
	if opts.Verbose {
		caretzArgs = append(caretzArgs, "-v")
	}
	cmd := exec.CommandContext(taskCtx, caretzBin, caretzArgs...)
	cmd.Dir = workspaceAbs
	var output bytes.Buffer
	_, _ = fmt.Fprintf(taskLog, "[bench] running CARETZ in %s\n", workspaceAbs)
	cmd.Stdout, cmd.Stderr = commandOutputWriters(&output, logFile, opts.Stdout, opts.Stderr, opts.Verbose)
	err = cmd.Run()
	_, _ = fmt.Fprintf(taskLog, "[bench] CARETZ exited: %v\n", err)

	sessionDir := filepath.Join(configRoot, "sessions", "cli", sessionID)
	if usage, usageErr := ExtractUsage(sessionDir); usageErr == nil {
		result.Usage = usage
	}
	_ = writeGitDiff(workspaceAbs, patchPath)

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
		if !opts.EvaluateOnCaretzError || !patchHasContent(patchPath) {
			return result
		}
		result.Error = "caretz exited before evaluation: " + err.Error()
	} else {
		result.ExitReason = "completed"
	}

	if strings.TrimSpace(task.EvaluatorCommand) == "" {
		if task.Benchmark == "swebench" {
			passed, evalErr := runSWEBenchEvaluator(ctx, runDir, taskName, model, suite, task, patchPath, int(timeout.Seconds()), opts)
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
		result.Status = "BENCHMARK_ERROR"
		result.Error = "task evaluator_command is required to decide PASS/FAIL"
		return result
	}
	passed, evalErr := runEvaluator(ctx, workspaceAbs, task.EvaluatorCommand, filepath.Join(runDir, "logs", taskName+"-evaluator.log"), opts)
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

func taskPrompt(task TaskSpec) (string, error) {
	if task.Prompt != "" && task.PromptFile != "" {
		return "", fmt.Errorf("task %s: prompt and prompt_file cannot both be set", task.ID)
	}
	if task.Prompt != "" {
		return task.Prompt, nil
	}
	if task.PromptFile == "" {
		return "", fmt.Errorf("task %s: prompt_file or prompt is required", task.ID)
	}
	data, err := os.ReadFile(task.PromptFile)
	if err != nil {
		return "", fmt.Errorf("read task prompt %s: %w", task.PromptFile, err)
	}
	return string(data), nil
}

func runEvaluator(ctx context.Context, workspace, command, logPath string, opts RunOptions) (bool, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = workspace
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return false, err
	}
	cmd.Env = append(os.Environ(),
		"BENCH_ROOT="+root,
		"BENCH_WORKSPACE="+workspace,
	)
	var output bytes.Buffer
	logFile, err := openLogFile(logPath)
	if err != nil {
		return false, err
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = commandOutputWriters(&output, logFile, opts.Stdout, opts.Stderr, opts.Verbose)
	err = cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode() == 0, nil
		}
		return false, err
	}
	return true, nil
}

func prepareTaskWorkspace(seedWorkspace, runWorkspace string, log io.Writer) (string, error) {
	if !isGitWorkspace(seedWorkspace) {
		_, _ = fmt.Fprintf(log, "[bench] using workspace %s\n", seedWorkspace)
		return seedWorkspace, nil
	}
	root, err := gitRoot(seedWorkspace)
	if err != nil {
		return "", err
	}
	seedRoot, err := canonicalPath(seedWorkspace)
	if err != nil {
		return "", err
	}
	if root != seedRoot {
		_, _ = fmt.Fprintf(log, "[bench] copying workspace %s to %s\n", seedWorkspace, runWorkspace)
		if err := copyDir(seedWorkspace, runWorkspace); err != nil {
			return "", fmt.Errorf("copy task workspace %s: %w", seedWorkspace, err)
		}
		_, _ = fmt.Fprintf(log, "[bench] prepared workspace %s\n", runWorkspace)
		return runWorkspace, nil
	}
	_, _ = fmt.Fprintf(log, "[bench] cloning workspace %s to %s\n", seedWorkspace, runWorkspace)
	if err := os.RemoveAll(runWorkspace); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(runWorkspace), 0755); err != nil {
		return "", err
	}
	cmd := exec.Command("git", "clone", "--progress", "--no-hardlinks", seedWorkspace, runWorkspace)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("clone task workspace %s: %w", seedWorkspace, err)
	}
	_, _ = fmt.Fprintf(log, "[bench] prepared workspace %s\n", runWorkspace)
	return runWorkspace, nil
}

func isGitWorkspace(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = path
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func gitRoot(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git root for %s: %w", path, err)
	}
	return canonicalPath(strings.TrimSpace(string(out)))
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return real, nil
}

func copyDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		info, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
		case mode.IsDir():
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
			if err := os.Chmod(dstPath, mode.Perm()); err != nil {
				return err
			}
		}
	}
	return nil
}

func runSWEBenchEvaluator(ctx context.Context, runDir, taskName string, model ModelConfig, suite SuiteConfig, task TaskSpec, patchPath string, timeoutSeconds int, opts RunOptions) (bool, error) {
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		return false, fmt.Errorf("read patch for SWE-bench evaluation: %w", err)
	}
	evalDir := filepath.Join(runDir, "evaluations", taskName)
	if err := os.MkdirAll(evalDir, 0755); err != nil {
		return false, err
	}
	predictionsPath := filepath.Join(evalDir, "predictions.jsonl")
	prediction := map[string]string{
		"instance_id":        task.ID,
		"model_name_or_path": model.Name,
		"model_patch":        string(patch),
	}
	line, err := json.Marshal(prediction)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(predictionsPath, append(line, '\n'), 0644); err != nil {
		return false, err
	}

	args := []string{
		"-m", "swebench.harness.run_evaluation",
		"--dataset_name", suite.DatasetName,
		"--split", suite.Split,
		"--instance_ids", task.ID,
		"--predictions_path", predictionsPath,
		"--max_workers", "1",
		"--run_id", taskName,
		"--timeout", fmt.Sprintf("%d", timeoutSeconds),
		"--report_dir", evalDir,
	}
	cmd := exec.CommandContext(ctx, sweBenchPython(), args...)
	cmd.Dir = evalDir
	var output bytes.Buffer
	logPath := filepath.Join(runDir, "logs", taskName+"-swebench.log")
	logFile, err := openLogFile(logPath)
	if err != nil {
		return false, err
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = commandOutputWriters(&output, logFile, opts.Stdout, opts.Stderr, opts.Verbose)
	err = cmd.Run()
	if err != nil {
		return false, fmt.Errorf("SWE-bench evaluator failed: %w", err)
	}
	passed, ok, err := readSWEBenchResolved(evalDir, task.ID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("SWE-bench evaluator did not write a report for %s under %s", task.ID, evalDir)
	}
	return passed, nil
}

func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return os.Create(path)
}

func commandOutputWriters(output *bytes.Buffer, logFile io.Writer, stdout, stderr io.Writer, verbose bool) (io.Writer, io.Writer) {
	var mu sync.Mutex
	captured := lockedWriter{mu: &mu, w: output}
	logged := lockedWriter{mu: &mu, w: logFile}
	return io.MultiWriter(captured, runLogWriter(logged, stdout, verbose)),
		io.MultiWriter(captured, runLogWriter(logged, stderr, verbose))
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func runLogWriter(logFile io.Writer, stream io.Writer, verbose bool) io.Writer {
	writers := []io.Writer{logFile}
	if !verbose || stream == nil {
		return io.MultiWriter(writers...)
	}
	writers = append(writers, stream)
	return io.MultiWriter(writers...)
}

func readSWEBenchResolved(root, taskID string) (bool, bool, error) {
	var resolved bool
	found := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != "report.json" || found {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var byTask map[string]struct {
			Resolved bool `json:"resolved"`
		}
		if err := json.Unmarshal(data, &byTask); err == nil {
			if report, ok := byTask[taskID]; ok {
				resolved = report.Resolved
				found = true
			}
		}
		return nil
	})
	if err != nil {
		return false, false, err
	}
	return resolved, found, nil
}

func sweBenchPython() string {
	if path := strings.TrimSpace(os.Getenv("SWEBENCH_PYTHON")); path != "" {
		if abs, err := filepath.Abs(path); err == nil {
			return abs
		}
		return path
	}
	path := filepath.Join(".bench", "swebench-venv", "bin", "python")
	if abs, err := filepath.Abs(path); err == nil {
		if _, statErr := os.Stat(abs); statErr == nil {
			return abs
		}
	}
	return "python"
}

func writeGitDiff(workspace, patchPath string) error {
	cmd := exec.Command("git", "diff", "--binary", "HEAD")
	cmd.Dir = workspace
	out, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("git", "diff", "--binary")
		cmd.Dir = workspace
		out, err = cmd.Output()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(patchPath, out, 0644)
}

func patchHasContent(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(data)) != ""
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func defaultCaretzBin() string {
	if _, err := os.Stat(filepath.Join(".", "bin", "caretz")); err == nil {
		return filepath.Join(".", "bin", "caretz")
	}
	return "caretz"
}

func gitCommit() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func classifyCaretzError(output string) string {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "api") || strings.Contains(lower, "model") || strings.Contains(lower, "empty response") {
		return "MODEL_ERROR"
	}
	return "CARETZ_ERROR"
}

func taskResultFile(taskID string, repeat, repeatTotal int) string {
	name := safeName(taskID)
	if repeatTotal > 1 {
		name = fmt.Sprintf("%s-r%d", name, repeat)
	}
	return name + ".json"
}

func taskResultName(taskID string, repeat int) string {
	if repeat > 1 {
		return safeName(fmt.Sprintf("%s-r%d", taskID, repeat))
	}
	return safeName(taskID)
}

func safeName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "unnamed"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

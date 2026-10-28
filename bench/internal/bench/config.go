package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultTimeoutSeconds = 1800

type ModelConfig struct {
	Name         string         `yaml:"name" json:"name"`
	Endpoint     string         `yaml:"endpoint" json:"endpoint"`
	Model        string         `yaml:"model" json:"model"`
	APIKey       string         `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	ContextSize  int            `yaml:"context_size" json:"context_size"`
	Temperature  *float64       `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	LLMTimeout   DurationOption `yaml:"llm_timeout,omitempty" json:"llm_timeout,omitempty"`
	Metadata     map[string]any `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	ResolvedPath string         `yaml:"-" json:"-"`
}

type DurationOption struct {
	Duration time.Duration
	Set      bool
}

type SuiteConfig struct {
	Name           string     `yaml:"name" json:"name"`
	Benchmark      string     `yaml:"benchmark" json:"benchmark"`
	DatasetName    string     `yaml:"dataset_name,omitempty" json:"dataset_name,omitempty"`
	Split          string     `yaml:"split,omitempty" json:"split,omitempty"`
	TimeoutSeconds int        `yaml:"timeout_seconds" json:"timeout_seconds"`
	Tasks          []TaskSpec `yaml:"tasks" json:"tasks"`
	ResolvedPath   string     `yaml:"-" json:"-"`
}

type TaskSpec struct {
	ID               string `yaml:"id" json:"id"`
	Tier             string `yaml:"tier,omitempty" json:"tier,omitempty"`
	Benchmark        string `yaml:"benchmark,omitempty" json:"benchmark,omitempty"`
	TaskDir          string `yaml:"task_dir,omitempty" json:"task_dir,omitempty"`
	Workspace        string `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Prompt           string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	PromptFile       string `yaml:"prompt_file,omitempty" json:"prompt_file,omitempty"`
	EvaluatorCommand string `yaml:"evaluator_command,omitempty" json:"evaluator_command,omitempty"`
	SandboxDisable   bool   `yaml:"sandbox_disable,omitempty" json:"sandbox_disable,omitempty"`
}

func appendCaretzModelArgs(args []string, model ModelConfig) []string {
	args = append(args, "--base-url", model.Endpoint)
	args = append(args, "--model", model.Model)
	if model.APIKey != "" {
		args = append(args, "--api-key", model.APIKey)
	}
	if model.ContextSize > 0 {
		args = append(args, "--context-window", strconv.Itoa(model.ContextSize))
	}
	return args
}

func LoadModel(root, nameOrPath string) (ModelConfig, error) {
	path := resolveConfigPath(root, "models", nameOrPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return ModelConfig{}, fmt.Errorf("read model config %s: %w", path, err)
	}
	var cfg ModelConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ModelConfig{}, fmt.Errorf("parse model config %s: %w", path, err)
	}
	cfg.ResolvedPath = path
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return ModelConfig{}, fmt.Errorf("model config %s: endpoint is required", path)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return ModelConfig{}, fmt.Errorf("model config %s: model is required", path)
	}
	return cfg, nil
}

func (d *DurationOption) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		*d = DurationOption{}
		return nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	if duration < 0 {
		return fmt.Errorf("duration must be >= 0")
	}
	d.Duration = duration
	d.Set = true
	return nil
}

func LoadSuite(root, nameOrPath string) (SuiteConfig, error) {
	path := resolveConfigPath(root, "suites", nameOrPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return SuiteConfig{}, fmt.Errorf("read suite config %s: %w", path, err)
	}
	var cfg SuiteConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return SuiteConfig{}, fmt.Errorf("parse suite config %s: %w", path, err)
	}
	cfg.ResolvedPath = path
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = defaultTimeoutSeconds
	}
	if cfg.DatasetName == "" && cfg.Benchmark == "swebench" {
		cfg.DatasetName = "SWE-bench/SWE-bench_Lite"
	}
	if cfg.Split == "" {
		cfg.Split = "test"
	}
	if len(cfg.Tasks) == 0 {
		return SuiteConfig{}, fmt.Errorf("suite config %s: at least one task is required", path)
	}
	for i := range cfg.Tasks {
		if strings.TrimSpace(cfg.Tasks[i].ID) == "" {
			return SuiteConfig{}, fmt.Errorf("suite config %s: task %d id is required", path, i+1)
		}
		if cfg.Tasks[i].Benchmark == "" {
			cfg.Tasks[i].Benchmark = cfg.Benchmark
		}
		if cfg.Tasks[i].Benchmark == "" {
			return SuiteConfig{}, fmt.Errorf("suite config %s: task %s benchmark is required", path, cfg.Tasks[i].ID)
		}
	}
	return cfg, nil
}

func resolveConfigPath(root, kind, nameOrPath string) string {
	if filepath.IsAbs(nameOrPath) || strings.ContainsRune(nameOrPath, filepath.Separator) {
		return nameOrPath
	}
	// Model names such as qwen3.5-9b contain dots, so only a real YAML
	// extension counts as one.
	file := nameOrPath
	if ext := filepath.Ext(nameOrPath); ext != ".yaml" && ext != ".yml" {
		file += ".yaml"
	}
	return filepath.Join(root, kind, file)
}

func timeoutDuration(suite SuiteConfig, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return time.Duration(suite.TimeoutSeconds) * time.Second
}

func effectiveLLMTimeout(model ModelConfig, opts RunOptions) (time.Duration, bool) {
	if opts.LLMTimeoutSet {
		return opts.LLMTimeout, true
	}
	if model.LLMTimeout.Set {
		return model.LLMTimeout.Duration, true
	}
	return 0, false
}

func appendLLMTimeoutArg(args []string, model ModelConfig, opts RunOptions) []string {
	if timeout, ok := effectiveLLMTimeout(model, opts); ok {
		args = append(args, "--llm-timeout", timeout.String())
	}
	return args
}

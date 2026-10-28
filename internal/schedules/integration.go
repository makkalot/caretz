package schedules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

const IntegrationName = "scheduler"

const registerScheduleDescription = "Register a durable every:<duration>, daily:<HH:MM>[@IANA/timezone], or one-shot at:<RFC3339> schedule. Daily times use the local timezone unless one is specified. Set exactly one of command or prompt. " +
	"Prefer command whenever a shell command or script can do the job deterministically, such as running tests, curl or health checks, git status, or grepping logs: it runs in the sandbox without a subagent, and you then receive its exit code and output to react to. " +
	"If a check needs several steps, write a script in the workspace and schedule it as the command. " +
	"Use prompt only when each run needs model judgment, such as reading code, summarizing, or deciding what to change; it runs in an isolated child agent and costs far more tokens. " +
	"Each run is bounded by timeout (default 30m, at most 60m) and each model phase by model_timeout (default 15m); a command gets what model_timeout leaves of timeout. " +
	"When a run times out, the error names the limit that fired. UpdateSchedule can raise the limits or change the command or prompt without registering a new schedule."

const timeoutDescription = "Limit for a whole run, as a Go duration such as 45m; at most 60m. Defaults to 30m."

const modelTimeoutDescription = "Limit for each model phase of a run (the child agent, and the turn that reads the result), as a Go duration such as 20m; at most the run timeout. Defaults to 15m, or half the run timeout if that is less. For a command, the command gets the rest of the run timeout."

// Defaults are the model settings of runs whose job sets none. They are
// applied when a run starts, so jobs follow config changes.
type Defaults struct {
	Model     string
	Reasoning string
	Limits    *eventing.ContextLimits
}

type Integration struct {
	gateway  sessiontools.SessionEventGateway
	manager  *Manager
	defaults Defaults

	mu      sync.Mutex
	waiters map[string]chan eventing.AsyncRunResult
	results eventing.Subscription
	cancel  context.CancelFunc
	once    sync.Once
}

func NewIntegration(gateway sessiontools.SessionEventGateway) (sessiontools.Integration, error) {
	if gateway == nil {
		return nil, fmt.Errorf("scheduler session event gateway is required")
	}
	events, err := gateway.AuditEvents()
	if err != nil {
		return nil, err
	}
	manager, err := New(gateway.ChannelID(), events, func(event *types.Event) error {
		return gateway.PublishAsyncEvent(context.Background(), event)
	}, nil)
	if err != nil {
		return nil, err
	}
	return &Integration{gateway: gateway, manager: manager, waiters: map[string]chan eventing.AsyncRunResult{}}, nil
}

// NewIntegrationWithDefaults returns a factory for an integration whose runs
// use defaults when their job sets no model.
func NewIntegrationWithDefaults(defaults Defaults) sessiontools.Factory {
	return func(gateway sessiontools.SessionEventGateway) (sessiontools.Integration, error) {
		integration, err := NewIntegration(gateway)
		if err != nil {
			return nil, err
		}
		integration.(*Integration).defaults = defaults
		return integration, nil
	}
}

// Defaults returns the model settings of runs whose job sets none.
func (i *Integration) Defaults() Defaults { return i.defaults }

func (i *Integration) Name() string { return IntegrationName }

func (i *Integration) Tools() []tools.Tool {
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	return []tools.Tool{
		{Definition: llm.ToolDefinition{Name: "RegisterSchedule", Description: registerScheduleDescription, Parameters: object(map[string]any{
			"schedule": map[string]any{"type": "string"},
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command run in the sandbox on each occurrence, with no subagent; you receive its exit code and output. Prefer this whenever a command or a script in the workspace can do the job.",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "Task for an isolated child agent on each occurrence. Use only when each run needs model judgment.",
			},
			"timeout":       map[string]any{"type": "string", "description": timeoutDescription},
			"model_timeout": map[string]any{"type": "string", "description": modelTimeoutDescription},
		}, "schedule")}, Handler: func(ctx context.Context, call llm.ToolCall) (string, error) {
			var input struct {
				Schedule     string `json:"schedule"`
				Prompt       string `json:"prompt"`
				Command      string `json:"command"`
				Timeout      string `json:"timeout"`
				ModelTimeout string `json:"model_timeout"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil {
				return "", err
			}
			prompt, command := strings.TrimSpace(input.Prompt), strings.TrimSpace(input.Command)
			if (prompt == "") == (command == "") {
				return "", fmt.Errorf("set exactly one of command or prompt")
			}
			var opts RegisterOptions
			if command != "" {
				prompt, opts.Mode = command, ModeCommand
			}
			var err error
			if opts.Timeout, err = parseTimeout("timeout", input.Timeout); err != nil {
				return "", err
			}
			if opts.ModelTimeout, err = parseTimeout("model_timeout", input.ModelTimeout); err != nil {
				return "", err
			}
			job, err := i.Register(ctx, input.Schedule, prompt, opts)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("registered %s for %s (%s)", job.ID, job.NextRun.Format(time.RFC3339), describeTimeouts(job)), nil
		}},
		{Definition: llm.ToolDefinition{Name: "UpdateSchedule", Description: "Change an existing schedule from its next run on, keeping its id and cadence: its command or prompt, for example to change course after runs fail, and its timeouts, for example after a run timed out. Omitted fields keep their current value. A command schedule takes command and a prompt schedule takes prompt; to switch between them, remove the schedule and register a new one.", Parameters: object(map[string]any{
			"id":            map[string]any{"type": "string"},
			"command":       map[string]any{"type": "string", "description": "New shell command for a command schedule."},
			"prompt":        map[string]any{"type": "string", "description": "New task for a prompt schedule."},
			"timeout":       map[string]any{"type": "string", "description": timeoutDescription},
			"model_timeout": map[string]any{"type": "string", "description": modelTimeoutDescription},
		}, "id")}, Handler: func(_ context.Context, call llm.ToolCall) (string, error) {
			var input struct {
				ID           string  `json:"id"`
				Command      *string `json:"command"`
				Prompt       *string `json:"prompt"`
				Timeout      *string `json:"timeout"`
				ModelTimeout *string `json:"model_timeout"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil {
				return "", err
			}
			if input.Command == nil && input.Prompt == nil && input.Timeout == nil && input.ModelTimeout == nil {
				return "", fmt.Errorf("set at least one of command, prompt, timeout, or model_timeout")
			}
			update := Update{Command: input.Command, Prompt: input.Prompt}
			var err error
			if update.Timeout, err = parseOptionalTimeout("timeout", input.Timeout); err != nil {
				return "", err
			}
			if update.ModelTimeout, err = parseOptionalTimeout("model_timeout", input.ModelTimeout); err != nil {
				return "", err
			}
			job, err := i.Update(input.ID, update)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("updated %s (%s)", job.ID, describeTimeouts(job)), nil
		}},
		{Definition: llm.ToolDefinition{Name: "ListSchedules", Description: "List active schedules for this session, with the timeouts in effect.", Parameters: object(map[string]any{})}, Handler: func(context.Context, llm.ToolCall) (string, error) {
			// Show the limits in effect as durations, not the job's zero
			// defaults in nanoseconds.
			type listedJob struct {
				Job
				Timeout        string `json:"timeout"`
				ModelTimeout   string `json:"model_timeout"`
				CommandTimeout string `json:"command_timeout,omitempty"`
			}
			jobs := i.List()
			listed := make([]listedJob, len(jobs))
			for n, job := range jobs {
				run, model := job.Timeouts()
				listed[n] = listedJob{Job: job, Timeout: formatDuration(run), ModelTimeout: formatDuration(model)}
				if job.Mode == ModeCommand {
					listed[n].CommandTimeout = formatDuration(job.CommandTimeout())
				}
			}
			data, _ := json.Marshal(listed)
			return string(data), nil
		}},
		{Definition: llm.ToolDefinition{Name: "RemoveSchedule", Description: "Remove a schedule from this session.", Parameters: object(map[string]any{"id": map[string]any{"type": "string"}}, "id")}, Handler: func(_ context.Context, call llm.ToolCall) (string, error) {
			var input struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil {
				return "", err
			}
			if err := i.Remove(input.ID); err != nil {
				return "", err
			}
			return "removed " + input.ID, nil
		}},
	}
}

func (i *Integration) Start(parent context.Context) error {
	results, err := i.gateway.SubscribeAuditEvents(eventing.AsyncRunCompleted)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	i.results = results
	i.cancel = cancel
	go i.consumeResults(ctx)
	i.manager.Start(i.run)
	return nil
}

func (i *Integration) Close() error {
	i.once.Do(func() {
		i.manager.Stop()
		if i.cancel != nil {
			i.cancel()
		}
		if i.results != nil {
			i.results.Close()
		}
	})
	return nil
}

func (i *Integration) Register(ctx context.Context, spec, prompt string, options ...RegisterOptions) (Job, error) {
	if len(options) > 0 && strings.TrimSpace(options[0].Mode) == ModeCommand && !i.sandboxed() {
		return Job{}, ErrNeedsSandbox
	}
	return i.manager.Register(ctx, spec, prompt, options...)
}

// sandboxed reports whether the session runs commands in the sandbox. It
// fails closed: a session that exposes no tool registry is not sandboxed.
// The agent checks again before each command run.
func (i *Integration) sandboxed() bool {
	host, ok := i.gateway.(interface{ ToolRegistry() *tools.Registry })
	if !ok {
		return false
	}
	registry := host.ToolRegistry()
	return registry != nil && !registry.SandboxDisabled
}

func (i *Integration) List() []Job { return i.manager.List() }

// Update changes schedule id; see Manager.Update.
func (i *Integration) Update(id string, update Update) (Job, error) {
	return i.manager.Update(strings.TrimSpace(id), update)
}

// parseTimeout reads a tool's duration argument; empty means the default.
func parseTimeout(name, value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 45m, got %q", name, value)
	}
	return d, nil
}

// parseOptionalTimeout reads an argument UpdateSchedule may omit: nil keeps
// the current limit, and an empty string restores the default.
func parseOptionalTimeout(name string, value *string) (*time.Duration, error) {
	if value == nil {
		return nil, nil
	}
	d, err := parseTimeout(name, *value)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func describeTimeouts(job Job) string {
	run, model := job.Timeouts()
	text := fmt.Sprintf("timeout %s, model timeout %s", formatDuration(run), formatDuration(model))
	if job.Mode == ModeCommand {
		text += ", command timeout " + formatDuration(job.CommandTimeout())
	}
	return text
}

func (i *Integration) Remove(id string) error { return i.manager.Remove(strings.TrimSpace(id)) }

func (i *Integration) run(ctx context.Context, job Job) error {
	waiter := make(chan eventing.AsyncRunResult, 1)
	i.mu.Lock()
	i.waiters[job.RequestID] = waiter
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		delete(i.waiters, job.RequestID)
		i.mu.Unlock()
	}()

	run, model := job.Timeouts()
	request := eventing.AsyncRunRequest{
		RequestID:    job.RequestID,
		Source:       IntegrationName,
		SourceID:     job.ID,
		Prompt:       job.Prompt,
		Mode:         eventing.AsyncRunCommand,
		Timeout:      run,
		ModelTimeout: model,
		PreviousRun:  job.previousRun(),

		CommandTimeoutSeconds: int(job.CommandTimeout() / time.Second),
	}
	if job.Mode != ModeCommand {
		model, reasoning := job.Model, job.Reasoning
		if model == "" {
			model, reasoning = i.defaults.Model, i.defaults.Reasoning
		}
		request.Mode, request.Model, request.ReasoningEffort, request.ContextLimits = eventing.AsyncRunSubagent, model, reasoning, i.defaults.Limits
	}
	if err := i.gateway.PublishAsyncEvent(ctx, eventing.NewAsyncRunRequested(i.gateway.ChannelID(), request)); err != nil {
		return err
	}
	select {
	case result := <-waiter:
		if result.Status == string(Succeeded) {
			return nil
		}
		return asyncResultError{status: Status(result.Status), message: result.Error}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *Integration) consumeResults(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-i.results.Events():
			if !ok {
				return
			}
			result, err := eventing.DecodeAsyncRunResult(event)
			if err != nil || result.Source != IntegrationName {
				continue
			}
			i.mu.Lock()
			waiter := i.waiters[result.RequestID]
			i.mu.Unlock()
			if waiter != nil {
				select {
				case waiter <- result:
				default:
				}
			}
		}
	}
}

type asyncResultError struct {
	status  Status
	message string
}

func (e asyncResultError) Error() string {
	if strings.TrimSpace(e.message) != "" {
		return e.message
	}
	return string(e.status)
}

func (e asyncResultError) ScheduleStatus() Status { return e.status }

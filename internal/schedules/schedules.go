// Package schedules implements durable, per-session schedule state.
package schedules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/types"
)

const (
	Registered     = "Schedule.Registered"
	Removed        = "Schedule.Removed"
	RunStarted     = "Schedule.RunStarted"
	RunFinished    = "Schedule.RunFinished"
	RunSkipped     = "Schedule.RunSkipped"
	Updated        = "Schedule.Updated"
	MaxJobs        = 100
	MaxPrompt      = 32 * 1024
	MinInterval    = time.Minute
	MaxRunRetries  = 3
	RetryBaseDelay = time.Minute
	// DefaultRunTimeout bounds a whole run of a job that sets no timeout;
	// MaxRunTimeout is the most a job may set.
	DefaultRunTimeout = 30 * time.Minute
	MaxRunTimeout     = 60 * time.Minute
	// DefaultModelTimeout bounds each model phase of a run whose job sets no
	// model timeout, but never more than half the run timeout. A command
	// gets what the model timeout leaves of the run timeout.
	DefaultModelTimeout = 15 * time.Minute
	MinTimeout          = time.Minute
)

// ModeCommand marks a job whose Prompt is a shell command run in the sandbox
// instead of a prompt for a subagent.
const ModeCommand = "command"

var (
	ErrMalformed        = errors.New("malformed schedule: expected every:<duration>, daily:<HH:MM>[@IANA/timezone], or at:<RFC3339 timestamp>")
	ErrIntervalTooShort = errors.New("schedule interval must be at least 1m")
	ErrPast             = errors.New("one-shot schedule must be in the future")
	ErrEmptyPrompt      = errors.New("schedule prompt must not be empty")
	ErrPromptTooLarge   = errors.New("schedule prompt exceeds 32 KiB")
	ErrCapacity         = errors.New("session schedule capacity of 100 reached")
	ErrUnknownID        = errors.New("unknown schedule id")
	ErrNested           = errors.New("scheduled runs cannot register schedules")
	ErrNeedsSandbox     = errors.New("command schedules need the sandbox, which is disabled for this session")
)

type Status string

const (
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	TimedOut  Status = "timed_out"
	Panicked  Status = "panicked"
	Cancelled Status = "cancelled"
	Skipped   Status = "skipped"
	// Refused runs were not started because running them would be unsafe;
	// they are not retried.
	Refused Status = "refused"
)

type Job struct {
	ID        string `json:"id"`
	Schedule  string `json:"schedule"`
	Prompt    string `json:"prompt"`
	Mode      string `json:"mode,omitempty"`
	Model     string `json:"model,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	// Timeout and ModelTimeout are the limits the job set; zero takes the
	// default when a run starts. Timeouts returns the values in effect.
	Timeout      time.Duration `json:"timeout,omitempty"`
	ModelTimeout time.Duration `json:"model_timeout,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	NextRun      time.Time     `json:"next_run"`
	LastRun      *time.Time    `json:"last_run,omitempty"`
	LastStatus   Status        `json:"last_status,omitempty"`
	LastError    string        `json:"last_error,omitempty"`
	Running      bool          `json:"-"`
	RequestID    string        `json:"-"`
	RetryCount   int           `json:"-"`
	Occurrence   time.Time     `json:"-"`
}

type RegisterOptions struct {
	// Mode is ModeCommand for a command job; empty runs a subagent.
	Mode      string
	Model     string
	Reasoning string
	// Timeout and ModelTimeout are the job's limits; zero uses the defaults.
	Timeout      time.Duration
	ModelTimeout time.Duration
}

// Timeouts returns the run and model timeouts in effect for j.
func (j Job) Timeouts() (run, model time.Duration) {
	return resolveTimeouts(j.Timeout, j.ModelTimeout)
}

// CommandTimeout returns how long the command of a command job may run: what
// the model timeout leaves of the run timeout for the turn that reads its
// output.
func (j Job) CommandTimeout() time.Duration {
	run, model := j.Timeouts()
	return run - model
}

// previousRun describes how j's last run ended when it did not succeed, so
// the next run can change course. Skipped occurrences never ran.
func (j Job) previousRun() string {
	switch j.LastStatus {
	case "", Succeeded, Skipped:
		return ""
	}
	note := "Status: " + string(j.LastStatus)
	if strings.TrimSpace(j.LastError) != "" {
		note += "\nError: " + j.LastError
	}
	if j.RetryCount > 0 {
		note += fmt.Sprintf("\nThis is retry %d of %d.", j.RetryCount, MaxRunRetries)
	}
	if j.LastStatus == TimedOut {
		run, model := j.Timeouts()
		note += fmt.Sprintf("\nLimits: run timeout %s, model timeout %s", formatDuration(run), formatDuration(model))
		if j.Mode == ModeCommand {
			note += ", command timeout " + formatDuration(j.CommandTimeout())
		}
		note += "."
	}
	// A command's output is read by the session, which can change the
	// schedule; a prompt run's subagent has no schedule tools.
	if j.Mode == ModeCommand {
		note += fmt.Sprintf("\nUpdateSchedule %s can change the command, or raise the limits up to a run timeout of %s.", j.ID, formatDuration(MaxRunTimeout))
	}
	return note
}

// formatDuration drops the zero units time.Duration prints, so 30m0s reads
// 30m.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func resolveTimeouts(timeout, modelTimeout time.Duration) (run, model time.Duration) {
	run, model = timeout, modelTimeout
	if run == 0 {
		run = DefaultRunTimeout
	}
	if model == 0 {
		model = min(DefaultModelTimeout, run/2)
	}
	return run, model
}

// validateTimeouts reports whether a job of mode may run with these limits.
func validateTimeouts(mode string, timeout, modelTimeout time.Duration) error {
	if timeout < 0 || modelTimeout < 0 {
		return errors.New("schedule timeouts must be positive durations")
	}
	run, model := resolveTimeouts(timeout, modelTimeout)
	if run < MinTimeout || run > MaxRunTimeout {
		return fmt.Errorf("schedule timeout must be between %s and %s, got %s", formatDuration(MinTimeout), formatDuration(MaxRunTimeout), formatDuration(run))
	}
	if model < MinTimeout || model > run {
		return fmt.Errorf("schedule model timeout must be between %s and the run timeout of %s, got %s", formatDuration(MinTimeout), formatDuration(run), formatDuration(model))
	}
	if mode == ModeCommand && run-model < MinTimeout {
		return fmt.Errorf("a command schedule's model timeout must leave at least %s of the %s run timeout for the command, got %s", formatDuration(MinTimeout), formatDuration(run), formatDuration(model))
	}
	return nil
}

type lifecycle struct {
	ID         string    `json:"id"`
	RequestID  string    `json:"request_id,omitempty"`
	At         time.Time `json:"at"`
	Status     Status    `json:"status,omitempty"`
	Error      string    `json:"error,omitempty"`
	NextRun    time.Time `json:"next_run,omitempty"`
	RetryCount int       `json:"retry_count,omitempty"`
	Occurrence time.Time `json:"occurrence,omitempty"`
	// Updated events carry the job's new prompt and limits.
	Prompt       string        `json:"prompt,omitempty"`
	Timeout      time.Duration `json:"timeout,omitempty"`
	ModelTimeout time.Duration `json:"model_timeout,omitempty"`
}

// Update names the changes to a job. A nil field keeps the current value.
type Update struct {
	// Prompt replaces a subagent job's prompt and Command a command job's
	// command; setting the one that does not match the job is an error.
	Prompt  *string
	Command *string
	// Timeout and ModelTimeout replace the job's limits; zero restores the
	// default.
	Timeout      *time.Duration
	ModelTimeout *time.Duration
}
type AppendFunc func(*types.Event) error
type Clock func() time.Time

type Manager struct {
	mu         sync.Mutex
	originator string
	append     AppendFunc
	now        Clock
	jobs       map[string]*Job
	wake       chan struct{}
	cancel     context.CancelFunc
	done       chan struct{}
	run        func(context.Context, Job) error
	wg         sync.WaitGroup
	location   *time.Location
}

func New(originator string, events []*types.Event, appendFn AppendFunc, now Clock) (*Manager, error) {
	if now == nil {
		now = time.Now
	}
	m := &Manager{originator: originator, append: appendFn, now: now, jobs: map[string]*Job{}, wake: make(chan struct{}, 1), location: now().Location()}
	for _, e := range events {
		m.fold(e)
	} // corrupt/unknown schedule events are deliberately ignored
	for _, e := range events {
		m.foldAsyncCompletion(e)
	}
	if err := m.reconcile(); err != nil {
		return nil, err
	}
	return m, nil
}

func Parse(spec string, now time.Time) (next time.Time, interval time.Duration, err error) {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "every:") {
		d, e := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(spec, "every:")))
		if e != nil || d <= 0 {
			return time.Time{}, 0, ErrMalformed
		}
		if d < MinInterval {
			return time.Time{}, 0, ErrIntervalTooShort
		}
		return now.UTC().Add(d), d, nil
	}
	if strings.HasPrefix(spec, "at:") {
		t, e := time.Parse(time.RFC3339, strings.TrimSpace(strings.TrimPrefix(spec, "at:")))
		if e != nil {
			return time.Time{}, 0, ErrMalformed
		}
		if !t.After(now) {
			return time.Time{}, 0, ErrPast
		}
		return t.UTC(), 0, nil
	}
	if strings.HasPrefix(spec, "daily:") {
		next, e := nextDaily(spec, now, now.Location())
		if e != nil {
			return time.Time{}, 0, ErrMalformed
		}
		return next.UTC(), 24 * time.Hour, nil
	}
	return time.Time{}, 0, ErrMalformed
}

func (m *Manager) Register(ctx context.Context, spec, prompt string, options ...RegisterOptions) (Job, error) {
	request, async := sessiontools.AsyncRequestFromContext(ctx)
	if IsScheduled(ctx) || async && request.Source == IntegrationName {
		return Job{}, ErrNested
	}
	prompt, err := cleanPrompt(prompt)
	if err != nil {
		return Job{}, err
	}
	clockNow := m.now()
	now := clockNow.UTC()
	next, _, err := Parse(spec, clockNow)
	if err != nil {
		return Job{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) >= MaxJobs {
		return Job{}, ErrCapacity
	}
	var opts RegisterOptions
	if len(options) > 0 {
		opts = options[0]
	}
	mode := strings.TrimSpace(opts.Mode)
	if mode != "" && mode != ModeCommand {
		return Job{}, fmt.Errorf("unknown schedule mode %q", mode)
	}
	if err := validateTimeouts(mode, opts.Timeout, opts.ModelTimeout); err != nil {
		return Job{}, err
	}
	j := Job{ID: "sched_" + uuid.NewV4().String()[:8], Schedule: strings.TrimSpace(spec), Prompt: prompt, Mode: mode, Model: strings.TrimSpace(opts.Model), Reasoning: strings.TrimSpace(opts.Reasoning), Timeout: opts.Timeout, ModelTimeout: opts.ModelTimeout, CreatedAt: now, NextRun: next}
	if err := m.emitLocked(Registered, j); err != nil {
		return Job{}, err
	}
	m.jobs[j.ID] = &j
	m.signal()
	return j, nil
}

// Update changes job id from its next run on; a run in progress keeps what
// it started with. The job's ID, cadence, and mode stay the same.
func (m *Manager) Update(id string, update Update) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrUnknownID
	}
	next := *j
	body := update.Prompt
	if j.Mode == ModeCommand {
		if update.Prompt != nil {
			return Job{}, fmt.Errorf("%s is a command schedule: set command, not prompt", id)
		}
		body = update.Command
	} else if update.Command != nil {
		return Job{}, fmt.Errorf("%s is a prompt schedule: set prompt, not command", id)
	}
	if body != nil {
		prompt, err := cleanPrompt(*body)
		if err != nil {
			return Job{}, err
		}
		next.Prompt = prompt
	}
	if update.Timeout != nil {
		next.Timeout = *update.Timeout
	}
	if update.ModelTimeout != nil {
		next.ModelTimeout = *update.ModelTimeout
	}
	if err := validateTimeouts(next.Mode, next.Timeout, next.ModelTimeout); err != nil {
		return Job{}, err
	}
	if err := m.emitLocked(Updated, lifecycle{ID: id, At: m.now().UTC(), Prompt: next.Prompt, Timeout: next.Timeout, ModelTimeout: next.ModelTimeout}); err != nil {
		return Job{}, err
	}
	j.Prompt, j.Timeout, j.ModelTimeout = next.Prompt, next.Timeout, next.ModelTimeout
	return *j, nil
}

func cleanPrompt(prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", ErrEmptyPrompt
	}
	if len([]byte(prompt)) > MaxPrompt {
		return "", ErrPromptTooLarge
	}
	return prompt, nil
}

func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[id]; !ok {
		return ErrUnknownID
	}
	if err := m.emitLocked(Removed, lifecycle{ID: id, At: m.now().UTC()}); err != nil {
		return err
	}
	delete(m.jobs, id)
	m.signal()
	return nil
}

func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextRun.Before(out[j].NextRun) })
	return out
}

func (m *Manager) Start(run func(context.Context, Job) error) {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	m.run = run
	m.mu.Unlock()
	go m.loop(ctx)
}
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
		m.wg.Wait()
	}
}

func (m *Manager) loop(ctx context.Context) {
	defer close(m.done)
	for {
		m.mu.Lock()
		var due *Job
		var next time.Time
		now := m.now()
		for _, j := range m.jobs {
			if !j.NextRun.After(now) {
				_, interval, _ := parseExisting(j.Schedule, j.CreatedAt)
				if j.Running {
					if interval == 0 {
						continue
					}
					// NextRun still names the occurrence currently executing. Do not
					// mistake it for an overlapping occurrence until another cadence
					// has actually elapsed.
					nextOccurrence := m.advance(j.Schedule, j.NextRun, interval, j.NextRun)
					if nextOccurrence.After(now) {
						if next.IsZero() || nextOccurrence.Before(next) {
							next = nextOccurrence
						}
						continue
					}
				}
				c := *j
				due = &c
				break
			}
			if next.IsZero() || j.NextRun.Before(next) {
				next = j.NextRun
			}
		}
		m.mu.Unlock()
		if due != nil {
			m.dispatch(ctx, *due)
			continue
		}
		wait := time.Hour
		if !next.IsZero() {
			wait = next.Sub(m.now())
			if wait < 0 {
				wait = 0
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-m.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

func (m *Manager) dispatch(parent context.Context, job Job) {
	m.mu.Lock()
	current, ok := m.jobs[job.ID]
	if !ok {
		m.mu.Unlock()
		return
	}
	now := m.now().UTC()
	_, interval, _ := parseExisting(current.Schedule, current.CreatedAt)
	if current.Running {
		if interval > 0 {
			m.skipLocked(current, now, interval, "previous occurrence still running")
		}
		m.mu.Unlock()
		return
	}
	current.Running = true
	current.RequestID = uuid.NewV4().String()
	if current.Occurrence.IsZero() {
		current.Occurrence = current.NextRun
	}
	job.RequestID = current.RequestID
	job.RetryCount = current.RetryCount
	job.Occurrence = current.Occurrence
	_ = m.emitLocked(RunStarted, lifecycle{ID: job.ID, RequestID: job.RequestID, At: now, RetryCount: job.RetryCount, Occurrence: job.Occurrence})
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.execute(parent, job)
	}()
}

func (m *Manager) execute(parent context.Context, job Job) {
	// The generic async executor applies the run timeout after it acquires
	// execution capacity. This context only ties the wait to scheduler shutdown.
	ctx, cancel := context.WithCancel(WithScheduled(parent))
	status := Succeeded
	var runErr error
	func() {
		defer func() {
			if p := recover(); p != nil {
				status = Panicked
				runErr = fmt.Errorf("%v", p)
			}
		}()
		if m.run != nil {
			runErr = m.run(ctx, job)
		}
	}()
	if runErr != nil {
		status = Failed
		if statusErr, ok := runErr.(interface{ ScheduleStatus() Status }); ok {
			status = statusErr.ScheduleStatus()
		} else if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = TimedOut
		} else if errors.Is(runErr, context.Canceled) {
			status = Cancelled
		}
	}
	cancel()
	m.finish(job.ID, job.RequestID, status, runErr)
}

func (m *Manager) finish(id, requestID string, status Status, runErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return
	}
	now := m.now().UTC()
	j.Running = false
	j.RequestID = ""
	j.LastRun = &now
	j.LastStatus = status
	j.LastError = ""
	if runErr != nil {
		j.LastError = runErr.Error()
	}
	_, interval, _ := parseExisting(j.Schedule, j.CreatedAt)
	next := time.Time{}
	if retryable(status) && j.RetryCount < MaxRunRetries {
		j.RetryCount++
		next = now.Add(retryDelay(j.RetryCount))
		j.NextRun = next
		_ = m.emitLocked(RunFinished, lifecycle{ID: id, RequestID: requestID, At: now, Status: status, Error: j.LastError, NextRun: next, RetryCount: j.RetryCount, Occurrence: j.Occurrence})
		m.signal()
		return
	}
	anchor := j.Occurrence
	if anchor.IsZero() {
		anchor = j.NextRun
	}
	j.RetryCount = 0
	j.Occurrence = time.Time{}
	if interval > 0 {
		next = m.advance(j.Schedule, anchor, interval, now)
		j.NextRun = next
	}
	_ = m.emitLocked(RunFinished, lifecycle{ID: id, RequestID: requestID, At: now, Status: status, Error: j.LastError, NextRun: next})
	if interval == 0 {
		_ = m.emitLocked(Removed, lifecycle{ID: id, At: now})
		delete(m.jobs, id)
	}
	m.signal()
}

func (m *Manager) reconcile() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	for _, j := range m.jobs {
		_, d, _ := parseExisting(j.Schedule, j.CreatedAt)
		if j.Running {
			requestID := j.RequestID
			j.Running = false
			j.RequestID = ""
			j.LastRun = &now
			j.LastStatus = Cancelled
			j.LastError = "scheduled run interrupted by shutdown"
			// Keep the interrupted occurrence due. Reconciliation below either
			// retries it or coalesces it into a newer missed occurrence.
			if err := m.emitLocked(RunFinished, lifecycle{ID: j.ID, RequestID: requestID, At: now, Status: Cancelled, Error: j.LastError, NextRun: j.NextRun, RetryCount: j.RetryCount, Occurrence: j.Occurrence}); err != nil {
				return err
			}
		}
		if d > 0 && j.RetryCount == 0 && !j.NextRun.After(now) {
			latest := m.latestDue(j.Schedule, j.NextRun, d, now)
			if latest.After(j.NextRun) {
				if err := m.skipToLocked(j, now, latest, "older occurrences missed while caretz was closed"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (m *Manager) skipLocked(j *Job, now time.Time, d time.Duration, reason string) {
	_ = m.skipToLocked(j, now, m.advance(j.Schedule, j.NextRun, d, now), reason)
}
func (m *Manager) skipToLocked(j *Job, now, next time.Time, reason string) error {
	j.LastRun = &now
	j.LastStatus = Skipped
	j.LastError = reason
	j.NextRun = next
	j.RetryCount = 0
	j.Occurrence = time.Time{}
	return m.emitLocked(RunSkipped, lifecycle{ID: j.ID, At: now, Status: Skipped, Error: reason, NextRun: next})
}

func retryable(status Status) bool {
	return status == Failed || status == TimedOut || status == Panicked
}

func retryDelay(retry int) time.Duration {
	if retry <= 1 {
		return RetryBaseDelay
	}
	return RetryBaseDelay << (retry - 1)
}
func (m *Manager) latestDue(spec string, anchor time.Time, d time.Duration, now time.Time) time.Time {
	if strings.HasPrefix(strings.TrimSpace(spec), "daily:") {
		if next, err := nextDaily(spec, now, m.location); err == nil {
			return next.AddDate(0, 0, -1).UTC()
		}
	}
	return advance(anchor, d, now).Add(-d)
}
func advance(anchor time.Time, d time.Duration, now time.Time) time.Time {
	for !anchor.After(now) {
		anchor = anchor.Add(d)
	}
	return anchor
}

func (m *Manager) advance(spec string, anchor time.Time, d time.Duration, now time.Time) time.Time {
	if strings.HasPrefix(strings.TrimSpace(spec), "daily:") {
		if next, err := nextDaily(spec, now, m.location); err == nil {
			return next.UTC()
		}
	}
	return advance(anchor, d, now)
}

func nextDaily(spec string, now time.Time, fallback *time.Location) (time.Time, error) {
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(spec), "daily:"))
	clock, zone, hasZone := strings.Cut(value, "@")
	loc := fallback
	if loc == nil {
		loc = time.Local
	}
	if hasZone {
		if strings.TrimSpace(zone) == "" {
			return time.Time{}, ErrMalformed
		}
		loaded, err := time.LoadLocation(strings.TrimSpace(zone))
		if err != nil {
			return time.Time{}, err
		}
		loc = loaded
	}
	parsed, err := time.Parse("15:04", strings.TrimSpace(clock))
	if err != nil {
		return time.Time{}, err
	}
	localNow := now.In(loc)
	next := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
	if !next.After(localNow) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}
func parseExisting(spec string, created time.Time) (time.Time, time.Duration, error) {
	return Parse(spec, created.Add(-time.Nanosecond))
}
func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
func (m *Manager) emitLocked(kind string, p any) error {
	if m.append == nil {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return m.append(types.NewEvent(types.NewOriginator(m.originator, 0), kind, string(b)))
}
func (m *Manager) fold(e *types.Event) {
	if e == nil {
		return
	}
	switch e.EventType {
	case Registered:
		var j Job
		if json.Unmarshal([]byte(e.Payload), &j) == nil && j.ID != "" {
			c := j
			m.jobs[j.ID] = &c
		}
	case Removed:
		var p lifecycle
		if json.Unmarshal([]byte(e.Payload), &p) == nil {
			delete(m.jobs, p.ID)
		}
	case Updated:
		var p lifecycle
		if json.Unmarshal([]byte(e.Payload), &p) == nil && m.jobs[p.ID] != nil {
			if p.Prompt != "" {
				m.jobs[p.ID].Prompt = p.Prompt
			}
			m.jobs[p.ID].Timeout = p.Timeout
			m.jobs[p.ID].ModelTimeout = p.ModelTimeout
		}
	case RunStarted:
		var p lifecycle
		if json.Unmarshal([]byte(e.Payload), &p) == nil && m.jobs[p.ID] != nil {
			m.jobs[p.ID].Running = true
			m.jobs[p.ID].RequestID = p.RequestID
			m.jobs[p.ID].RetryCount = p.RetryCount
			m.jobs[p.ID].Occurrence = p.Occurrence
		}
	case RunFinished, RunSkipped:
		var p lifecycle
		if json.Unmarshal([]byte(e.Payload), &p) == nil && m.jobs[p.ID] != nil {
			j := m.jobs[p.ID]
			j.Running = false
			j.RequestID = ""
			j.LastRun = &p.At
			j.LastStatus = p.Status
			j.LastError = p.Error
			if !p.NextRun.IsZero() {
				j.NextRun = p.NextRun
			}
			j.RetryCount = p.RetryCount
			j.Occurrence = p.Occurrence
		}
	}
}

func (m *Manager) foldAsyncCompletion(e *types.Event) {
	if e == nil || e.EventType != eventing.AsyncRunCompleted {
		return
	}
	result, err := eventing.DecodeAsyncRunResult(e)
	if err != nil || result.Source != IntegrationName {
		return
	}
	j := m.jobs[result.SourceID]
	if j == nil || !j.Running || j.RequestID != result.RequestID {
		return
	}
	j.Running = false
	j.RequestID = ""
	at := e.OccurredOn.UTC()
	j.LastRun = &at
	j.LastStatus = Status(result.Status)
	j.LastError = result.Error
	_, interval, _ := parseExisting(j.Schedule, j.CreatedAt)
	if retryable(j.LastStatus) && j.RetryCount < MaxRunRetries {
		if j.Occurrence.IsZero() {
			j.Occurrence = j.NextRun
		}
		j.RetryCount++
		j.NextRun = at.Add(retryDelay(j.RetryCount))
		return
	}
	if interval == 0 {
		delete(m.jobs, j.ID)
		return
	}
	anchor := j.Occurrence
	if anchor.IsZero() {
		anchor = j.NextRun
	}
	j.RetryCount = 0
	j.Occurrence = time.Time{}
	j.NextRun = m.advance(j.Schedule, anchor, interval, at)
}

type scheduledKey struct{}

func WithScheduled(ctx context.Context) context.Context {
	return context.WithValue(ctx, scheduledKey{}, true)
}
func IsScheduled(ctx context.Context) bool { return ctx != nil && ctx.Value(scheduledKey{}) == true }

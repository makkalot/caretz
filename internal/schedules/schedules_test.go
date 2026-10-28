package schedules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/types"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, spec string
		want       time.Time
		duration   time.Duration
		err        error
	}{
		{"minimum", "every:1m", now.Add(time.Minute), time.Minute, nil},
		{"offset", "at:2026-09-05T16:00:00+03:00", now.Add(time.Hour), 0, nil},
		{"short", "every:59s", time.Time{}, 0, ErrIntervalTooShort},
		{"past", "at:2026-09-05T11:00:00Z", time.Time{}, 0, ErrPast},
		{"bad", "daily", time.Time{}, 0, ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, d, err := Parse(tc.spec, now)
			if !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if !got.Equal(tc.want) || d != tc.duration {
				t.Fatalf("Parse = %s, %s; want %s, %s", got, d, tc.want, tc.duration)
			}
		})
	}
}

func TestParseDailyWithTimezone(t *testing.T) {
	now := time.Date(2026, 9, 5, 6, 30, 0, 0, time.UTC)
	next, recurring, err := Parse("daily:10:00@Europe/Sofia", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC)
	if !next.Equal(want) || recurring == 0 {
		t.Fatalf("Parse daily = %s, %s; want %s recurring", next, recurring, want)
	}
}

func TestDailyCadenceSurvivesDaylightSavingChange(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Sofia")
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{location: loc}
	anchor := time.Date(2026, 3, 28, 10, 0, 0, 0, loc)
	next := m.advance("daily:10:00@Europe/Sofia", anchor, 24*time.Hour, anchor)
	local := next.In(loc)
	if local.Hour() != 10 || local.Minute() != 0 || local.Day() != 29 {
		t.Fatalf("next local run = %s", local)
	}
	if next.Sub(anchor) != 23*time.Hour {
		t.Fatalf("DST interval = %s, want 23h", next.Sub(anchor))
	}
}

func TestCommandModeSurvivesRestore(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var events []*types.Event
	m, err := New("session", nil, func(e *types.Event) error { events = append(events, e); return nil }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	command, err := m.Register(context.Background(), "every:1h", "go test ./...", RegisterOptions{Mode: ModeCommand})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := m.Register(context.Background(), "every:1h", "review the diff")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register(context.Background(), "every:1h", "x", RegisterOptions{Mode: "shell"}); err == nil {
		t.Fatal("unknown mode was accepted")
	}
	restored, err := New("session", events, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, j := range restored.List() {
		modes[j.ID] = j.Mode
	}
	// A job registered before modes existed has none and stays a subagent job.
	if len(modes) != 2 || modes[command.ID] != ModeCommand || modes[prompt.ID] != "" {
		t.Fatalf("restored modes = %#v", modes)
	}
}

func TestTimeoutsDefaultValidateAndSurviveRestore(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var events []*types.Event
	m, err := New("session", nil, func(e *types.Event) error { events = append(events, e); return nil }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Register(context.Background(), "every:1h", "go test ./...", RegisterOptions{Mode: ModeCommand})
	if err != nil {
		t.Fatal(err)
	}
	if run, model := job.Timeouts(); run != 30*time.Minute || model != 15*time.Minute || job.CommandTimeout() != 15*time.Minute {
		t.Fatalf("default timeouts = %s/%s/%s, want 30m/15m/15m", run, model, job.CommandTimeout())
	}
	// A shorter run keeps half of it for the command.
	short := Job{Timeout: 10 * time.Minute}
	if run, model := short.Timeouts(); run != 10*time.Minute || model != 5*time.Minute {
		t.Fatalf("10m run timeouts = %s/%s, want 10m/5m", run, model)
	}

	tooLong := 61 * time.Minute
	if _, err := m.Update(job.ID, Update{Timeout: &tooLong}); err == nil {
		t.Fatal("61m run timeout was accepted")
	}
	run, model := 50*time.Minute, 20*time.Minute
	command := "  go test -run TestFast ./...  "
	if _, err := m.Update(job.ID, Update{Command: &command, Timeout: &run, ModelTimeout: &model}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update("sched_missing", Update{Timeout: &run}); !errors.Is(err, ErrUnknownID) {
		t.Fatalf("unknown id error = %v", err)
	}
	restored, err := New("session", events, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	got := restored.List()
	if len(got) != 1 || got[0].Prompt != "go test -run TestFast ./..." || got[0].Timeout != run || got[0].ModelTimeout != model || got[0].CommandTimeout() != 30*time.Minute {
		t.Fatalf("restored jobs = %#v, want the new command, 50m run, 20m model", got)
	}
	// Zero restores the default; nil keeps the current value.
	var zero time.Duration
	updated, err := restored.Update(job.ID, Update{Timeout: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Timeout != 0 || updated.ModelTimeout != model || updated.Prompt != "go test -run TestFast ./..." {
		t.Fatalf("updated = %#v, want default run, 20m model, and the command kept", updated)
	}
}

func TestUpdateBodyMatchesMode(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	m, err := New("session", nil, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	command, err := m.Register(context.Background(), "every:1h", "go test ./...", RegisterOptions{Mode: ModeCommand})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := m.Register(context.Background(), "every:1h", "review the diff")
	if err != nil {
		t.Fatal(err)
	}
	text, empty := "summarize the diff", "  "
	if _, err := m.Update(command.ID, Update{Prompt: &text}); err == nil || !strings.Contains(err.Error(), "set command, not prompt") {
		t.Fatalf("prompt on a command schedule error = %v", err)
	}
	if _, err := m.Update(prompt.ID, Update{Command: &text}); err == nil || !strings.Contains(err.Error(), "set prompt, not command") {
		t.Fatalf("command on a prompt schedule error = %v", err)
	}
	if _, err := m.Update(prompt.ID, Update{Prompt: &empty}); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("empty prompt error = %v", err)
	}
	updated, err := m.Update(prompt.ID, Update{Prompt: &text})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != prompt.ID || updated.Prompt != text || !updated.NextRun.Equal(prompt.NextRun) || updated.Mode != "" {
		t.Fatalf("updated = %#v, want the new prompt with the same id, cadence, and mode", updated)
	}
}

func TestPreviousRunNamesTimedOutLimits(t *testing.T) {
	job := Job{ID: "sched_1", Mode: ModeCommand, LastStatus: TimedOut, LastError: "command timeout of 15m exceeded"}
	note := job.previousRun()
	for _, want := range []string{"Status: timed_out", "Error: command timeout of 15m exceeded", "run timeout 30m, model timeout 15m, command timeout 15m", "UpdateSchedule sched_1 can change the command", "1h"} {
		if !strings.Contains(note, want) {
			t.Fatalf("previous run = %q, want %q", note, want)
		}
	}
	// A prompt run's subagent cannot change the schedule, so it is not told to.
	if note := (Job{LastStatus: TimedOut, LastError: "model timeout of 15m exceeded"}).previousRun(); strings.Contains(note, "UpdateSchedule") || !strings.Contains(note, "model timeout 15m") {
		t.Fatalf("prompt previous run = %q", note)
	}
	if (Job{LastStatus: Skipped, LastError: "previous occurrence still running"}).previousRun() != "" {
		t.Fatal("a skipped occurrence was reported as a previous run")
	}
}

func TestRegisterFoldRemoveAndAuditOnlyPayload(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var events []*types.Event
	m, err := New("session", nil, func(e *types.Event) error { events = append(events, e); return nil }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.Register(context.Background(), "every:1h", "check changes", RegisterOptions{Model: "local/code", Reasoning: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventType != Registered {
		t.Fatalf("events = %#v", events)
	}
	if j.Model != "local/code" || j.Reasoning != "high" {
		t.Fatalf("options not stored: %#v", j)
	}
	restored, err := New("session", events, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	jobs := restored.List()
	if len(jobs) != 1 || jobs[0].ID != j.ID || jobs[0].Prompt != "check changes" || jobs[0].Model != "local/code" || jobs[0].Reasoning != "high" {
		t.Fatalf("restored jobs = %#v", jobs)
	}
	if err := restored.Remove(j.ID); err != nil {
		t.Fatal(err)
	}
	if len(restored.List()) != 0 {
		t.Fatal("job was not removed")
	}
}

func TestRestoreReplaysLatestRecurringAndKeepsOverdueOneShot(t *testing.T) {
	created := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	now := created.Add(3*time.Hour + time.Minute)
	recurring := Job{ID: "sched_rec", Schedule: "every:1h", Prompt: "r", CreatedAt: created, NextRun: created.Add(time.Hour)}
	oneShot := Job{ID: "sched_at", Schedule: "at:2026-09-05T11:00:00Z", Prompt: "o", CreatedAt: created, NextRun: created.Add(time.Hour)}
	events := []*types.Event{eventFor(t, "s", Registered, recurring), eventFor(t, "s", Registered, oneShot)}
	var appended []*types.Event
	m, err := New("s", events, func(e *types.Event) error { appended = append(appended, e); return nil }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	jobs := m.List()
	if len(jobs) != 2 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	for _, j := range jobs {
		if j.ID == "sched_rec" {
			want := created.Add(3 * time.Hour)
			if !j.NextRun.Equal(want) || j.LastStatus != Skipped {
				t.Fatalf("recurring=%#v, want latest due at %s", j, want)
			}
		}
		if j.ID == "sched_at" && !j.NextRun.Before(now) {
			t.Fatalf("one-shot=%#v", j)
		}
	}
	if len(appended) != 1 || appended[0].EventType != RunSkipped {
		t.Fatalf("appended=%#v", appended)
	}
}

func TestRestoreLeavesSingleMissedOccurrenceDueWithoutSkip(t *testing.T) {
	created := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	now := created.Add(time.Hour + time.Minute)
	job := Job{ID: "sched_rec", Schedule: "every:1h", Prompt: "r", CreatedAt: created, NextRun: created.Add(time.Hour)}
	var appended []*types.Event
	m, err := New("s", []*types.Event{eventFor(t, "s", Registered, job)}, func(e *types.Event) error {
		appended = append(appended, e)
		return nil
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	jobs := m.List()
	if len(jobs) != 1 || !jobs[0].NextRun.Equal(job.NextRun) || jobs[0].LastStatus != "" {
		t.Fatalf("jobs=%#v", jobs)
	}
	if len(appended) != 0 {
		t.Fatalf("appended=%#v", appended)
	}
}

func TestRestoreInterruptedRecurringRetriesLatestWithFreshRequest(t *testing.T) {
	created := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	now := created.Add(3*time.Hour + time.Minute)
	job := Job{ID: "sched_rec", Schedule: "every:1h", Prompt: "r", CreatedAt: created, NextRun: created.Add(time.Hour)}
	events := []*types.Event{
		eventFor(t, "s", Registered, job),
		eventFor(t, "s", RunStarted, lifecycle{ID: job.ID, RequestID: "old-request", At: job.NextRun}),
	}
	var appended []*types.Event
	m, err := New("s", events, func(e *types.Event) error {
		appended = append(appended, e)
		return nil
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	jobs := m.List()
	want := created.Add(3 * time.Hour)
	if len(jobs) != 1 || !jobs[0].NextRun.Equal(want) || jobs[0].Running {
		t.Fatalf("jobs=%#v, want latest due at %s", jobs, want)
	}
	if len(appended) != 2 || appended[0].EventType != RunFinished || appended[1].EventType != RunSkipped {
		t.Fatalf("appended=%#v", appended)
	}

	started := make(chan Job, 1)
	release := make(chan struct{})
	m.Start(func(_ context.Context, got Job) error {
		started <- got
		<-release
		return nil
	})
	defer m.Stop()
	select {
	case got := <-started:
		if got.RequestID == "" || got.RequestID == "old-request" {
			t.Fatalf("recovery request id = %q", got.RequestID)
		}
	case <-time.After(time.Second):
		t.Fatal("latest interrupted occurrence was not replayed")
	}
	close(release)
}

func TestRestoreInterruptedRecurringRetriesSameOccurrenceWhenNothingNewerIsDue(t *testing.T) {
	created := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	runAt := created.Add(time.Hour)
	job := Job{ID: "sched_rec", Schedule: "every:1h", Prompt: "r", CreatedAt: created, NextRun: runAt}
	events := []*types.Event{
		eventFor(t, "s", Registered, job),
		eventFor(t, "s", RunStarted, lifecycle{ID: job.ID, RequestID: "old-request", At: runAt}),
	}
	var appended []*types.Event
	m, err := New("s", events, func(e *types.Event) error {
		appended = append(appended, e)
		return nil
	}, func() time.Time { return runAt.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	jobs := m.List()
	if len(jobs) != 1 || !jobs[0].NextRun.Equal(runAt) || jobs[0].LastStatus != Cancelled {
		t.Fatalf("jobs=%#v", jobs)
	}
	if len(appended) != 1 || appended[0].EventType != RunFinished {
		t.Fatalf("appended=%#v", appended)
	}
}

func TestRestoreInterruptedOneShotRetriesOnce(t *testing.T) {
	created := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	runAt := created.Add(time.Hour)
	job := Job{ID: "sched_at", Schedule: "at:2026-09-05T11:00:00Z", Prompt: "o", CreatedAt: created, NextRun: runAt}
	events := []*types.Event{
		eventFor(t, "s", Registered, job),
		eventFor(t, "s", RunStarted, lifecycle{ID: job.ID, RequestID: "old-request", At: runAt}),
	}
	m, err := New("s", events, nil, func() time.Time { return runAt.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	jobs := m.List()
	if len(jobs) != 1 || jobs[0].Running || !jobs[0].NextRun.Equal(runAt) || jobs[0].LastStatus != Cancelled {
		t.Fatalf("jobs=%#v", jobs)
	}
	done := make(chan struct{})
	m.Start(func(context.Context, Job) error {
		close(done)
		return nil
	})
	defer m.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("interrupted one-shot was not replayed")
	}
	deadline := time.After(time.Second)
	for len(m.List()) != 0 {
		select {
		case <-deadline:
			t.Fatalf("one-shot remained after replay: %#v", m.List())
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestFailedRunRetriesThreeTimesWithExponentialBackoff(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	setNow := func(value time.Time) {
		clockMu.Lock()
		now = value
		clockMu.Unlock()
	}

	m, err := New("s", nil, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Register(context.Background(), "every:1h", "retry me")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan Job, MaxRunRetries+1)
	m.Start(func(_ context.Context, got Job) error {
		started <- got
		return errors.New("model overloaded")
	})
	defer m.Stop()

	attemptTimes := []time.Time{
		job.NextRun,
		job.NextRun.Add(time.Minute),
		job.NextRun.Add(3 * time.Minute),
		job.NextRun.Add(7 * time.Minute),
	}
	requestIDs := map[string]bool{}
	for attempt, runAt := range attemptTimes {
		setNow(runAt)
		m.signal()
		select {
		case got := <-started:
			if got.RetryCount != attempt {
				t.Fatalf("attempt %d retry count = %d", attempt+1, got.RetryCount)
			}
			if requestIDs[got.RequestID] {
				t.Fatalf("attempt %d reused request id %q", attempt+1, got.RequestID)
			}
			// A retry is told how the attempt before it failed.
			previous := got.previousRun()
			if attempt == 0 && previous != "" {
				t.Fatalf("first attempt previous run = %q, want none", previous)
			}
			if attempt > 0 && (!strings.Contains(previous, "Status: failed") || !strings.Contains(previous, "Error: model overloaded") || !strings.Contains(previous, fmt.Sprintf("retry %d of %d", attempt, MaxRunRetries))) {
				t.Fatalf("attempt %d previous run = %q", attempt+1, previous)
			}
			requestIDs[got.RequestID] = true
		case <-time.After(time.Second):
			t.Fatalf("attempt %d did not start", attempt+1)
		}

		wantRetryCount := attempt + 1
		wantNext := time.Time{}
		if attempt < MaxRunRetries {
			wantNext = attemptTimes[attempt+1]
			waitForJob(t, m, func(got Job) bool {
				return got.LastStatus == Failed && got.RetryCount == wantRetryCount && got.NextRun.Equal(wantNext)
			})
		}
	}

	got := waitForJob(t, m, func(got Job) bool {
		return got.LastStatus == Failed && got.RetryCount == 0
	})
	if want := job.NextRun.Add(time.Hour); !got.NextRun.Equal(want) {
		t.Fatalf("next normal occurrence = %s, want %s", got.NextRun, want)
	}
}

func TestOneShotSucceedsOnRetry(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	setNow := func(value time.Time) {
		clockMu.Lock()
		now = value
		clockMu.Unlock()
	}

	m, err := New("s", nil, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Register(context.Background(), "at:2026-09-05T11:00:00Z", "retry once")
	if err != nil {
		t.Fatal(err)
	}
	attempts := make(chan Job, 2)
	m.Start(func(_ context.Context, got Job) error {
		attempts <- got
		if got.RetryCount == 0 {
			return errors.New("temporary timeout")
		}
		return nil
	})
	defer m.Stop()

	setNow(job.NextRun)
	m.signal()
	<-attempts
	retrying := waitForJob(t, m, func(got Job) bool { return got.RetryCount == 1 })
	setNow(retrying.NextRun)
	m.signal()
	<-attempts

	deadline := time.After(time.Second)
	for len(m.List()) != 0 {
		select {
		case <-deadline:
			t.Fatalf("successful one-shot retry remained active: %#v", m.List())
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func waitForJob(t *testing.T, m *Manager, ready func(Job) bool) Job {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		jobs := m.List()
		if len(jobs) == 1 && ready(jobs[0]) {
			return jobs[0]
		}
		select {
		case <-deadline:
			t.Fatalf("schedule state did not become ready: %#v", jobs)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestRestoreDailyReplaysLatestAcrossDaylightSavingChange(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Sofia")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 3, 27, 9, 0, 0, 0, loc)
	first := time.Date(2026, 3, 27, 10, 0, 0, 0, loc)
	now := time.Date(2026, 3, 29, 12, 0, 0, 0, loc)
	job := Job{ID: "sched_daily", Schedule: "daily:10:00@Europe/Sofia", Prompt: "d", CreatedAt: created.UTC(), NextRun: first.UTC()}
	m, err := New("s", []*types.Event{eventFor(t, "s", Registered, job)}, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	jobs := m.List()
	want := time.Date(2026, 3, 29, 10, 0, 0, 0, loc)
	if len(jobs) != 1 || !jobs[0].NextRun.Equal(want) {
		t.Fatalf("jobs=%#v, want latest daily occurrence at %s", jobs, want)
	}
}

func TestValidationAndScheduledRegistration(t *testing.T) {
	now := time.Now().UTC()
	m, _ := New("s", nil, nil, func() time.Time { return now })
	if _, err := m.Register(context.Background(), "every:1m", " "); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatal(err)
	}
	if _, err := m.Register(WithScheduled(context.Background()), "every:1m", "nested"); !errors.Is(err, ErrNested) {
		t.Fatal(err)
	}
	asyncCtx := sessiontools.WithAsyncRequest(context.Background(), eventing.AsyncRunRequest{Source: IntegrationName})
	if _, err := m.Register(asyncCtx, "every:1m", "nested async"); !errors.Is(err, ErrNested) {
		t.Fatal(err)
	}
	if err := m.Remove("sched_missing"); !errors.Is(err, ErrUnknownID) {
		t.Fatal(err)
	}
}

func TestDailyScheduleFiresAfterDayChangesWithoutSkippingRunningOccurrence(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Sofia")
	if err != nil {
		t.Fatal(err)
	}
	var clockMu sync.Mutex
	now := time.Date(2026, 9, 5, 23, 59, 0, 0, loc)
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	setNow := func(value time.Time) {
		clockMu.Lock()
		now = value
		clockMu.Unlock()
	}

	m, err := New("s", nil, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Register(context.Background(), "daily:00:01@Europe/Sofia", "cross midnight")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	m.Start(func(context.Context, Job) error {
		close(started)
		<-release
		return nil
	})
	defer m.Stop()

	setNow(time.Date(2026, 9, 6, 0, 1, 0, 0, loc))
	m.signal()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("daily schedule did not fire after the date changed")
	}

	// Let the scheduler loop inspect the running job. The current occurrence
	// must not be reported as a second, overlapping occurrence.
	time.Sleep(25 * time.Millisecond)
	running := m.List()
	if len(running) != 1 || running[0].ID != job.ID {
		t.Fatalf("jobs while running = %#v", running)
	}
	if running[0].LastStatus == Skipped {
		t.Fatalf("running occurrence was incorrectly skipped: %#v", running[0])
	}
	close(release)

	deadline := time.After(time.Second)
	for {
		jobs := m.List()
		if len(jobs) == 1 && jobs[0].LastStatus == Succeeded {
			want := time.Date(2026, 9, 7, 0, 1, 0, 0, loc)
			if !jobs[0].NextRun.Equal(want) {
				t.Fatalf("next run = %s, want %s", jobs[0].NextRun, want)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("daily schedule did not finish: %#v", jobs)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func eventFor(t *testing.T, id, kind string, p any) *types.Event {
	t.Helper()
	m, _ := New(id, nil, nil, nil)
	m.originator = id
	var result *types.Event
	m.append = func(e *types.Event) error { result = e; return nil }
	m.mu.Lock()
	if err := m.emitLocked(kind, p); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	return result
}

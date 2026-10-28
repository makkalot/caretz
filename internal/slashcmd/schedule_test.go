package slashcmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/schedules"
)

func TestLoopCommandRegistersRecurringSchedule(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     string
		interval time.Duration
		spec     string
		cadence  string
	}{
		{name: "default", args: "check the repository", interval: 5 * time.Minute, spec: "every:5m", cadence: "every 5 minutes"},
		{name: "explicit equals", args: "--interval=1h check the repository", interval: time.Hour, spec: "every:1h", cadence: "every hour"},
		{name: "explicit space", args: "--interval 1m check the repository", interval: time.Minute, spec: "every:1m", cadence: "every minute"},
		{name: "duration shorthand", args: "1m check the repository", interval: time.Minute, spec: "every:1m", cadence: "every minute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newLoopTestSession(t, &loopMockProvider{}, false)
			if err := session.InstallIntegration(schedules.NewIntegration); err != nil {
				t.Fatal(err)
			}
			before := time.Now().UTC()
			result, err := (LoopCommand{}).Run(context.Background(), session, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			hosted, _ := session.Integration(schedules.IntegrationName)
			jobs := hosted.(*schedules.Integration).List()
			if len(jobs) != 1 {
				t.Fatalf("jobs = %+v", jobs)
			}
			job := jobs[0]
			if job.Schedule != tc.spec || job.Prompt != "check the repository" {
				t.Fatalf("job = %+v", job)
			}
			if job.NextRun.Before(before.Add(tc.interval)) || job.NextRun.After(time.Now().UTC().Add(tc.interval)) {
				t.Fatalf("next run = %s, interval = %s", job.NextRun, tc.interval)
			}
			for _, want := range []string{job.ID, `"check the repository"`, tc.cadence, job.NextRun.Format(time.RFC3339), "resumes when you reopen it", "Cancel schedule " + job.ID} {
				if !strings.Contains(result.Message, want) {
					t.Fatalf("registration message = %q, missing %q", result.Message, want)
				}
			}
		})
	}
}

func TestLoopCommandBangRegistersCommandJob(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	if err := session.InstallIntegration(schedules.NewIntegration); err != nil {
		t.Fatal(err)
	}
	if _, err := (LoopCommand{}).Run(context.Background(), session, "10m !go test ./..."); err != nil {
		t.Fatal(err)
	}
	hosted, _ := session.Integration(schedules.IntegrationName)
	jobs := hosted.(*schedules.Integration).List()
	if len(jobs) != 1 || jobs[0].Mode != schedules.ModeCommand || jobs[0].Prompt != "go test ./..." {
		t.Fatalf("jobs = %+v, want one command job", jobs)
	}
	result, err := (ScheduleListCommand{}).Run(context.Background(), session, "")
	if err != nil || !strings.Contains(result.Message, "mode=command") || strings.Contains(result.Message, "model=") {
		t.Fatalf("list = %q, %v", result.Message, err)
	}

	session.ToolRegistry().SandboxDisabled = true
	if _, err := (LoopCommand{}).Run(context.Background(), session, "10m !ls"); err == nil || !strings.Contains(err.Error(), "need the sandbox") {
		t.Fatalf("error = %v, want a sandbox refusal", err)
	}
}

func TestLoopCommandBangKeepsShellQuoting(t *testing.T) {
	for _, tc := range []struct{ args, want string }{
		{`10m !grep "foo bar" app.log`, `grep "foo bar" app.log`},
		{`--interval 10m !echo '$HOME'`, `echo '$HOME'`},
		{`!find . -name \*.go`, `find . -name \*.go`},
		{`10m !grep "a !b" log`, `grep "a !b" log`},
		// A quoted command has no shell quoting of its own to keep.
		{`10m "!ls -la"`, `ls -la`},
	} {
		t.Run(tc.args, func(t *testing.T) {
			session := newLoopTestSession(t, &loopMockProvider{}, false)
			if err := session.InstallIntegration(schedules.NewIntegration); err != nil {
				t.Fatal(err)
			}
			if _, err := (LoopCommand{}).Run(context.Background(), session, tc.args); err != nil {
				t.Fatal(err)
			}
			hosted, _ := session.Integration(schedules.IntegrationName)
			jobs := hosted.(*schedules.Integration).List()
			if len(jobs) != 1 || jobs[0].Mode != schedules.ModeCommand || jobs[0].Prompt != tc.want {
				t.Fatalf("jobs = %+v, want command %q", jobs, tc.want)
			}
		})
	}
}

func TestLoopCommandRejectsInvalidInput(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	if err := session.InstallIntegration(schedules.NewIntegration); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args string
		want string
	}{
		{args: "", want: "usage: /loop"},
		{args: "1m", want: "usage: /loop"},
		{args: "--interval=1h", want: "usage: /loop"},
		{args: "--interval=30s check", want: "at least 1m"},
		{args: "30s check", want: "at least 1m"},
		{args: "--interval=bogus check", want: "malformed schedule"},
		{args: "--model local/model check", want: "unknown option"},
		{args: "--reasoning high check", want: "unknown option"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			_, err := (LoopCommand{}).Run(context.Background(), session, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestScheduleListCommand(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	if err := session.InstallIntegration(schedules.NewIntegration); err != nil {
		t.Fatal(err)
	}
	list := ScheduleListCommand{}
	result, err := list.Run(context.Background(), session, "")
	if err != nil || result.Message != "no active schedules" {
		t.Fatalf("empty list = %q, %v", result.Message, err)
	}
	if _, err := list.Run(context.Background(), session, "extra"); err == nil || !strings.Contains(err.Error(), "does not accept arguments") {
		t.Fatalf("list arguments error = %v", err)
	}
	if _, err := (LoopCommand{}).Run(context.Background(), session, "--interval=1h check the repository"); err != nil {
		t.Fatal(err)
	}
	hosted, _ := session.Integration(schedules.IntegrationName)
	job := hosted.(*schedules.Integration).List()[0]
	result, err = list.Run(context.Background(), session, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{job.ID, "every:1h", "check the repository", "next="} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("list = %q, missing %q", result.Message, want)
		}
	}
}

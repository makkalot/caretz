package slashcmd

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/schedules"
)

type LoopCommand struct{}

func (LoopCommand) Name() string        { return "loop" }
func (LoopCommand) Description() string { return "Run a prompt repeatedly in this session" }
func (LoopCommand) Usage() string {
	return "/loop [DURATION] <prompt|!command> | /loop --interval DURATION <prompt|!command>"
}
func (LoopCommand) Aliases() []string { return nil }
func (LoopCommand) Run(ctx context.Context, session *agent.AgentSession, args string) (Result, error) {
	scheduler, err := sessionScheduler(session)
	if err != nil {
		return Result{}, err
	}
	parsed, err := parseCommandArgs(args, []optionDef{{Name: "interval", TakesValue: true}})
	if err != nil {
		return Result{}, err
	}
	positionals := parsed.Positionals
	interval := parsed.Options["interval"]
	if interval == "" {
		interval = "5m"
		if len(positionals) > 0 {
			if _, parseErr := time.ParseDuration(positionals[0]); parseErr == nil {
				interval = positionals[0]
				positionals = positionals[1:]
			}
		}
	}
	if len(positionals) == 0 {
		return Result{}, fmt.Errorf("usage: %s", (LoopCommand{}).Usage())
	}
	// A body starting with ! is a command run in the sandbox, like the !
	// shell shortcut, instead of a prompt for a subagent.
	body := strings.Join(positionals, " ")
	var opts schedules.RegisterOptions
	if command, ok := strings.CutPrefix(body, "!"); ok {
		// The parsed tokens lost the quotes and escapes the shell needs, so
		// take the command as typed when it is not itself quoted.
		if raw, found := rawBangCommand(args); found {
			command = raw
		}
		body, opts.Mode = strings.TrimSpace(command), schedules.ModeCommand
	}
	j, err := scheduler.Register(ctx, "every:"+interval, body, opts)
	if err != nil {
		return Result{}, err
	}
	duration, _ := time.ParseDuration(interval) // Registration already validated the interval.
	return Result{Message: fmt.Sprintf("Scheduled: %q, %s.\nJob: %s · Next run: %s\nRuns while this session is open and resumes when you reopen it.\nTo cancel, ask: Cancel schedule %s", shortSchedulePrompt(j.Prompt), loopCadence(duration), j.ID, j.NextRun.Format(time.RFC3339), j.ID)}, nil
}

// rawBangCommand returns the text after the first "!" that starts a word in
// args, exactly as typed.
func rawBangCommand(args string) (string, bool) {
	previous := ' '
	for i, r := range args {
		if r == '!' && unicode.IsSpace(previous) {
			return args[i+1:], true
		}
		previous = r
	}
	return "", false
}

func loopCadence(interval time.Duration) string {
	for _, unit := range []struct {
		duration time.Duration
		name     string
	}{
		{24 * time.Hour, "day"},
		{time.Hour, "hour"},
		{time.Minute, "minute"},
	} {
		if interval%unit.duration != 0 {
			continue
		}
		count := interval / unit.duration
		if count == 1 {
			return "every " + unit.name
		}
		return fmt.Sprintf("every %d %ss", count, unit.name)
	}
	return "every " + interval.String()
}

type ScheduleListCommand struct{}

func (ScheduleListCommand) Name() string        { return "schedule-list" }
func (ScheduleListCommand) Description() string { return "List active schedules in this session" }
func (ScheduleListCommand) Usage() string       { return "/schedule-list" }
func (ScheduleListCommand) Aliases() []string   { return nil }
func (ScheduleListCommand) Run(_ context.Context, session *agent.AgentSession, args string) (Result, error) {
	if err := rejectArguments("schedule-list", args); err != nil {
		return Result{}, err
	}
	scheduler, err := sessionScheduler(session)
	if err != nil {
		return Result{}, err
	}
	jobs := scheduler.List()
	if len(jobs) == 0 {
		return Result{Message: "no active schedules"}, nil
	}
	var b strings.Builder
	for i, j := range jobs {
		if i > 0 {
			b.WriteByte('\n')
		}
		prompt := shortSchedulePrompt(j.Prompt)
		last := "-"
		if j.LastRun != nil {
			last = j.LastRun.Format(time.RFC3339)
		}
		if j.Mode == schedules.ModeCommand {
			fmt.Fprintf(&b, "%s  %s  mode=command  next=%s  last=%s  status=%s  %q", j.ID, j.Schedule, j.NextRun.Format(time.RFC3339), last, j.LastStatus, prompt)
		} else {
			model, reasoning := j.Model, j.Reasoning
			if model == "" {
				defaults := scheduler.Defaults()
				model, reasoning = defaults.Model, defaults.Reasoning
			}
			fmt.Fprintf(&b, "%s  %s  mode=subagent  model=%s  reasoning=%s  next=%s  last=%s  status=%s  %q", j.ID, j.Schedule, valueOrDash(model), valueOrDash(reasoning), j.NextRun.Format(time.RFC3339), last, j.LastStatus, prompt)
		}
		if j.LastError != "" {
			fmt.Fprintf(&b, "  error=%q", j.LastError)
		}
	}
	return Result{Message: b.String()}, nil
}

func shortSchedulePrompt(prompt string) string {
	runes := []rune(prompt)
	if len(runes) > 60 {
		return string(runes[:57]) + "..."
	}
	return prompt
}

func sessionScheduler(session *agent.AgentSession) (*schedules.Integration, error) {
	if session == nil {
		return nil, fmt.Errorf("schedules are not enabled for this session")
	}
	hosted, _ := session.Integration(schedules.IntegrationName)
	scheduler, ok := hosted.(*schedules.Integration)
	if !ok {
		return nil, fmt.Errorf("schedules are not enabled for this session")
	}
	return scheduler, nil
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

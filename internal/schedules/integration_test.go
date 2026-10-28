package schedules

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type integrationGateway struct {
	mu     sync.Mutex
	events []*types.Event
	audit  *eventing.SyncEventLoop
	ctx    *eventing.SyncEventLoop
}

func newIntegrationGateway() *integrationGateway {
	return &integrationGateway{audit: eventing.NewSyncEventLoop(context.Background()), ctx: eventing.NewSyncEventLoop(context.Background())}
}

func (h *integrationGateway) PublishAsyncEvent(_ context.Context, event *types.Event) error {
	h.mu.Lock()
	h.events = append(h.events, event)
	h.mu.Unlock()
	return h.audit.RunEvent(event)
}

func (h *integrationGateway) SubscribeAuditEvents(types ...string) (eventing.Subscription, error) {
	return h.audit.Subscribe(types...)
}

func (h *integrationGateway) SubscribeContextEvents(types ...string) (eventing.Subscription, error) {
	return h.ctx.Subscribe(types...)
}

func (h *integrationGateway) AuditEvents() ([]*types.Event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*types.Event(nil), h.events...), nil
}

func (*integrationGateway) ChannelID() string { return "session" }

// sandboxGateway is a session whose tool registry reports the sandbox state.
type sandboxGateway struct {
	*integrationGateway
	registry *tools.Registry
}

func (g sandboxGateway) ToolRegistry() *tools.Registry { return g.registry }

func registerScheduleTool(t *testing.T, integration sessiontools.Integration) tools.Tool {
	t.Helper()
	for _, tool := range integration.Tools() {
		if tool.Definition.Name == "RegisterSchedule" {
			return tool
		}
	}
	t.Fatal("RegisterSchedule tool not found")
	return tools.Tool{}
}

func TestRegisterScheduleCommand(t *testing.T) {
	tests := []struct {
		name     string
		gateway  sessiontools.SessionEventGateway
		args     string
		wantErr  string
		wantMode string
	}{
		{name: "command", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{}}, args: `{"schedule":"every:5m","command":"go test ./..."}`, wantMode: ModeCommand},
		{name: "prompt", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{}}, args: `{"schedule":"every:5m","prompt":"review the diff"}`},
		{name: "both", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{}}, args: `{"schedule":"every:5m","prompt":"a","command":"b"}`, wantErr: "exactly one of command or prompt"},
		{name: "neither", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{}}, args: `{"schedule":"every:5m"}`, wantErr: "exactly one of command or prompt"},
		{name: "sandbox disabled", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{SandboxDisabled: true}}, args: `{"schedule":"every:5m","command":"ls"}`, wantErr: ErrNeedsSandbox.Error()},
		{name: "no registry", gateway: newIntegrationGateway(), args: `{"schedule":"every:5m","command":"ls"}`, wantErr: ErrNeedsSandbox.Error()},
		{name: "timeouts", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{}}, args: `{"schedule":"every:5m","command":"ls","timeout":"45m","model_timeout":"10m"}`, wantMode: ModeCommand},
		{name: "timeout over max", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{}}, args: `{"schedule":"every:5m","prompt":"a","timeout":"90m"}`, wantErr: "between 1m and 1h"},
		{name: "bad timeout", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{}}, args: `{"schedule":"every:5m","prompt":"a","timeout":"soon"}`, wantErr: "positive duration"},
		{name: "command without time", gateway: sandboxGateway{newIntegrationGateway(), &tools.Registry{}}, args: `{"schedule":"every:5m","command":"ls","timeout":"20m","model_timeout":"20m"}`, wantErr: "for the command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			integration, err := NewIntegration(tt.gateway)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = integration.Close() }()
			_, err = registerScheduleTool(t, integration).Handler(context.Background(), llm.ToolCall{Arguments: tt.args})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			jobs := integration.(*Integration).List()
			if len(jobs) != 1 || jobs[0].Mode != tt.wantMode {
				t.Fatalf("jobs = %#v, want mode %q", jobs, tt.wantMode)
			}
		})
	}
}

func TestIntegrationPublishesCommandRunWithoutModel(t *testing.T) {
	defaults := Defaults{Model: "local/small", Reasoning: "low", Limits: &eventing.ContextLimits{Context: 64000}}
	request := firstScheduledRequest(t, defaults, RegisterOptions{Mode: ModeCommand})
	if request.Mode != eventing.AsyncRunCommand || request.Prompt != "check status" {
		t.Fatalf("request = %#v, want a command run", request)
	}
	if request.Timeout != DefaultRunTimeout || request.ModelTimeout != DefaultModelTimeout || request.CommandTimeoutSeconds != 15*60 {
		t.Fatalf("request timeouts = %s/%s/%ds, want 30m run, 15m model, 15m command", request.Timeout, request.ModelTimeout, request.CommandTimeoutSeconds)
	}
	if request.Model != "" || request.ReasoningEffort != "" || request.ContextLimits != nil {
		t.Fatalf("request = %#v, want no model settings on a command run", request)
	}
}

func TestRegisterScheduleToolHidesModelOptions(t *testing.T) {
	gateway := newIntegrationGateway()
	integration, err := NewIntegration(gateway)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := integration.Close(); err != nil {
			t.Error(err)
		}
	}()

	var parameters map[string]any
	for _, tool := range integration.Tools() {
		if tool.Definition.Name != "RegisterSchedule" {
			continue
		}
		encoded, err := json.Marshal(tool.Definition.Parameters)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &parameters); err != nil {
			t.Fatal(err)
		}
		break
	}
	if parameters == nil {
		t.Fatal("RegisterSchedule tool not found")
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", parameters["properties"])
	}
	if _, ok := properties["command"]; !ok {
		t.Fatal("RegisterSchedule does not expose command")
	}
	if _, ok := properties["model"]; ok {
		t.Fatal("RegisterSchedule exposes model")
	}
	if _, ok := properties["reasoning"]; ok {
		t.Fatal("RegisterSchedule exposes reasoning")
	}
	if _, ok := properties["foreground"]; ok {
		t.Fatal("RegisterSchedule exposes foreground")
	}
}

func TestIntegrationPublishesGenericRunAndConsumesCompletion(t *testing.T) {
	gateway := newIntegrationGateway()
	now := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
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
	manager, err := New(gateway.ChannelID(), nil, func(event *types.Event) error {
		return gateway.PublishAsyncEvent(context.Background(), event)
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	integration := &Integration{gateway: gateway, manager: manager, waiters: map[string]chan eventing.AsyncRunResult{}}
	requests, err := gateway.SubscribeAuditEvents(eventing.AsyncRunRequested)
	if err != nil {
		t.Fatal(err)
	}
	defer requests.Close()
	if err := integration.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := integration.Close(); err != nil {
			t.Error(err)
		}
	}()

	job, err := integration.Register(context.Background(), "every:1m", "check status")
	if err != nil {
		t.Fatal(err)
	}
	setNow(clock().Add(time.Minute))
	manager.signal()

	var request eventing.AsyncRunRequest
	select {
	case event := <-requests.Events():
		request, err = eventing.DecodeAsyncRunRequest(event)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not publish an async run request")
	}
	if request.Source != IntegrationName || request.SourceID != job.ID || request.RequestID == "" || request.Mode != eventing.AsyncRunSubagent {
		t.Fatalf("request = %#v", request)
	}
	if err := gateway.PublishAsyncEvent(context.Background(), eventing.NewAsyncRunCompleted(gateway.ChannelID(), eventing.AsyncRunResult{
		RequestID: request.RequestID,
		Source:    IntegrationName,
		SourceID:  job.ID,
		Status:    string(Succeeded),
	})); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(time.Second)
	for {
		jobs := integration.List()
		if len(jobs) == 1 && jobs[0].LastStatus == Succeeded {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("schedule did not consume completion: %#v", jobs)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestIntegrationAppliesDefaultsToRunsWithoutModel(t *testing.T) {
	limits := &eventing.ContextLimits{Context: 64000, SummaryTokens: 4000}
	defaults := Defaults{Model: "local/small", Reasoning: "low", Limits: limits}
	tests := []struct {
		name          string
		defaults      Defaults
		options       []RegisterOptions
		wantModel     string
		wantReasoning string
		wantLimits    *eventing.ContextLimits
	}{
		{name: "no defaults"},
		{name: "defaults", defaults: defaults, wantModel: "local/small", wantReasoning: "low", wantLimits: limits},
		{name: "job model wins", defaults: defaults, options: []RegisterOptions{{Model: "local/big"}}, wantModel: "local/big", wantLimits: limits},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := firstScheduledRequest(t, tt.defaults, tt.options...)
			if request.Model != tt.wantModel || request.ReasoningEffort != tt.wantReasoning {
				t.Fatalf("model = %q reasoning = %q, want %q %q", request.Model, request.ReasoningEffort, tt.wantModel, tt.wantReasoning)
			}
			if (request.ContextLimits == nil) != (tt.wantLimits == nil) || (request.ContextLimits != nil && *request.ContextLimits != *tt.wantLimits) {
				t.Fatalf("limits = %#v, want %#v", request.ContextLimits, tt.wantLimits)
			}
		})
	}
}

// firstScheduledRequest registers a one-minute schedule and returns the run
// request it publishes when it first fires.
func firstScheduledRequest(t *testing.T, defaults Defaults, options ...RegisterOptions) eventing.AsyncRunRequest {
	t.Helper()
	gateway := newIntegrationGateway()
	now := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	manager, err := New(gateway.ChannelID(), nil, func(event *types.Event) error {
		return gateway.PublishAsyncEvent(context.Background(), event)
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	integration := &Integration{gateway: sandboxGateway{gateway, &tools.Registry{}}, manager: manager, defaults: defaults, waiters: map[string]chan eventing.AsyncRunResult{}}
	requests, err := gateway.SubscribeAuditEvents(eventing.AsyncRunRequested)
	if err != nil {
		t.Fatal(err)
	}
	defer requests.Close()
	if err := integration.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := integration.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := integration.Register(context.Background(), "every:1m", "check status", options...); err != nil {
		t.Fatal(err)
	}
	clockMu.Lock()
	now = now.Add(time.Minute)
	clockMu.Unlock()
	manager.signal()
	select {
	case event := <-requests.Events():
		request, err := eventing.DecodeAsyncRunRequest(event)
		if err != nil {
			t.Fatal(err)
		}
		return request
	case <-time.After(time.Second):
		t.Fatal("scheduler did not publish an async run request")
	}
	return eventing.AsyncRunRequest{}
}

func TestRestoreUsesHistoricalAsyncCompletionWithoutReplaying(t *testing.T) {
	created := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	job := Job{ID: "sched_1", Schedule: "every:1h", Prompt: "check", CreatedAt: created, NextRun: created.Add(time.Hour)}
	events := []*types.Event{
		eventFor(t, "session", Registered, job),
		eventFor(t, "session", RunStarted, lifecycle{ID: job.ID, RequestID: "request-1", At: created.Add(time.Hour)}),
		eventing.NewAsyncRunCompleted("session", eventing.AsyncRunResult{RequestID: "request-1", Source: IntegrationName, SourceID: job.ID, Status: string(Succeeded)}),
	}
	events[2].OccurredOn = created.Add(time.Hour + time.Minute)
	restored, err := New("session", events, nil, func() time.Time { return created.Add(time.Hour + 2*time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	jobs := restored.List()
	if len(jobs) != 1 || jobs[0].Running || jobs[0].LastStatus != Succeeded || !jobs[0].NextRun.After(events[2].OccurredOn) {
		t.Fatalf("restored jobs = %#v", jobs)
	}
}

func TestRestoreUsesHistoricalAsyncFailureToScheduleRetry(t *testing.T) {
	created := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	runAt := created.Add(time.Hour)
	failedAt := runAt.Add(5 * time.Minute)
	job := Job{ID: "sched_1", Schedule: "at:2026-09-06T11:00:00Z", Prompt: "check", CreatedAt: created, NextRun: runAt}
	events := []*types.Event{
		eventFor(t, "session", Registered, job),
		eventFor(t, "session", RunStarted, lifecycle{ID: job.ID, RequestID: "request-1", At: runAt, Occurrence: runAt}),
		eventing.NewAsyncRunCompleted("session", eventing.AsyncRunResult{RequestID: "request-1", Source: IntegrationName, SourceID: job.ID, Status: string(TimedOut), Error: "model timed out"}),
	}
	events[2].OccurredOn = failedAt

	restored, err := New("session", events, nil, func() time.Time { return failedAt.Add(30 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	jobs := restored.List()
	wantNext := failedAt.Add(RetryBaseDelay)
	if len(jobs) != 1 || jobs[0].Running || jobs[0].LastStatus != TimedOut || jobs[0].RetryCount != 1 || !jobs[0].NextRun.Equal(wantNext) {
		t.Fatalf("restored retry = %#v, want next run %s", jobs, wantNext)
	}
}

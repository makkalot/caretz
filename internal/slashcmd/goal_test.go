package slashcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/goal"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

// goalMockProvider answers the goal roles. A nil handler uses the default: a
// one-task plan with one evidence check, an "ok" worker, a review without
// findings and a judge that finds the goal done.
type goalMockProvider struct {
	plan   func(call int, prompt string) string
	worker func(call int, prompt string) string
	review func(call int, prompt string) string
	judge  func(call int, prompt string) string

	planCalls, workerCalls, reviewCalls, judgeCalls         int
	planPrompts, workerPrompts, reviewPrompts, judgePrompts []string
	corrections                                             []string
	calls                                                   []string
	toolSets                                                map[string][]llm.ToolDefinition
	followUpMessages                                        []llm.Message
}

const (
	goalMarkerPlanner  = "You are the planner of a goal harness"
	goalMarkerWorker   = "Work on this bounded task"
	goalMarkerReviewer = "You are an independent code reviewer"
	goalMarkerJudge    = "You are an independent judge"
)

func goalPromptKind(prompt string) string {
	for marker, kind := range map[string]string{goalMarkerPlanner: "planner", goalMarkerWorker: "worker", goalMarkerReviewer: "reviewer", goalMarkerJudge: "judge"} {
		if strings.Contains(prompt, marker) {
			return kind
		}
	}
	return ""
}

func (m *goalMockProvider) Chat(_ string, messages []llm.Message, defs []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	last := messages[len(messages)-1].Content
	if strings.Contains(last, "What did /goal finish?") {
		m.followUpMessages = append([]llm.Message(nil), messages...)
		return goalMockAnswer("The export report goal was verified."), nil
	}
	prompt := last
	if strings.HasPrefix(last, "Your previous response was invalid") {
		m.corrections = append(m.corrections, last)
		for _, message := range messages {
			if goalPromptKind(message.Content) != "" {
				prompt = message.Content
				break
			}
		}
	}
	kind := goalPromptKind(prompt)
	if kind != "" {
		if m.toolSets == nil {
			m.toolSets = map[string][]llm.ToolDefinition{}
		}
		m.toolSets[kind] = append([]llm.ToolDefinition(nil), defs...)
	}
	switch kind {
	case "planner":
		m.planCalls++
		m.calls = append(m.calls, "plan")
		m.planPrompts = append(m.planPrompts, prompt)
		if m.plan != nil {
			return goalMockAnswer(m.plan(m.planCalls, prompt)), nil
		}
		return goalMockAnswer(defaultGoalPlan(m.planCalls)), nil
	case "worker":
		m.workerCalls++
		m.calls = append(m.calls, "worker")
		m.workerPrompts = append(m.workerPrompts, prompt)
		if m.worker != nil {
			return goalMockAnswer(m.worker(m.workerCalls, prompt)), nil
		}
		return goalMockAnswer(goalHandoff(prompt, "ok", "finished")), nil
	case "reviewer":
		m.reviewCalls++
		m.calls = append(m.calls, "reviewer")
		m.reviewPrompts = append(m.reviewPrompts, prompt)
		if m.review != nil {
			return goalMockAnswer(m.review(m.reviewCalls, prompt)), nil
		}
		return goalMockAnswer(goalReviewJSON(prompt, nil)), nil
	case "judge":
		m.judgeCalls++
		m.calls = append(m.calls, "judge")
		m.judgePrompts = append(m.judgePrompts, prompt)
		if m.judge != nil {
			return goalMockAnswer(m.judge(m.judgeCalls, prompt)), nil
		}
		return goalMockAnswer(`{"done":true,"reason":"goal verified","summary":"Report verified."}`), nil
	}
	return llm.Response{}, fmt.Errorf("unexpected goal prompt: %s", last)
}

// goalReviewJSON answers one met entry per criterion in the reviewer
// prompt, citing the README every test workspace has.
func goalReviewJSON(prompt string, findings []map[string]any) string {
	block, _, _ := strings.Cut(strings.SplitN(prompt, "Overall criteria:\n", 2)[1], "\nApproved evidence:")
	var criteria []map[string]any
	for _, criterion := range strings.Split(block, "\n") {
		criteria = append(criteria, map[string]any{"criterion": criterion, "status": "met", "evidence": []string{"README.md:1"}})
	}
	if findings == nil {
		findings = []map[string]any{}
	}
	return mustJSON(map[string]any{"criteria": criteria, "findings": findings, "summary": "reviewed"})
}

func goalMockAnswer(answer string) llm.Response {
	return llm.Response{Content: answer, Message: llm.Message{Role: llm.RoleAssistant, Content: answer}}
}

func (m *goalMockProvider) GetModel() string   { return "mock" }
func (m *goalMockProvider) GetBaseURL() string { return "mock" }

var goalTaskIDPattern = regexp.MustCompile(`"task_id":"([^"]+)"`)

func goalHandoff(prompt, result, notes string) string {
	match := goalTaskIDPattern.FindStringSubmatch(prompt)
	id := ""
	if len(match) == 2 {
		id = match[1]
	}
	return mustJSON(map[string]any{"task_id": id, "result": result, "notes": notes, "discoveries": []string{}})
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// defaultGoalPlan gives every plan a distinct task check so a replan never
// reuses the check of a verified task.
func defaultGoalPlan(call int) string {
	return fmt.Sprintf(`{"plan_summary":"Implement the change, then verify it with the project check.","goal_criteria":["Goal works"],"evidence":[{"id":"E1","check":{"shell":"true"},"covers":"goal","status":"valid"}],"ops":[{"op":"add","ref":"task","kind":"work","title":"task %d","next_action":"implement","validation":["Task works"],"check":{"shell":"true # task %d"}}]}`, call, call)
}

func isGoalProbe(command string) bool {
	return strings.HasPrefix(command, "bash -n -c ") || strings.HasPrefix(command, "command -v -- ") || strings.HasPrefix(command, "git ")
}

func defaultGoalRunner(command string) (string, string, error) {
	if strings.HasPrefix(command, "git ") {
		return "", "", nil
	}
	if isGoalProbe(command) || strings.HasPrefix(command, "true") {
		return "ok", "", nil
	}
	return "", "", fmt.Errorf("unexpected command %q", command)
}

// setGoalRunner routes check commands to run; probes always succeed.
func setGoalRunner(session *agent.AgentSession, run func(command string) (string, string, error)) {
	session.ToolRegistry().RunSandboxedOptions = func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		if isGoalProbe(command) {
			return "", "", nil
		}
		return run(command)
	}
}

// writeGoalReadme gives the reviewer a file to cite.
func writeGoalReadme(t *testing.T, workspace string) {
	t.Helper()
	path := filepath.Join(workspace, "README.md")
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.WriteFile(path, []byte("# Report\nExports rows.\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func newGoalTestSession(t *testing.T, baseDir, workspace string, provider llm.Provider) *agent.AgentSession {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	writeGoalReadme(t, resolved)
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: resolved, RunSandboxedOptions: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		return defaultGoalRunner(command)
	}}
	session, err := agent.NewAgentSession("test", "goal-session", agent.SessionParams{
		BaseDir: baseDir, WorkspaceDir: resolved, Logger: log.New(), Provider: provider, ToolRegistry: registry,
		SystemPrompt: "test", ContextWindow: 128000, ReservedTokens: 16000, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func closeGoalTestSession(t *testing.T, session *agent.AgentSession) {
	t.Helper()
	if err := session.Close(); err != nil {
		t.Errorf("close goal session: %v", err)
	}
}

func mustLoadGoal(t *testing.T, session *agent.AgentSession) goal.State {
	t.Helper()
	state, found, err := loadGoalState(session)
	if err != nil || !found {
		t.Fatalf("loadGoalState() = %v, %v", found, err)
	}
	return state
}

// appendGoalTestEvent records a goal event directly, as a process that died
// mid-run would have left it.
func appendGoalTestEvent(t *testing.T, session *agent.AgentSession, eventType string, at time.Time, data any) {
	t.Helper()
	state := mustLoadGoal(t, session)
	event := eventing.GoalEvent{Type: eventType, GoalID: state.ID, Revision: state.Revision + 1, At: at}
	if data != nil {
		event.Data, _ = json.Marshal(data)
	}
	if err := session.AppendAuditEvent(eventing.NewGoalEvent(session.ChannelID(), eventType, event)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadGoalState(session); err != nil {
		t.Fatal(err)
	}
}

func runGoalCommand(t *testing.T, session *agent.AgentSession, command Command, args string) Result {
	t.Helper()
	result, err := command.Run(context.Background(), session, args)
	if err != nil {
		t.Fatalf("%s %q: %v", command.Name(), args, err)
	}
	return result
}

func toolNames(defs []llm.ToolDefinition) string {
	var names []string
	for _, def := range defs {
		names = append(names, def.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestGoalPlannerCreatesFullPlanBeforeApproval(t *testing.T) {
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	result := runGoalCommand(t, session, GoalCommand{}, "verify report")
	if result.ExitCode != 3 {
		t.Fatalf("goal intake = %+v", result)
	}
	if provider.workerCalls != 0 || provider.planCalls != 1 || len(provider.calls) != 1 {
		t.Fatalf("work started before approval: %v", provider.calls)
	}
	for _, def := range provider.toolSets["planner"] {
		if def.Name == "Write" || def.Name == "Edit" {
			t.Fatalf("planner has file mutation tool %s", def.Name)
		}
	}
	// The planner does its own discovery from a workspace scan.
	if !strings.Contains(provider.planPrompts[0], "Workspace scan: {") || !strings.Contains(provider.planPrompts[0], `"available_binaries"`) {
		t.Fatalf("first plan lacks the workspace scan:\n%s", provider.planPrompts[0])
	}
	// Approval must show the exact commands the harness will run.
	for _, want := range []string{"1 tasks, 1 evidence checks, 1 goal criteria", "**E1** `shell: true`", "check: `shell: true # task 1`", "### Approach"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("approval message missing %q:\n%s", want, result.Message)
		}
	}
	state := mustLoadGoal(t, session)
	if len(state.Evidence.Criteria) != 1 || len(state.Evidence.Items) != 1 || len(state.Tasks) != 1 || state.Tasks[0].Kind != goal.KindWork || state.Tasks[0].ID != state.ID+"-T1" || state.PlanSummary == "" {
		t.Fatalf("plan before approval = %+v", state)
	}
	markdown, err := os.ReadFile(goalStateFile(session))
	if err != nil || !strings.Contains(string(markdown), "# Plan v1\n"+state.PlanSummary+"\n") {
		t.Fatalf("state.md plan = %q, %v", markdown, err)
	}
}

func TestGoalPlanningValidatesChecksWithoutRunningThem(t *testing.T) {
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	var executed []string
	session.ToolRegistry().RunSandboxedOptions = func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		executed = append(executed, command)
		return defaultGoalRunner(command)
	}
	runGoalCommand(t, session, GoalCommand{}, "verify report")
	probes := 0
	for _, command := range executed {
		if !isGoalProbe(command) {
			t.Fatalf("planning executed a check: %q", command)
		}
		if strings.HasPrefix(command, "bash -n -c ") {
			probes++
		}
	}
	if probes != 2 {
		t.Fatalf("syntax probes = %d, want one per distinct check: %q", probes, executed)
	}
}

func TestGoalApprovalRunsToCompletionAfterJudgeGap(t *testing.T) {
	provider := &goalMockProvider{judge: func(call int, _ string) string {
		if call == 1 {
			return `{"done":false,"reason":"missing second task","gaps":["the export omits totals"]}`
		}
		return `{"done":true,"reason":"goal verified","summary":"Report verified."}`
	}}
	baseDir, workspace := t.TempDir(), t.TempDir()
	session := newGoalTestSession(t, baseDir, workspace, provider)
	defer closeGoalTestSession(t, session)
	started, completed := map[string]string{}, map[string]bool{}
	var published []string
	if err := session.OnContextEvent(eventing.LLMAgentResponseCreated, func(event *types.Event) error {
		var payload eventing.LLMAgentResponse
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return err
		}
		published = append(published, payload.Message.Content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{eventing.SubagentInvocationStarted, eventing.SubagentInvocationCompleted} {
		if err := session.OnAuditEvent(eventType, func(event *types.Event) error {
			var payload eventing.SubagentInvocation
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				return err
			}
			if event.EventType == eventing.SubagentInvocationStarted {
				started[payload.ChildSessionID] = payload.Name
			} else {
				if payload.Stats == nil {
					t.Errorf("goal child %s completed without stats", payload.Name)
				}
				completed[payload.ChildSessionID] = true
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	result := runGoalCommand(t, session, GoalCommand{}, "export report")
	if result.ExitCode != 3 || result.MessagePublished || len(published) != 0 || !strings.Contains(result.Message, "Session: goal-session (test)") {
		t.Fatalf("intake = %+v, published %q", result, published)
	}
	result = runGoalCommand(t, session, GoalApproveCommand{}, "")
	if !strings.Contains(result.Message, "Goal complete") || !result.MessagePublished || len(published) != 1 || published[0] != result.Message {
		t.Fatalf("result = %+v, published %q", result, published)
	}
	for _, want := range []string{"Goal: export report", "Tasks verified: 2/2", "Goal checks passed: 1/1", "Spent:", "Details:"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("final message missing %q: %q", want, result.Message)
		}
	}
	state := mustLoadGoal(t, session)
	if state.Replans != 1 || state.WorkerAttempts != 2 || len(state.Tasks) != 2 || state.Tasks[0].Status != "done" || state.Tasks[1].Status != "done" || len(state.Failures) != 0 {
		t.Fatalf("final state = %+v", state)
	}
	if !strings.Contains(provider.planPrompts[1], `"source": "judge"`) || !strings.Contains(provider.planPrompts[1], "the export omits totals") {
		t.Fatalf("replan prompt lacks the structured judge gap:\n%s", provider.planPrompts[1])
	}
	if strings.Contains(provider.planPrompts[1], "Workspace scan: {") {
		t.Fatalf("replan repeated the workspace scan")
	}
	if published, err := session.RecordCommandOutcome(state.ID, result.Message); err != nil || published {
		t.Fatalf("duplicate outcome = %v, %v", published, err)
	}
	roles := map[string]bool{}
	for childID, name := range started {
		roles[name] = true
		if !completed[childID] {
			t.Errorf("goal child %s has no completion event", name)
		}
	}
	if len(roles) != 4 || !roles["goal-planner"] || !roles["goal-worker"] || !roles["goal-reviewer"] || !roles["goal-judge"] {
		t.Errorf("goal roles = %v, want planner, worker, reviewer and judge", roles)
	}
	if got := toolNames(provider.toolSets["reviewer"]); got != "Find,Grep,Ls,Read" {
		t.Fatalf("reviewer tools = %s", got)
	}
	if len(provider.toolSets["judge"]) != 0 {
		t.Fatalf("judge has tools: %s", toolNames(provider.toolSets["judge"]))
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newGoalTestSession(t, baseDir, workspace, provider)
	defer closeGoalTestSession(t, reopened)
	if _, err := reopened.Call(context.Background(), "What did /goal finish?"); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, message := range provider.followUpMessages {
		if message.Content == result.Message {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("goal handoff count in follow-up = %d", found)
	}
}

func TestGoalLifecycleEventsAreConciseAndOrdered(t *testing.T) {
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), &goalMockProvider{})
	defer closeGoalTestSession(t, session)
	runGoalCommand(t, session, GoalCommand{}, "verify PR 43")
	events, err := session.AuditEvents()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, event := range events {
		if eventing.IsGoalEvent(event.EventType) {
			got = append(got, event.EventType)
			if strings.Contains(event.Payload, "available_binaries") || len(event.Payload) > 16000 {
				t.Fatalf("goal event contains raw or oversized data: %s", event.Payload)
			}
		}
	}
	want := []string{eventing.GoalCreated, eventing.GoalPlanCreated}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("goal events = %v, want %v", got, want)
	}
}

func TestGoalStateIsRebuiltFromEventsOnly(t *testing.T) {
	baseDir, workspace := t.TempDir(), t.TempDir()
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, baseDir, workspace, provider)
	runGoalCommand(t, session, GoalCommand{}, "verify report")
	before := mustLoadGoal(t, session)
	dir := session.SessionDir()
	if _, err := os.Stat(filepath.Join(dir, "goal.json")); !os.IsNotExist(err) {
		t.Fatalf("goal.json should not exist: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	// state.md is a view: removing it loses nothing.
	if err := os.Remove(filepath.Join(dir, "state.md")); err != nil {
		t.Fatal(err)
	}
	reopened := newGoalTestSession(t, baseDir, workspace, provider)
	defer closeGoalTestSession(t, reopened)
	after := mustLoadGoal(t, reopened)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("replayed state differs:\nbefore %+v\nafter  %+v", before, after)
	}
	runGoalCommand(t, reopened, GoalApproveCommand{}, "")
	markdown, err := os.ReadFile(filepath.Join(dir, "state.md"))
	if err != nil || !strings.Contains(string(markdown), "stage: done") || !strings.Contains(string(markdown), "# Review") {
		t.Fatalf("state.md was not rebuilt: %q, %v", markdown, err)
	}
}

func TestGoalLegacyEventsAskForNewGoal(t *testing.T) {
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), &goalMockProvider{})
	defer closeGoalTestSession(t, session)
	payload := eventing.GoalEvent{GoalID: "goal-legacy", Goal: "verify", Revision: 1, Stage: "created", Status: "planning"}
	if err := session.AppendAuditEvent(eventing.NewGoalEvent(session.ChannelID(), eventing.GoalCreated, payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := (GoalResumeCommand{}).Run(context.Background(), session, ""); !errors.Is(err, goal.ErrLegacy) {
		t.Fatalf("resume legacy goal error = %v", err)
	}
	if result := runGoalCommand(t, session, GoalCommand{}, "verify again"); result.ExitCode != 3 {
		t.Fatalf("new goal after legacy = %+v", result)
	}
}

func TestGoalIncognitoSessionKeepsGoalInMemory(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeGoalReadme(t, workspace)
	registry := &tools.Registry{Logger: log.New(), WorkspaceDir: workspace, RunSandboxedOptions: func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		return defaultGoalRunner(command)
	}}
	session, err := agent.NewAgentSession("test", "incognito", agent.SessionParams{
		WorkspaceDir: workspace, Logger: log.New(), Provider: &goalMockProvider{}, ToolRegistry: registry, InMemorySessionStore: true,
		SystemPrompt: "test", ContextWindow: 128000, ReservedTokens: 16000, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGoalTestSession(t, session)
	runGoalCommand(t, session, GoalCommand{}, "verify report")
	if result := runGoalCommand(t, session, GoalApproveCommand{}, ""); !strings.Contains(result.Message, "Goal complete") {
		t.Fatalf("incognito goal = %q", result.Message)
	}
}

func questionUntilAnswered(call int, prompt string) string {
	if !strings.Contains(prompt, "User answer: filtered rows") {
		return `{"needs_human":true,"question":"Which rows should be exported?","plan_summary":"","ops":[]}`
	}
	return defaultGoalPlan(call)
}

func TestGoalQuestionSurvivesSessionResume(t *testing.T) {
	provider := &goalMockProvider{plan: questionUntilAnswered}
	baseDir, workspace := t.TempDir(), t.TempDir()
	session := newGoalTestSession(t, baseDir, workspace, provider)
	if result := runGoalCommand(t, session, GoalCommand{}, "export rows"); result.ExitCode != 4 {
		t.Fatalf("pause = %+v", result)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	resumed := newGoalTestSession(t, baseDir, workspace, provider)
	defer closeGoalTestSession(t, resumed)
	if status := runGoalCommand(t, resumed, GoalStatusCommand{}, ""); !strings.Contains(status.Message, "Which rows should be exported?") {
		t.Fatalf("status = %q", status.Message)
	}
	if result := runGoalCommand(t, resumed, GoalAnswerCommand{}, "filtered rows"); result.ExitCode != 3 || !strings.Contains(result.Message, "/goal:approve") {
		t.Fatalf("answer should show plan approval = %+v", result)
	}
	if result := runGoalCommand(t, resumed, GoalApproveCommand{}, ""); !strings.Contains(result.Message, "Goal complete") {
		t.Fatalf("approve result = %+v", result)
	}
}

func TestGoalYesResumesAutomaticallyAfterPlanningQuestion(t *testing.T) {
	provider := &goalMockProvider{plan: questionUntilAnswered}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	if result := runGoalCommand(t, session, GoalCommand{}, "--yes export rows"); result.ExitCode != 4 {
		t.Fatalf("planning question = %+v", result)
	}
	if result := runGoalCommand(t, session, GoalAnswerCommand{}, "filtered rows"); result.ExitCode != 0 || provider.workerCalls != 1 {
		t.Fatalf("automatic continuation = %+v, calls = %v", result, provider.calls)
	}
}

func TestGoalEditRequiresSecondApproval(t *testing.T) {
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	runGoalCommand(t, session, GoalCommand{}, "export rows")
	result := runGoalCommand(t, session, GoalApproveCommand{}, `--edit "split the export"`)
	if result.ExitCode != 3 || provider.planCalls != 2 || provider.workerCalls != 0 || !strings.Contains(provider.planPrompts[1], "user requested changes: split the export") {
		t.Fatalf("edit = %+v, calls %v", result, provider.calls)
	}
	// The edit patch adds a task; the first proposal's task stays.
	if state := mustLoadGoal(t, session); len(state.ActiveTaskIDs()) != 2 || state.PlanVersion != 2 {
		t.Fatalf("edited plan = %+v", state.Tasks)
	}
}

func TestGoalResumeInterruptedWorker(t *testing.T) {
	provider := &goalMockProvider{}
	baseDir, workspace := t.TempDir(), t.TempDir()
	session := newGoalTestSession(t, baseDir, workspace, provider)
	runGoalCommand(t, session, GoalCommand{}, "export rows")
	// Record a run that died while its worker was running.
	oldStart := time.Now().Add(-24 * time.Hour)
	appendGoalTestEvent(t, session, eventing.GoalApproved, oldStart, nil)
	appendGoalTestEvent(t, session, eventing.GoalRunStarted, oldStart, goal.RunStartedData{})
	state := mustLoadGoal(t, session)
	appendGoalTestEvent(t, session, eventing.GoalTaskStarted, oldStart, goal.TaskStartedData{TaskID: state.Tasks[0].ID})
	if err := projectGoalTasks(session, mustLoadGoal(t, session)); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	resumed := newGoalTestSession(t, baseDir, workspace, provider)
	defer closeGoalTestSession(t, resumed)
	if status := runGoalCommand(t, resumed, GoalStatusCommand{}, ""); !strings.Contains(status.Message, "/goal:resume") {
		t.Fatalf("status = %+v", status)
	}
	result := runGoalCommand(t, resumed, GoalResumeCommand{}, "")
	if result.ExitCode != 0 || !strings.Contains(result.Message, "Goal complete") {
		t.Fatalf("resume = %+v", result)
	}
	state = mustLoadGoal(t, resumed)
	if state.WorkerAttempts != 2 || state.Tasks[0].Status != "done" || state.RunningSince != nil || state.ActiveTime >= time.Minute {
		t.Fatalf("resumed state = %+v", state)
	}
}

func TestGoalBlockedWorkerPausesAndRerunsWithAnswer(t *testing.T) {
	provider := &goalMockProvider{worker: func(call int, prompt string) string {
		if call == 1 {
			return goalHandoff(prompt, "blocked", "Which rows should be exported?")
		}
		return goalHandoff(prompt, "ok", "exported filtered rows")
	}}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	result := runGoalCommand(t, session, GoalCommand{}, "--yes export rows")
	if result.ExitCode != 4 || !strings.Contains(result.Message, "is blocked: Which rows should be exported?") || !strings.Contains(result.Message, "/goal:answer") {
		t.Fatalf("pause = %+v", result)
	}
	state := mustLoadGoal(t, session)
	if state.Stage != goal.AwaitingInput || state.Tasks[0].Status != "pending" || state.Replans != 0 || provider.planCalls != 1 {
		t.Fatalf("blocked state = %+v, plan calls %d", state, provider.planCalls)
	}
	result = runGoalCommand(t, session, GoalAnswerCommand{}, "filtered rows")
	if result.ExitCode != 0 || !strings.Contains(result.Message, "Goal complete") {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(provider.workerPrompts[1], "User answer: filtered rows") || provider.planCalls != 1 {
		t.Fatalf("rerun did not carry the answer:\n%s", provider.workerPrompts[1])
	}
}

func TestGoalFailedTaskCheckRetriesWorkerWithCheckOutput(t *testing.T) {
	provider := &goalMockProvider{plan: func(int, string) string {
		return `{"plan_summary":"Fix totals, then verify.","goal_criteria":["totals match"],"evidence":[{"id":"E1","check":{"shell":"true"},"covers":"totals","status":"valid"}],"ops":[{"op":"add","ref":"fix","title":"fix totals","next_action":"fix","validation":["totals match"],"check":{"argv":["make","test"]}}]}`
	}}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	checks := 0
	setGoalRunner(session, func(command string) (string, string, error) {
		if command == "'make' 'test'" {
			checks++
			if checks == 1 {
				// make exits 2 when a recipe fails; that is a failed check, not a broken one.
				return "", "--- FAIL: TestTotals\n    expected 3, got 2\nmake: *** [test] Error 1", fmt.Errorf("exit status 2")
			}
			return "ok", "", nil
		}
		return defaultGoalRunner(command)
	})
	result := runGoalCommand(t, session, GoalCommand{}, "--yes fix totals")
	if result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	state := mustLoadGoal(t, session)
	task := state.Tasks[0]
	if provider.workerCalls != 2 || task.Attempts != 2 || task.Status != "done" || len(task.CheckHistory) != 2 || task.CheckHistory[0].Passed || state.Replans != 0 {
		t.Fatalf("retried task = %+v", task)
	}
	if !strings.Contains(provider.workerPrompts[1], "expected 3, got 2") || !strings.Contains(provider.workerPrompts[1], "exit 2") {
		t.Fatalf("retry worker lacks the failing check output:\n%s", provider.workerPrompts[1])
	}
}

func TestGoalTaskFailingTheSameWayEscalates(t *testing.T) {
	provider := &goalMockProvider{plan: func(call int, _ string) string {
		if call == 1 {
			return `{"plan_summary":"Fix totals.","goal_criteria":["totals match"],"evidence":[{"id":"E1","check":{"shell":"true"},"covers":"totals","status":"valid"}],"ops":[{"op":"add","ref":"fix","title":"fix totals","next_action":"fix","validation":["totals match"],"check":{"argv":["make","test"]}}]}`
		}
		return `{"plan_summary":"Try a smaller step.","ops":[{"op":"revise","id":"T1","next_action":"fix the rounding only"}]}`
	}}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	checks := 0
	setGoalRunner(session, func(command string) (string, string, error) {
		if command == "'make' 'test'" {
			if checks++; checks <= 2 {
				return "", fmt.Sprintf("FAIL totals (%d.0%ds)", checks, checks), fmt.Errorf("exit status 1")
			}
			return "ok", "", nil
		}
		return defaultGoalRunner(command)
	})
	result := runGoalCommand(t, session, GoalCommand{}, "--yes fix totals")
	state := mustLoadGoal(t, session)
	// Timings differ, the failure does not: the second attempt fails the task,
	// and the replan revises it instead of retrying blindly.
	if result.ExitCode != 0 || provider.planCalls != 2 || provider.workerCalls != 3 || state.Tasks[0].NextAction != "fix the rounding only" || state.Tasks[0].Attempts != 3 {
		t.Fatalf("result = %+v, calls %v, task %+v", result, provider.calls, state.Tasks[0])
	}
	if !strings.Contains(provider.planPrompts[1], `"attempts": 2`) && !strings.Contains(provider.planPrompts[1], `"attempts":2`) {
		t.Fatalf("replan does not show attempts:\n%s", provider.planPrompts[1])
	}
}

func TestGoalEnvBlockedVerificationPausesInsteadOfReplanning(t *testing.T) {
	provider := &goalMockProvider{plan: func(call int, _ string) string {
		return fmt.Sprintf(`{"plan_summary":"Implement, then run the suite.","goal_criteria":["suite passes"],"evidence":[{"id":"E1","check":{"argv":["go","test","./..."]},"covers":"suite","status":"valid"}],"ops":[{"op":"add","ref":"impl","title":"implement %d","next_action":"implement","validation":["done"],"check":{"shell":"true # %d"}}]}`, call, call)
	}}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	blocked := true
	setGoalRunner(session, func(command string) (string, string, error) {
		if command == "'go' 'test' './...'" && blocked {
			return "", "panic: httptest: failed to listen on a port: listen tcp6 [::1]:0: bind: operation not permitted", fmt.Errorf("exit status 1")
		}
		if command == "'go' 'test' './...'" {
			return "ok", "", nil
		}
		return defaultGoalRunner(command)
	})
	result := runGoalCommand(t, session, GoalCommand{}, "--yes add feature")
	if result.ExitCode != 4 || !strings.Contains(result.Message, "blocked by the environment") || !strings.Contains(result.Message, "sandbox denied socket") {
		t.Fatalf("env block = %+v", result)
	}
	state := mustLoadGoal(t, session)
	if provider.planCalls != 1 || state.Replans != 0 || state.PausedFrom != goal.Verifying || state.Tasks[0].Status != "done" {
		t.Fatalf("env block replanned or lost work: plan calls %d, state %+v", provider.planCalls, state)
	}
	blocked = false
	result = runGoalCommand(t, session, GoalAnswerCommand{}, "allowed loopback listeners")
	if result.ExitCode != 0 || provider.workerCalls != 1 || provider.planCalls != 1 {
		t.Fatalf("resume after access = %+v, calls %v", result, provider.calls)
	}
}

func TestGoalNetworkDenialOfTaskCheckRerunsOnlyTheCheck(t *testing.T) {
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	blocked := true
	setGoalRunner(session, func(command string) (string, string, error) {
		if command == "true # task 1" && blocked {
			return "", "dial tcp: connection refused", &sandbox.NetworkBlockedError{Target: "proxy.golang.org:443"}
		}
		return defaultGoalRunner(command)
	})
	if result := runGoalCommand(t, session, GoalCommand{}, "--yes add feature"); result.ExitCode != 4 || !strings.Contains(result.Message, "was blocked by the environment") || !strings.Contains(result.Message, "sandbox denied network proxy.golang.org:443") {
		t.Fatalf("env block = %+v", result)
	}
	if state := mustLoadGoal(t, session); state.Tasks[0].Status != "attempted" || state.Tasks[0].Attempts != 1 {
		t.Fatalf("blocked task = %+v", state.Tasks[0])
	}
	blocked = false
	if result := runGoalCommand(t, session, GoalAnswerCommand{}, "network allowed"); result.ExitCode != 0 || provider.workerCalls != 1 {
		t.Fatalf("resume = %+v, worker calls %d", result, provider.workerCalls)
	}
}

func TestGoalStopsWhenTheSameFailureRepeats(t *testing.T) {
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	setGoalRunner(session, func(command string) (string, string, error) {
		if command == "true" {
			return "", "totals mismatch: expected 3, got 2", fmt.Errorf("exit status 1")
		}
		return defaultGoalRunner(command)
	})
	result := runGoalCommand(t, session, GoalCommand{}, "--yes fix totals")
	state := mustLoadGoal(t, session)
	// Only evidence fails, and again after a replan: work cannot change it, so
	// the user decides whether the evidence is wrong.
	if result.ExitCode != 4 || state.Stage != goal.AwaitingInput || !strings.Contains(state.Question, "/goal:evidence --edit") || !strings.Contains(state.Question, "totals mismatch") || provider.planCalls != 2 || provider.reviewCalls != 0 || provider.judgeCalls != 0 {
		t.Fatalf("result = %+v, state = %+v, plan calls %d", result, state, provider.planCalls)
	}
	if !strings.Contains(provider.planPrompts[1], "totals mismatch") {
		t.Fatalf("replan lacks the failure output:\n%s", provider.planPrompts[1])
	}
	runGoalCommand(t, session, GoalStopCommand{}, "")
	if message := goalStatusMessage(mustLoadGoal(t, session), ""); !strings.Contains(message, "Next: /goal:evidence --edit") {
		t.Fatalf("status after evidence-only failures:\n%s", message)
	}
	// Retry replans with evidence locked and waits for approval.
	retry := runGoalCommand(t, session, GoalRetryCommand{}, "")
	state = mustLoadGoal(t, session)
	if retry.ExitCode != 3 || state.Stage != goal.AwaitingApproval || state.Evidence.Version != 1 || len(state.Evidence.Items) != 1 || state.NoProgressPasses != 0 {
		t.Fatalf("retry = %+v, state = %+v", retry, state)
	}
	if want := fmt.Sprintf("### Changes from plan v%d to v%d", state.PlanVersion-1, state.PlanVersion); !strings.HasPrefix(retry.Message, want) {
		t.Fatalf("retry message does not open with the plan diff %q:\n%s", want, retry.Message)
	}
}

func TestGoalPlannerCorrectsSelfGradedWorkTask(t *testing.T) {
	provider := &goalMockProvider{plan: func(call int, prompt string) string {
		if call == 1 {
			return `{"plan_summary":"Write the code and its verifier.","goal_criteria":["report verified"],"evidence":[{"id":"E1","check":{"shell":"true"},"covers":"report","status":"valid"}],"ops":[{"op":"add","ref":"build","kind":"work","title":"build report","next_action":"build","validation":["verified"],"check":{"argv":["./scripts/verify.sh"],"written_by":"self"}}]}`
		}
		return defaultGoalPlan(call)
	}}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	if result := runGoalCommand(t, session, GoalCommand{}, "build report"); result.ExitCode != 3 {
		t.Fatalf("plan = %+v", result)
	}
	if provider.planCalls != 2 || len(provider.corrections) != 1 || !strings.Contains(provider.corrections[0], "cannot write its own check") {
		t.Fatalf("corrections = %q, plan calls %d", provider.corrections, provider.planCalls)
	}
}

func TestGoalPlanValidationRules(t *testing.T) {
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), &goalMockProvider{})
	defer closeGoalTestSession(t, session)
	validator := newGoalCheckValidator(session.ToolRegistry())
	verify := goal.Check{Shell: "verify-a"}
	state := goal.State{ID: "goal-v", Evidence: goal.Evidence{Criteria: []string{"works"}, Items: []goal.EvidenceItem{{ID: "E1", Check: goal.Check{Shell: "true"}}}}, Tasks: []goal.Task{{ID: "goal-v-T1", Kind: goal.KindWork, Status: "done", Check: verify}}}
	add := func(ref, kind string, check goal.Check) goal.PlanOp {
		return goal.PlanOp{Op: goal.OpAdd, Ref: ref, Kind: kind, Title: "t " + ref, NextAction: "do", Validation: []string{"ok"}, Check: &check}
	}
	plan := func(ops ...goal.PlanOp) goal.Proposal {
		return goal.Proposal{PlanSummary: "summary", Ops: ops}
	}
	tests := []struct {
		name string
		plan goal.Proposal
		mode goal.EvidenceMode
		want string
	}{
		{"redo of verified work", plan(add("a", goal.KindWork, verify)), goal.EvidenceLocked, "reuses the check"},
		{"empty plan", plan(), goal.EvidenceLocked, "no unfinished tasks"},
		{"missing summary", goal.Proposal{Ops: []goal.PlanOp{add("a", goal.KindWork, goal.Check{Shell: "true"})}}, goal.EvidenceLocked, "plan_summary"},
		{"revise done task", plan(goal.PlanOp{Op: goal.OpRevise, ID: "T1", Title: "again"}), goal.EvidenceLocked, "done and superseded tasks cannot change"},
		{"work task written by work task", plan(add("a", goal.KindWork, goal.Check{Shell: "true"}), add("b", goal.KindWork, goal.Check{Argv: []string{"./x.sh"}, WrittenBy: "a"})), goal.EvidenceLocked, "not a check task"},
		{"check task cycle", plan(add("a", goal.KindCheck, goal.Check{Argv: []string{"./x.sh"}, WrittenBy: "b"}), add("b", goal.KindCheck, goal.Check{Argv: []string{"./y.sh"}, WrittenBy: "a"})), goal.EvidenceLocked, "cycle"},
		{"unknown kind", plan(add("a", "docs", goal.Check{Shell: "true"})), goal.EvidenceLocked, "use work or check"},
		{"empty check", plan(add("a", goal.KindWork, goal.Check{})), goal.EvidenceLocked, "needs argv"},
		{"multiword executable", plan(add("a", goal.KindWork, goal.Check{Argv: []string{"go test"}})), goal.EvidenceLocked, "one executable"},
		{"missing evidence without writer", goal.Proposal{PlanSummary: "s", Ops: []goal.PlanOp{add("a", goal.KindWork, goal.Check{Shell: "true"})}, Evidence: []goal.EvidenceItem{{ID: "E2", Check: goal.Check{Argv: []string{"./v.sh"}}, Status: "missing"}}}, goal.EvidenceRevision, "set written_by"},
		{"evidence written by work task", goal.Proposal{PlanSummary: "s", Ops: []goal.PlanOp{add("a", goal.KindWork, goal.Check{Shell: "true"})}, Evidence: []goal.EvidenceItem{{ID: "E2", Check: goal.Check{Argv: []string{"./v.sh"}, WrittenBy: "a"}, Status: "missing"}}}, goal.EvidenceRevision, "not a check task"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := goal.ValidatePlan(context.Background(), validator, state, test.plan, test.mode)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidatePlan() = %v, want %q", err, test.want)
			}
		})
	}
	// A verified check may be reused once the failure report shows it failing.
	failing := state
	failing.Failures = []goal.Failure{{Source: "task goal-v-T2", Key: verify.Key(), Status: "failed"}}
	if err := goal.ValidatePlan(context.Background(), validator, failing, plan(add("a", goal.KindWork, verify)), goal.EvidenceLocked); err != nil {
		t.Fatalf("failing check reuse rejected: %v", err)
	}
	// A review or judge gap fails no check, so its fix may reuse a passing one.
	reviewed := state
	reviewed.Failures = []goal.Failure{{Source: "review", Check: "main.go:10", Status: "blocker", Reason: "wrong key"}}
	if err := goal.ValidatePlan(context.Background(), validator, reviewed, plan(add("a", goal.KindWork, verify)), goal.EvidenceLocked); err != nil {
		t.Fatalf("review gap fix with a passing check rejected: %v", err)
	}
	// A work task graded by a check task's script gets that dependency.
	valid := plan(add("tests", goal.KindCheck, goal.Check{Argv: []string{"test", "-e", "x.sh"}}), add("impl", goal.KindWork, goal.Check{Argv: []string{"./x.sh"}, WrittenBy: "tests"}))
	if err := goal.ValidatePlan(context.Background(), validator, state, valid, goal.EvidenceLocked); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

func TestGoalCheckTaskEvidenceIsProtectedAndTamperingPauses(t *testing.T) {
	workspace := t.TempDir()
	script := filepath.Join(workspace, "scripts", "verify.sh")
	provider := &goalMockProvider{
		plan: func(int, string) string {
			return `{"plan_summary":"Write the verifier first, then build the report it checks.","goal_criteria":["report verified"],"evidence":[{"id":"E1","check":{"argv":["./scripts/verify.sh"],"written_by":"verifier"},"covers":"report totals","status":"missing"}],"ops":[{"op":"add","ref":"verifier","kind":"check","title":"write verifier","next_action":"write scripts/verify.sh","validation":["verifier exists"],"check":{"argv":["test","-e","scripts/verify.sh"]}},{"op":"add","ref":"report","kind":"work","title":"build report","next_action":"build","validation":["report verified"],"check":{"argv":["./scripts/verify.sh"],"written_by":"verifier"}}]}`
		},
		worker: func(_ int, prompt string) string {
			if strings.Contains(prompt, "Task: write verifier") {
				if !strings.Contains(prompt, "This is a check task") || !strings.Contains(prompt, "Read-only files: none") {
					return goalHandoff(prompt, "blocked", "check task was restricted")
				}
				_ = os.MkdirAll(filepath.Dir(script), 0700)
				_ = os.WriteFile(script, []byte("#!/bin/sh\ntest -f report.csv\n"), 0700)
			} else {
				if !strings.Contains(prompt, "Read-only files: scripts/verify.sh") {
					return goalHandoff(prompt, "blocked", "evidence was not protected")
				}
				// The mock writes past the sandbox; the hash check is the backstop.
				_ = os.WriteFile(script, []byte("#!/bin/sh\n# weakened\nexit 0\n"), 0700)
			}
			return goalHandoff(prompt, "ok", "done")
		},
	}
	session := newGoalTestSession(t, t.TempDir(), workspace, provider)
	defer closeGoalTestSession(t, session)
	setGoalRunner(session, func(command string) (string, string, error) {
		if command == "'test' '-e' 'scripts/verify.sh'" || command == "'./scripts/verify.sh'" {
			return "ok", "", nil
		}
		return defaultGoalRunner(command)
	})
	session.ToolRegistry().RunSandboxedOptions = wrapGitDiff(session.ToolRegistry().RunSandboxedOptions)
	result := runGoalCommand(t, session, GoalCommand{}, "--yes build report")
	if result.ExitCode != 4 || !strings.Contains(result.Message, "Evidence files changed") || !strings.Contains(result.Message, "E1 scripts/verify.sh") {
		t.Fatalf("tampering was not caught = %+v", result)
	}
	state := mustLoadGoal(t, session)
	if !goal.Contains(state.Tasks[1].DependsOn, state.Tasks[0].ID) || state.Evidence.Items[0].Check.WrittenBy != state.Tasks[0].ID || state.Evidence.Items[0].Files["scripts/verify.sh"] == "" {
		t.Fatalf("check task wiring = %+v", state)
	}
	result = runGoalCommand(t, session, GoalAnswerCommand{}, "reviewed; accept")
	if result.ExitCode != 0 || provider.judgeCalls != 1 {
		t.Fatalf("accepted evidence = %+v", result)
	}
	prompt := provider.judgePrompts[0]
	for _, want := range []string{"--- scripts/verify.sh (E1)", "# weakened", "Changed files:\n- new scripts/verify.sh", "Review report:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("judge prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestGoalWorkerSandboxProtectsTestFiles(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"go.mod": "module x\n", "x.go": "package x\n", "x_test.go": "package x\n", "testdata/in.txt": "1\n", "vendor/y/y_test.go": "package y\n"} {
		full := filepath.Join(workspace, path)
		_ = os.MkdirAll(filepath.Dir(full), 0700)
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := goal.Check{Argv: []string{"go", "test", "./..."}}
	if got := goalEvidenceFiles(workspace, check); !reflect.DeepEqual(got, []string{"testdata/in.txt", "x_test.go"}) {
		t.Fatalf("go test evidence files = %v", got)
	}
	state := goal.State{Evidence: goal.Evidence{Items: []goal.EvidenceItem{{ID: "E1", Check: check, Files: goal.Freeze(goalExecFiles(workspace), goal.State{Evidence: goal.Evidence{Items: []goal.EvidenceItem{{ID: "E1", Check: check}}}}, false)["E1"]}}}}
	work := goal.Task{ID: "T1", Kind: goal.KindWork, Check: goal.Check{Shell: "true"}}
	protected := goalWorkerProtected(workspace, state, work)
	if !reflect.DeepEqual(protected, []string{"testdata/in.txt", "x_test.go"}) || goalWorkerProtected(workspace, state, goal.Task{Kind: goal.KindCheck}) != nil {
		t.Fatalf("protected = %v", protected)
	}
	registry := (&tools.Registry{Logger: log.New(), WorkspaceDir: workspace}).WithProtectedPaths(protected...)
	call := llm.ToolCall{ID: "w", Name: "Write", Arguments: mustJSON(map[string]any{"file_path": filepath.Join(workspace, "x_test.go"), "content": "package x // weakened\n"})}
	message, err := registry.HandleToolCallContext(context.Background(), call)
	data, _ := os.ReadFile(filepath.Join(workspace, "x_test.go"))
	if string(data) != "package x\n" {
		t.Fatalf("protected test file was written: %q (%v, %v)", data, message.Content, err)
	}
	call.Arguments = mustJSON(map[string]any{"file_path": filepath.Join(workspace, "x.go"), "content": "package x // fixed\n"})
	if _, err := registry.HandleToolCallContext(context.Background(), call); err != nil {
		t.Fatalf("product file write refused: %v", err)
	}
}

// goalExecFiles hashes workspace files the way the goal store does.
type goalExecFiles string

func (w goalExecFiles) EvidenceFiles(check goal.Check) []string {
	return goalEvidenceFiles(string(w), check)
}
func (w goalExecFiles) FileHash(rel string) string { return goalFileHash(string(w), rel) }

func wrapGitDiff(next func(context.Context, string, string, sandbox.RunOptions) (string, string, error)) func(context.Context, string, string, sandbox.RunOptions) (string, string, error) {
	return func(ctx context.Context, cwd, command string, opts sandbox.RunOptions) (string, string, error) {
		if command == "git ls-files --others --exclude-standard" {
			return "scripts/verify.sh\n", "", nil
		}
		return next(ctx, cwd, command, opts)
	}
}

func TestGoalInvalidWorkerHandoffStillRunsCheck(t *testing.T) {
	provider := &goalMockProvider{worker: func(int, string) string { return "I finished, trust me" }}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	result := runGoalCommand(t, session, GoalCommand{}, "--yes export rows")
	state := mustLoadGoal(t, session)
	if result.ExitCode != 0 || state.Tasks[0].Status != "done" || state.Tasks[0].LastOutcome != "worker returned no valid handoff" || state.Tasks[0].WorkerStatus != goal.WorkerContinue {
		t.Fatalf("result = %+v, task = %+v", result, state.Tasks[0])
	}
}

func TestGoalBudgetStopsBeforeAnotherWorker(t *testing.T) {
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	runGoalCommand(t, session, GoalCommand{}, "export rows")
	defer func(limits goal.Limits) { goalLimits = limits }(goalLimits)
	goalLimits.MaxActiveTime = time.Nanosecond
	result := runGoalCommand(t, session, GoalApproveCommand{}, "")
	state := mustLoadGoal(t, session)
	if result.ExitCode != 5 || provider.workerCalls != 0 || provider.judgeCalls != 0 || state.Stage != goal.OverBudget || !strings.Contains(state.ExitReason, "time budget") {
		t.Fatalf("budget = %+v, calls %v", result, provider.calls)
	}
}

func TestConfigureGoalBudgetAndModels(t *testing.T) {
	defer func(limits goal.Limits, models goal.Models) { goalLimits, goalDefaultModels = limits, models }(goalLimits, goalDefaultModels)
	tokens := int64(500)
	ConfigureGoal(config.Goal{MaxTokens: &tokens, MaxDuration: config.Duration{Duration: 5 * time.Minute, Set: true}, Planner: &config.GoalRole{Model: "a/p", Reasoning: "high"}})
	if goalLimits.MaxTokens != 500 || goalLimits.MaxActiveTime != 5*time.Minute || goalDefaultModels.Planner != (goal.RoleModel{Model: "a/p", Reasoning: "high"}) || goalDefaultModels.Worker.Model != "" {
		t.Fatalf("limits = %+v, models = %+v", goalLimits, goalDefaultModels)
	}
	ConfigureGoal(config.Goal{})
	if goalLimits != goal.DefaultLimits || !goalDefaultModels.Empty() {
		t.Fatalf("defaults = %+v, %+v", goalLimits, goalDefaultModels)
	}
}

func TestScanGoalOptionsKeepsGoalTextRaw(t *testing.T) {
	options, rest, err := scanGoalOptions(`--yes --planner-model a/p --worker-reasoning=low don't drop "quoted" text`, goalStartOptionDefs)
	if err != nil || options["yes"] != "true" || options["planner-model"] != "a/p" || options["worker-reasoning"] != "low" || rest != `don't drop "quoted" text` {
		t.Fatalf("scan = %v, %q, %v", options, rest, err)
	}
	options, rest, err = scanGoalOptions(`--worker-model a/w --edit "split the export, then test it"`, goalApproveOptionDefs)
	if err != nil || options["edit"] != "split the export, then test it" || options["worker-model"] != "a/w" || rest != "" {
		t.Fatalf("approve scan = %v, %q, %v", options, rest, err)
	}
	for _, input := range []string{"--bogus x", "--model", "--model --yes x", "--yes --yes x", "--yes=1 x"} {
		if _, _, err := scanGoalOptions(input, goalStartOptionDefs); err == nil {
			t.Errorf("scanGoalOptions(%q) accepted", input)
		}
	}
}

func TestGoalModelsFromOptions(t *testing.T) {
	current := func(*agent.AgentSession) string { return "local/current" }
	models, err := goalModelsFromOptions(map[string]string{"model": "a/all", "worker-model": "a/w", "verifier-reasoning": "high"}, nil, current)
	if err != nil || *models != (goal.Models{Planner: goal.RoleModel{Model: "a/all"}, Worker: goal.RoleModel{Model: "a/w"}, Verifier: goal.RoleModel{Model: "a/all", Reasoning: "high"}}) {
		t.Fatalf("models = %+v, %v", models, err)
	}
	models, err = goalModelsFromOptions(map[string]string{"planner-reasoning": "high"}, nil, current)
	if err != nil || models.Planner != (goal.RoleModel{Model: "local/current", Reasoning: "high"}) || models.Worker.Model != "" {
		t.Fatalf("reasoning on the current model = %+v, %v", models, err)
	}
	if _, err := goalModelsFromOptions(map[string]string{"planner-reasoning": "high"}, nil, nil); err == nil || !strings.Contains(err.Error(), "--planner-reasoning needs a model") {
		t.Fatalf("reasoning without a model = %v", err)
	}
	if models, err := goalModelsFromOptions(map[string]string{"yes": "true"}, nil, current); models != nil || err != nil {
		t.Fatalf("no model flags = %+v, %v", models, err)
	}
}

// goalRoleResolver records which model each child asked for and answers
// with the goal mock provider.
type goalRoleResolver struct {
	provider *goalMockProvider
	refs     []string
}

func (r *goalRoleResolver) ResolveSubagentModel(_ context.Context, request agent.SubagentModelRequest) (agent.SubagentModelResolution, error) {
	if strings.HasPrefix(request.Ref, "bad/") {
		return agent.SubagentModelResolution{}, fmt.Errorf("unknown model %s", request.Ref)
	}
	r.refs = append(r.refs, request.Ref+"@"+request.ReasoningEffort)
	return agent.SubagentModelResolution{Ref: request.Ref, Model: request.Ref, Provider: r.provider, ContextWindow: 128000, ReservedTokens: 16000, KeepRecentTokens: 20000, SummaryTokens: 2000}, nil
}

func TestGoalRunsEachRoleOnItsModel(t *testing.T) {
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	resolver := &goalRoleResolver{provider: provider}
	session.SetSubagentModelResolver(resolver)
	if _, err := (GoalCommand{}).Run(context.Background(), session, "--yes --planner-model bad/p export rows"); err == nil || !strings.Contains(err.Error(), "planner model bad/p") || provider.planCalls != 0 {
		t.Fatalf("unknown model = %v, plan calls %d", err, provider.planCalls)
	}
	result := runGoalCommand(t, session, GoalCommand{}, "--yes --planner-model a/p --planner-reasoning high --worker-model a/w --verifier-model a/v export rows")
	if result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	// Three validations, then one child per role call; the reviewer and the
	// judge both run on the verifier model.
	want := []string{"a/p@high", "a/v@", "a/w@", "a/p@high", "a/w@", "a/v@", "a/v@"}
	if !reflect.DeepEqual(resolver.refs, want) {
		t.Fatalf("model requests = %v, want %v", resolver.refs, want)
	}
	state := mustLoadGoal(t, session)
	if state.Models.Line() != "planner a/p (high) · worker a/w · verifier a/v" {
		t.Fatalf("models = %s", state.Models.Line())
	}
	// Retry escalates only the worker; the choice survives in the events.
	resolver.refs = nil
	retry := runGoalCommand(t, session, GoalRetryCommand{}, "--worker-model a/x")
	if !strings.Contains(retry.Message, "Models: planner a/p (high) · worker a/x · verifier a/v") {
		t.Fatalf("retry approval lacks the models:\n%s", retry.Message)
	}
	markdown, err := os.ReadFile(goalStateFile(session))
	if err != nil || !strings.Contains(string(markdown), "models: planner a/p (high) · worker a/x · verifier a/v") {
		t.Fatalf("state.md models = %q, %v", markdown, err)
	}
	if !reflect.DeepEqual(resolver.refs, []string{"a/x@", "a/p@high"}) {
		t.Fatalf("retry model requests = %v", resolver.refs)
	}
}

func TestGoalModelCompletion(t *testing.T) {
	choices := func() []config.ModelChoice {
		return []config.ModelChoice{{Ref: "local/qwen", Reasoning: "high"}, {Ref: "anthropic/opus"}}
	}
	completion, ok := (GoalCommand{Choices: choices}).Complete("--worker-model ", len("--worker-model "))
	if !ok || !strings.Contains(fmt.Sprint(completion), "local/qwen") {
		t.Fatalf("model completion = %+v, %v", completion, ok)
	}
	completion, ok = (GoalRetryCommand{Choices: choices}).Complete("--ver", 5)
	if !ok || !strings.Contains(fmt.Sprint(completion), "--verifier-model") {
		t.Fatalf("option completion = %+v, %v", completion, ok)
	}
}

func TestGoalFailureKeyIgnoresTaskIDs(t *testing.T) {
	first := []goal.Failure{{Source: "task goal-x-T1", Key: "true", Status: goal.CheckFailed, Reason: "exit 1: mismatch"}, {Source: "E1", Key: "go test", Status: goal.CheckFailed, Reason: "exit 1"}}
	second := []goal.Failure{{Source: "E1", Key: "go test", Status: goal.CheckFailed, Reason: "exit 1"}, {Source: "task goal-x-T7", Key: "true", Status: goal.CheckFailed, Reason: "exit 1: mismatch"}}
	if goal.FailureKey(first) != goal.FailureKey(second) {
		t.Fatal("failure key depends on task IDs or order")
	}
	second[0].Reason = "exit 2: other"
	if goal.FailureKey(first) == goal.FailureKey(second) {
		t.Fatal("failure key ignores a changed reason")
	}
}

func TestGoalFetchesLinkedIssueForEveryRole(t *testing.T) {
	var fetched []string
	previous := goalFetchURL
	goalFetchURL = func(_ context.Context, _ *agent.AgentSession, url string) (string, error) {
		fetched = append(fetched, url)
		switch url {
		case "https://api.github.com/repos/makkalot/caretz/issues/51":
			return `{"title":"Add CSV export","state":"open","body":"Export the filtered rows as CSV.","user":{"login":"makkalot"},"labels":[{"name":"feature"}]}`, nil
		case "https://api.github.com/repos/makkalot/caretz/issues/51/comments":
			return `[{"body":"Include a header row.","user":{"login":"reviewer"}}]`, nil
		}
		return "", fmt.Errorf("unexpected fetch %s", url)
	}
	defer func() { goalFetchURL = previous }()
	previousGH := goalRunGH
	goalRunGH = func(context.Context, *agent.AgentSession, string) (string, error) {
		return "", fmt.Errorf("gh: not logged in")
	}
	defer func() { goalRunGH = previousGH }()
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	result := runGoalCommand(t, session, GoalCommand{}, "--yes lets implement https://github.com/makkalot/caretz/issues/51.")
	if result.ExitCode != 0 || len(fetched) != 2 {
		t.Fatalf("result = %+v, fetched = %q", result, fetched)
	}
	state := mustLoadGoal(t, session)
	if len(state.References) != 1 || state.References[0].Source != "github-api" || !strings.Contains(state.References[0].Content, "Include a header row.") {
		t.Fatalf("references = %+v", state.References)
	}
	for role, prompt := range map[string]string{"planner": provider.planPrompts[0], "worker": provider.workerPrompts[0], "judge": provider.judgePrompts[0]} {
		if !strings.Contains(prompt, "# Add CSV export (#51, open, by makkalot)") || !strings.Contains(prompt, "Untrusted data") {
			t.Fatalf("%s prompt lacks the issue:\n%s", role, prompt)
		}
	}
	if !strings.Contains(toolNames(provider.toolSets["planner"]), "WebFetch") {
		t.Fatalf("planner lacks WebFetch: %s", toolNames(provider.toolSets["planner"]))
	}
}

func TestGoalQuestionWithoutPlanAwaitsAnswerNotApproval(t *testing.T) {
	provider := &goalMockProvider{plan: questionUntilAnswered}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	result := runGoalCommand(t, session, GoalCommand{}, "export rows")
	if result.ExitCode != 4 || strings.Contains(result.Message, "awaiting approval") || !strings.Contains(result.Message, "Next: /goal:answer") {
		t.Fatalf("question without a plan = %+v", result)
	}
}

func TestGoalFetchesPrivateIssueWithGH(t *testing.T) {
	previous := goalRunGH
	var command string
	goalRunGH = func(_ context.Context, _ *agent.AgentSession, cmd string) (string, error) {
		command = cmd
		return `{"title":"Sandbox env files","state":"OPEN","body":"Support --sandbox-env-file.","author":{"login":"makkalot"},"labels":[],"comments":[{"author":{"login":"bot"},"body":"Document precedence."}]}`, nil
	}
	defer func() { goalRunGH = previous }()
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), &goalMockProvider{})
	defer closeGoalTestSession(t, session)
	references := fetchGoalReferences(context.Background(), session, "implement https://github.com/makkalot/caretz/issues/51")
	if command != "gh issue view '51' --repo 'makkalot/caretz' --json title,state,body,author,labels,comments" {
		t.Fatalf("gh command = %q", command)
	}
	if len(references) != 1 || references[0].Source != "gh" || !strings.Contains(references[0].Content, "# Sandbox env files (#51, open, by makkalot)") || !strings.Contains(references[0].Content, "Document precedence.") {
		t.Fatalf("references = %+v", references)
	}
}

func TestGoalChangeSetExcludesWorkFromBeforeTheGoal(t *testing.T) {
	workspace := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(workspace, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("unchanged.go", "package a // dirty before the goal\n")
	write("touched.go", "package a // dirty before the goal\n")
	session := newGoalTestSession(t, t.TempDir(), workspace, &goalMockProvider{})
	defer closeGoalTestSession(t, session)
	untracked := ""
	session.ToolRegistry().RunSandboxedOptions = func(_ context.Context, _, command string, _ sandbox.RunOptions) (string, string, error) {
		switch command {
		case "git diff --name-only HEAD":
			return "unchanged.go\ntouched.go\n", "", nil
		case "git ls-files --others --exclude-standard":
			return untracked, "", nil
		case "git diff --name-status 'base1'":
			return "M\tunchanged.go\nM\ttouched.go\n", "", nil
		}
		return "", "", nil
	}
	state := goal.State{BaseRevision: "base1", StartDirty: captureGoalStartDirty(context.Background(), session)}
	write("touched.go", "package a // changed by the goal\n")
	write("feature.go", "package a\nfunc Feature() {}\n")
	write("feature_test.go", "package a\nfunc TestFeature() {}\n")
	write(".caretz/sessions/x.json", "{}")
	untracked = "feature.go\nfeature_test.go\n.caretz/sessions/x.json\n"
	files, err := goalChangeSet(context.Background(), session, state)
	if err != nil {
		t.Fatal(err)
	}
	want := []goalChangedFile{{Path: "feature_test.go", Kind: "new"}, {Path: "feature.go", Kind: "new"}, {Path: "touched.go", Kind: "modified", DirtyAtStart: true}}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("change set = %+v, want %+v", files, want)
	}
	rendered := renderGoalChanges(context.Background(), session, state, goalReviewDiffBudget)
	for _, want := range []string{"func TestFeature() {}", "func Feature() {}", "- modified touched.go (already modified before the goal started)", "read the file"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered changes missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "unchanged.go") || strings.Contains(rendered, ".caretz") {
		t.Fatalf("rendered changes include work from before the goal:\n%s", rendered)
	}
}

func TestValidateGoalReview(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	criteria := []string{"works"}
	valid := goal.Review{Criteria: []goal.CriterionResult{{Status: "met", Evidence: []string{"main.go:2"}}}, Findings: []goal.Finding{{Severity: "blocker", File: "main.go", Line: 2, Problem: "does nothing"}}}
	if err := valid.Validate(criteria); err != nil {
		t.Fatalf("valid review rejected: %v", err)
	}
	if err := validateReviewCitations(workspace, nil, valid); err != nil {
		t.Fatalf("valid citations rejected: %v", err)
	}
	for name, review := range map[string]goal.Review{
		"count":        {},
		"bad severity": {Criteria: []goal.CriterionResult{{Status: "unclear"}}, Findings: []goal.Finding{{Severity: "nit", File: "main.go", Problem: "x"}}},
		"bad status":   {Criteria: []goal.CriterionResult{{Status: "maybe"}}},
		"no file":      {Criteria: []goal.CriterionResult{{Status: "unclear"}}, Findings: []goal.Finding{{Severity: "minor", Problem: "x"}}},
	} {
		if err := review.Validate(criteria); err == nil {
			t.Errorf("%s: invalid review accepted", name)
		}
	}
	for name, review := range map[string]goal.Review{
		"missing file":   {Criteria: []goal.CriterionResult{{Status: "met", Evidence: []string{"nope.go:1"}}}},
		"line too large": {Criteria: []goal.CriterionResult{{Status: "met", Evidence: []string{"main.go:40"}}}},
		"finding line":   {Findings: []goal.Finding{{Severity: "minor", File: "main.go", Line: 9, Problem: "x"}}},
	} {
		if err := validateReviewCitations(workspace, nil, review); err == nil {
			t.Errorf("%s: invalid citation accepted", name)
		}
	}
}

func writeGoalMain(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return workspace
}

var goalBlocker = []map[string]any{{"severity": "blocker", "file": "main.go", "line": 2, "problem": "the command is never registered", "fix": "register it"}}

func goalBlockerReview(prompt string) string { return goalReviewJSON(prompt, goalBlocker) }

func TestGoalAcceptedReviewBlockerReplans(t *testing.T) {
	provider := &goalMockProvider{
		review: func(call int, prompt string) string {
			if call == 1 {
				return goalBlockerReview(prompt)
			}
			return goalReviewJSON(prompt, nil)
		},
		judge: func(call int, _ string) string {
			if call == 1 {
				return `{"done":false,"reason":"the review found a real gap","findings":[{"index":1,"decision":"accept"}]}`
			}
			return `{"done":true,"reason":"goal verified","summary":"Done."}`
		},
	}
	session := newGoalTestSession(t, t.TempDir(), writeGoalMain(t), provider)
	defer closeGoalTestSession(t, session)
	result := runGoalCommand(t, session, GoalCommand{}, "--yes register the command")
	if result.ExitCode != 0 || provider.reviewCalls != 2 || provider.judgeCalls != 2 {
		t.Fatalf("result = %+v, calls %v", result, provider.calls)
	}
	if !strings.Contains(provider.reviewPrompts[0], "whether the logic is right") || !strings.Contains(provider.reviewPrompts[0], "E1 | shell: true | passed") {
		t.Fatalf("reviewer prompt lacks its focus or the check results:\n%s", provider.reviewPrompts[0])
	}
	if !strings.Contains(provider.judgePrompts[0], "F1 [blocker] main.go:2: the command is never registered") || !strings.Contains(provider.judgePrompts[0], "Check results:") {
		t.Fatalf("judge did not get the review and check results:\n%s", provider.judgePrompts[0])
	}
	replan := provider.planPrompts[1]
	if !strings.Contains(replan, `"source": "review"`) || !strings.Contains(replan, `"check": "main.go:2"`) || !strings.Contains(replan, "fix: register it") {
		t.Fatalf("replan lacks the accepted finding:\n%s", replan)
	}
}

func TestGoalJudgeMustAddressBlockingFindings(t *testing.T) {
	provider := &goalMockProvider{
		review: func(_ int, prompt string) string { return goalBlockerReview(prompt) },
		judge: func(call int, _ string) string {
			if call == 1 {
				return `{"done":true,"reason":"checks pass"}`
			}
			return `{"done":true,"reason":"registration happens in the app registry","summary":"Done.","findings":[{"index":1,"decision":"dismiss","reason":"registered in internal/app instead"}]}`
		},
	}
	session := newGoalTestSession(t, t.TempDir(), writeGoalMain(t), provider)
	defer closeGoalTestSession(t, session)
	result := runGoalCommand(t, session, GoalCommand{}, "--yes register the command")
	if result.ExitCode != 0 || provider.judgeCalls != 2 || len(provider.corrections) != 1 || !strings.Contains(provider.corrections[0], "address blocker finding F1") {
		t.Fatalf("result = %+v, corrections %q", result, provider.corrections)
	}
	if !strings.Contains(result.Message, "Review findings: 1") || !strings.Contains(result.Message, "dismissed: registered in internal/app instead") {
		t.Fatalf("final message lacks the dismissed finding: %q", result.Message)
	}
	markdown, err := os.ReadFile(goalStateFile(session))
	if err != nil || !strings.Contains(string(markdown), "# Review") || !strings.Contains(string(markdown), "judge on F1: dismiss") {
		t.Fatalf("state.md review = %q, %v", markdown, err)
	}
}

func TestGoalSuspectPassIsShownToReviewer(t *testing.T) {
	provider := &goalMockProvider{}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	setGoalRunner(session, func(command string) (string, string, error) {
		if command == "true" {
			return "FAIL TestFlaky (retried)\nok", "", nil
		}
		return defaultGoalRunner(command)
	})
	result := runGoalCommand(t, session, GoalCommand{}, "--yes export rows")
	if result.ExitCode != 0 || !strings.Contains(provider.reviewPrompts[0], "passed, but the output reports a failure") || !strings.Contains(provider.judgePrompts[0], "passed, but the output reports a failure") {
		t.Fatalf("result = %+v, reviewer prompt:\n%s", result, provider.reviewPrompts[0])
	}
}

// A malformed evidence check ("go ." for "go build .") must not burn replans:
// work cannot change approved evidence. The run pauses at once, and an
// evidence revision with every task done goes straight to verification.
func TestGoalMalformedEvidencePausesAndEvidenceEditReverifies(t *testing.T) {
	provider := &goalMockProvider{plan: func(call int, _ string) string {
		if call == 1 {
			return `{"plan_summary":"Implement, then build.","goal_criteria":["binary builds"],"evidence":[{"id":"E1","check":{"argv":["go","."]},"covers":"build","status":"valid"}],"ops":[{"op":"add","ref":"impl","kind":"work","title":"implement","next_action":"implement","validation":["done"],"check":{"shell":"true # task"}}]}`
		}
		return `{"plan_summary":"Work is done; only the build evidence changes.","evidence":[{"id":"E1","check":{"argv":["go","build","."]},"covers":"build","status":"valid"}],"ops":[]}`
	}}
	session := newGoalTestSession(t, t.TempDir(), t.TempDir(), provider)
	defer closeGoalTestSession(t, session)
	setGoalRunner(session, func(command string) (string, string, error) {
		switch command {
		case "'go' '.'":
			return "", "go .: unknown command\nRun 'go help' for usage.", fmt.Errorf("exit status 2")
		case "'go' 'build' '.'":
			return "", "", nil
		}
		return defaultGoalRunner(command)
	})
	result := runGoalCommand(t, session, GoalCommand{}, "--yes build it")
	state := mustLoadGoal(t, session)
	if result.ExitCode != 4 || state.PausedFrom != goal.Verifying || provider.planCalls != 1 || state.Replans != 0 || !strings.Contains(state.Question, "E1 `go .`") || !strings.Contains(state.Question, "/goal:evidence --edit") {
		t.Fatalf("malformed evidence = %+v, plan calls %d, state %+v", result, provider.planCalls, state)
	}
	result = runGoalCommand(t, session, GoalEvidenceCommand{}, `--edit "E1 should run go build ."`)
	state = mustLoadGoal(t, session)
	if result.ExitCode != 3 || state.Stage != goal.AwaitingApproval || state.Evidence.Version != 2 || len(state.ActiveTaskIDs()) != 0 {
		t.Fatalf("evidence revision = %+v, state %+v", result, state)
	}
	result = runGoalCommand(t, session, GoalApproveCommand{}, "")
	if result.ExitCode != 0 || provider.workerCalls != 1 || provider.planCalls != 2 {
		t.Fatalf("approve after revision = %+v, worker calls %d, plan calls %d", result, provider.workerCalls, provider.planCalls)
	}
}

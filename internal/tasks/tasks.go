package tasks

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusBlocked    = "blocked"

	OpAdd    = "add"
	OpStart  = "start"
	OpDone   = "done"
	OpBlock  = "block"
	OpUpdate = "update"
	OpPrune  = "prune_expired"
)

type Task struct {
	ID          string     `json:"id"`
	Scope       string     `json:"scope,omitempty"`
	Title       string     `json:"title"`
	Validation  string     `json:"validation,omitempty"`
	Status      string     `json:"status"`
	NextAction  string     `json:"next_action,omitempty"`
	Outcome     string     `json:"outcome,omitempty"`
	UpdatedSeq  int        `json:"updated_seq"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Stats       *Stats     `json:"stats,omitempty"`
}

type Stats struct {
	PromptTokens       int64          `json:"prompt_tokens"`
	CachedPromptTokens int64          `json:"cached_prompt_tokens"`
	CompletionTokens   int64          `json:"completion_tokens"`
	TotalTokens        int64          `json:"total_tokens"`
	ToolCalls          map[string]int `json:"tool_calls,omitempty"`
}

type Update struct {
	Op         string   `json:"op"`
	ID         string   `json:"id"`
	Scope      string   `json:"scope,omitempty"`
	Title      string   `json:"title,omitempty"`
	Validation string   `json:"validation,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
	Outcome    string   `json:"outcome,omitempty"`
	IDs        []string `json:"ids,omitempty"`
}

type Snapshot struct {
	Tasks []Task `json:"tasks"`
	Seq   int    `json:"seq"`
}

type State struct {
	Tasks map[string]Task
	Seq   int
}

func NewState() *State {
	return &State{Tasks: map[string]Task{}}
}

func FromSnapshot(snapshot Snapshot) *State {
	state := NewState()
	state.Seq = snapshot.Seq
	for _, task := range snapshot.Tasks {
		if strings.TrimSpace(task.ID) == "" {
			continue
		}
		state.Tasks[task.ID] = task
		if task.UpdatedSeq > state.Seq {
			state.Seq = task.UpdatedSeq
		}
	}
	return state
}

func (s *State) Clone() *State {
	if s == nil {
		return NewState()
	}
	clone := &State{Tasks: make(map[string]Task, len(s.Tasks)), Seq: s.Seq}
	for id, task := range s.Tasks {
		clone.Tasks[id] = task
	}
	return clone
}

func (s *State) Apply(update Update) (string, error) {
	if s == nil {
		return "", fmt.Errorf("task state is not initialized")
	}
	update.Op = strings.TrimSpace(update.Op)
	update.ID = strings.TrimSpace(update.ID)
	update.Scope = strings.TrimSpace(update.Scope)
	update.Title = strings.TrimSpace(update.Title)
	update.Validation = strings.TrimSpace(update.Validation)
	update.NextAction = strings.TrimSpace(update.NextAction)
	update.Outcome = strings.TrimSpace(update.Outcome)
	update.IDs = normalizeIDs(update.IDs)

	if update.Op == "" {
		return "", fmt.Errorf("op is required")
	}
	if update.Op != OpPrune && update.ID == "" {
		return "", fmt.Errorf("id is required")
	}

	switch update.Op {
	case OpAdd:
		return s.applyAdd(update)
	case OpStart:
		return s.applyStart(update)
	case OpDone:
		return s.applyDone(update)
	case OpBlock:
		return s.applyBlock(update)
	case OpUpdate:
		return s.applyUpdate(update)
	case OpPrune:
		return s.applyPrune(update)
	default:
		return "", fmt.Errorf("invalid op %q", update.Op)
	}
}

func (s *State) applyAdd(update Update) (string, error) {
	if update.Title == "" {
		return "", fmt.Errorf("title is required for add")
	}
	if _, exists := s.Tasks[update.ID]; exists {
		return "", fmt.Errorf("task %s already exists", update.ID)
	}
	s.Seq++
	s.Tasks[update.ID] = Task{
		ID:         update.ID,
		Scope:      update.Scope,
		Title:      update.Title,
		Validation: update.Validation,
		Status:     StatusPending,
		NextAction: update.NextAction,
		UpdatedSeq: s.Seq,
	}
	return fmt.Sprintf("%s added.", update.ID), nil
}

func (s *State) applyStart(update Update) (string, error) {
	task, ok := s.Tasks[update.ID]
	if !ok {
		// Small models often start a task they never added; with a title that
		// is enough to create it rather than fail the call.
		if update.Title == "" {
			return "", fmt.Errorf(`unknown task %s; add it with {"op":"add","id":%q,"title":"..."} or include a title to create and start it in one call`, update.ID, update.ID)
		}
		task = Task{ID: update.ID, Scope: update.Scope, Title: update.Title, Status: StatusPending}
	}
	if task.Status == StatusDone {
		return "", fmt.Errorf("task %s is already done", update.ID)
	}
	if active, ok := s.inProgressTask(update.ID, task.Scope); ok {
		return "", fmt.Errorf("task %s is already in progress; finish or block it before starting %s", active.ID, update.ID)
	}
	s.Seq++
	task.Status = StatusInProgress
	task.NextAction = update.NextAction
	if task.NextAction == "" {
		task.NextAction = task.Title
	}
	if update.Validation != "" {
		task.Validation = update.Validation
	}
	task.UpdatedSeq = s.Seq
	s.Tasks[task.ID] = task
	return fmt.Sprintf("%s marked in progress.", task.ID), nil
}

func (s *State) applyDone(update Update) (string, error) {
	if update.Outcome == "" {
		return "", fmt.Errorf(`outcome is required for done, e.g. {"op":"done","id":%q,"outcome":"one-sentence result"}`, update.ID)
	}
	task, ok := s.Tasks[update.ID]
	if !ok {
		// Record finished work even when the task was never added.
		title := update.Title
		if title == "" {
			title = update.Outcome
		}
		task = Task{ID: update.ID, Scope: update.Scope, Title: title, Validation: update.Validation}
	}
	if task.Status == StatusDone {
		return "", fmt.Errorf("task %s is already done", update.ID)
	}
	s.Seq++
	task.Status = StatusDone
	task.Outcome = update.Outcome
	task.NextAction = ""
	task.UpdatedSeq = s.Seq
	s.Tasks[task.ID] = task
	return fmt.Sprintf("%s marked done.", task.ID), nil
}

func (s *State) applyBlock(update Update) (string, error) {
	task, ok := s.Tasks[update.ID]
	if !ok {
		return "", fmt.Errorf("unknown task %s", update.ID)
	}
	if task.Status == StatusDone {
		return "", fmt.Errorf("task %s is already done", update.ID)
	}
	s.Seq++
	task.Status = StatusBlocked
	if update.NextAction != "" {
		task.NextAction = update.NextAction
	}
	task.UpdatedSeq = s.Seq
	s.Tasks[task.ID] = task
	return fmt.Sprintf("%s marked blocked.", task.ID), nil
}

func (s *State) applyUpdate(update Update) (string, error) {
	if update.NextAction == "" && update.Validation == "" {
		return "", fmt.Errorf("next_action or validation is required for update")
	}
	task, ok := s.Tasks[update.ID]
	if !ok {
		return "", fmt.Errorf("unknown task %s", update.ID)
	}
	if task.Status == StatusDone {
		return "", fmt.Errorf("task %s is already done", update.ID)
	}
	s.Seq++
	if update.Title != "" {
		task.Title = update.Title
	}
	if update.NextAction != "" {
		task.NextAction = update.NextAction
	}
	if update.Validation != "" {
		task.Validation = update.Validation
	}
	task.UpdatedSeq = s.Seq
	s.Tasks[task.ID] = task
	return fmt.Sprintf("%s updated.", task.ID), nil
}

func (s *State) applyPrune(update Update) (string, error) {
	if len(update.IDs) == 0 {
		return "", fmt.Errorf("ids is required for prune_expired")
	}
	pruned := make([]string, 0, len(update.IDs))
	for _, id := range update.IDs {
		task, ok := s.Tasks[id]
		if !ok || task.Status != StatusDone {
			continue
		}
		delete(s.Tasks, id)
		pruned = append(pruned, id)
	}
	if len(pruned) == 0 {
		return "No expired done tasks pruned.", nil
	}
	s.Seq++
	return fmt.Sprintf("Pruned expired done tasks: %s.", strings.Join(pruned, ", ")), nil
}

func normalizeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *State) MarkCompletedAt(id string, completedAt time.Time) {
	if s == nil || completedAt.IsZero() {
		return
	}
	task, ok := s.Tasks[id]
	if !ok || task.Status != StatusDone {
		return
	}
	completedAt = completedAt.UTC()
	task.CompletedAt = &completedAt
	s.Tasks[id] = task
}

func (s *State) SetStats(id string, stats *Stats) {
	if s == nil || stats == nil {
		return
	}
	task, ok := s.Tasks[id]
	if !ok || task.Status != StatusDone {
		return
	}
	task.Stats = stats
	s.Tasks[id] = task
}

func (s *State) ExpiredDoneTaskIDs(now time.Time, maxAge time.Duration) []string {
	if s == nil || maxAge <= 0 || now.IsZero() {
		return nil
	}
	cutoff := now.UTC().Add(-maxAge)
	var ids []string
	for _, task := range s.Tasks {
		if task.Status != StatusDone || task.CompletedAt == nil || task.CompletedAt.IsZero() {
			continue
		}
		if task.CompletedAt.Before(cutoff) {
			ids = append(ids, task.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s *State) inProgressTask(exceptID, scope string) (Task, bool) {
	for _, task := range s.Tasks {
		if task.ID == exceptID {
			continue
		}
		if task.Status == StatusInProgress && task.Scope == scope {
			return task, true
		}
	}
	return Task{}, false
}

func (s *State) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	return Snapshot{Tasks: s.sortedTasks(), Seq: s.Seq}
}

func (s *State) MarshalSnapshot() (string, error) {
	data, err := json.Marshal(s.Snapshot())
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func Render(state *State) string {
	if state == nil || len(state.Tasks) == 0 {
		return "## Tasks\n○ (none)"
	}
	tasks := state.sortedTasks()
	var active []Task
	var pending []Task
	var blocked []Task
	var done []Task
	for _, task := range tasks {
		switch task.Status {
		case StatusInProgress:
			active = append(active, task)
		case StatusBlocked:
			blocked = append(blocked, task)
		case StatusDone:
			done = append(done, task)
		default:
			pending = append(pending, task)
		}
	}

	var b strings.Builder
	b.WriteString("## Tasks\n")
	for _, task := range active {
		writeTaskLine(&b, "▶", task, true)
	}
	visiblePending := append([]Task{}, blocked...)
	visiblePending = append(visiblePending, pending...)
	for i, task := range visiblePending {
		if i >= 5 {
			_, _ = fmt.Fprintf(&b, "○ (+%d more)\n", len(visiblePending)-i)
			break
		}
		prefix := "○"
		if task.Status == StatusBlocked {
			prefix = "■"
		}
		writeTaskLine(&b, prefix, task, task.Status == StatusBlocked)
	}
	if len(done) > 0 {
		sort.SliceStable(done, func(i, j int) bool { return done[i].UpdatedSeq > done[j].UpdatedSeq })
		latest := done[0]
		_, _ = fmt.Fprintf(&b, "✓ %s: %s", latest.ID, latest.Title)
		if latest.Outcome != "" {
			_, _ = fmt.Fprintf(&b, " — outcome: %s", latest.Outcome)
		}
		if latest.Validation != "" {
			_, _ = fmt.Fprintf(&b, " — validation: %s", latest.Validation)
		}
		b.WriteByte('\n')
		if len(done) > 1 {
			ids := make([]string, 0, len(done)-1)
			for _, task := range done[1:] {
				ids = append(ids, task.ID)
			}
			sort.Strings(ids)
			_, _ = fmt.Fprintf(&b, "✓ %s (done)\n", strings.Join(ids, ", "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func RenderSummary(state *State) string {
	return renderGrouped(state, false)
}

func RenderFull(state *State) string {
	return renderGrouped(state, true)
}

func renderGrouped(state *State, includeDetails bool) string {
	if state == nil || len(state.Tasks) == 0 {
		return "## Tasks\n○ (none)"
	}
	tasks := state.sortedTasks()
	var active []Task
	var pending []Task
	var blocked []Task
	var done []Task
	for _, task := range tasks {
		switch task.Status {
		case StatusInProgress:
			active = append(active, task)
		case StatusBlocked:
			blocked = append(blocked, task)
		case StatusDone:
			done = append(done, task)
		default:
			pending = append(pending, task)
		}
	}

	var b strings.Builder
	b.WriteString("## Tasks\n\n")
	writeTaskGroup(&b, "In Progress", "▶", active, includeDetails)
	writeTaskGroup(&b, "Blocked", "■", blocked, includeDetails)
	writeTaskGroup(&b, "Pending", "○", pending, includeDetails)
	writeTaskGroup(&b, "Done", "✓", done, includeDetails)
	return strings.TrimRight(b.String(), "\n")
}

func writeTaskGroup(b *strings.Builder, heading, prefix string, tasks []Task, includeDetails bool) {
	if len(tasks) == 0 {
		return
	}
	_, _ = fmt.Fprintf(b, "### %s\n", heading)
	for _, task := range tasks {
		writeTaskBlock(b, prefix, task, includeDetails)
	}
}

func writeTaskBlock(b *strings.Builder, prefix string, task Task, includeDetails bool) {
	_, _ = fmt.Fprintf(b, "%s %s: %s\n", prefix, task.ID, task.Title)
	if includeDetails && task.Validation != "" {
		b.WriteString("  Validate:\n")
		for _, criterion := range strings.Split(task.Validation, "\n") {
			if criterion = strings.TrimSpace(criterion); criterion != "" {
				_, _ = fmt.Fprintf(b, "    - %s\n", criterion)
			}
		}
	}
	if includeDetails && task.NextAction != "" {
		_, _ = fmt.Fprintf(b, "  Next: %s\n", task.NextAction)
	}
	if includeDetails && task.Outcome != "" {
		_, _ = fmt.Fprintf(b, "  Outcome: %s\n", task.Outcome)
	}
	if includeDetails && task.Stats != nil {
		writeTaskStats(b, task.Stats)
	}
	b.WriteByte('\n')
}

func writeTaskLine(b *strings.Builder, prefix string, task Task, includeNext bool) {
	_, _ = fmt.Fprintf(b, "%s %s: %s", prefix, task.ID, task.Title)
	if task.Validation != "" {
		_, _ = fmt.Fprintf(b, " — validation: %s", task.Validation)
	}
	if includeNext && task.NextAction != "" {
		_, _ = fmt.Fprintf(b, " — next: %s", task.NextAction)
	}
	if includeNext && task.Outcome != "" {
		_, _ = fmt.Fprintf(b, " — outcome: %s", task.Outcome)
	}
	b.WriteByte('\n')
	if includeNext && task.Stats != nil {
		writeTaskStats(b, task.Stats)
	}
}

func writeTaskStats(b *strings.Builder, stats *Stats) {
	_, _ = fmt.Fprintf(b, "  ↳ %d tokens (prompt %d, cached %d, completion %d)", stats.TotalTokens, stats.PromptTokens, stats.CachedPromptTokens, stats.CompletionTokens)
	toolNames := sortedToolNames(stats.ToolCalls)
	if len(toolNames) > 0 {
		b.WriteString(" · tools: ")
		for i, name := range toolNames {
			if i > 0 {
				b.WriteString(", ")
			}
			_, _ = fmt.Fprintf(b, "%s×%d", name, stats.ToolCalls[name])
		}
	}
	b.WriteByte('\n')
}

func sortedToolNames(calls map[string]int) []string {
	names := make([]string, 0, len(calls))
	for name := range calls {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if calls[names[i]] == calls[names[j]] {
			return names[i] < names[j]
		}
		return calls[names[i]] > calls[names[j]]
	})
	return names
}

func (s *State) sortedTasks() []Task {
	if s == nil {
		return nil
	}
	out := make([]Task, 0, len(s.Tasks))
	for _, task := range s.Tasks {
		out = append(out, task)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedSeq == out[j].UpdatedSeq {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedSeq < out[j].UpdatedSeq
	})
	return out
}

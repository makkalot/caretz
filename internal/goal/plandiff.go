package goal

import (
	"fmt"
	"slices"
	"strings"
)

// PlanDiff describes how the plan in after changed from the one in before:
// goal criteria, evidence and unfinished tasks that were added, removed or
// revised, with changed fields in full. It is empty when no new plan version
// was created.
func PlanDiff(before, after State) string {
	if after.PlanVersion <= before.PlanVersion || before.PlanVersion == 0 {
		return ""
	}
	var lines []string
	section := func(title string, entries []string) {
		if len(entries) > 0 {
			lines = append(lines, "", "**"+title+"**")
			lines = append(lines, entries...)
		}
	}
	if before.PlanSummary != after.PlanSummary {
		section("Summary", changedField("", before.PlanSummary, after.PlanSummary))
	}
	section("Goal criteria", listDiff(before.Evidence.Criteria, after.Evidence.Criteria))
	section("Evidence", evidenceDiff(before.Evidence, after.Evidence))
	section("Tasks", taskDiff(before, after))
	header := fmt.Sprintf("### Changes from plan v%d to v%d", before.PlanVersion, after.PlanVersion)
	if before.Evidence.Version != after.Evidence.Version {
		header += fmt.Sprintf(" (evidence v%d to v%d)", before.Evidence.Version, after.Evidence.Version)
	}
	if len(lines) == 0 {
		return header + "\n\nNo changes to criteria, evidence or unfinished tasks."
	}
	return header + "\n" + strings.Join(lines, "\n")
}

func listDiff(before, after []string) []string {
	var entries []string
	for _, item := range before {
		if !slices.Contains(after, item) {
			entries = append(entries, "- − "+item)
		}
	}
	for _, item := range after {
		if !slices.Contains(before, item) {
			entries = append(entries, "- + "+item)
		}
	}
	return entries
}

func evidenceLine(item EvidenceItem) string {
	line := item.ID + " `" + item.Check.Label() + "`"
	if item.Covers != "" {
		line += " — " + item.Covers
	}
	return line
}

func evidenceDiff(before, after Evidence) []string {
	var entries []string
	old := make(map[string]EvidenceItem, len(before.Items))
	for _, item := range before.Items {
		old[item.ID] = item
	}
	kept := map[string]bool{}
	for _, item := range after.Items {
		prev, ok := old[item.ID]
		if !ok {
			entries = append(entries, "- + "+evidenceLine(item))
			continue
		}
		kept[item.ID] = true
		var fields []string
		fields = append(fields, changedField("check", "`"+prev.Check.Label()+"`", "`"+item.Check.Label()+"`")...)
		fields = append(fields, changedField("covers", prev.Covers, item.Covers)...)
		fields = append(fields, changedField("gaps", prev.Gaps, item.Gaps)...)
		fields = append(fields, changedField("status", prev.Status, item.Status)...)
		if len(fields) > 0 {
			entries = append(entries, "- ~ "+item.ID)
			entries = append(entries, indent(fields)...)
		}
	}
	for _, item := range before.Items {
		if !kept[item.ID] {
			entries = append(entries, "- − "+evidenceLine(item))
		}
	}
	return entries
}

func taskLine(state State, task Task) string {
	line := state.ShortTaskID(task.ID) + " " + task.Title
	if task.Kind == KindCheck {
		line += " (check)"
	}
	return line
}

func taskDiff(before, after State) []string {
	var entries []string
	open := map[string]Task{}
	for _, task := range before.Tasks {
		if !task.Status.Closed() {
			open[task.ID] = task
		}
	}
	for _, task := range after.Tasks {
		if task.Status.Closed() {
			continue
		}
		prev, ok := open[task.ID]
		if !ok {
			entries = append(entries, "- + "+taskLine(after, task))
			entries = append(entries, indent(append([]string{"next action: " + task.NextAction, "check: `" + task.Check.Label() + "`"}, prefixed("validation: ", task.Validation)...))...)
			continue
		}
		delete(open, task.ID)
		var fields []string
		fields = append(fields, changedField("title", prev.Title, task.Title)...)
		fields = append(fields, changedField("next action", prev.NextAction, task.NextAction)...)
		fields = append(fields, changedField("check", "`"+prev.Check.Label()+"`", "`"+task.Check.Label()+"`")...)
		fields = append(fields, changedField("depends on", shortIDs(before, prev.DependsOn), shortIDs(after, task.DependsOn))...)
		for _, entry := range listDiff(prev.Validation, task.Validation) {
			fields = append(fields, "validation "+strings.TrimPrefix(entry, "- "))
		}
		if len(fields) > 0 {
			entries = append(entries, "- ~ "+taskLine(after, task))
			entries = append(entries, indent(fields)...)
		}
	}
	for _, task := range before.Tasks {
		if _, dropped := open[task.ID]; dropped {
			entries = append(entries, "- − "+taskLine(before, task))
		}
	}
	return entries
}

// changedField reports a field that differs as its old and new value.
func changedField(name, before, after string) []string {
	if before == after {
		return nil
	}
	if name != "" {
		name += ": "
	}
	return []string{name + "− " + DefaultString(before, "(none)"), name + "+ " + DefaultString(after, "(none)")}
}

func shortIDs(state State, ids []string) string {
	short := make([]string, len(ids))
	for i, id := range ids {
		short[i] = state.ShortTaskID(id)
	}
	return strings.Join(short, ", ")
}

func prefixed(prefix string, values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = prefix + value
	}
	return out
}

func indent(fields []string) []string {
	out := make([]string, len(fields))
	for i, field := range fields {
		out[i] = "  - " + field
	}
	return out
}

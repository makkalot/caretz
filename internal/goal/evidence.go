package goal

import (
	"sort"
	"strings"
)

// Workspace answers what evidence checks execute and what those files hold.
type Workspace interface {
	// EvidenceFiles lists workspace files a check executes, relative to the
	// workspace root.
	EvidenceFiles(check Check) []string
	// FileHash hashes a workspace file; a missing file hashes to MissingFile.
	FileHash(rel string) string
}

// MissingFile is the hash of an evidence file that does not exist.
const MissingFile = "missing"

// Freeze returns hashes of evidence files that exist, by evidence ID. Without
// refresh it skips files already frozen; with refresh a check task or the
// user has legitimately changed the verification and every file is rehashed.
// Evidence written by a check task is frozen only after that task is done.
// A refresh also rehashes files frozen earlier, so accepting a deleted file
// records it as missing instead of keeping its stale hash.
func Freeze(workspace Workspace, state State, refresh bool) map[string]map[string]string {
	frozen := map[string]map[string]string{}
	for _, item := range state.Evidence.Items {
		if writer := item.Check.WrittenBy; writer != "" {
			if index := state.TaskIndex(writer); index >= 0 && state.Tasks[index].Status != TaskDone {
				continue
			}
		}
		files := workspace.EvidenceFiles(item.Check)
		if refresh {
			for rel := range item.Files {
				files = append(files, rel)
			}
		}
		for _, rel := range files {
			if _, ok := item.Files[rel]; ok && !refresh {
				continue
			}
			if frozen[item.ID] == nil {
				frozen[item.ID] = map[string]string{}
			}
			frozen[item.ID][rel] = workspace.FileHash(rel)
		}
	}
	if len(frozen) == 0 {
		return nil
	}
	return frozen
}

// Tampered reports frozen evidence files that changed since, as
// "ID path (deleted|created|modified)".
func Tampered(workspace Workspace, state State) []string {
	var changed []string
	for _, item := range state.Evidence.Items {
		for rel, hash := range item.Files {
			current := workspace.FileHash(rel)
			if current == hash {
				continue
			}
			kind := "modified"
			switch {
			case current == MissingFile:
				kind = "deleted"
			case hash == MissingFile:
				kind = "created"
			}
			changed = append(changed, item.ID+" "+rel+" ("+kind+")")
		}
	}
	sort.Strings(changed)
	return changed
}

// ProtectedFiles lists frozen evidence files work tasks must not modify.
func (s State) ProtectedFiles() []string {
	seen := map[string]bool{}
	var files []string
	for _, item := range s.Evidence.Items {
		for rel := range item.Files {
			if !seen[rel] {
				seen[rel] = true
				files = append(files, rel)
			}
		}
	}
	sort.Strings(files)
	return files
}

// TamperedQuestion asks the user to review evidence files a work task changed.
func TamperedQuestion(tampered []string) string {
	var files []string
	deleted := false
	for _, entry := range tampered {
		if _, rest, ok := strings.Cut(entry, " "); ok {
			path, kind, _ := strings.Cut(rest, " (")
			files = append(files, path)
			deleted = deleted || kind == "deleted)"
		}
	}
	question := "Evidence files changed after they were frozen:\n- " + strings.Join(tampered, "\n- ") +
		"\nWork tasks must not edit evidence, so review the change: git diff -- " + strings.Join(files, " ") +
		"\nIt is likely a false positive when the plan itself requires the change (a refactor, rename or removal that the edited tests covered) and the checks were not weakened."
	if deleted {
		question += " A deleted file no longer runs as evidence; make sure its coverage was not dropped silently."
	}
	return question + "\nAnswer to accept the change as the new baseline, or revise the evidence with /goal:evidence --edit."
}

// ProtectedFilesIn lists protected files that text names, at most five.
func (s State) ProtectedFilesIn(text string) []string {
	var named []string
	for _, rel := range s.ProtectedFiles() {
		if strings.Contains(text, rel) {
			named = append(named, rel)
			if len(named) == 5 {
				break
			}
		}
	}
	return named
}

// EvidenceQuestion asks the user about evidence the work cannot fix.
func EvidenceQuestion(stuck []Failure) string {
	return "Approved evidence keeps failing in a way the work cannot fix:\n- " + strings.Join(FailureLines(stuck), "\n- ") +
		"\nIf the check itself is wrong, revise it with /goal:evidence --edit \"CHANGES\". Answer to rerun verification as is."
}

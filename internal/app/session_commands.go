package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/explorer"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/sessions"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/eventstore"
	libruntime "github.com/makkalot/caretz/lib/runtime"
	"github.com/makkalot/caretz/lib/types"
)

func validSessionAdapter(adapter string) bool {
	return adapter == "" || adapter == "cli" || adapter == "tui" || adapter == "slack" || adapter == "telegram"
}

func resolveSessionLocation(workspace, configRoot string, configRootSet bool) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(workspace) != "" {
		cwd, err = resolveRootDir(workspace)
		if err != nil {
			return "", fmt.Errorf("invalid --workspace %q: %w", workspace, err)
		}
	}
	root, err := resolveConfigRoot(cwd, configRoot, configRootFlagState{ConfigRootSet: configRootSet}, os.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "sessions"), nil
}

type sessionCommandOptions struct {
	sessionLocationOptions
	ConfigRootSet    bool
	IncludeSubagents bool   `flag:"include-subagents" help:"Include child/subagent sessions" group:"Filters"`
	FromVersion      uint64 `flag:"from-version" help:"First originator version to remove" group:"Trimming"`
}

type sessionLocationOptions struct {
	Workspace  string `flag:"workspace" help:"Workspace directory used to discover .caretz" group:"Session location"`
	ConfigRoot string `flag:"config-root" help:"Directory containing sessions; overrides CARETZ_CONFIG_ROOT" group:"Session location"`
	Adapter    string `flag:"adapter" help:"Adapter namespace: cli, tui, slack, or telegram" group:"Session location"`
}

func runSessionCommand(action string, args []string, commandOpts sessionCommandOptions, stdout, stderr io.Writer, opts Options) int {
	workspace := commandOpts.Workspace
	configRoot := commandOpts.ConfigRoot
	adapter := commandOpts.Adapter
	includeSubagents := commandOpts.IncludeSubagents
	fromVersion := commandOpts.FromVersion
	if !validSessionAdapter(strings.TrimSpace(adapter)) {
		output.Errorf(stderr, "invalid adapter %q: must be one of cli, tui, slack, telegram", adapter)
		return 2
	}
	if action == "trim" && fromVersion == 0 {
		output.Errorf(stderr, "--from-version is required with session trim")
		return 2
	}
	sessionsDir, err := resolveSessionLocation(workspace, configRoot, commandOpts.ConfigRootSet)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	if action == "list" {
		list, err := sessions.ListWithOptions(sessionsDir, sessions.ListOptions{IncludeSubagents: includeSubagents, Adapter: strings.TrimSpace(adapter)})
		if err != nil {
			output.Errorf(stderr, "listing sessions: %v", err)
			return 1
		}
		if len(list) == 0 {
			_, _ = fmt.Fprintln(stdout, "No sessions found.")
			return 0
		}
		_, _ = fmt.Fprintf(stdout, "%-8s %-36s %-12s %-8s %-20s %s\n", "ADAPTER", "ID", "CREATED", "EVENTS", "MESSAGES", "FIRST PROMPT")
		for _, s := range list {
			_, _ = fmt.Fprintf(stdout, "%-8s %-36s %-12s %-8d %-8d %s\n", s.AdapterType, s.ID, s.CreatedAt.Format("2006-01-02"), s.EventCount, s.MessageCount, s.FirstPrompt)
		}
		return 0
	}
	id := args[0]
	switch action {
	case "explore":
		dir, adapterType, err := explorer.FindSessionDirWithAdapter(sessionsDir, id, strings.TrimSpace(adapter))
		if err == nil && opts.ExplorerRunner != nil {
			err = opts.ExplorerRunner(libruntime.ExplorerParams{SessionDir: dir, SessionID: id, AdapterType: adapterType})
		} else if err == nil {
			err = explorer.StartExplorer(dir, id, adapterType)
		}
		if err != nil {
			output.Errorf(stderr, "%v", err)
			return 1
		}
	case "inspect":
		err = inspectSessionRepair(stdout, sessionsDir, id, strings.TrimSpace(adapter))
	case "repair":
		err = repairSession(stdout, sessionsDir, id, strings.TrimSpace(adapter))
	case "trim":
		err = trimSessionTail(stdout, sessionsDir, id, fromVersion, strings.TrimSpace(adapter))
	}
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	return 0
}

func inspectSessionRepair(stdout io.Writer, sessionsDir, sessionID string, requestedAdapter ...string) error {
	adapter := ""
	if len(requestedAdapter) > 0 {
		adapter = requestedAdapter[0]
	}
	sessionDir, adapterType, err := explorer.FindSessionDirWithAdapter(sessionsDir, sessionID, adapter)
	if err != nil {
		return err
	}
	events, err := readContextEventsForSession(sessionDir, sessionID)
	if err != nil {
		return err
	}
	analysis := analyzeSessionRepair(events, sessionID)
	_, _ = fmt.Fprintf(stdout, "Session %s/%s\n\n", adapterType, sessionID)
	if !analysis.Required {
		_, _ = fmt.Fprintln(stdout, "No repair is required. The restored conversation has a complete message sequence.")
		return nil
	}
	_, _ = fmt.Fprintln(stdout, "Repair is required before this session can continue.")
	_, _ = fmt.Fprintln(stdout)
	_, _ = fmt.Fprintln(stdout, "What happened:")
	_, _ = fmt.Fprintf(stdout, "- caretz found an assistant/tool turn that did not write a final assistant response.\n")
	if analysis.InterruptedEventCount > 0 {
		_, _ = fmt.Fprintf(stdout, "- The unfinished turn has %d context event(s) after the last safe completed response.\n", analysis.InterruptedEventCount)
	}
	if analysis.CompletedThroughVersion > 0 {
		_, _ = fmt.Fprintf(stdout, "- The last safe completed context event is version %d.\n", analysis.CompletedThroughVersion)
	}
	if analysis.TargetBeforeVersion > 0 {
		_, _ = fmt.Fprintf(stdout, "- A later %s message exists at context version %d, so caretz cannot continue without closing the unfinished turn first.\n", analysis.TargetBeforeRole, analysis.TargetBeforeVersion)
	} else {
		_, _ = fmt.Fprintln(stdout, "- The context log ends inside that unfinished turn, so restore trims it to keep the model history valid.")
	}
	_, _ = fmt.Fprintln(stdout)
	_, _ = fmt.Fprintln(stdout, "What repair will do:")
	if len(analysis.PendingToolCallIDs) > 0 {
		_, _ = fmt.Fprintf(stdout, "- Add synthetic interrupted tool results for pending tool call(s): %s.\n", strings.Join(analysis.PendingToolCallIDs, ", "))
	} else {
		_, _ = fmt.Fprintln(stdout, "- No synthetic tool results are needed because all recorded tool calls already have results.")
	}
	if analysis.TargetBeforeVersion > 0 {
		_, _ = fmt.Fprintf(stdout, "- Add this synthetic assistant message before context version %d:\n  %s\n", analysis.TargetBeforeVersion, analysis.Message)
	} else {
		_, _ = fmt.Fprintf(stdout, "- Append this synthetic assistant message at the end:\n  %s\n", analysis.Message)
	}
	_, _ = fmt.Fprintln(stdout, "- Rewrite context.jsonpl with those recovery messages in the correct chronological position.")
	_, _ = fmt.Fprintln(stdout, "- Back up the original context.jsonpl before replacing it.")
	_, _ = fmt.Fprintln(stdout)
	_, _ = fmt.Fprintf(stdout, "Next step: run `caretz session repair %s`, then reopen with `caretz -s %s`.\n", sessionID, sessionID)
	return nil
}

func repairSession(stdout io.Writer, sessionsDir, sessionID string, requestedAdapter ...string) error {
	adapter := ""
	if len(requestedAdapter) > 0 {
		adapter = requestedAdapter[0]
	}
	sessionDir, adapterType, err := explorer.FindSessionDirWithAdapter(sessionsDir, sessionID, adapter)
	if err != nil {
		return err
	}
	contextPath := filepath.Join(sessionDir, "context.jsonpl")
	lockPath := filepath.Join(sessionDir, ".repair.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("acquire session repair lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		return fmt.Errorf("close session repair lock: %w", err)
	}
	defer func() { _ = os.Remove(lockPath) }()

	events, err := readContextEventsForSession(sessionDir, sessionID)
	if err != nil {
		return err
	}

	recoveryIDs := []string{}
	targetVersions := []string{}
	repaired := events
	for {
		analysis := analyzeSessionRepair(repaired, sessionID)
		if !analysis.Required {
			break
		}
		recoveryID := eventing.NewRecoveryID()
		var inserted bool
		repaired, inserted = injectContextRecovery(repaired, analysis, recoveryID)
		if !inserted {
			return fmt.Errorf("repair target context version %d was not found", analysis.TargetBeforeVersion)
		}
		recoveryIDs = append(recoveryIDs, recoveryID)
		if analysis.TargetBeforeVersion > 0 {
			targetVersions = append(targetVersions, fmt.Sprintf("%d", analysis.TargetBeforeVersion))
		} else {
			targetVersions = append(targetVersions, "end")
		}
		if len(recoveryIDs) > 100 {
			return fmt.Errorf("repair did not converge after %d injected recoveries", len(recoveryIDs))
		}
	}
	if len(recoveryIDs) == 0 {
		_, _ = fmt.Fprintf(stdout, "session %s does not require repair\n", sessionID)
		return nil
	}
	repaired = renumberContextEvents(repaired, sessionID)
	if analysis := analyzeSessionRepair(repaired, sessionID); analysis.Required {
		return fmt.Errorf("generated context still requires repair before version %d", analysis.TargetBeforeVersion)
	}
	if _, err := eventing.EventsToLLMMessages(filterModelContextEvents(repaired)); err != nil {
		return fmt.Errorf("generated context does not convert to LLM messages: %w", err)
	}

	recoveryID := eventing.NewRecoveryID()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	backup := fmt.Sprintf("%s.%s.%s.bak", contextPath, stamp, recoveryID)
	if err := copyFile(contextPath, backup); err != nil {
		return fmt.Errorf("backup %s: %w", contextPath, err)
	}
	tmp := contextPath + "." + recoveryID + ".tmp"
	if err := writeContextJSONPL(tmp, repaired); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := validateJSONPL(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("generated invalid JSONPL for %s: %w", contextPath, err)
	}
	if err := os.Rename(tmp, contextPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", contextPath, err)
	}
	_, _ = fmt.Fprintf(stdout, "Repaired session %s/%s.\n", adapterType, sessionID)
	_, _ = fmt.Fprintf(stdout, "Recovery IDs: %s\n", strings.Join(recoveryIDs, ", "))
	_, _ = fmt.Fprintf(stdout, "Rewrote context.jsonpl with recovery before original context version(s): %s.\n", strings.Join(targetVersions, ", "))
	_, _ = fmt.Fprintf(stdout, "backup: %s\n", backup)
	_, _ = fmt.Fprintf(stdout, "You can continue with `caretz -s %s`.\n", sessionID)
	return nil
}

func readContextEventsForSession(sessionDir, sessionID string) ([]*types.Event, error) {
	contextLog, err := eventstore.NewFileMemoryStore(filepath.Join(sessionDir, "context.jsonpl"))
	if err != nil {
		return nil, fmt.Errorf("open context log: %w", err)
	}
	return contextLog.Get(types.NewOriginator(sessionID, 0), false)
}

func trimSessionTail(stdout io.Writer, sessionsDir, sessionID string, fromVersion uint64, requestedAdapter ...string) error {
	adapter := ""
	if len(requestedAdapter) > 0 {
		adapter = requestedAdapter[0]
	}
	sessionDir, _, err := explorer.FindSessionDirWithAdapter(sessionsDir, sessionID, adapter)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(sessionDir, ".repair.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("acquire session repair lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		return fmt.Errorf("close session repair lock: %w", err)
	}
	defer func() { _ = os.Remove(lockPath) }()

	recoveryID := eventing.NewRecoveryID()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	paths := []string{filepath.Join(sessionDir, "events.jsonpl"), filepath.Join(sessionDir, "context.jsonpl")}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return err
		}
	}

	backups := make([]string, 0, len(paths))
	for _, p := range paths {
		backup := fmt.Sprintf("%s.%s.%s.bak", p, stamp, recoveryID)
		if err := copyFile(p, backup); err != nil {
			return fmt.Errorf("backup %s: %w", p, err)
		}
		backups = append(backups, backup)
	}

	for _, p := range paths {
		tmp := p + "." + recoveryID + ".tmp"
		removed, err := writeTrimmedJSONPL(p, tmp, sessionID, fromVersion)
		if err != nil {
			return err
		}
		if removed == 0 {
			_ = os.Remove(tmp)
			return fmt.Errorf("no events at or after version %d in %s", fromVersion, p)
		}
		if err := validateJSONPL(tmp); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("generated invalid JSONPL for %s: %w", p, err)
		}
		if err := os.Rename(tmp, p); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("replace %s: %w", p, err)
		}
		_, _ = fmt.Fprintf(stdout, "trimmed %d event(s) from %s\n", removed, filepath.Base(p))
	}
	_, _ = fmt.Fprintf(stdout, "backups:\n%s\n", strings.Join(backups, "\n"))
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeTrimmedJSONPL(src, dst, sessionID string, fromVersion uint64) (int, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Close() }()

	removed := 0
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var fe struct {
			OriginatorID      string `json:"originator_id"`
			OriginatorVersion uint64 `json:"originator_version"`
		}
		if err := json.Unmarshal(line, &fe); err != nil {
			return removed, err
		}
		if fe.OriginatorID == sessionID && fe.OriginatorVersion >= fromVersion {
			removed++
			continue
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			return removed, err
		}
	}
	return removed, scanner.Err()
}

func validateJSONPL(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	var lastByOriginator = map[string]uint64{}
	for scanner.Scan() {
		var fe struct {
			OriginatorID      string          `json:"originator_id"`
			OriginatorVersion uint64          `json:"originator_version"`
			EventType         string          `json:"event_type"`
			Payload           json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &fe); err != nil {
			return err
		}
		if fe.OriginatorID == "" || fe.OriginatorVersion == 0 || fe.EventType == "" {
			return fmt.Errorf("missing required event fields")
		}
		if last := lastByOriginator[fe.OriginatorID]; last > 0 && fe.OriginatorVersion <= last {
			return fmt.Errorf("non-monotonic version for %s: %d after %d", fe.OriginatorID, fe.OriginatorVersion, last)
		}
		lastByOriginator[fe.OriginatorID] = fe.OriginatorVersion
	}
	return scanner.Err()
}

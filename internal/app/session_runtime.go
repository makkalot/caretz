package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/internal/config"
)

const sessionRuntimeFilename = "runtime.json"

type sessionRuntimeState struct {
	ModelRef        string `json:"modelRef,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	WorkspaceDir    string `json:"workspaceDir,omitempty"`
	Profile         string `json:"profile,omitempty"`
}

func sessionRuntimePath(sessionsDir, adapterType, sessionID string) string {
	return filepath.Join(sessionsDir, adapterType, sessionID, sessionRuntimeFilename)
}

func loadSessionRuntime(path string) (sessionRuntimeState, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sessionRuntimeState{}, false, nil
		}
		return sessionRuntimeState{}, false, fmt.Errorf("read session runtime %s: %w", path, err)
	}
	var state sessionRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return sessionRuntimeState{}, false, fmt.Errorf("parse session runtime %s: %w", path, err)
	}
	state.ModelRef = strings.TrimSpace(state.ModelRef)
	state.Model = strings.TrimSpace(state.Model)
	state.ReasoningEffort = strings.TrimSpace(state.ReasoningEffort)
	state.WorkspaceDir = strings.TrimSpace(state.WorkspaceDir)
	state.Profile = strings.TrimSpace(state.Profile)
	if state.ModelRef == "" && state.Model == "" {
		return sessionRuntimeState{}, false, fmt.Errorf("session runtime %s does not select a model", path)
	}
	return state, true, nil
}

func saveSessionRuntime(path string, state sessionRuntimeState) error {
	state.ModelRef = strings.TrimSpace(state.ModelRef)
	state.Model = strings.TrimSpace(state.Model)
	state.ReasoningEffort = strings.TrimSpace(state.ReasoningEffort)
	state.WorkspaceDir = strings.TrimSpace(state.WorkspaceDir)
	state.Profile = strings.TrimSpace(state.Profile)
	if state.ModelRef == "" && state.Model == "" {
		return fmt.Errorf("session runtime model is required")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session runtime: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create session runtime directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary session runtime: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op after successful rename
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set session runtime permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary session runtime: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary session runtime: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace session runtime %s: %w", path, err)
	}
	return nil
}

func settingsForSessionRuntime(fileConfig config.Config, fallback runtimeSettings, state sessionRuntimeState) (runtimeSettings, error) {
	settings := fallback
	if state.ModelRef != "" {
		resolved, ok, err := fileConfig.ResolveModelRef(state.ModelRef)
		if err != nil {
			return runtimeSettings{}, fmt.Errorf("resolve saved session model %q: %w", state.ModelRef, err)
		}
		if !ok {
			return runtimeSettings{}, fmt.Errorf("saved session model %q is no longer configured", state.ModelRef)
		}
		settings, err = runtimeSettingsForModelSwitch(resolved)
		if err != nil {
			return runtimeSettings{}, fmt.Errorf("restore saved session model %q: %w", state.ModelRef, err)
		}
		settings.ActiveModelRef = state.ModelRef
	} else {
		settings.Model = state.Model
		settings.ActiveModelRef = ""
	}
	settings.ReasoningEffort = state.ReasoningEffort
	settings.LLMTimeout = fallback.LLMTimeout
	settings.LLMMaxAttempts = fallback.LLMMaxAttempts
	settings.OutputMode = fallback.OutputMode
	settings.Theme = fallback.Theme
	return settings, nil
}

func sessionRuntimeStateFromSettings(settings runtimeSettings, profile string) sessionRuntimeState {
	return sessionRuntimeState{
		ModelRef:        settings.ActiveModelRef,
		Model:           settings.Model,
		ReasoningEffort: settings.ReasoningEffort,
		Profile:         strings.TrimSpace(profile),
	}
}

func applyExplicitSessionRuntimeOverrides(settings *runtimeSettings, selected runtimeSettings, flags baseFlagValues, getenv func(string) string) {
	// The restored provider keeps its own key unless --api-key was typed or a
	// base URL override makes the endpoint ad hoc; OPENAI_API_KEY alone never
	// replaces it.
	baseURLOverride := flags.BaseURL != "" || getenv("OPENAI_API_BASE_URL") != ""
	if flags.APIKey != "" || baseURLOverride {
		settings.APIKey = selected.APIKey
		settings.ExplicitAPIKey = selected.ExplicitAPIKey
	}
	if baseURLOverride {
		settings.BaseURL = selected.BaseURL
	}
	if flags.ReasoningEffort != "" || getenv("OPENAI_REASONING") != "" {
		settings.ReasoningEffort = selected.ReasoningEffort
	}
	if flags.ContextWindow > 0 || getenv("CARETZ_CONTEXT_WINDOW") != "" {
		settings.ContextWindow = selected.ContextWindow
	}
	if flags.ReservedTokens > 0 || getenv("CARETZ_RESERVED_TOKENS") != "" {
		settings.ReservedTokens = selected.ReservedTokens
	}
	if flags.KeepRecentTokens > 0 || getenv("CARETZ_KEEP_RECENT_TOKENS") != "" {
		settings.KeepRecentTokens = selected.KeepRecentTokens
	}
	if flags.SummaryTokens > 0 || getenv("CARETZ_SUMMARY_TOKENS") != "" {
		settings.SummaryTokens = selected.SummaryTokens
	}
}

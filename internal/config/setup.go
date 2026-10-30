package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var ErrProviderExists = errors.New("provider already exists")
var setupProviderKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type SetupProvider struct {
	Key, Name, BaseURL, APIKey, ModelID string
	ContextWindow                       int
	Local                               *bool
	// Auth is the provider's auth mode; AuthChatGPT also pins the model to
	// the Responses protocol.
	Auth string
	// KeepActiveModel leaves an existing activeModel alone; the new model
	// becomes active only when none is set.
	KeepActiveModel bool
}

// SaveSetupProvider safely merges a setup result while preserving unrelated
// and unknown JSON fields.
func SaveSetupProvider(path string, setup SetupProvider, replace bool) error {
	if err := validateSetupProvider(setup); err != nil {
		return err
	}
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	providers := map[string]json.RawMessage{}
	if value, ok := raw["provider"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &providers); err != nil {
			return fmt.Errorf("parse config %s provider: %w", path, err)
		}
	}
	existing, exists := providers[setup.Key]
	if exists && !replace {
		return fmt.Errorf("%w: %s", ErrProviderExists, setup.Key)
	}
	endpointChanged := exists && setupProviderBaseURL(existing) != normalizeSetupBaseURL(setup.BaseURL)
	model := map[string]any{}
	if setup.ContextWindow > 0 {
		model["limit"] = map[string]int{"context": setup.ContextWindow}
	}
	provider := map[string]any{
		"name":    setup.Name,
		"options": map[string]string{"baseURL": setup.BaseURL},
		"models":  map[string]any{setup.ModelID: model},
	}
	if setup.Local != nil {
		provider["local"] = *setup.Local
	}
	if setup.Auth != "" {
		provider["auth"] = setup.Auth
		model["protocol"] = "responses"
	}
	providerJSON, err := json.Marshal(provider)
	if err != nil {
		return fmt.Errorf("encode provider: %w", err)
	}
	providers[setup.Key] = providerJSON
	providersJSON, err := json.Marshal(providers)
	if err != nil {
		return fmt.Errorf("encode providers: %w", err)
	}
	raw["provider"] = providersJSON
	if _, hasActive := raw["activeModel"]; !setup.KeepActiveModel || !hasActive {
		activeJSON, _ := json.Marshal(setup.Key + "/" + setup.ModelID)
		raw["activeModel"] = activeJSON
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	out = append(out, '\n')
	// The key goes only to credentials.json. Reading it first means an insecure
	// or corrupt file fails before the config changes. A stored key belongs to
	// the old host, so it is dropped before the config points elsewhere and a
	// new key is saved only after: a failure in between leaves the provider
	// without a key, never with one meant for another host. No key leaves a
	// stored one alone while the endpoint stays the same.
	credentialsPath := CredentialsPath(path)
	if _, err := LoadCredentials(credentialsPath); err != nil {
		return err
	}
	if endpointChanged {
		if err := SaveProviderCredential(credentialsPath, setup.Key, ""); err != nil {
			return err
		}
	}
	if err := writeConfigFileAtomic(path, out); err != nil {
		return err
	}
	if apiKey := strings.TrimSpace(setup.APIKey); apiKey != "" && apiKey != NoAPIKey {
		return SaveProviderCredential(credentialsPath, setup.Key, apiKey)
	}
	return nil
}

// SaveLocalImageModel points profiles.general.toolConfig.localImage at
// providerKey/modelID, adding the model to the provider's catalog when it is
// missing so the reference resolves. A direct endpoint is dropped, since it
// would send images elsewhere; other localImage fields and unknown JSON fields
// are kept.
func SaveLocalImageModel(path, providerKey, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return fmt.Errorf("model ID must not be empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	providers := map[string]map[string]json.RawMessage{}
	if value, ok := raw["provider"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &providers); err != nil {
			return fmt.Errorf("parse config %s provider: %w", path, err)
		}
	}
	provider, ok := providers[providerKey]
	if !ok {
		return fmt.Errorf("provider %q is not defined in %s", providerKey, path)
	}
	models := map[string]json.RawMessage{}
	if value, ok := provider["models"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &models); err != nil {
			return fmt.Errorf("parse config %s provider.%s.models: %w", path, providerKey, err)
		}
	}
	if _, ok := models[modelID]; !ok {
		models[modelID] = json.RawMessage(`{}`)
		if provider["models"], err = json.Marshal(models); err != nil {
			return fmt.Errorf("encode models: %w", err)
		}
		if raw["provider"], err = json.Marshal(providers); err != nil {
			return fmt.Errorf("encode providers: %w", err)
		}
	}
	profiles := map[string]map[string]json.RawMessage{}
	if value, ok := raw["profiles"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &profiles); err != nil {
			return fmt.Errorf("parse config %s profiles: %w", path, err)
		}
	}
	general := profiles["general"]
	if general == nil {
		general = map[string]json.RawMessage{}
	}
	toolConfig := map[string]map[string]any{}
	if value, ok := general["toolConfig"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &toolConfig); err != nil {
			return fmt.Errorf("parse config %s profiles.general.toolConfig: %w", path, err)
		}
	}
	localImage := toolConfig["localImage"]
	if localImage == nil {
		localImage = map[string]any{}
	}
	delete(localImage, "endpoint")
	localImage["model"] = providerKey + "/" + modelID
	toolConfig["localImage"] = localImage
	if general["toolConfig"], err = json.Marshal(toolConfig); err != nil {
		return fmt.Errorf("encode toolConfig: %w", err)
	}
	profiles["general"] = general
	if raw["profiles"], err = json.Marshal(profiles); err != nil {
		return fmt.Errorf("encode profiles: %w", err)
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeConfigFileAtomic(path, append(out, '\n'))
}

// setupProviderBaseURL returns the normalized baseURL of a raw provider entry,
// or "" when it has none or cannot be parsed.
func setupProviderBaseURL(raw json.RawMessage) string {
	var provider struct {
		Options struct {
			BaseURL string `json:"baseURL"`
		} `json:"options"`
	}
	if json.Unmarshal(raw, &provider) != nil {
		return ""
	}
	return normalizeSetupBaseURL(provider.Options.BaseURL)
}

func normalizeSetupBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

// ProviderRemoval reports the references to removed providers that
// RemoveProviders cleaned up besides the provider entries themselves.
type ProviderRemoval struct {
	// ClearedActive reports that activeModel used a removed provider.
	ClearedActive bool
	// RuleSelectors lists each dropped modelRules selector as "path: selector".
	RuleSelectors []string
	// EmptiedRules lists rule paths left with an empty allow list. Such a rule
	// is kept, so its tree allows only local models rather than inheriting a
	// broader rule.
	EmptiedRules []string
	// ProfileModels lists dropped profile settings, e.g. "general.goal.worker".
	ProfileModels []string
}

// RemoveProviders deletes provider entries from the config at path while
// preserving unrelated and unknown JSON fields. References to a removed
// provider go too: an activeModel, so the next start picks an available model;
// modelRules selectors; and profile goal roles and schedule models, which then
// fall back to the session's model.
func RemoveProviders(path string, keys []string) (ProviderRemoval, error) {
	out, report, err := planProviderRemoval(path, keys)
	if err != nil {
		return report, err
	}
	// Keys go first: if the config write then fails, the providers stay
	// configured without keys instead of leaving keys behind for removed ones.
	if err := DeleteProviderCredentials(CredentialsPath(path), keys); err != nil {
		return report, err
	}
	if err := writeConfigFileAtomic(path, out); err != nil {
		return report, err
	}
	// The cache only lists models, so a stale entry is harmless; ignore errors.
	_ = removeCachedProviders(ModelCachePath(path), keys)
	return report, nil
}

// PreviewRemoveProviders reports what RemoveProviders would change without
// writing anything, so a caller can show it before asking.
func PreviewRemoveProviders(path string, keys []string) (ProviderRemoval, error) {
	_, report, err := planProviderRemoval(path, keys)
	return report, err
}

// planProviderRemoval returns the config at path with keys and their
// references removed, and what was removed.
func planProviderRemoval(path string, keys []string) ([]byte, ProviderRemoval, error) {
	var report ProviderRemoval
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, report, fmt.Errorf("read config %s: %w", path, err)
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, report, fmt.Errorf("parse config %s: %w", path, err)
	}
	providers := map[string]json.RawMessage{}
	if value, ok := raw["provider"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &providers); err != nil {
			return nil, report, fmt.Errorf("parse config %s provider: %w", path, err)
		}
	}
	removed := map[string]bool{}
	for _, key := range keys {
		if _, ok := providers[key]; !ok {
			return nil, report, fmt.Errorf("provider %q is not defined in %s", key, path)
		}
		delete(providers, key)
		removed[key] = true
	}
	providersJSON, err := json.Marshal(providers)
	if err != nil {
		return nil, report, fmt.Errorf("encode providers: %w", err)
	}
	raw["provider"] = providersJSON
	usesRemoved := func(ref string) bool {
		providerKey, _, found := strings.Cut(ref, "/")
		return found && removed[providerKey]
	}
	if value, ok := raw["activeModel"]; ok {
		var active string
		if json.Unmarshal(value, &active) == nil && usesRemoved(active) {
			delete(raw, "activeModel")
			report.ClearedActive = true
		}
	}
	if err := removeRuleSelectors(raw, usesRemoved, &report); err != nil {
		return nil, report, fmt.Errorf("parse config %s modelRules: %w", path, err)
	}
	if err := removeProfileModels(raw, usesRemoved, &report); err != nil {
		return nil, report, fmt.Errorf("parse config %s profiles: %w", path, err)
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, report, fmt.Errorf("encode config: %w", err)
	}
	return append(out, '\n'), report, nil
}

// removeRuleSelectors drops modelRules selectors naming a removed provider.
func removeRuleSelectors(raw map[string]json.RawMessage, usesRemoved func(string) bool, report *ProviderRemoval) error {
	value, ok := raw["modelRules"]
	if !ok || string(value) == "null" {
		return nil
	}
	var rules []ModelRule
	if err := json.Unmarshal(value, &rules); err != nil {
		return err
	}
	changed := false
	for i := range rules {
		kept := []string{}
		for _, selector := range rules[i].Allow {
			if usesRemoved(selector) {
				report.RuleSelectors = append(report.RuleSelectors, rules[i].Path+": "+selector)
				continue
			}
			kept = append(kept, selector)
		}
		if len(kept) == len(rules[i].Allow) {
			continue
		}
		changed = true
		rules[i].Allow = kept
		if len(kept) == 0 {
			report.EmptiedRules = append(report.EmptiedRules, rules[i].Path)
		}
	}
	if !changed {
		return nil
	}
	rulesJSON, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	raw["modelRules"] = rulesJSON
	return nil
}

// removeProfileModels drops goal roles and schedule models that name a
// removed provider, so those runs use the session's model again. A schedule's
// reasoning goes with its model, since reasoning needs a model.
func removeProfileModels(raw map[string]json.RawMessage, usesRemoved func(string) bool, report *ProviderRemoval) error {
	value, ok := raw["profiles"]
	if !ok || string(value) == "null" {
		return nil
	}
	profiles := map[string]map[string]json.RawMessage{}
	if err := json.Unmarshal(value, &profiles); err != nil {
		return err
	}
	modelOf := func(value json.RawMessage) string {
		var block struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(value, &block)
		return block.Model
	}
	changed := false
	var err error
	for _, name := range slices.Sorted(maps.Keys(profiles)) {
		profile := profiles[name]
		if goalJSON, ok := profile["goal"]; ok && string(goalJSON) != "null" {
			goal := map[string]json.RawMessage{}
			if err := json.Unmarshal(goalJSON, &goal); err != nil {
				return err
			}
			goalChanged := false
			for _, role := range []string{"planner", "worker", "verifier"} {
				if roleJSON, ok := goal[role]; ok && usesRemoved(modelOf(roleJSON)) {
					delete(goal, role)
					goalChanged = true
					report.ProfileModels = append(report.ProfileModels, name+".goal."+role)
				}
			}
			if goalChanged {
				if profile["goal"], err = json.Marshal(goal); err != nil {
					return err
				}
				changed = true
			}
		}
		if schedulesJSON, ok := profile["schedules"]; ok && string(schedulesJSON) != "null" && usesRemoved(modelOf(schedulesJSON)) {
			schedules := map[string]json.RawMessage{}
			if err := json.Unmarshal(schedulesJSON, &schedules); err != nil {
				return err
			}
			delete(schedules, "model")
			delete(schedules, "reasoning")
			if profile["schedules"], err = json.Marshal(schedules); err != nil {
				return err
			}
			changed = true
			report.ProfileModels = append(report.ProfileModels, name+".schedules.model")
		}
	}
	if !changed {
		return nil
	}
	profilesJSON, err := json.Marshal(profiles)
	if err != nil {
		return err
	}
	raw["profiles"] = profilesJSON
	return nil
}

// writeConfigFileAtomic replaces path with data, readable only by the user.
func writeConfigFileAtomic(path string, out []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("secure config: %w", err)
	}
	return nil
}

func validateSetupProvider(s SetupProvider) error {
	if !setupProviderKeyPattern.MatchString(s.Key) {
		return fmt.Errorf("provider key may contain only letters, numbers, '.', '_', and '-'")
	}
	u, err := url.ParseRequestURI(strings.TrimSpace(s.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base URL must be a valid HTTP(S) URL")
	}
	if strings.TrimSpace(s.ModelID) == "" {
		return fmt.Errorf("model ID must not be empty")
	}
	if s.ContextWindow < 0 {
		return fmt.Errorf("context window must be positive")
	}
	return nil
}

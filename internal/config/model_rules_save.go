package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// NewWorkspaceRule returns a rule covering workspace, already canonicalized.
func NewWorkspaceRule(workspace string, allow []string, source string) (*ModelRule, error) {
	canonical, err := canonicalModelPath(workspace)
	if err != nil {
		return nil, err
	}
	return &ModelRule{Path: canonical, Allow: append([]string{}, allow...), Source: source, canonical: canonical}, nil
}

// WithModelRule returns c with rule added, replacing the rule for the same
// directory, so a rule saved during a session applies without a reload.
func (c Config) WithModelRule(rule ModelRule) (Config, error) {
	target, err := canonicalModelPath(rule.Path)
	if err != nil {
		return c, err
	}
	rule.canonical = target
	rules := make([]ModelRule, 0, len(c.ModelRules)+1)
	for _, existing := range c.ModelRules {
		if path, err := canonicalModelPath(existing.Path); err == nil && path == target {
			continue
		}
		rules = append(rules, existing)
	}
	c.ModelRules = append(rules, rule)
	return c, nil
}

// ModelRulesFile returns the personal config that should receive a new rule.
// A config-root file is used only when it already defines modelRules: its
// list replaces the home rules, so starting one there would drop them.
func ModelRulesFile(homePath, configRootPath string) (string, error) {
	if configRootPath == "" {
		return homePath, nil
	}
	data, err := os.ReadFile(configRootPath)
	if os.IsNotExist(err) {
		return homePath, nil
	}
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", configRootPath, err)
	}
	raw := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return "", fmt.Errorf("parse config %s: %w", configRootPath, err)
		}
	}
	if _, ok := raw["modelRules"]; ok {
		return configRootPath, nil
	}
	return homePath, nil
}

// SaveModelRule adds rule to the config at path, or replaces the allow list
// of an existing rule for the same directory, preserving other fields.
func SaveModelRule(path string, rule ModelRule) error {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &raw); err != nil {
				return fmt.Errorf("parse config %s: %w", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	rules := []ModelRule{}
	if value, ok := raw["modelRules"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &rules); err != nil {
			return fmt.Errorf("parse config %s modelRules: %w", path, err)
		}
	}
	target, err := canonicalModelPath(rule.Path)
	if err != nil {
		return err
	}
	replaced := false
	for i := range rules {
		if existing, err := canonicalModelPath(rules[i].Path); err == nil && existing == target {
			rules[i].Allow = append([]string{}, rule.Allow...)
			replaced = true
			break
		}
	}
	if !replaced {
		rules = append(rules, ModelRule{Path: rule.Path, Allow: append([]string{}, rule.Allow...)})
	}
	if err := validateModelRules(append([]ModelRule(nil), rules...), path); err != nil {
		return err
	}
	rulesJSON, err := json.Marshal(rules)
	if err != nil {
		return fmt.Errorf("encode modelRules: %w", err)
	}
	raw["modelRules"] = rulesJSON
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeConfigFileAtomic(path, append(out, '\n'))
}

package tools

import (
	"encoding/json"
	"fmt"
)

type Config map[string]map[string]any

type ToolConfigValidator func(config map[string]any) error

func MergeConfig(base, override Config) Config {
	out := cloneConfig(base)
	if out == nil {
		out = Config{}
	}
	for toolName, overrideFields := range override {
		fields := map[string]any{}
		for key, value := range out[toolName] {
			fields[key] = value
		}
		for key, value := range overrideFields {
			fields[key] = value
		}
		out[toolName] = fields
	}
	return out
}

func (r *Registry) WithConfig(cfg Config) *Registry {
	clone := r.clone()
	clone.ToolConfig = cloneConfig(cfg)
	return clone
}

func (r *Registry) ValidateConfig() error {
	if err := validateLocalImageConfig(r.toolConfigFor("localImage")); err != nil {
		return err
	}
	if r != nil {
		for name, tool := range r.customTools {
			if tool.ValidateConfig == nil {
				continue
			}
			if err := tool.ValidateConfig(r.toolConfigFor(name)); err != nil {
				return fmt.Errorf("tools.%s: %w", name, err)
			}
		}
	}
	return nil
}

func (r *Registry) localImageConfig() (LocalImageConfig, error) {
	return decodeToolConfig[LocalImageConfig](r.toolConfigFor("localImage"))
}

func (r *Registry) toolConfigFor(name string) map[string]any {
	if r == nil || r.ToolConfig == nil {
		return nil
	}
	return r.ToolConfig[name]
}

func cloneConfig(cfg Config) Config {
	if cfg == nil {
		return nil
	}
	out := make(Config, len(cfg))
	for toolName, fields := range cfg {
		copied := make(map[string]any, len(fields))
		for key, value := range fields {
			copied[key] = value
		}
		out[toolName] = copied
	}
	return out
}

func decodeToolConfig[T any](raw map[string]any) (T, error) {
	var out T
	if raw == nil {
		return out, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

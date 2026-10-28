package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ModelCache is the content of models.json: the model IDs each provider's
// /models endpoint advertised when caretz setup or caretz models --refresh last
// asked. Startup reads it instead of contacting every provider.
type ModelCache struct {
	Provider map[string]CachedProviderModels `json:"provider,omitempty"`
}

type CachedProviderModels struct {
	BaseURL   string    `json:"baseURL"`
	FetchedAt time.Time `json:"fetchedAt"`
	Models    []string  `json:"models"`
}

// ModelCachePath returns the model cache that sits next to configPath.
func ModelCachePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "models.json")
}

// LoadModelCache reads path. A missing file yields an empty cache.
func LoadModelCache(path string) (ModelCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ModelCache{}, nil
		}
		return ModelCache{}, fmt.Errorf("read model cache %s: %w", path, err)
	}
	var cache ModelCache
	if len(bytes.TrimSpace(data)) == 0 {
		return cache, nil
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return ModelCache{}, fmt.Errorf("parse model cache %s: %w", path, err)
	}
	return cache, nil
}

// UpdateModelCache rewrites path after update changes the loaded cache. A
// corrupt cache is replaced rather than blocking the update.
func UpdateModelCache(path string, update func(*ModelCache)) error {
	cache, err := LoadModelCache(path)
	if err != nil {
		cache = ModelCache{}
	}
	if cache.Provider == nil {
		cache.Provider = map[string]CachedProviderModels{}
	}
	update(&cache)
	out, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("encode model cache: %w", err)
	}
	return writeConfigFileAtomic(path, append(out, '\n'))
}

// SetCachedModels records models for providerKey as fetched now from baseURL.
func (c *ModelCache) SetCachedModels(providerKey, baseURL string, models []string, now time.Time) {
	if c.Provider == nil {
		c.Provider = map[string]CachedProviderModels{}
	}
	c.Provider[providerKey] = CachedProviderModels{BaseURL: normalizeSetupBaseURL(baseURL), FetchedAt: now.UTC(), Models: models}
}

// Entry returns the cached models for providerKey when they were fetched from
// the provider's current baseURL; a provider pointed elsewhere, or a
// same-named provider from another config, gets nothing.
func (c ModelCache) Entry(providerKey string, provider Provider) (CachedProviderModels, bool) {
	entry, ok := c.Provider[providerKey]
	if !ok || provider.Options.BaseURL == nil {
		return CachedProviderModels{}, false
	}
	if entry.BaseURL != normalizeSetupBaseURL(*provider.Options.BaseURL) {
		return CachedProviderModels{}, false
	}
	return entry, true
}

// DiscoveredFor returns the cached models of every provider in cfg, in the
// form Config.WithDiscoveredModels takes.
func (c ModelCache) DiscoveredFor(cfg Config) map[string][]string {
	discovered := make(map[string][]string, len(cfg.Provider))
	for key, provider := range cfg.Provider {
		if entry, ok := c.Entry(key, provider); ok {
			discovered[key] = entry.Models
		}
	}
	return discovered
}

// removeCachedProviders drops providerKeys from the cache at path, if present.
func removeCachedProviders(path string, providerKeys []string) error {
	cache, err := LoadModelCache(path)
	if err != nil || len(cache.Provider) == 0 {
		return err
	}
	found := false
	for _, key := range providerKeys {
		if _, ok := cache.Provider[strings.TrimSpace(key)]; ok {
			found = true
		}
	}
	if !found {
		return nil
	}
	return UpdateModelCache(path, func(cache *ModelCache) {
		for _, key := range providerKeys {
			delete(cache.Provider, strings.TrimSpace(key))
		}
	})
}

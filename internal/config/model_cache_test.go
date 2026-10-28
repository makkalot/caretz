package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestModelCacheRoundTripMatchesCurrentBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	err := UpdateModelCache(path, func(cache *ModelCache) {
		cache.SetCachedModels("live", "http://live.example/v1/", []string{"a", "b"}, now)
		cache.SetCachedModels("moved", "http://old.example/v1", []string{"stale"}, now)
		cache.SetCachedModels("removed", "http://gone.example/v1", []string{"x"}, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := LoadModelCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cache.Provider["live"].FetchedAt; !got.Equal(now) {
		t.Fatalf("FetchedAt = %v, want %v", got, now)
	}
	cfg := Config{Provider: map[string]Provider{
		"live":  {Options: ProviderOptions{BaseURL: ptr(" http://live.example/v1")}},
		"moved": {Options: ProviderOptions{BaseURL: ptr("http://new.example/v1")}},
	}}
	want := map[string][]string{"live": {"a", "b"}}
	if got := cache.DiscoveredFor(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoveredFor = %v, want %v", got, want)
	}
}

func TestLoadModelCacheMissingFileIsEmpty(t *testing.T) {
	cache, err := LoadModelCache(filepath.Join(t.TempDir(), "models.json"))
	if err != nil || len(cache.Provider) != 0 {
		t.Fatalf("cache = %+v, err = %v", cache, err)
	}
}

func TestUpdateModelCacheReplacesCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModelCache(path); err == nil {
		t.Fatal("LoadModelCache accepted corrupt JSON")
	}
	err := UpdateModelCache(path, func(cache *ModelCache) {
		cache.SetCachedModels("p", "http://p.example/v1", []string{"m"}, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	if cache, err := LoadModelCache(path); err != nil || len(cache.Provider["p"].Models) != 1 {
		t.Fatalf("cache = %+v, err = %v", cache, err)
	}
}

func TestRemoveProvidersDropsCachedModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{"provider":{"a":{"options":{"baseURL":"http://a.example/v1"}},"b":{"options":{"baseURL":"http://b.example/v1"}}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	err := UpdateModelCache(ModelCachePath(path), func(cache *ModelCache) {
		cache.SetCachedModels("a", "http://a.example/v1", []string{"m"}, time.Now())
		cache.SetCachedModels("b", "http://b.example/v1", []string{"m"}, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveProviders(path, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	cache, err := LoadModelCache(ModelCachePath(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Provider["a"]; ok {
		t.Fatal("removed provider still cached")
	}
	if _, ok := cache.Provider["b"]; !ok {
		t.Fatal("kept provider lost its cache entry")
	}
}

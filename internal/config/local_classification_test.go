package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func classificationBool(value bool) *bool { return &value }

func TestLocalClassification(t *testing.T) {
	for _, endpoint := range []string{"http://100.100.1.2/v1", "https://gpu.example.ts.net/v1", "https://gpu.example.com/v1", "http://127.0.0.1/v1"} {
		for _, classification := range []*bool{nil, classificationBool(true), classificationBool(false)} {
			cfg := Config{Provider: map[string]Provider{"gpu": {Local: classification, Options: ProviderOptions{BaseURL: &endpoint}, Models: map[string]Model{"m": {}}}}}
			want := IsLocalEndpoint(endpoint)
			if classification != nil {
				want = *classification
			}
			if got := cfg.IsLocalModel("gpu/m", " "+endpoint+"/// "); got != want {
				t.Fatalf("endpoint=%s classification=%v: got %v want %v", endpoint, classification, got, want)
			}
			if got := cfg.LocalSelectors(); (len(got) == 1) != want {
				t.Fatalf("selectors=%v want local=%v", got, want)
			}
			for _, override := range []string{"https://other.example/v1", "http://192.168.1.2/v1", ""} {
				if got := cfg.IsLocalModel("gpu/m", override); got != IsLocalEndpoint(override) {
					t.Fatalf("override %q inherited classification", override)
				}
				if got := cfg.IsLocalModel("unknown/m", override); got != IsLocalEndpoint(override) {
					t.Fatal("unconfigured model did not use URL detection")
				}
			}
			// Rules restrict hosted models only; a local one is always allowed.
			for _, allow := range [][]string{{"other/*"}, {}} {
				policy := ModelPolicy{Rule: &ModelRule{Allow: allow}}
				if got := policy.Allows(cfg, "gpu/m"); got != want {
					t.Fatalf("endpoint=%s classification=%v allow=%v: allowed=%v want %v", endpoint, classification, allow, got, want)
				}
			}
		}
	}
}

func TestLocalClassificationPersistenceAndMerge(t *testing.T) {
	for _, local := range []*bool{nil, classificationBool(true), classificationBool(false)} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := SaveSetupProvider(path, SetupProvider{Key: "gpu", BaseURL: "https://gpu.example/v1", ModelID: "m", Local: local}, false); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path, "")
		if err != nil || !reflect.DeepEqual(cfg.Provider["gpu"].Local, local) {
			t.Fatalf("round trip: local=%v cfg=%+v err=%v", local, cfg, err)
		}
		root := filepath.Join(t.TempDir(), "config.json")
		write := func(body string) {
			t.Helper()
			if err := os.WriteFile(root, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		write(`{"provider":{"gpu":{"options":{"baseURL":"https://other.example/v1"}}}}`)
		merged, err := LoadProfileWithTrust(path, root, "", true)
		if err != nil || merged.Provider["gpu"].Local != nil {
			t.Fatalf("new URL inherited trust: %+v %v", merged, err)
		}
		write(`{"provider":{"gpu":{"local":false}}}`)
		merged, err = LoadProfileWithTrust(path, root, "", true)
		if err != nil || merged.Provider["gpu"].Local == nil || *merged.Provider["gpu"].Local {
			t.Fatalf("explicit false lost: %+v %v", merged, err)
		}
		if _, err := Load(path, root); err == nil {
			t.Fatal("workspace provider.local accepted")
		}
		write(`{"provider":{"gpu":{"name":"renamed"}}}`)
		merged, err = LoadProfileWithTrust(path, root, "", true)
		if err != nil || !reflect.DeepEqual(merged.Provider["gpu"].Local, local) {
			t.Fatalf("non-URL override lost classification: %+v %v", merged, err)
		}
	}
}

func TestLocalClassificationRequiresURL(t *testing.T) {
	for _, options := range []string{`{}`, `{"baseURL":""}`, `{"baseURL":"  /// "}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"provider":{"gpu":{"local":true,"options":`+options+`}}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, ""); err == nil {
			t.Fatal("local without URL accepted")
		}
	}
}

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIsLocalEndpoint(t *testing.T) {
	for url, want := range map[string]bool{
		"http://127.0.0.1:8080/v1":   true,
		"http://[::1]:11434/v1":      true,
		"http://localhost:1234/v1":   true,
		"http://gpu.localhost/v1":    true,
		"http://192.168.1.20:8080":   true,
		"http://10.0.0.5/v1":         true,
		"http://172.16.4.2/v1":       true,
		"http://box.local:8080/v1":   true,
		"https://api.openai.com/v1":  false,
		"https://8.8.8.8/v1":         false,
		"https://localhost.evil.com": false,
		"":                           false,
		"not a url":                  false,
	} {
		if got := IsLocalEndpoint(url); got != want {
			t.Errorf("IsLocalEndpoint(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestLocalSelectors(t *testing.T) {
	local, lan, cloud := "http://127.0.0.1:8080/v1", "http://192.168.1.2/v1", "https://api.example.com/v1"
	cfg := Config{Provider: map[string]Provider{
		"ollama": {Options: ProviderOptions{BaseURL: &local}},
		"lan":    {Options: ProviderOptions{BaseURL: &lan}},
		"cloud":  {Options: ProviderOptions{BaseURL: &cloud}},
		"none":   {},
	}}
	if got, want := cfg.LocalSelectors(), []string{"lan/*", "ollama/*"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LocalSelectors() = %v, want %v", got, want)
	}
}

func TestSaveModelRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"activeModel":"cloud/x","custom":{"keep":true},"modelRules":[{"path":"/a","allow":["*"]}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveModelRule(path, ModelRule{Path: "/b", Allow: []string{"local/*"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ActiveModel string          `json:"activeModel"`
		Custom      map[string]bool `json:"custom"`
		ModelRules  []ModelRule     `json:"modelRules"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ActiveModel != "cloud/x" || !got.Custom["keep"] {
		t.Fatalf("unrelated fields not preserved: %s", data)
	}
	if len(got.ModelRules) != 2 || got.ModelRules[1].Path != "/b" || !reflect.DeepEqual(got.ModelRules[1].Allow, []string{"local/*"}) {
		t.Fatalf("modelRules = %+v", got.ModelRules)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v err=%v, want 0600", info.Mode().Perm(), err)
	}
	// Saving a rule for an existing directory replaces its allow list.
	if err := SaveModelRule(path, ModelRule{Path: "/b", Allow: []string{"local/*", "cloud/x"}}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got.ModelRules = nil
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.ModelRules) != 2 || !reflect.DeepEqual(got.ModelRules[1].Allow, []string{"local/*", "cloud/x"}) {
		t.Fatalf("modelRules after replace = %+v", got.ModelRules)
	}
	fresh := filepath.Join(dir, "new", "config.json")
	if err := SaveModelRule(fresh, ModelRule{Path: "/c", Allow: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
}

func TestModelRulesFile(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home.json")
	root := filepath.Join(dir, "root.json")
	for _, tt := range []struct {
		name, content, want string
	}{
		{"no config root file", "", home},
		{"config root without rules", `{"activeModel":"a/b"}`, home},
		{"config root with rules", `{"modelRules":[]}`, root},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(root)
			if tt.content != "" {
				if err := os.WriteFile(root, []byte(tt.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ModelRulesFile(home, root)
			if err != nil || got != tt.want {
				t.Fatalf("ModelRulesFile() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if got, err := ModelRulesFile(home, ""); err != nil || got != home {
		t.Fatalf("no config root: %q %v", got, err)
	}
}

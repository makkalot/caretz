package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveProvidersPreservesOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := `{"unknown":{"keep":true},"activeModel":"other/m","provider":{"other":{"models":{"m":{}}},"gone":{"models":{"x":{}}}}}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := RemoveProviders(path, []string{"gone"})
	if err != nil || report.ClearedActive {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"gone"`) || !strings.Contains(string(data), `"unknown"`) || !strings.Contains(string(data), `"activeModel": "other/m"`) {
		t.Fatalf("unexpected config: %s", data)
	}
	if report, err := RemoveProviders(path, []string{"other"}); err != nil || !report.ClearedActive {
		t.Fatalf("removing active provider: report=%+v err=%v", report, err)
	}
	if _, err := RemoveProviders(path, []string{"missing"}); err == nil {
		t.Fatal("removing an undefined provider succeeded")
	}
}

func TestRemoveProvidersDropsReferences(t *testing.T) {
	t.Setenv("CARETZ_CONFIG_ROOT", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	work, only := filepath.Join(dir, "work"), filepath.Join(dir, "only")
	before := `{
"provider":{"keep":{"models":{"m":{}}},"gone":{"models":{"x":{}}}},
"modelRules":[
 {"path":"` + work + `","allow":["keep/m","gone/*","gone/x","*"]},
 {"path":"` + only + `","allow":["gone/x"]}
],
"profiles":{
 "general":{"goal":{"maxTokens":5,"planner":{"model":"gone/x","reasoning":"high"},"worker":{"model":"keep/m"}},
  "schedules":{"model":"gone/x","reasoning":"low","limit":{"context":1000}}},
 "other":{"schedules":{"model":"keep/m"}}
}}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := RemoveProviders(path, []string{"gone"})
	if err != nil {
		t.Fatal(err)
	}
	wantSelectors := []string{work + ": gone/*", work + ": gone/x", only + ": gone/x"}
	if strings.Join(report.RuleSelectors, "|") != strings.Join(wantSelectors, "|") {
		t.Fatalf("RuleSelectors = %q, want %q", report.RuleSelectors, wantSelectors)
	}
	if len(report.EmptiedRules) != 1 || report.EmptiedRules[0] != only {
		t.Fatalf("EmptiedRules = %q", report.EmptiedRules)
	}
	if want := "general.goal.planner|general.schedules.model"; strings.Join(report.ProfileModels, "|") != want {
		t.Fatalf("ProfileModels = %q, want %q", report.ProfileModels, want)
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ModelRules; len(got) != 2 || strings.Join(got[0].Allow, ",") != "keep/m,*" || got[1].Allow == nil || len(got[1].Allow) != 0 {
		t.Fatalf("ModelRules = %+v", got)
	}
	general := cfg.Profiles["general"]
	if general.Goal.Planner != nil || general.Goal.Worker == nil || general.Goal.Worker.Model != "keep/m" || general.Goal.MaxTokens == nil || *general.Goal.MaxTokens != 5 {
		t.Fatalf("general goal = %+v", general.Goal)
	}
	if s := general.Schedules; s == nil || s.Model != "" || s.Reasoning != "" || s.Limit.Context == nil || *s.Limit.Context != 1000 {
		t.Fatalf("general schedules = %+v", s)
	}
	if s := cfg.Profiles["other"].Schedules; s == nil || s.Model != "keep/m" {
		t.Fatalf("other schedules = %+v", s)
	}
}

func TestSaveSetupProviderPreservesUnknownFieldsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	before := `{"unknown":{"keep":true},"provider":{"other":{"name":"Other","options":{"baseURL":"https://other.test/v1"},"models":{"old":{}}}}}`
	if err := os.WriteFile(path, []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	err := SaveSetupProvider(path, SetupProvider{Key: "local", Name: "Local", BaseURL: "http://127.0.0.1:8001/v1", APIKey: "noset", ModelID: "model/a", ContextWindow: 32768}, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"unknown"`) || !strings.Contains(string(data), `"other"`) {
		t.Fatalf("unrelated JSON was lost: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	cfg, _, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok, err := cfg.ResolveModelRef("local/model/a")
	if err != nil || !ok || resolved.ContextWindow != 32768 {
		t.Fatalf("resolution = %+v, %v, %v", resolved, ok, err)
	}
}

func TestSaveSetupProviderReplaceDropsKeyWhenEndpointChanges(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	path := filepath.Join(t.TempDir(), "config.json")
	setup := SetupProvider{Key: "myapi", BaseURL: "https://api.vendor-a.test/v1", APIKey: "vendor-a-secret", ModelID: "m"}
	if err := SaveSetupProvider(path, setup, false); err != nil {
		t.Fatal(err)
	}
	storedKey := func() string {
		t.Helper()
		creds, err := LoadCredentials(CredentialsPath(path))
		if err != nil {
			t.Fatal(err)
		}
		return creds.Provider["myapi"].APIKey
	}

	setup.APIKey = ""
	setup.BaseURL = "https://api.vendor-a.test/v1/"
	if err := SaveSetupProvider(path, setup, true); err != nil {
		t.Fatal(err)
	}
	if got := storedKey(); got != "vendor-a-secret" {
		t.Fatalf("key after same-endpoint replace = %q, want it kept", got)
	}

	setup.BaseURL = "https://other-host.test/v1"
	if err := SaveSetupProvider(path, setup, true); err != nil {
		t.Fatal(err)
	}
	if got := storedKey(); got != "" {
		t.Fatalf("key after endpoint change = %q, want it dropped", got)
	}
}

func TestSetupWritesLeaveConfigUnchangedWhenCredentialsAreUnreadable(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	t.Setenv(AllowInsecureCredentialsEnv, "")
	path := filepath.Join(t.TempDir(), "config.json")
	before := `{"provider":{"myapi":{"options":{"baseURL":"https://api.vendor-a.test/v1"},"models":{"m":{}}}},"activeModel":"myapi/m"}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CredentialsPath(path), []byte(`{"provider":{"myapi":{"apiKey":"vendor-a-secret"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	unchanged := func(op string) {
		t.Helper()
		after, _ := os.ReadFile(path)
		if string(after) != before {
			t.Fatalf("%s rewrote config despite unreadable credentials: %s", op, after)
		}
	}

	err := SaveSetupProvider(path, SetupProvider{Key: "myapi", BaseURL: "https://other-host.test/v1", APIKey: "new-secret", ModelID: "m"}, true)
	if err == nil {
		t.Fatal("SaveSetupProvider succeeded with a group-readable credentials file")
	}
	unchanged("SaveSetupProvider")
	if _, err := RemoveProviders(path, []string{"myapi"}); err == nil {
		t.Fatal("RemoveProviders succeeded with a group-readable credentials file")
	}
	unchanged("RemoveProviders")
}

func TestSaveSetupProviderDropsOldKeyBeforeEndpointChange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	t.Setenv(CredentialsFileEnv, filepath.Join(t.TempDir(), "credentials.json"))
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	setup := SetupProvider{Key: "myapi", BaseURL: "https://api.vendor-a.test/v1", APIKey: "vendor-a-secret", ModelID: "m"}
	if err := SaveSetupProvider(path, setup, false); err != nil {
		t.Fatal(err)
	}
	// A read-only config directory makes the config write fail after the
	// credentials step.
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	setup.BaseURL = "https://other-host.test/v1"
	setup.APIKey = "other-secret"
	if err := SaveSetupProvider(path, setup, true); err == nil {
		t.Fatal("SaveSetupProvider succeeded in a read-only directory")
	}
	creds, err := LoadCredentials(CredentialsPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := creds.Provider["myapi"].APIKey; got != "" {
		t.Fatalf("key after failed endpoint change = %q, want none", got)
	}
}

func TestSaveSetupProviderCollisionDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := `{"provider":{"same":{}},"activeModel":"same/old"}`
	if err := os.WriteFile(path, []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	err := SaveSetupProvider(path, SetupProvider{Key: "same", BaseURL: "https://example.test/v1", ModelID: "new"}, false)
	if !errors.Is(err, ErrProviderExists) {
		t.Fatalf("error = %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != before {
		t.Fatalf("file changed on collision: %s", after)
	}
}

func TestSaveSetupProviderValidatesInput(t *testing.T) {
	valid := SetupProvider{Key: "provider", BaseURL: "https://example.test/v1", ModelID: "model"}
	for _, mutate := range []func(*SetupProvider){func(s *SetupProvider) { s.Key = "bad/key" }, func(s *SetupProvider) { s.Key = "bad key" }, func(s *SetupProvider) { s.BaseURL = "file:///tmp/x" }, func(s *SetupProvider) { s.ModelID = "" }, func(s *SetupProvider) { s.ContextWindow = -1 }} {
		candidate := valid
		mutate(&candidate)
		if err := SaveSetupProvider(filepath.Join(t.TempDir(), "config.json"), candidate, false); err == nil {
			t.Fatalf("accepted invalid setup: %+v", candidate)
		}
	}
}

func TestSaveLocalImageModelAddsCatalogEntryAndKeepsOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := `{"unknown":true,"activeModel":"p/coder","provider":{"p":{"name":"P","options":{"baseURL":"http://127.0.0.1:1/v1"},"models":{"coder":{"limit":{"context":4096}}}}},` +
		`"profiles":{"general":{"tools":["*"],"toolConfig":{"localImage":{"endpoint":"http://127.0.0.1:2/v1/chat/completions","model":"vl","maxTimeSeconds":30},"other":{"x":1}}}}}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalImageModel(path, "p", "vision"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok, err := cfg.ResolveModelRef("p/vision"); !ok || err != nil {
		t.Fatalf("p/vision does not resolve: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := cfg.ResolveModelRef("p/coder"); !ok {
		t.Fatal("existing model dropped")
	}
	localImage := cfg.Profiles["general"].ToolConfig["localImage"]
	if localImage["model"] != "p/vision" || localImage["maxTimeSeconds"] != float64(30) {
		t.Fatalf("localImage = %v", localImage)
	}
	if _, ok := localImage["endpoint"]; ok {
		t.Fatalf("endpoint kept: %v", localImage)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{`"unknown": true`, `"other"`, `"tools"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("config lost %s: %s", want, data)
		}
	}
	if err := SaveLocalImageModel(path, "missing", "vision"); err == nil {
		t.Fatal("accepted an unknown provider")
	}
}

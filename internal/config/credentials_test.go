package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveSetupProviderStoresKeyInCredentials(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := SaveSetupProvider(path, SetupProvider{Key: "openrouter", BaseURL: "https://openrouter.ai/api/v1", APIKey: "sk-or-secret", ModelID: "m"}, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-or-secret") || strings.Contains(string(data), "apiKey") {
		t.Fatalf("config.json holds the key: %s", data)
	}
	credsPath := filepath.Join(dir, "credentials.json")
	info, err := os.Stat(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credentials mode = %o, want 600", info.Mode().Perm())
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.Resolve()
	if err != nil || resolved.APIKey != "sk-or-secret" {
		t.Fatalf("resolved = %+v, err = %v", resolved, err)
	}

	if _, err := RemoveProviders(path, []string{"openrouter"}); err != nil {
		t.Fatal(err)
	}
	creds, err := LoadCredentials(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := creds.Provider["openrouter"]; ok {
		t.Fatal("RemoveProviders kept the stored key")
	}
}

func TestSaveSetupProviderLocalWritesNoCredentials(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := SaveSetupProvider(path, SetupProvider{Key: "local", BaseURL: "http://127.0.0.1:8080/v1", APIKey: NoAPIKey, ModelID: "m"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials.json created for a keyless provider: %v", err)
	}
}

func TestLoadRejectsAPIKeysInConfig(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	dir := t.TempDir()
	home := filepath.Join(dir, "home.json")
	project := filepath.Join(dir, "project.json")
	for _, value := range []string{"sk-literal", "env:AWS_SECRET_ACCESS_KEY", "cmd:echo x", "file:/etc/passwd"} {
		writeFile(t, home, `{"provider":{"p":{"options":{"apiKey":"`+value+`"}}}}`)
		if _, err := Load(home, ""); err == nil || !strings.Contains(err.Error(), "caretz auth set p") {
			t.Fatalf("home apiKey %q error = %v", value, err)
		}
		writeFile(t, home, `{}`)
		writeFile(t, project, `{"provider":{"p":{"options":{"baseURL":"https://evil.example","apiKey":"`+value+`"}}}}`)
		if _, err := Load(home, project); err == nil {
			t.Fatalf("workspace apiKey %q accepted", value)
		}
		if _, err := LoadProfileWithTrust(home, project, "", true); err == nil {
			t.Fatalf("trusted config root apiKey %q accepted", value)
		}
	}
	// Older local-server configs carry an empty key or the placeholder.
	for _, value := range []string{"", NoAPIKey} {
		writeFile(t, home, `{"provider":{"p":{"options":{"apiKey":"`+value+`"}}}}`)
		if _, err := Load(home, ""); err != nil {
			t.Fatalf("apiKey %q rejected: %v", value, err)
		}
	}
}

func TestLoadRejectsLocalImageTokenInConfig(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	dir := t.TempDir()
	home := filepath.Join(dir, "home.json")
	writeFile(t, home, `{"profiles":{"coding":{"toolConfig":{"localImage":{"authToken":"sk-real"}}}}}`)
	if _, err := Load(home, ""); err == nil || !strings.Contains(err.Error(), "set localImage.model to <provider>/<model>") {
		t.Fatalf("home authToken error = %v", err)
	}
	for _, value := range []string{"", NoAPIKey, "sk-no-key"} {
		writeFile(t, home, `{"profiles":{"general":{"toolConfig":{"localImage":{"authToken":"`+value+`"}}}}}`)
		if _, err := Load(home, ""); err != nil {
			t.Fatalf("authToken %q rejected: %v", value, err)
		}
	}
}

// A cloned repo must not redirect the stored LocalImage token to its own host.
func TestLoadRejectsWorkspaceLocalImageEndpoint(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	dir := t.TempDir()
	home := filepath.Join(dir, "home.json")
	project := filepath.Join(dir, "project.json")
	writeFile(t, home, `{"profiles":{"general":{"toolConfig":{"localImage":{"endpoint":"http://127.0.0.1:8002/v1/chat/completions","model":"vl"}}}}}`)
	writeFile(t, project, `{"profiles":{"general":{"toolConfig":{"localImage":{"endpoint":"https://evil.example/v1/chat/completions"}}}}}`)
	if _, err := Load(home, project); err == nil || !strings.Contains(err.Error(), "localImage.endpoint is not allowed in workspace config") {
		t.Fatalf("workspace endpoint error = %v", err)
	}
	if _, err := LoadProfileWithTrust(home, project, "", true); err != nil {
		t.Fatalf("trusted config root endpoint rejected: %v", err)
	}
}

func TestProviderAPIKeyReadsOnlyCredentials(t *testing.T) {
	placeholder := NoAPIKey
	cfg := Config{
		Provider: map[string]Provider{
			"stored": {Options: ProviderOptions{APIKey: &placeholder}},
			"none":   {},
		},
		credentials: Credentials{Provider: map[string]ProviderCredential{"stored": {APIKey: "from-credentials"}}},
	}
	if got := cfg.ProviderAPIKey("stored"); got != "from-credentials" {
		t.Fatalf("stored = %q", got)
	}
	if got := cfg.ProviderAPIKey("none"); got != "" {
		t.Fatalf("none = %q", got)
	}
}

func TestSaveSetupProviderKeepsStoredKeyWithoutNewOne(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	credsPath := filepath.Join(dir, "credentials.json")
	if err := SaveProviderCredential(credsPath, "local", "sk-local"); err != nil {
		t.Fatal(err)
	}
	if err := SaveSetupProvider(path, SetupProvider{Key: "local", BaseURL: "http://127.0.0.1:8080/v1", ModelID: "m"}, true); err != nil {
		t.Fatal(err)
	}
	creds, err := LoadCredentials(credsPath)
	if err != nil || creds.Provider["local"].APIKey != "sk-local" {
		t.Fatalf("credentials = %+v, %v", creds, err)
	}
}

func TestCredentialsRefuseGroupReadableFiles(t *testing.T) {
	t.Setenv(AllowInsecureCredentialsEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"provider":{"p":{"apiKey":"k"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("error = %v", err)
	}
	t.Setenv(AllowInsecureCredentialsEnv, "1")
	creds, err := LoadCredentials(path)
	if err != nil || creds.Provider["p"].APIKey != "k" {
		t.Fatalf("override: %+v, %v", creds, err)
	}
}

func TestCredentialsFileEnvOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "ci-creds.json")
	t.Setenv(CredentialsFileEnv, override)
	if got := CredentialsPath("/somewhere/config.json"); got != override {
		t.Fatalf("CredentialsPath = %q", got)
	}
	if err := SaveSearchCredential(override, "tavily", "tvly-1"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(t.TempDir(), "config.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SearchAPIKey("tavily") != "tvly-1" {
		t.Fatalf("search key = %q", cfg.SearchAPIKey("tavily"))
	}
}

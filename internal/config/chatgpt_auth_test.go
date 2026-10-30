package config

import (
	"path/filepath"
	"testing"
)

func TestChatGPTProviderResolvesToCredentialReference(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"activeModel": "chatgpt/gpt-6.1-sol",
		"provider": {"chatgpt": {"auth": "chatgpt", "models": {"gpt-6.1-sol": {"protocol": "chat-completions"}}}}
	}`)
	// An API key stored under the same name never reaches a ChatGPT provider.
	if err := SaveProviderCredential(CredentialsPath(path), "chatgpt", "sk-stray"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProfileWithTrust(path, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.APIKey != OAuthKeyRef("chatgpt") {
		t.Fatalf("APIKey = %q, want the credential reference", resolved.APIKey)
	}
	if resolved.BaseURL != ChatGPTBaseURL || resolved.Protocol != "responses" {
		t.Fatalf("resolved = %s %s, want %s responses", resolved.BaseURL, resolved.Protocol, ChatGPTBaseURL)
	}
	unlisted, known, err := cfg.ResolveExplicitModelRef("chatgpt/gpt-next")
	if err != nil || !known || unlisted.BaseURL != ChatGPTBaseURL || unlisted.Protocol != "responses" || unlisted.APIKey != OAuthKeyRef("chatgpt") {
		t.Fatalf("unlisted model resolved to %+v (known=%v, err=%v)", unlisted, known, err)
	}
	if key, ok := ParseOAuthKeyRef(resolved.APIKey); !ok || key != "chatgpt" {
		t.Fatalf("ParseOAuthKeyRef = %q %v", key, ok)
	}
	if _, ok := ParseOAuthKeyRef("sk-plain"); ok {
		t.Fatal("a plain key parsed as a credential reference")
	}
}

func TestChatGPTProviderValidation(t *testing.T) {
	cases := map[string]string{
		"other host":   `{"auth": "chatgpt", "options": {"baseURL": "https://proxy.example/v1"}}`,
		"local":        `{"auth": "chatgpt", "local": true, "options": {"baseURL": "https://api.openai.com/v1"}}`,
		"unknown auth": `{"auth": "oauth"}`,
	}
	for name, provider := range cases {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"provider": {"p": `+provider+`}}`)
		if _, err := Load(path, ""); err == nil {
			t.Errorf("%s: Load() accepted the provider", name)
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"provider": {"p": {"auth": "chatgpt", "options": {"baseURL": "https://api.openai.com/v1/"}}}}`)
	if _, err := Load(path, ""); err != nil {
		t.Fatalf("Load() rejected the OpenAI base URL: %v", err)
	}
}

func TestSaveSetupProviderWritesChatGPTProviderAndKeepsActiveModel(t *testing.T) {
	t.Setenv(CredentialsFileEnv, "")
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"activeModel": "local/qwen"}`)
	err := SaveSetupProvider(path, SetupProvider{
		Key: "chatgpt", Name: "ChatGPT", BaseURL: ChatGPTBaseURL, ModelID: "gpt-6.1-sol",
		Auth: AuthChatGPT, KeepActiveModel: true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.Provider["chatgpt"]
	if provider.Auth != AuthChatGPT || provider.Models["gpt-6.1-sol"].Protocol != "responses" {
		t.Fatalf("provider = %+v, want auth chatgpt with a responses model", provider)
	}
	if cfg.ActiveModel != "local/qwen" {
		t.Fatalf("activeModel = %q, want the existing local/qwen kept", cfg.ActiveModel)
	}
}

func TestUpdateProviderOAuthWithoutSignIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	_, err := UpdateProviderOAuth(path, "chatgpt", func(*OAuthCredential) (bool, error) {
		t.Fatal("update called without a sign-in")
		return false, nil
	})
	if err != ErrNotSignedIn {
		t.Fatalf("error = %v, want ErrNotSignedIn", err)
	}
}

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/makkalot/caretz/internal/tools"
)

// CredentialsFileEnv overrides where caretz reads and writes API keys.
const CredentialsFileEnv = "CARETZ_CREDENTIALS_FILE"

// AllowInsecureCredentialsEnv lets caretz read a credentials file that other
// users can read. Without it such a file is refused, like ssh refuses keys.
const AllowInsecureCredentialsEnv = "CARETZ_ALLOW_INSECURE_CREDENTIALS"

// NoAPIKey is the placeholder key sent to local servers that ignore auth.
// Older configs carry it as apiKey, so loading still accepts it.
const NoAPIKey = "noset"

// SearchProviders are the web search services whose keys credentials.json holds.
var SearchProviders = []string{"tavily", "brave"}

// Credentials is the content of credentials.json. API keys live here instead
// of config.json so the config can be edited, shared, and committed freely.
type Credentials struct {
	Provider map[string]ProviderCredential `json:"provider,omitempty"`
	Search   map[string]string             `json:"search,omitempty"`
}

type ProviderCredential struct {
	APIKey string `json:"apiKey"`
}

// CredentialsPath returns the credentials file that sits next to configPath,
// or the CARETZ_CREDENTIALS_FILE override.
func CredentialsPath(configPath string) string {
	if override := strings.TrimSpace(os.Getenv(CredentialsFileEnv)); override != "" {
		return expandHome(override)
	}
	return filepath.Join(filepath.Dir(configPath), "credentials.json")
}

// LoadCredentials reads path. A missing file yields empty credentials; a file
// readable by group or others is an error.
func LoadCredentials(path string) (Credentials, error) {
	data, err := readPrivateFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credentials{}, nil
		}
		return Credentials{}, err
	}
	var creds Credentials
	if len(bytes.TrimSpace(data)) == 0 {
		return creds, nil
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	return creds, nil
}

// SaveProviderCredential stores apiKey for providerKey; an empty apiKey
// removes the entry.
func SaveProviderCredential(path, providerKey, apiKey string) error {
	return updateCredentials(path, func(creds *Credentials) bool {
		if apiKey == "" {
			_, found := creds.Provider[providerKey]
			delete(creds.Provider, providerKey)
			return found
		}
		if creds.Provider == nil {
			creds.Provider = map[string]ProviderCredential{}
		}
		creds.Provider[providerKey] = ProviderCredential{APIKey: apiKey}
		return true
	})
}

// DeleteProviderCredentials removes the stored keys for providerKeys in one
// rewrite of path.
func DeleteProviderCredentials(path string, providerKeys []string) error {
	return updateCredentials(path, func(creds *Credentials) bool {
		changed := false
		for _, key := range providerKeys {
			if _, found := creds.Provider[key]; found {
				delete(creds.Provider, key)
				changed = true
			}
		}
		return changed
	})
}

// SaveSearchCredential stores apiKey for a web search service; an empty
// apiKey removes the entry.
func SaveSearchCredential(path, service, apiKey string) error {
	return updateCredentials(path, func(creds *Credentials) bool {
		if apiKey == "" {
			_, found := creds.Search[service]
			delete(creds.Search, service)
			return found
		}
		if creds.Search == nil {
			creds.Search = map[string]string{}
		}
		creds.Search[service] = apiKey
		return true
	})
}

// updateCredentials rewrites path when update reports a change.
func updateCredentials(path string, update func(*Credentials) bool) error {
	creds, err := LoadCredentials(path)
	if err != nil {
		return err
	}
	if !update(&creds) {
		return nil
	}
	out, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	return writeConfigFileAtomic(path, append(out, '\n'))
}

// ProviderAPIKey returns the key stored in credentials.json for providerKey,
// or "" when none is stored.
func (c Config) ProviderAPIKey(providerKey string) string {
	return c.credentials.Provider[providerKey].APIKey
}

// WithCredentials returns a copy of c that reads keys from creds.
func (c Config) WithCredentials(creds Credentials) Config {
	c.credentials = creds
	return c
}

// SearchAPIKey returns the stored key for a web search service.
func (c Config) SearchAPIKey(service string) string {
	return c.credentials.Search[service]
}

// validateNoAPIKeys rejects API keys in config files. Keys live only in
// credentials.json, so a config, including a cloned repo's workspace config,
// can never choose which secret caretz sends. The empty value and the legacy
// local-server placeholders carry no secret and are ignored.
func validateNoAPIKeys(cfg Config, path string) error {
	profiles := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	for _, name := range profiles {
		if !tools.LocalImageConfigHasToken(cfg.Profiles[name].ToolConfig["localImage"]) {
			continue
		}
		return fmt.Errorf("profiles.%s.toolConfig.localImage.authToken in %s: config files no longer hold API keys. Add the vision server as a provider, run `caretz auth set <provider>`, set localImage.model to <provider>/<model> without endpoint, then delete authToken from %s", name, path, path)
	}
	keys := make([]string, 0, len(cfg.Provider))
	for key := range cfg.Provider {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ref := cfg.Provider[key].Options.APIKey
		if ref == nil {
			continue
		}
		if value := strings.TrimSpace(*ref); value == "" || value == NoAPIKey {
			continue
		}
		return fmt.Errorf("provider.%s.options.apiKey in %s: config files no longer hold API keys. Run `caretz auth set %s` to save the key in credentials.json, then delete apiKey from %s", key, path, key, path)
	}
	return nil
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 && os.Getenv(AllowInsecureCredentialsEnv) != "1" {
		return nil, fmt.Errorf("%s is readable by other users (mode %04o); run chmod 600 %s", path, info.Mode().Perm(), path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// MaskSecret shows only the last four characters of a key.
func MaskSecret(value string) string {
	if len(value) <= 8 {
		return "****"
	}
	return "…" + value[len(value)-4:]
}

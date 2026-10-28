package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/lib/llm"
	"golang.org/x/term"
)

type setupPreset struct{ key, name, baseURL string }

const setupConnectionTimeout = 5 * time.Second

var errInvalidSetupBaseURL = errors.New("invalid setup base URL")

var localSetupPresets = []setupPreset{
	{"llama.cpp", "llama.cpp (local)", "http://127.0.0.1:8080/v1"},
	{"ollama", "Ollama (local)", "http://127.0.0.1:11434/v1"},
	{"lmstudio", "LM Studio (local)", "http://127.0.0.1:1234/v1"},
	{"local", "Custom local provider", "http://127.0.0.1:8080/v1"},
}

var hostedSetupPresets = []setupPreset{
	{"openai", "OpenAI", "https://api.openai.com/v1"}, {"openrouter", "OpenRouter", "https://openrouter.ai/api/v1"},
	{"groq", "Groq", "https://api.groq.com/openai/v1"}, {"together", "Together AI", "https://api.together.xyz/v1"},
	{"mistral", "Mistral", "https://api.mistral.ai/v1"}, {"opencode-go", "OpenCode Go", "https://opencode.ai/zen/go/v1"},
	{"custom", "Custom", "https://api.example.com/v1"},
}

type setupWizard struct {
	ctx         context.Context
	in          io.Reader
	out, errOut io.Writer
	reader      *bufio.Reader
}

func runSetupCommand(ctx context.Context, args []string, in io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			_, _ = fmt.Fprintln(stdout, "Usage: caretz setup\n\nInteractively configure a local or hosted OpenAI-compatible provider,\nthen optionally web search keys (Tavily, Brave) and an image analysis model.")
			return 0
		}
		output.Errorf(stderr, "usage: caretz setup")
		return 2
	}
	path, err := config.HomeConfigPath()
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	w := &setupWizard{ctx: ctx, in: in, out: stdout, errOut: stderr, reader: bufio.NewReader(in)}
	if err := w.run(path); err != nil {
		if errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintln(stderr, "Setup cancelled; configuration was not changed.")
			return 1
		}
		output.Errorf(stderr, "%v", err)
		return 1
	}
	return 0
}

func (w *setupWizard) run(path string) error {
	_, _ = fmt.Fprintln(w.out, "Caretz setup")
	mode, err := w.choose("Provider type", []string{
		"Local provider (I already run a model server)",
		"Hosted provider",
	}, 0)
	if err != nil {
		return err
	}
	local := mode == 0
	var preset setupPreset
	if local {
		labels := make([]string, len(localSetupPresets))
		for i, p := range localSetupPresets {
			labels[i] = p.name
		}
		choice, err := w.choose("Local provider", labels, 0)
		if err != nil {
			return err
		}
		preset = localSetupPresets[choice]
	} else {
		labels := make([]string, len(hostedSetupPresets))
		for i, p := range hostedSetupPresets {
			labels[i] = p.name
		}
		choice, err := w.choose("Hosted provider", labels, 0)
		if err != nil {
			return err
		}
		preset = hostedSetupPresets[choice]
	}
	for {
		key, err := w.prompt("Provider key", preset.key)
		if err != nil {
			return err
		}
		name, err := w.prompt("Display name", preset.name)
		if err != nil {
			return err
		}
		baseURL, err := w.prompt("Base URL", preset.baseURL)
		if err != nil {
			return err
		}
		apiKey, err := w.secret("API token (input hidden; empty allowed for local)")
		if err != nil {
			return err
		}
		if local && strings.TrimSpace(apiKey) == "" {
			apiKey = "noset"
		}
		candidate := config.SetupProvider{Key: strings.TrimSpace(key), Name: strings.TrimSpace(name), BaseURL: strings.TrimSpace(baseURL), APIKey: apiKey}
		var modelID string
		var verifiedModels []string
		editConnection := false
		for {
			_, _ = fmt.Fprintf(w.out, "\nTesting connection to %s at %s...\n", candidate.Name, candidate.BaseURL)
			models, discoveryErr := w.discover(candidate)
			if discoveryErr == nil && len(models) > 0 {
				_, _ = fmt.Fprintf(w.out, "Connection successful; found %d model(s).\n", len(models))
				choice, err := w.choose("Model", models, 0)
				if err != nil {
					return err
				}
				modelID = models[choice]
				verifiedModels = models
				break
			}

			_, _ = fmt.Fprintln(w.errOut, setupConnectionFailure(candidate, preset.key, discoveryErr))
			action, err := w.choose("Next action", []string{"Retry connection test", "Edit connection details", "Enter model manually and save unverified", "Cancel"}, 0)
			if err != nil {
				return err
			}
			switch action {
			case 0:
				continue
			case 1:
				preset = setupPreset{candidate.Key, candidate.Name, candidate.BaseURL}
				editConnection = true
			case 2:
				modelID, err = w.prompt("Model ID (unverified)", "")
				if err != nil {
					return err
				}
				yes, err := w.confirm("Connection test did not pass. Save this unverified configuration?")
				if err != nil {
					return err
				}
				if !yes {
					return io.EOF
				}
				output.Warnf(w.errOut, "saving an unverified provider configuration.")
			case 3:
				return io.EOF
			}
			break
		}
		if editConnection {
			continue
		}
		candidate.ModelID = strings.TrimSpace(modelID)
		if local {
			for {
				value, err := w.prompt("Context window", "32768")
				if err != nil {
					return err
				}
				candidate.ContextWindow, err = strconv.Atoi(strings.TrimSpace(value))
				if err == nil && candidate.ContextWindow > 0 {
					break
				}
				_, _ = fmt.Fprintln(w.errOut, "Context window must be a positive integer.")
			}
		}
		isLocal := false
		if local {
			_, _ = fmt.Fprintln(w.out, "Local/self-hosted models participate in \"Use local\" and \"Always local here\".")
			defaultChoice := 1
			if config.IsLocalEndpoint(candidate.BaseURL) {
				defaultChoice = 0
			}
			choice, err := w.choose("Model location", []string{"Local / self-hosted — this machine or another server you control", "Hosted service — treat as cloud"}, defaultChoice)
			if err != nil {
				return err
			}
			isLocal = choice == 0
			if u, err := url.Parse(candidate.BaseURL); err == nil && isLocal && u.Scheme == "http" && !config.IsLocalEndpoint(candidate.BaseURL) {
				_, _ = fmt.Fprintln(w.errOut, "Warning: workspace content travels unencrypted over HTTP to this server.")
			}
		}
		candidate.Local = &isLocal
		previousImageModel := setupLocalImageModel(path)
		if err := w.save(path, candidate); err != nil {
			return err
		}
		if len(verifiedModels) > 0 {
			cachePath := config.ModelCachePath(path)
			err := config.UpdateModelCache(cachePath, func(cache *config.ModelCache) {
				cache.SetCachedModels(candidate.Key, candidate.BaseURL, verifiedModels, time.Now())
			})
			if err != nil {
				_, _ = fmt.Fprintf(w.errOut, "Warning: could not cache the model list: %v\n", err)
			}
		}
		if err := w.optionalTools(path, candidate, verifiedModels, previousImageModel); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w.out, "Run caretz to get started.")
		return nil
	}
}

// optionalTools offers web search and image analysis once the provider is
// saved. Cancelling here skips the remaining steps but keeps the provider.
func (w *setupWizard) optionalTools(path string, provider config.SetupProvider, models []string, previousImageModel string) error {
	err := w.setupWebSearch(path)
	if err == nil {
		err = w.setupLocalImage(path, provider, models, previousImageModel)
	}
	if errors.Is(err, io.EOF) {
		_, _ = fmt.Fprintln(w.out, "\nSkipped the remaining optional steps; the provider is saved.")
		return nil
	}
	return err
}

var setupSearchServices = []struct{ key, name, hint string }{
	{"tavily", "Tavily", "https://app.tavily.com"},
	{"brave", "Brave Search", "https://brave.com/search/api/"},
}

func (w *setupWizard) setupWebSearch(path string) error {
	credentialsPath := config.CredentialsPath(path)
	creds, err := config.LoadCredentials(credentialsPath)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w.out, "\nWeb search lets caretz look up current docs and error messages. WebFetch works without a key.")
	for _, service := range setupSearchServices {
		if creds.Search[service.key] != "" {
			_, _ = fmt.Fprintf(w.out, "%s key already stored (%s).\n", service.name, config.MaskSecret(creds.Search[service.key]))
		}
	}
	choice, err := w.choose("Web search", []string{
		"Skip",
		"Tavily (search, plus cleaner page fetching)",
		"Brave Search",
		"Both (searches with Brave, fetches pages with Tavily)",
	}, 0)
	if err != nil || choice == 0 {
		return err
	}
	selected := setupSearchServices
	if choice <= len(setupSearchServices) {
		selected = setupSearchServices[choice-1 : choice]
	}
	for _, service := range selected {
		key, err := w.secret(fmt.Sprintf("%s API key from %s (input hidden; empty skips)", service.name, service.hint))
		if err != nil {
			return err
		}
		if key == "" {
			_, _ = fmt.Fprintf(w.out, "No %s key entered; left unchanged.\n", service.name)
			continue
		}
		if err := config.SaveSearchCredential(credentialsPath, service.key, key); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w.out, "Saved %s key to %s\n", service.name, credentialsPath)
	}
	return nil
}

// setupLocalImage points LocalImage at a model of the provider just saved.
// Keeping a previous choice re-adds its model to this provider's catalog,
// which saving the provider replaced.
func (w *setupWizard) setupLocalImage(path string, provider config.SetupProvider, models []string, previous string) error {
	_, _ = fmt.Fprintln(w.out, "\nImage analysis (LocalImage) lets a text-only coding model read screenshots through a vision model.")
	selected := provider.Key + "/" + provider.ModelID
	options := []string{"Skip", "Use " + selected + " (pick this only if it accepts images)", "Choose another " + provider.Name + " model"}
	if previous != "" {
		options[0] = "Keep the current image model (" + previous + ")"
	}
	choice, err := w.choose("Image analysis", options, 0)
	if err != nil {
		return err
	}
	modelID := provider.ModelID
	switch choice {
	case 0:
		providerKey, previousModel, _ := strings.Cut(previous, "/")
		if providerKey != provider.Key {
			return nil
		}
		modelID = previousModel
	case 2:
		if len(models) > 0 {
			index, err := w.choose("Vision model", models, 0)
			if err != nil {
				return err
			}
			modelID = models[index]
		} else if modelID, err = w.prompt("Vision model ID", ""); err != nil {
			return err
		}
		if strings.TrimSpace(modelID) == "" {
			_, _ = fmt.Fprintln(w.out, "No model entered; image analysis left unchanged.")
			return nil
		}
	}
	if err := config.SaveLocalImageModel(path, provider.Key, modelID); err != nil {
		return err
	}
	if choice != 0 {
		_, _ = fmt.Fprintf(w.out, "Image analysis uses %s/%s\n", provider.Key, strings.TrimSpace(modelID))
	}
	return nil
}

// setupLocalImageModel returns the general profile's localImage model
// reference in the config at path, or "" when none is set or the config can't
// be read.
func setupLocalImageModel(path string) string {
	cfg, err := config.Load(path, "")
	if err != nil {
		return ""
	}
	if endpoint, _ := cfg.Profiles["general"].ToolConfig["localImage"]["endpoint"].(string); strings.TrimSpace(endpoint) != "" {
		return ""
	}
	model, _ := cfg.Profiles["general"].ToolConfig["localImage"]["model"].(string)
	return strings.TrimSpace(model)
}

func (w *setupWizard) save(path string, candidate config.SetupProvider) error {
	err := config.SaveSetupProvider(path, candidate, false)
	if errors.Is(err, config.ErrProviderExists) {
		yes, askErr := w.confirm(fmt.Sprintf("Provider %q exists. Replace it?", candidate.Key))
		if askErr != nil {
			return askErr
		}
		if !yes {
			return io.EOF
		}
		err = config.SaveSetupProvider(path, candidate, true)
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w.out, "\nSaved %s\n", path)
	if key := strings.TrimSpace(candidate.APIKey); key != "" && key != config.NoAPIKey {
		_, _ = fmt.Fprintf(w.out, "Saved API key to %s\n", config.CredentialsPath(path))
	}
	_, _ = fmt.Fprintf(w.out, "Selected %s/%s\n", candidate.Key, candidate.ModelID)
	return nil
}

func (w *setupWizard) discover(c config.SetupProvider) ([]string, error) {
	u, err := url.ParseRequestURI(strings.TrimSpace(c.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: use a complete http:// or https:// URL", errInvalidSetupBaseURL)
	}
	ctx, cancel := context.WithTimeout(w.ctx, setupConnectionTimeout)
	defer cancel()
	models, err := llm.ListOpenAIModels(ctx, c.APIKey, c.BaseURL, llm.OpenAIProviderOptions{RequestTimeout: setupConnectionTimeout, MaxAttempts: 1})
	if err != nil {
		return nil, err
	}
	clean := models[:0]
	for _, model := range models {
		if strings.TrimSpace(model) != "" {
			clean = append(clean, strings.TrimSpace(model))
		}
	}
	sort.Strings(clean)
	return clean, nil
}

func setupConnectionFailure(candidate config.SetupProvider, presetKey string, err error) string {
	prefix := fmt.Sprintf("Connection test failed for %s at %s: ", candidate.Name, candidate.BaseURL)
	if err == nil {
		return prefix + localSetupHint(presetKey, "connected successfully, but no models were advertised; load or pull a model, then retry")
	}
	if errors.Is(err, errInvalidSetupBaseURL) {
		return prefix + "the Base URL is invalid; enter a complete http:// or https:// URL"
	}
	safeErr := credentialSafeError(err, candidate.APIKey)
	if errors.Is(err, context.DeadlineExceeded) {
		return prefix + "the request timed out after 5 seconds; the server may still be starting, so wait a moment and retry"
	}
	if status, ok := llm.APIErrorStatus(err); ok {
		switch status {
		case 401, 403:
			return fmt.Sprintf("%sauthentication failed (HTTP %d); check the API token and the server's authentication settings", prefix, status)
		case 404:
			return prefix + "the OpenAI-compatible /models endpoint was not found (HTTP 404); check the Base URL, which normally ends in /v1"
		default:
			return fmt.Sprintf("%sthe provider returned HTTP %d: %v", prefix, status, safeErr)
		}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return prefix + localSetupHint(presetKey, "could not connect; make sure the server is running and listening at this URL")
	}
	if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "decode models response") {
		return prefix + "the endpoint responded, but /models did not return valid OpenAI-compatible JSON"
	}
	return fmt.Sprintf("%s%v", prefix, safeErr)
}

func localSetupHint(presetKey, message string) string {
	switch presetKey {
	case "ollama":
		return message + " (start Ollama and pull a model first)"
	case "lmstudio":
		return message + " (start the LM Studio server and load a model first)"
	case "llama.cpp":
		return message + " (start llama-server with a model first)"
	default:
		return message
	}
}

func (w *setupWizard) prompt(label, defaultValue string) (string, error) {
	if defaultValue == "" {
		_, _ = fmt.Fprintf(w.out, "%s: ", label)
	} else {
		_, _ = fmt.Fprintf(w.out, "%s [%s]: ", label, defaultValue)
	}
	line, err := w.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	line = strings.TrimSpace(line)
	if strings.ContainsAny(line, "\x1b\x03") {
		return "", io.EOF
	}
	if line == "" {
		line = defaultValue
	}
	return line, nil
}
func (w *setupWizard) secret(label string) (string, error) {
	_, _ = fmt.Fprintf(w.out, "%s: ", label)
	if f, ok := w.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		value, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(w.out)
		return strings.TrimSpace(string(value)), err
	}
	line, err := w.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
func (w *setupWizard) choose(label string, options []string, defaultIndex int) (int, error) {
	_, _ = fmt.Fprintf(w.out, "\n%s:\n", label)
	for i, option := range options {
		_, _ = fmt.Fprintf(w.out, "  %d) %s\n", i+1, option)
	}
	for {
		value, err := w.prompt("Choose", strconv.Itoa(defaultIndex+1))
		if err != nil {
			return 0, err
		}
		n, convErr := strconv.Atoi(value)
		if convErr == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		_, _ = fmt.Fprintln(w.errOut, "Enter one of the listed numbers.")
	}
}
func (w *setupWizard) confirm(label string) (bool, error) {
	value, err := w.prompt(label+" (y/N)", "n")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(value, "y") || strings.EqualFold(value, "yes"), nil
}
func credentialSafeError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
}

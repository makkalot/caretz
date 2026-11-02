package app

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/chatgptauth"
	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/lib/llm"
	"golang.org/x/term"
)

// searchCredentialPrefix names web search keys in `caretz auth`, as in
// search/tavily, so they never collide with provider keys.
const searchCredentialPrefix = "search/"

func newAuthCLICommand(env cliEnvironment) *clicmd.Command {
	parent := &clicmd.Command{Use: "caretz auth", Short: "Store, remove, and list the API keys caretz keeps in credentials.json, or sign in with ChatGPT"}
	run := func(action func(authCommand, []string) int) func(context.Context, *clicmd.Result) int {
		return func(_ context.Context, result *clicmd.Result) int {
			homePath, err := config.HomeConfigPath()
			if err != nil {
				_, _ = fmt.Fprintf(env.stderr, "error: %v\n", err)
				return 1
			}
			cmd := authCommand{homePath: homePath, in: env.stdin, out: env.stdout, errOut: env.stderr}
			if opts, ok := result.Options.Value.(*configRootOptions); ok {
				cmd.projectPath, cmd.projectTrusted, err = opts.projectConfig(result)
				if err != nil {
					_, _ = fmt.Fprintf(env.stderr, "error: %v\n", err)
					return 1
				}
			}
			return action(cmd, result.Args)
		}
	}
	set := &clicmd.Command{
		Use: "caretz auth set NAME", Short: "Save the API key for a provider, or search/tavily or search/brave, read from the terminal or stdin", Args: clicmd.ExactArgs(1),
		Run: run(authCommand.set),
	}
	rm := &clicmd.Command{
		Use: "caretz auth rm NAME", Short: "Delete a stored API key", Args: clicmd.ExactArgs(1),
		Run: run(authCommand.rm),
	}
	list := &clicmd.Command{
		Use: "caretz auth list", Short: "Show which providers have a stored API key, with keys masked", Options: &configRootOptions{}, Args: clicmd.NoArgs,
		Run: run(authCommand.list),
	}
	login := &clicmd.Command{
		Use: "caretz auth login SERVICE [PROVIDER]", Short: "Sign in to a subscription (" + signInServiceNames() + ") in the browser and use your plan instead of API credits; PROVIDER defaults to SERVICE", Args: clicmd.RangeArgs(1, 2),
		Run: run(authCommand.login),
	}
	logout := &clicmd.Command{
		Use: "caretz auth logout PROVIDER", Short: "Sign out of a subscription and revoke caretz's access", Args: clicmd.ExactArgs(1),
		Run: run(authCommand.logout),
	}
	parent.AddCommand(set, rm, list, login, logout)
	return parent
}

// chatGPTLoginTimeout bounds how long login waits for the browser.
const chatGPTLoginTimeout = 10 * time.Minute

// signInService is a subscription caretz signs in to in the browser instead
// of storing an API key. Its name in signInServices is the provider's "auth"
// value.
type signInService struct {
	title   string
	baseURL string
	signIn  func(ctx context.Context, homePath, providerKey string, out, errOut io.Writer) (models []string, exists bool, err error)
}

var signInServices = map[string]signInService{
	config.AuthChatGPT: {title: "ChatGPT", baseURL: config.ChatGPTBaseURL, signIn: signInWithChatGPT},
}

func signInServiceNames() string {
	return strings.Join(slices.Sorted(maps.Keys(signInServices)), ", ")
}

func (c authCommand) login(args []string) int {
	serviceName := strings.TrimSpace(args[0])
	service, ok := signInServices[serviceName]
	if !ok {
		_, _ = fmt.Fprintf(c.errOut, "error: unknown sign-in service %q; available: %s\n", serviceName, signInServiceNames())
		return 1
	}
	name := serviceName
	if len(args) > 1 {
		name = strings.TrimSpace(args[1])
	}
	if err := validateProviderName(name); err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatGPTLoginTimeout)
	defer cancel()
	models, exists, err := service.signIn(ctx, c.homePath, name, c.out, c.errOut)
	if err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	if !exists {
		if len(models) == 0 {
			_, _ = fmt.Fprintf(c.errOut, "error: no models to add; add provider %q with \"auth\": %q to %s by hand\n", name, serviceName, c.homePath)
			return 1
		}
		err := config.SaveSetupProvider(c.homePath, config.SetupProvider{
			Key: name, Name: service.title, BaseURL: service.baseURL, ModelID: models[0],
			Auth: serviceName, KeepActiveModel: true,
		}, false)
		if err != nil {
			_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(c.out, "Added provider %s to %s.\n", name, c.homePath)
	}
	if len(models) > 0 {
		_, _ = fmt.Fprintf(c.out, "Models on your plan: %s\nUse one with caretz --model %s/%s, or /models in a session.\n", strings.Join(models, ", "), name, models[0])
	}
	return 0
}

// signInWithChatGPT signs provider name in through the browser, saves the
// tokens, and returns the models the plan offers, caching them for /models.
// exists reports whether name was already a ChatGPT provider; one that uses
// an API key is refused, so signing in never overwrites its key. When the
// models can't be listed it prints a warning and returns none.
func signInWithChatGPT(ctx context.Context, homePath, name string, out, errOut io.Writer) (models []string, exists bool, err error) {
	home, err := config.LoadProfileWithTrust(homePath, "", "", false)
	if err != nil {
		return nil, false, err
	}
	provider, exists := home.Provider[name]
	if exists && provider.Auth != config.AuthChatGPT {
		return nil, false, fmt.Errorf("provider %q already uses an API key; sign in under another name with %s NAME", name, config.SignInCommand(config.AuthChatGPT, config.AuthChatGPT))
	}
	credentialsPath := config.CredentialsPath(homePath)
	creds, err := config.LoadCredentials(credentialsPath)
	if err != nil {
		return nil, false, err
	}
	cred, err := chatgptauth.NewClient().Login(ctx, chatgptauth.LoginOptions{
		Previous: creds.ProviderOAuth(name),
		Out:      out,
		OnRegistered: func(registered config.OAuthCredential) error {
			return config.SaveProviderOAuth(credentialsPath, name, registered)
		},
	})
	if err != nil {
		return nil, false, err
	}
	if err := config.SaveProviderOAuth(credentialsPath, name, cred); err != nil {
		return nil, false, err
	}
	_, _ = fmt.Fprintf(out, "Signed in to ChatGPT as %s.\n", cmp.Or(cred.Email, cred.Subject))
	if !cred.HasScope(chatgptauth.PlanScope) {
		return nil, false, fmt.Errorf("plan usage was not allowed, so caretz cannot use this account's ChatGPT plan; run %s again and allow it", config.SignInCommand(config.AuthChatGPT, name))
	}
	models, err = chatgptauth.ListModels(ctx, config.OAuthKeyRef(name), config.ChatGPTBaseURL, llm.OpenAIProviderOptions{RequestTimeout: setupConnectionTimeout, MaxAttempts: 1})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "Warning: could not list your plan's models: %v\n", err)
		return nil, exists, nil
	}
	if err := config.UpdateModelCache(config.ModelCachePath(homePath), func(cache *config.ModelCache) {
		cache.SetCachedModels(name, config.ChatGPTBaseURL, models, time.Now())
	}); err != nil {
		_, _ = fmt.Fprintf(errOut, "Warning: could not cache the model list: %v\n", err)
	}
	return models, exists, nil
}

func (c authCommand) logout(args []string) int {
	name := strings.TrimSpace(args[0])
	creds, err := config.LoadCredentials(c.credentialsPath())
	if err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	cred := creds.ProviderOAuth(name)
	if !cred.SignedIn() {
		_, _ = fmt.Fprintf(c.errOut, "error: %s is not signed in\n", name)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupConnectionTimeout)
	defer cancel()
	revokeErr := chatgptauth.NewClient().Revoke(ctx, *cred)
	// The registration stays so signing in again reuses it; only the
	// tokens go, even when OpenAI could not confirm the revocation.
	_, err = config.UpdateProviderOAuth(c.credentialsPath(), name, func(cur *config.OAuthCredential) (bool, error) {
		cur.AccessToken, cur.RefreshToken, cur.ExpiresAt, cur.Scope = "", "", time.Time{}, ""
		return true, nil
	})
	if err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	if revokeErr != nil {
		_, _ = fmt.Fprintf(c.errOut, "Signed out of ChatGPT locally, but OpenAI did not confirm the revocation (%v). You can also disconnect caretz in ChatGPT Settings.\n", revokeErr)
		return 0
	}
	_, _ = fmt.Fprintf(c.out, "Signed out of ChatGPT for %s.\n", name)
	return 0
}

func validateProviderName(name string) error {
	if name == "" || strings.ContainsAny(name, "/ \t") {
		return fmt.Errorf("provider name %q must be a provider key from config.json, or one of %s", name, searchCredentialNames())
	}
	return nil
}

type authCommand struct {
	homePath, projectPath string
	projectTrusted        bool
	in                    io.Reader
	out, errOut           io.Writer
}

func (c authCommand) credentialsPath() string {
	return config.CredentialsPath(c.homePath)
}

func (c authCommand) set(args []string) int {
	name := strings.TrimSpace(args[0])
	key, err := c.readKey(name)
	if err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: read API key: %v\n", err)
		return 1
	}
	if key == "" {
		_, _ = fmt.Fprintln(c.errOut, "error: API key must not be empty; use caretz auth rm to delete one")
		return 1
	}
	if err := c.save(name, key); err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(c.out, "Saved API key for %s to %s\n", name, c.credentialsPath())
	return 0
}

func (c authCommand) rm(args []string) int {
	name := strings.TrimSpace(args[0])
	creds, err := config.LoadCredentials(c.credentialsPath())
	if err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	_, found := creds.Provider[name]
	if service, ok := strings.CutPrefix(name, searchCredentialPrefix); ok {
		_, found = creds.Search[service]
	}
	if !found {
		_, _ = fmt.Fprintf(c.errOut, "error: no API key stored for %s\n", name)
		return 1
	}
	if err := c.save(name, ""); err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(c.out, "Deleted API key for %s\n", name)
	return 0
}

func (c authCommand) save(name, key string) error {
	if service, ok := strings.CutPrefix(name, searchCredentialPrefix); ok {
		if !slices.Contains(config.SearchProviders, service) {
			return fmt.Errorf("unknown search service %q; use one of %s", service, searchCredentialNames())
		}
		return config.SaveSearchCredential(c.credentialsPath(), service, key)
	}
	if err := validateProviderName(name); err != nil {
		return err
	}
	return config.SaveProviderCredential(c.credentialsPath(), name, key)
}

func (c authCommand) readKey(name string) (string, error) {
	if f, ok := c.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		_, _ = fmt.Fprintf(c.out, "API key for %s (input hidden): ", name)
		value, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(c.out)
		return strings.TrimSpace(string(value)), err
	}
	line, err := bufio.NewReader(c.in).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (c authCommand) list([]string) int {
	creds, err := config.LoadCredentials(c.credentialsPath())
	if err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	cfg, err := config.LoadProfileWithTrust(c.homePath, c.projectPath, "", c.projectTrusted)
	if err != nil {
		_, _ = fmt.Fprintf(c.errOut, "error: %v\n", err)
		return 1
	}
	rows := map[string]string{}
	for key := range cfg.Provider {
		rows[key] = "not set"
	}
	for key, stored := range creds.Provider {
		rows[key] = "credentials.json " + config.MaskSecret(stored.APIKey)
		if stored.OAuth != nil {
			rows[key] = chatGPTSignInStatus(stored.OAuth)
		}
	}
	for _, service := range config.SearchProviders {
		if stored, ok := creds.Search[service]; ok {
			rows[searchCredentialPrefix+service] = "credentials.json " + config.MaskSecret(stored)
		}
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintf(c.out, "No API keys configured. Run caretz auth set NAME to store one in %s\n", c.credentialsPath())
		return 0
	}
	names := make([]string, 0, len(rows))
	width := 0
	for name := range rows {
		names = append(names, name)
		width = max(width, len(name))
	}
	sort.Strings(names)
	for _, name := range names {
		_, _ = fmt.Fprintf(c.out, "%-*s  %s\n", width, name, rows[name])
	}
	return 0
}

func chatGPTSignInStatus(cred *config.OAuthCredential) string {
	switch {
	case !cred.SignedIn():
		return "ChatGPT, signed out"
	case !cred.HasScope(chatgptauth.PlanScope):
		return "ChatGPT " + cred.Email + ", plan usage not allowed"
	default:
		return "ChatGPT " + cred.Email + ", plan usage"
	}
}

func searchCredentialNames() string {
	names := make([]string, 0, len(config.SearchProviders))
	for _, service := range config.SearchProviders {
		names = append(names, searchCredentialPrefix+service)
	}
	return strings.Join(names, ", ")
}

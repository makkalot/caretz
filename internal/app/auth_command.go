package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/config"
	"golang.org/x/term"
)

// searchCredentialPrefix names web search keys in `caretz auth`, as in
// search/tavily, so they never collide with provider keys.
const searchCredentialPrefix = "search/"

func newAuthCLICommand(env cliEnvironment) *clicmd.Command {
	parent := &clicmd.Command{Use: "caretz auth", Short: "Store, remove, and list the API keys caretz keeps in credentials.json"}
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
	parent.AddCommand(set, rm, list)
	return parent
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
	if name == "" || strings.ContainsAny(name, "/ \t") {
		return fmt.Errorf("provider name %q must be a provider key from config.json, or one of %s", name, searchCredentialNames())
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

func searchCredentialNames() string {
	names := make([]string, 0, len(config.SearchProviders))
	for _, service := range config.SearchProviders {
		names = append(names, searchCredentialPrefix+service)
	}
	return strings.Join(names, ", ")
}

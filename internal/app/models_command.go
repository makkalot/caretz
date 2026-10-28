package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/clicmd"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/output"
)

// modelsProbeTimeout bounds each provider's /models request during an
// explicit refresh or prune.
const modelsProbeTimeout = 3 * time.Second

type modelsCommandOptions struct {
	configRootOptions
	Refresh bool     `flag:"refresh" help:"Ask every provider for its models and update the cache that startup and /models read" group:"Models"`
	Prune   bool     `flag:"prune" help:"Walk through unreachable providers in your home config and choose which to delete; config root providers are never removed" group:"Models"`
	Remove  []string `flag:"remove" help:"Delete a provider from your home config with its API key and the model rules, goal roles and schedule models that use it; repeatable" group:"Models"`
}

// configRootOptions lets a command see the providers a --config-root or
// CARETZ_CONFIG_ROOT config adds, as a normal run does.
type configRootOptions struct {
	ConfigRoot string `flag:"config-root" help:"Directory whose config.json adds providers; overrides CARETZ_CONFIG_ROOT" group:"Config"`
}

// projectConfig resolves the project config.json the way a normal run does,
// and reports whether it came from an explicit config root and so may define
// providers.
func (o configRootOptions) projectConfig(result *clicmd.Result) (path string, trusted bool, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false, err
	}
	set := result.Options.Changed("config-root")
	root, err := resolveConfigRoot(cwd, o.ConfigRoot, configRootFlagState{ConfigRootSet: set}, os.Getenv)
	if err != nil {
		return "", false, err
	}
	trusted = set || strings.TrimSpace(os.Getenv("CARETZ_CONFIG_ROOT")) != ""
	return filepath.Join(root, "config.json"), trusted, nil
}

func newModelsCLICommand(env cliEnvironment) *clicmd.Command {
	return &clicmd.Command{
		Use: "caretz models", Short: "List configured providers and their models, or remove providers", Options: &modelsCommandOptions{}, Args: clicmd.NoArgs,
		Run: func(ctx context.Context, result *clicmd.Result) int {
			opts := result.Options.Value.(*modelsCommandOptions)
			homePath, err := config.HomeConfigPath()
			if err != nil {
				output.Errorf(env.stderr, "%v", err)
				return 1
			}
			projectPath, projectTrusted, err := opts.projectConfig(result)
			if err != nil {
				_, _ = fmt.Fprintf(env.stderr, "error: %v\n", err)
				return 1
			}
			cmd := modelsCommand{homePath: homePath, projectPath: projectPath, projectTrusted: projectTrusted, in: env.stdin, out: env.stdout, errOut: env.stderr, timeout: modelsProbeTimeout, now: time.Now}
			modes := 0
			for _, set := range []bool{opts.Prune, opts.Refresh, len(opts.Remove) > 0} {
				if set {
					modes++
				}
			}
			switch {
			case modes > 1:
				return cmd.fail(errors.New("--prune, --refresh and --remove cannot be combined"))
			case len(opts.Remove) > 0:
				return cmd.remove(opts.Remove)
			case opts.Prune:
				return cmd.prune(ctx)
			case opts.Refresh:
				return cmd.refresh(ctx)
			}
			return cmd.list()
		},
	}
}

type modelsCommand struct {
	homePath, projectPath string
	projectTrusted        bool
	in                    io.Reader
	out, errOut           io.Writer
	timeout               time.Duration
	now                   func() time.Time
}

func (c modelsCommand) cachePath() string {
	return config.ModelCachePath(c.homePath)
}

// load reads the home config and the project config, which may add
// providers only when it comes from an explicit config root.
func (c modelsCommand) load() (config.Config, error) {
	return config.LoadProfileWithTrust(c.homePath, c.projectPath, "", c.projectTrusted)
}

func (c modelsCommand) fail(err error) int {
	output.Errorf(c.errOut, "%v", err)
	return 1
}

// list prints every provider with the models startup will offer, from config
// and the cache, without contacting any provider. "*" marks the active model.
func (c modelsCommand) list() int {
	cfg, err := c.load()
	if err != nil {
		return c.fail(err)
	}
	if len(cfg.Provider) == 0 {
		_, _ = fmt.Fprintln(c.out, "No providers configured; run caretz setup to add one.")
		return 0
	}
	cache, err := config.LoadModelCache(c.cachePath())
	if err != nil {
		_, _ = fmt.Fprintf(c.errOut, "Warning: %v\n", err)
	}
	merged := cfg.WithDiscoveredModels(cache.DiscoveredFor(cfg))
	style := output.NewStyle(c.out)
	never := 0
	for i, key := range sortedProviderKeys(cfg) {
		if i > 0 {
			_, _ = fmt.Fprintln(c.out)
		}
		status := style.Warning(output.GlyphPending + " never fetched")
		if entry, ok := cache.Entry(key, cfg.Provider[key]); ok {
			status = style.Muted("fetched " + formatAge(c.now().Sub(entry.FetchedAt)) + " ago")
		} else {
			never++
		}
		_, _ = fmt.Fprintf(c.out, "%s  %s  %s\n", style.Bold(key), status, style.Muted(providerBaseURL(cfg.Provider[key])))
		c.printModels(cfg, merged, key, style)
	}
	if never > 0 {
		_, _ = fmt.Fprintf(c.out, "\n%s\n", style.Muted("fetch the models providers advertise with: caretz models --refresh"))
	}
	return 0
}

// refresh asks every provider for its models, caches the answers, and prints
// each provider's reachability. A failed provider keeps its cached models, so
// refreshing away from a home server does not forget them.
func (c modelsCommand) refresh(ctx context.Context) int {
	cfg, err := c.load()
	if err != nil {
		return c.fail(err)
	}
	if len(cfg.Provider) == 0 {
		_, _ = fmt.Fprintln(c.out, "No providers configured; run caretz setup to add one.")
		return 0
	}
	probes := probeProviders(ctx, cfg, c.timeout)
	now := c.now()
	err = config.UpdateModelCache(c.cachePath(), func(cache *config.ModelCache) {
		for _, probe := range probes {
			if probe.Err == nil {
				cache.SetCachedModels(probe.Key, providerBaseURL(probe.Provider), probe.Models, now)
			}
		}
	})
	if err != nil {
		return c.fail(err)
	}
	cache, _ := config.LoadModelCache(c.cachePath())
	merged := cfg.WithDiscoveredModels(cache.DiscoveredFor(cfg))
	style := output.NewStyle(c.out)
	failed := 0
	for i, probe := range probes {
		if i > 0 {
			_, _ = fmt.Fprintln(c.out)
		}
		status := style.Success(fmt.Sprintf("%s online, %d models", output.GlyphOnline, len(probe.Models)))
		if probe.Err != nil {
			status = style.Warning(output.GlyphWarning + " unreachable: " + providerProbeReason(probe))
			if !probeUnreachable(probe) {
				status = style.Warning(output.GlyphWarning + " " + providerProbeReason(probe))
			}
			failed++
		}
		_, _ = fmt.Fprintf(c.out, "%s  %s  %s\n", style.Bold(probe.Key), status, style.Muted(providerBaseURL(probe.Provider)))
		if probe.Err != nil {
			_, _ = fmt.Fprintf(c.out, "  %s\n", style.Muted("error: "+probe.Err.Error()))
		}
		c.printModels(cfg, merged, probe.Key, style)
	}
	_, _ = fmt.Fprintf(c.out, "\n%s\n", style.Muted("Saved "+c.cachePath()))
	if failed > 0 {
		_, _ = fmt.Fprintf(c.out, "%s\n", style.Muted(fmt.Sprintf("%d of %d providers failed and kept their cached models; remove ones you no longer use with: caretz models --prune", failed, len(probes))))
	}
	if failed == len(probes) {
		return 1
	}
	return 0
}

func (c modelsCommand) printModels(cfg, merged config.Config, providerKey string, style output.Style) {
	models := make([]string, 0, len(merged.Provider[providerKey].Models))
	for key := range merged.Provider[providerKey].Models {
		models = append(models, key)
	}
	sort.Strings(models)
	for _, model := range models {
		ref := providerKey + "/" + model
		if ref == cfg.ActiveModel {
			_, _ = fmt.Fprintf(c.out, "  %s\n", style.Prompt("* "+ref))
			continue
		}
		_, _ = fmt.Fprintf(c.out, "    %s\n", ref)
	}
	if len(models) == 0 {
		_, _ = fmt.Fprintln(c.out, style.Muted("    (no models)"))
	}
}

func sortedProviderKeys(cfg config.Config) []string {
	keys := make([]string, 0, len(cfg.Provider))
	for key := range cfg.Provider {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// formatAge renders d coarsely, e.g. "3m", "5h" or "2d".
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// prune asks, provider by provider, whether to delete each unreachable
// provider from the home config, then writes all deletions at once.
func (c modelsCommand) prune(ctx context.Context) int {
	home, err := config.Load(c.homePath, "")
	if err != nil {
		return c.fail(err)
	}
	var candidates []providerProbe
	for _, probe := range probeProviders(ctx, home, c.timeout) {
		// An HTTP error such as 401 means the server answered, so it stays.
		if probeUnreachable(probe) {
			candidates = append(candidates, probe)
		}
	}
	if len(candidates) == 0 {
		_, _ = fmt.Fprintf(c.out, "All %d providers in %s are reachable; nothing to prune.\n", len(home.Provider), c.homePath)
		return 0
	}
	_, _ = fmt.Fprintf(c.out, "%d unreachable providers in %s.\n", len(candidates), c.homePath)
	w := &setupWizard{in: c.in, out: c.out, errOut: c.errOut, reader: bufio.NewReader(c.in)}
	activeProvider, _, _ := strings.Cut(home.ActiveModel, "/")
	var remove []string
	for _, probe := range candidates {
		_, _ = fmt.Fprintf(c.out, "\n%s  %s\n  unreachable: %v\n", probe.Key, providerBaseURL(probe.Provider), probe.Err)
		if n := len(probe.Provider.Models); n > 0 {
			_, _ = fmt.Fprintf(c.out, "  %d configured models\n", n)
		}
		if probe.Key == activeProvider {
			_, _ = fmt.Fprintf(c.out, "  note: your active model %s uses this provider\n", home.ActiveModel)
		}
		yes, err := w.confirm("Delete " + probe.Key + "?")
		if errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintln(c.errOut, "\nPrune cancelled; configuration was not changed.")
			return 1
		}
		if err != nil {
			return c.fail(err)
		}
		if yes {
			remove = append(remove, probe.Key)
		}
	}
	if len(remove) == 0 {
		_, _ = fmt.Fprintln(c.out, "\nKept every provider; configuration was not changed.")
		return 0
	}
	_, _ = fmt.Fprintln(c.out)
	return c.removeProviders(remove)
}

// remove deletes the named providers from the home config. A provider that
// only a config root defines is refused, as prune never touches that file.
func (c modelsCommand) remove(keys []string) int {
	home, err := config.Load(c.homePath, "")
	if err != nil {
		return c.fail(err)
	}
	seen := map[string]bool{}
	var unique []string
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := home.Provider[key]; !ok {
			if c.projectTrusted {
				if cfg, err := c.load(); err == nil {
					if _, ok := cfg.Provider[key]; ok {
						return c.fail(fmt.Errorf("provider %q is defined in %s; edit that file to remove it", key, c.projectPath))
					}
				}
			}
			return c.fail(fmt.Errorf("provider %q is not defined in %s", key, c.homePath))
		}
		unique = append(unique, key)
	}
	// Show everything that goes before asking: the references may be spread
	// across modelRules and profiles.
	preview, err := config.PreviewRemoveProviders(c.homePath, unique)
	if err != nil {
		return c.fail(err)
	}
	_, _ = fmt.Fprintf(c.out, "Providers to delete from %s, with their API keys:\n", c.homePath)
	for _, key := range unique {
		_, _ = fmt.Fprintf(c.out, "  %s  %s\n", key, providerBaseURL(home.Provider[key]))
	}
	c.printRemovalDetails(preview)
	w := &setupWizard{in: c.in, out: c.out, errOut: c.errOut, reader: bufio.NewReader(c.in)}
	yes, err := w.confirm("Remove " + strings.Join(unique, ", ") + "?")
	if errors.Is(err, io.EOF) || (err == nil && !yes) {
		_, _ = fmt.Fprintln(c.errOut, "\nRemove cancelled; configuration was not changed.")
		return 1
	}
	if err != nil {
		return c.fail(err)
	}
	if _, err := config.RemoveProviders(c.homePath, unique); err != nil {
		return c.fail(err)
	}
	_, _ = fmt.Fprintf(c.out, "\nRemoved %s from %s.\n", strings.Join(unique, ", "), c.homePath)
	return 0
}

// removeProviders deletes keys from the home config and reports every
// reference that went with them.
func (c modelsCommand) removeProviders(keys []string) int {
	report, err := config.RemoveProviders(c.homePath, keys)
	if err != nil {
		return c.fail(err)
	}
	_, _ = fmt.Fprintf(c.out, "Removed %s and their API keys from %s.\n", strings.Join(keys, ", "), c.homePath)
	c.printRemovalDetails(report)
	return 0
}

// printRemovalDetails lists the references a provider removal drops.
func (c modelsCommand) printRemovalDetails(report config.ProviderRemoval) {
	if len(report.RuleSelectors) > 0 || len(report.ProfileModels) > 0 || report.ClearedActive {
		_, _ = fmt.Fprintln(c.out, "References to them, also deleted:")
	}
	for _, selector := range report.RuleSelectors {
		_, _ = fmt.Fprintf(c.out, "  model rule selector %s\n", selector)
	}
	for _, setting := range report.ProfileModels {
		_, _ = fmt.Fprintf(c.out, "  profiles.%s, which then uses the session's model\n", setting)
	}
	if report.ClearedActive {
		_, _ = fmt.Fprintln(c.out, "  activeModel; caretz picks an available model next start (or use /models to choose)")
	}
	for _, path := range report.EmptiedRules {
		_, _ = fmt.Fprintf(c.out, "Model rule %s is left allowing only local models; allow a hosted one with /allow-model or edit modelRules.\n", path)
	}
}

func providerBaseURL(provider config.Provider) string {
	if provider.Options.BaseURL == nil || strings.TrimSpace(*provider.Options.BaseURL) == "" {
		return "(no baseURL)"
	}
	return strings.TrimSpace(*provider.Options.BaseURL)
}

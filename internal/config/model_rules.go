package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// ModelRule selects the models trusted to work in a directory tree.
type ModelRule struct {
	Path      string   `json:"path"`
	Allow     []string `json:"allow"`
	Source    string   `json:"-"`
	canonical string
}

type ModelPolicy struct {
	Workspace string
	Rule      *ModelRule
}

func canonicalModelPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q must be absolute or start with ~/", path)
	}
	path = filepath.Clean(path)
	// Resolve existing ancestors too, so future directories beneath symlinks match.
	current, suffix := path, []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func validateModelRules(rules []ModelRule, source string) error {
	seen := map[string]bool{}
	for i := range rules {
		r := &rules[i]
		path, err := canonicalModelPath(r.Path)
		if err != nil {
			return fmt.Errorf("modelRules in %s: %w", source, err)
		}
		if seen[path] {
			return fmt.Errorf("modelRules in %s: duplicate path %q", source, r.Path)
		}
		seen[path] = true
		if r.Allow == nil {
			return fmt.Errorf("modelRules in %s: rule %q requires an allow list", source, r.Path)
		}
		for _, s := range r.Allow {
			p, m, ok := strings.Cut(s, "/")
			if s != "*" && (!ok || p == "" || m == "" || strings.Contains(p, "*") || strings.IndexFunc(s, unicode.IsSpace) >= 0 || (strings.Contains(m, "*") && m != "*")) {
				return fmt.Errorf("modelRules in %s: invalid selector %q", source, s)
			}
		}
		r.Source = source
		r.canonical = path
	}
	return nil
}

func (c Config) PolicyForWorkspace(workspace string) (ModelPolicy, error) {
	if len(c.ModelRules) == 0 {
		return ModelPolicy{Workspace: workspace}, nil
	}
	path, err := canonicalModelPath(workspace)
	if err != nil {
		return ModelPolicy{}, err
	}
	policy := ModelPolicy{Workspace: path}
	for i := range c.ModelRules {
		r := c.ModelRules[i]
		canonical := r.canonical
		if canonical == "" {
			canonical, err = canonicalModelPath(r.Path)
			if err != nil {
				return ModelPolicy{}, err
			}
		}
		rel, err := filepath.Rel(canonical, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && (policy.Rule == nil || len(canonical) > len(policy.Rule.canonical)) {
			r.canonical = canonical
			policy.Rule = &r
		}
	}
	return policy, nil
}

func (p ModelPolicy) Description() string {
	if p.Rule == nil {
		return ""
	}
	return fmt.Sprintf("model rule %s (%s): allow [%s]; workspace %s", p.Rule.Path, p.Rule.Source, strings.Join(p.Rule.Allow, ", "), p.Workspace)
}
func (p ModelPolicy) Allows(c Config, ref string) bool {
	if p.Rule == nil {
		return true
	}
	resolved, known, err := c.ResolveExplicitModelRef(ref)
	if err != nil || !known {
		return false
	}
	provider, _, _ := strings.Cut(ref, "/")
	// Rules keep workspace content away from hosted models; a local model is
	// always allowed. An endpoint override under a rule must match the
	// provider's baseURL (see checkModelEndpoint), so the configured URL is
	// the one requests go to.
	if configured, ok := c.Provider[provider]; ok && configured.Options.BaseURL != nil && c.IsLocalModel(ref, *configured.Options.BaseURL) {
		return true
	}
	for _, s := range p.Rule.Allow {
		if s == "*" || s == ref || s == provider+"/*" || s == provider+"/"+resolved.Model {
			return true
		}
	}
	return false
}
func (p ModelPolicy) Choices(c Config, adapter map[string]bool) []ModelChoice {
	return p.Filter(c, c.ModelChoices(), adapter)
}

// Filter keeps the candidates the policy and adapter restriction both allow.
func (p ModelPolicy) Filter(c Config, candidates []ModelChoice, adapter map[string]bool) []ModelChoice {
	choices := []ModelChoice{}
	for _, choice := range candidates {
		if p.Allows(c, choice.Ref) && (len(adapter) == 0 || adapter[choice.Ref]) {
			choices = append(choices, choice)
		}
	}
	return choices
}
func (p ModelPolicy) Select(c Config, ref string, adapter map[string]bool) (string, string, error) {
	if p.Allows(c, ref) && (len(adapter) == 0 || adapter[ref]) {
		return ref, "", nil
	}
	description := p.Description()
	if description == "" {
		description = "adapter allowed-model restriction; workspace " + p.Workspace
	}
	choices := p.Choices(c, adapter)
	if len(choices) == 0 {
		return "", "", fmt.Errorf("requested model %q: stopped; no allowed model; %s", ref, description)
	}
	replacement := choices[0].Ref
	by := "the adapter allowed-model restriction"
	if !p.Allows(c, ref) {
		by = "model rule " + ShortenHome(p.Rule.Path)
	}
	return replacement, fmt.Sprintf("using %s: %s is not allowed by %s", replacement, ref, by), nil
}

// ShortenHome writes a path under the home directory as ~/...
func ShortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	if rel == "." {
		return "~"
	}
	return "~" + string(filepath.Separator) + rel
}
func (p ModelPolicy) Check(c Config, ref string) error {
	if p.Allows(c, ref) {
		return nil
	}
	return fmt.Errorf("requested model %q refused; current model kept; %s; %s", ref, p.Description(), AllowModelHint(ref))
}

// Require refuses a model the user or a definition named when the rule does
// not allow it; unlike Select it never substitutes another model. The error
// names the rule; callers add how to allow the model where they run.
func (p ModelPolicy) Require(c Config, ref string) error {
	if p.Allows(c, ref) {
		return nil
	}
	if _, known, err := c.ResolveExplicitModelRef(ref); err == nil && !known {
		return fmt.Errorf("unknown configured model %q; %s", ref, p.Description())
	}
	return fmt.Errorf("model %q is not allowed; %s", ref, p.Description())
}

// AllowModelHint tells the user how to allow ref in the current workspace.
func AllowModelHint(ref string) string {
	return "run /allow-model " + ref + " to allow it here"
}

// IsLocalEndpoint reports whether baseURL points at this machine or a private
// network. A missing or unparsable URL is not local.
func IsLocalEndpoint(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// EndpointHost returns the host of baseURL for notices, or baseURL itself.
func EndpointHost(baseURL string) string {
	if u, err := url.Parse(strings.TrimSpace(baseURL)); err == nil && u.Host != "" {
		return u.Host
	}
	if strings.TrimSpace(baseURL) == "" {
		return "default endpoint"
	}
	return baseURL
}

// IsLocalModel applies an explicit classification only at the provider's own URL.
// Endpoint overrides use URL detection and never inherit the provider's trust.
func (c Config) IsLocalModel(ref, effectiveBaseURL string) bool {
	key, _, _ := strings.Cut(ref, "/")
	if provider, ok := c.Provider[key]; ok && provider.Local != nil && provider.Options.BaseURL != nil {
		configured := normalizeSetupBaseURL(*provider.Options.BaseURL)
		if configured != "" && configured == normalizeSetupBaseURL(effectiveBaseURL) {
			return *provider.Local
		}
	}
	return IsLocalEndpoint(effectiveBaseURL)
}

// LocalSelectors returns provider/* selectors for providers classified as local.
func (c Config) LocalSelectors() []string {
	selectors := []string{}
	for key, provider := range c.Provider {
		if provider.Options.BaseURL != nil && c.IsLocalModel(key+"/*", *provider.Options.BaseURL) {
			selectors = append(selectors, key+"/*")
		}
	}
	sort.Strings(selectors)
	return selectors
}

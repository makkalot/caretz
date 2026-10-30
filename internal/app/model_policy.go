package app

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/lib/types"
)

// enforceModelSettings replaces the entire model configuration on fallback.
func enforceModelSettings(cfg config.Config, policy config.ModelPolicy, settings runtimeSettings, adapter map[string]bool) (runtimeSettings, string, error) {
	requested := settings.ActiveModelRef
	if requested == "" {
		requested = settings.Model
	}
	ref, notice, err := policy.Select(cfg, requested, adapter)
	if err != nil {
		return runtimeSettings{}, "", err
	}
	if notice == "" {
		if policy.Rule != nil && settings.ActiveModelRef == "" {
			resolved, known, err := cfg.ResolveExplicitModelRef(ref)
			if err != nil {
				return runtimeSettings{}, "", err
			}
			if known {
				overlayResolvedModelConfig(&settings, resolved)
				settings.ActiveModelRef = ref
			}
		}
		return settings, "", nil
	}
	resolved, ok, err := cfg.ResolveModelRef(ref)
	if err != nil {
		return runtimeSettings{}, "", err
	}
	if !ok {
		return runtimeSettings{}, "", fmt.Errorf("fallback model %q could not be resolved", ref)
	}
	replacement, err := runtimeSettingsForModelSwitch(resolved)
	if err != nil {
		return runtimeSettings{}, "", err
	}
	replacement.ActiveModelRef = ref
	replacement.LLMTimeout = settings.LLMTimeout
	replacement.LLMMaxAttempts = settings.LLMMaxAttempts
	replacement.OutputMode = settings.OutputMode
	replacement.Theme = settings.Theme
	return replacement, notice, nil
}

func checkModelEndpoint(cfg config.Config, policy config.ModelPolicy, settings runtimeSettings, override, source string) error {
	if policy.Rule == nil || override == "" || slices.Contains(policy.Rule.Allow, "*") {
		return nil
	}
	resolved, known, err := cfg.ResolveExplicitModelRef(settings.ActiveModelRef)
	if err != nil {
		return err
	}
	if !known || override != resolved.BaseURL {
		return fmt.Errorf("%s endpoint override rejected for requested model %q; %s", source, settings.ActiveModelRef, policy.Description())
	}
	return nil
}

// modelRulesReport lists the configured model rules and marks the one
// enforced in the workspace. rules are re-read from config so rules saved
// during the session appear; active may be a rule kept only for this run.
func modelRulesReport(rules []config.ModelRule, active *config.ModelRule, workspace string) string {
	if active != nil && !slices.ContainsFunc(rules, func(r config.ModelRule) bool { return r.Path == active.Path && r.Source == active.Source }) {
		rules = append(rules, *active)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "workspace: %s\n", workspace)
	if active == nil {
		b.WriteString("no model rule covers this workspace; cloud models ask before use\n")
	}
	if len(rules) == 0 {
		b.WriteString("no model rules configured; add modelRules to your personal config")
		return b.String()
	}
	b.WriteString("model rules (the most specific covering path wins):\n")
	width := 0
	for _, r := range rules {
		width = max(width, len(r.Path))
	}
	for _, r := range rules {
		fmt.Fprintf(&b, "  %-*s  allow [%s]  (%s)", width, r.Path, strings.Join(r.Allow, ", "), r.Source)
		if active != nil && r.Path == active.Path && r.Source == active.Source {
			b.WriteString("  ← active here")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// modelPolicyNoticeEvent records a model switch caused by a model rule.
const modelPolicyNoticeEvent = "Model.PolicyNotice"

// modelRuleNotice describes the rule active in a workspace, or "" when no rule
// matches. It is shown, not recorded: only switches go into session history.
func modelRuleNotice(policy config.ModelPolicy) string {
	if summary := modelRuleSummary(policy); summary != "" {
		return "model rule " + summary
	}
	return ""
}

// modelRuleSummary is modelRuleNotice without the leading "model rule", for
// display under a "Model rule" heading.
func modelRuleSummary(policy config.ModelPolicy) string {
	if policy.Rule == nil {
		return ""
	}
	return fmt.Sprintf("%s: allow [%s] (%s)", config.ShortenHome(policy.Rule.Path), strings.Join(policy.Rule.Allow, ", "), config.ShortenHome(policy.Rule.Source))
}

// printModelNotices writes the active rule and the startup model switches.
func (rt *Runtime) printModelNotices(w io.Writer) {
	if notice := modelRuleNotice(rt.ModelPolicy); notice != "" && !rt.ModelRuleAnswered {
		_, _ = fmt.Fprintln(w, notice)
	}
	for _, notice := range rt.ModelNotices {
		_, _ = fmt.Fprintln(w, notice)
	}
}

func recordModelNotice(session *agent.AgentSession, notice string) error {
	payload, _ := json.Marshal(map[string]string{"message": notice})
	return session.AppendAuditEvent(types.NewEvent(types.NewOriginator(session.ChannelID(), 0), modelPolicyNoticeEvent, string(payload)))
}

// modelPolicyNotices returns the recorded model switches in order.
func modelPolicyNotices(session *agent.AgentSession) ([]string, error) {
	events, err := session.AuditEvents()
	if err != nil {
		return nil, err
	}
	notices := []string{}
	for _, event := range events {
		if event.EventType != modelPolicyNoticeEvent {
			continue
		}
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			return nil, err
		}
		notices = append(notices, payload.Message)
	}
	return notices, nil
}

// requireModel refuses ref unless both the workspace's rule and the adapter's
// allowed-model restriction allow it. It never substitutes another model.
func requireModel(cfg config.Config, policy config.ModelPolicy, ref string, adapter map[string]bool) error {
	if len(adapter) > 0 && !adapter[ref] {
		return fmt.Errorf("model %q is not allowed by the adapter allowed-model restriction", ref)
	}
	return policy.Require(cfg, ref)
}

// enforceSessionModel applies the workspace's rule to a new session. A model
// the user named at startup (source, e.g. "--model") must be allowed; one
// from config falls back to an allowed model with a notice.
func enforceSessionModel(cfg config.Config, policy config.ModelPolicy, settings runtimeSettings, adapter map[string]bool, source string) (runtimeSettings, string, error) {
	if source != "" {
		ref := settings.ActiveModelRef
		if ref == "" {
			ref = settings.Model
		}
		if err := requireModel(cfg, policy, ref, adapter); err != nil {
			return runtimeSettings{}, "", fmt.Errorf("%s: %w; %s", source, err, config.AllowModelHint(ref))
		}
	}
	return enforceModelSettings(cfg, policy, settings, adapter)
}

// allowModelsForRun adds refs (--allow-model) to the workspace's model rule
// for this run only. The rule goes into the in-memory config, so subagents,
// goal roles and model switches in the workspace see it; nothing is saved.
// Without a covering rule, the run rule allows exactly refs (and local
// models), which also answers the cloud prompt for them.
func allowModelsForRun(cfg config.Config, policy config.ModelPolicy, refs []string) (config.Config, config.ModelPolicy, error) {
	if len(refs) == 0 {
		return cfg, policy, nil
	}
	var allow []string
	if policy.Rule != nil {
		allow = append(allow, policy.Rule.Allow...)
	}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if err := validateAllowModelSelector(cfg, ref); err != nil {
			return cfg, policy, fmt.Errorf("--allow-model: %w", err)
		}
		if !slices.Contains(allow, ref) {
			allow = append(allow, ref)
		}
	}
	rule, err := config.NewWorkspaceRule(policy.Workspace, allow, cloudConsentSource)
	if err != nil {
		return cfg, policy, err
	}
	if cfg, err = cfg.WithModelRule(*rule); err != nil {
		return cfg, policy, err
	}
	policy, err = cfg.PolicyForWorkspace(policy.Workspace)
	return cfg, policy, err
}

// validateAllowModelSelector accepts a configured provider/model, a
// provider/* for a configured provider, or *.
func validateAllowModelSelector(cfg config.Config, ref string) error {
	if ref == "*" {
		return nil
	}
	provider, model, ok := strings.Cut(ref, "/")
	if !ok || provider == "" || model == "" {
		return fmt.Errorf("%q must be provider/model, provider/*, or *", ref)
	}
	if model == "*" {
		if _, known := cfg.Provider[provider]; !known {
			return fmt.Errorf("unknown provider %q; see caretz models", provider)
		}
		return nil
	}
	if _, known, err := cfg.ResolveExplicitModelRef(ref); err != nil {
		return err
	} else if !known {
		return fmt.Errorf("unknown configured model %q; see caretz models", ref)
	}
	return nil
}

// allowModelHere saves a rule for the policy's workspace that keeps the
// active rule's allow list and adds ref, and makes it the active rule.
func allowModelHere(policy *config.ModelPolicy, ruleFile, ref string) (*config.ModelRule, error) {
	allow := append([]string{}, policy.Rule.Allow...)
	if !slices.Contains(allow, ref) {
		allow = append(allow, ref)
	}
	rule, err := config.NewWorkspaceRule(policy.Workspace, allow, ruleFile)
	if err != nil {
		return nil, err
	}
	if err := config.SaveModelRule(ruleFile, *rule); err != nil {
		return nil, err
	}
	policy.Workspace = rule.Path
	policy.Rule = rule
	return rule, nil
}

// allowModelCommand runs /allow-model against the workspace's policy. It
// returns the saved rule, or nil when ref was already allowed, and the
// message to show.
func allowModelCommand(cfg config.Config, policy *config.ModelPolicy, ruleFile, ref string) (*config.ModelRule, string, error) {
	if _, known, err := cfg.ResolveExplicitModelRef(ref); err != nil {
		return nil, "", err
	} else if !known {
		return nil, "", fmt.Errorf("unknown configured model %q; see /models", ref)
	}
	if policy.Rule == nil {
		return nil, fmt.Sprintf("No model rule covers %s; every model is already allowed here.", policy.Workspace), nil
	}
	if policy.Allows(cfg, ref) {
		return nil, fmt.Sprintf("%s is already allowed here by model rule %s.", ref, policy.Rule.Path), nil
	}
	rule, err := allowModelHere(policy, ruleFile, ref)
	if err != nil {
		return nil, "", err
	}
	return rule, fmt.Sprintf("Allowed %s in %s (saved to %s).", ref, config.ShortenHome(rule.Path), config.ShortenHome(ruleFile)), nil
}

// restoreRestrictedSession reopens a session on the model it last used. A
// model the rule now blocks is refused, not replaced: switching models
// mid-conversation, often to a smaller one, can break the session.
func restoreRestrictedSession(cfg config.Config, policy config.ModelPolicy, fallback runtimeSettings, state sessionRuntimeState, adapter map[string]bool) (runtimeSettings, error) {
	requested := state.ModelRef
	if requested == "" {
		requested = state.Model
	}
	if err := requireModel(cfg, policy, requested, adapter); err != nil {
		return runtimeSettings{}, fmt.Errorf("resumed session: %w; %s", err, config.AllowModelHint(requested))
	}
	// Preserve the existing raw-model restore behavior outside restrictions.
	if policy.Rule != nil && state.ModelRef != "" {
		resolved, known, err := cfg.ResolveExplicitModelRef(requested)
		if err != nil {
			return runtimeSettings{}, err
		}
		if known {
			settings, err := runtimeSettingsForModelSwitch(resolved)
			if err != nil {
				return runtimeSettings{}, err
			}
			settings.ActiveModelRef = requested
			settings.ReasoningEffort = state.ReasoningEffort
			settings.LLMTimeout = fallback.LLMTimeout
			settings.LLMMaxAttempts = fallback.LLMMaxAttempts
			settings.OutputMode = fallback.OutputMode
			settings.Theme = fallback.Theme
			return settings, nil
		}
	}
	return settingsForSessionRuntime(cfg, fallback, state)
}

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/lib/eventing"
	"golang.org/x/term"
)

// cloudConsentSource names rules that last only for this run.
const cloudConsentSource = "this run"

// cloudModelCapability names cloud-model consent in permission requests.
const cloudModelCapability = "cloud model"

// cloudAnswer is the user's choice in the cloud-model prompt. It travels in
// PermissionResolution.Metadata under cloudAnswerKey for mid-session switches.
type cloudAnswer string

const (
	cloudAllowOnce   cloudAnswer = "allow_once"
	cloudAllowAlways cloudAnswer = "allow_always"
	// cloudLocalOnce switches to a local model at startup and keeps the
	// current model mid-session.
	cloudLocalOnce   cloudAnswer = "local_once"
	cloudLocalAlways cloudAnswer = "local_always"

	cloudAnswerKey = "cloud_answer"
)

var errCloudNotConfirmed = errors.New("cloud model not confirmed; stopped before sending workspace content")

// modelOverrideCapability names a mid-session switch to a model the
// workspace's rule blocks.
const modelOverrideCapability = "model rule override"

// cloudConsentQuestion is what both the startup and the mid-session prompt show.
type cloudConsentQuestion struct {
	Workspace, Ref, Host, Local, RuleFile string
	// MidSession labels the safe choice "Keep current" instead of "Use local".
	MidSession bool
	// RulePath and RuleAllow describe the rule that blocks Ref. When set, the
	// prompt asks to override that rule instead of asking about the cloud.
	RulePath, RuleAllow string
}

func newCloudConsentQuestion(cfg config.Config, workspace, ref, baseURL, ruleFile string, midSession bool) cloudConsentQuestion {
	q := cloudConsentQuestion{Workspace: workspace, Ref: ref, Host: config.EndpointHost(baseURL), RuleFile: ruleFile, MidSession: midSession}
	if local := cfg.LocalSelectors(); len(local) > 0 {
		rule := &config.ModelRule{Path: workspace, Allow: local}
		if choices := (config.ModelPolicy{Workspace: workspace, Rule: rule}).Choices(cfg, nil); len(choices) > 0 {
			q.Local = choices[0].Ref
		}
	}
	return q
}

func (q cloudConsentQuestion) details() map[string]string {
	return map[string]string{"workspace": q.Workspace, "model": q.Ref, "host": q.Host, "local": q.Local, "rule_file": q.RuleFile, "rule_path": q.RulePath, "rule_allow": q.RuleAllow}
}

func cloudConsentQuestionFromRequest(request eventing.PermissionRequest) cloudConsentQuestion {
	d := request.Details
	return cloudConsentQuestion{Workspace: d["workspace"], Ref: d["model"], Host: d["host"], Local: d["local"], RuleFile: d["rule_file"], MidSession: true, RulePath: d["rule_path"], RuleAllow: d["rule_allow"]}
}

// readCloudConsent renders the cloud-model prompt, or the rule-override
// prompt when q.RulePath is set. cancelled reports Escape or Ctrl-C; the
// answer is then the safe choice.
func readCloudConsent(terminal *ansi.Terminal, q cloudConsentQuestion, expiresAt time.Time) (answer cloudAnswer, cancelled bool, err error) {
	out := terminal.Writer()
	style := output.NewStyle(out)
	if q.RulePath != "" {
		_, _ = fmt.Fprintf(out, "\n%s\n", style.Bold(style.Warning("Model not allowed here by model rule "+q.RulePath)))
		_, _ = fmt.Fprintf(out, "  %s %s\n", style.Muted("directory:"), style.Accent(q.Workspace))
		_, _ = fmt.Fprintf(out, "  %s %s (%s)\n", style.Muted("model:    "), q.Ref, q.Host)
		_, _ = fmt.Fprintf(out, "  %s allow [%s]\n", style.Muted("rule:     "), q.RuleAllow)
		_, _ = fmt.Fprintf(out, "  %s\n", style.Muted("\"Always allow here\" saves a rule for this directory to "+q.RuleFile))
		choices := []buttonChoice{
			{label: "Allow once", key: 'y'},
			{label: "Always allow here", key: 'a'},
			{label: "Keep current", key: 'n'},
		}
		choice, err := readButtonChoice(terminal, choices, 2, expiresAt, "No answer; keeping the current model.", nil)
		if err != nil {
			return "", false, err
		}
		if choice == buttonCancelled {
			return cloudLocalOnce, true, nil
		}
		return []cloudAnswer{cloudAllowOnce, cloudAllowAlways, cloudLocalOnce}[choice], false, nil
	}
	local := q.Local
	if local == "" {
		local = "none configured"
	}
	_, _ = fmt.Fprintf(out, "\n%s\n", style.Bold(style.Warning("Cloud model requested; no model rule covers this directory")))
	_, _ = fmt.Fprintf(out, "  %s %s\n", style.Muted("directory:"), style.Accent(q.Workspace))
	_, _ = fmt.Fprintf(out, "  %s %s (%s)\n", style.Muted("model:    "), q.Ref, q.Host)
	_, _ = fmt.Fprintf(out, "  %s %s\n", style.Muted("local:    "), local)
	_, _ = fmt.Fprintf(out, "  %s\n", style.Muted("\"Always\" choices save a model rule to "+q.RuleFile))
	safe := "Use local"
	if q.MidSession {
		safe = "Keep current"
	}
	choices := []buttonChoice{
		{label: "Allow once", key: 'y'},
		{label: "Always allow here", key: 'a'},
		{label: safe, key: 'n'},
		{label: "Always local here", key: 'l'},
	}
	answers := []cloudAnswer{cloudAllowOnce, cloudAllowAlways, cloudLocalOnce, cloudLocalAlways}
	choice, err := readButtonChoice(terminal, choices, 2, expiresAt, "No answer; keeping the current model.", nil)
	if err != nil {
		return "", false, err
	}
	if choice == buttonCancelled {
		return cloudLocalOnce, true, nil
	}
	return answers[choice], false, nil
}

// applyCloudAnswer turns an answer into the workspace policy. "Always" answers
// save the rule; startup's local answer adds a rule for this run only.
func applyCloudAnswer(answer cloudAnswer, cfg config.Config, policy *config.ModelPolicy, ruleFile string, midSession bool, out io.Writer) error {
	allow, source := []string{"*"}, ruleFile
	switch answer {
	case cloudAllowOnce:
		return nil
	case cloudAllowAlways:
	case cloudLocalOnce, cloudLocalAlways:
		if answer == cloudLocalOnce {
			if midSession {
				return nil
			}
			source = cloudConsentSource
		}
		allow = cfg.LocalSelectors()
		if len(allow) == 0 {
			return fmt.Errorf("no local model configured for %s; add a local provider with `caretz setup`", policy.Workspace)
		}
	default:
		return fmt.Errorf("unknown cloud model answer %q", answer)
	}
	rule, err := config.NewWorkspaceRule(policy.Workspace, allow, source)
	if err != nil {
		return err
	}
	if source == ruleFile {
		if err := config.SaveModelRule(ruleFile, *rule); err != nil {
			return err
		}
		style := output.NewStyle(out)
		_, _ = fmt.Fprintln(out, style.Success(output.GlyphSuccess)+" "+style.Muted(fmt.Sprintf("Saved model rule for %s to %s.", rule.Path, ruleFile)))
	}
	policy.Workspace = rule.Path
	policy.Rule = rule
	return nil
}

// needsCloudConsent reports whether a cloud endpoint would receive a
// workspace that no model rule covers.
func needsCloudConsent(cfg config.Config, policy config.ModelPolicy, ref, baseURL string) bool {
	return policy.Rule == nil && !cfg.IsLocalModel(ref, baseURL)
}

// Rules are saved selector snapshots, so later classification changes cannot
// revoke them. Make their continued permission visible when using a hosted model.
func hostedModelRuleNotice(cfg config.Config, policy config.ModelPolicy, ref, baseURL string) string {
	if policy.Rule != nil && policy.Allows(cfg, ref) && !cfg.IsLocalModel(ref, baseURL) {
		return fmt.Sprintf("notice: %s is classified hosted and allowed by model rule %s; saved allowlists do not change when providers are reclassified. Review the rule if you intended local-only access.", ref, policy.Rule.Path)
	}
	return ""
}

// cloudWorkspaceWarning is shown when nobody can be asked.
func cloudWorkspaceWarning(workspace, ref, baseURL string) string {
	return fmt.Sprintf("warning: no model rule covers %s; sending it to %s (%s). Add a modelRules entry to restrict this.", workspace, ref, config.EndpointHost(baseURL))
}

// cloudConsentChooser asks the startup question and returns the answer.
type cloudConsentChooser func(cloudConsentQuestion) (cloudAnswer, error)

// terminalCloudChooser shows the startup prompt with the shared button widget.
func terminalCloudChooser(in io.Reader, out io.Writer) cloudConsentChooser {
	return func(q cloudConsentQuestion) (cloudAnswer, error) {
		terminal := ansi.NewTerminal(in, out)
		restore, err := terminal.BeginInput()
		if err != nil {
			return "", err
		}
		defer restore()
		answer, cancelled, err := readCloudConsent(terminal, q, time.Time{})
		if err != nil {
			return "", err
		}
		if cancelled {
			return "", errCloudNotConfirmed
		}
		return answer, nil
	}
}

// confirmCloudWorkspace asks at startup whether a cloud model may work in a
// workspace no rule covers, and applies the answer to policy. It returns a
// one-line summary of the outcome, or "" when nothing was asked.
func confirmCloudWorkspace(choose cloudConsentChooser, cfg config.Config, policy *config.ModelPolicy, ref, baseURL, ruleFile string) (string, error) {
	if !needsCloudConsent(cfg, *policy, ref, baseURL) {
		return "", nil
	}
	q := newCloudConsentQuestion(cfg, policy.Workspace, ref, baseURL, ruleFile, false)
	answer, err := choose(q)
	if err != nil {
		return "", err
	}
	if err := applyCloudAnswer(answer, cfg, policy, ruleFile, false, io.Discard); err != nil {
		return "", err
	}
	switch answer {
	case cloudAllowOnce:
		return fmt.Sprintf("using %s for this run", ref), nil
	case cloudAllowAlways:
		return fmt.Sprintf("using %s; all models allowed here, rule saved to %s", ref, ruleFile), nil
	case cloudLocalAlways:
		return fmt.Sprintf("using %s instead of %s; local-only rule saved to %s", q.Local, ref, ruleFile), nil
	default:
		return fmt.Sprintf("using %s instead of %s for this run", q.Local, ref), nil
	}
}

// modelSwitchBaseURL returns the endpoint ref will use, falling back to its
// provider's endpoint when the model itself is not configured.
func modelSwitchBaseURL(cfg config.Config, ref string) string {
	if resolved, ok, err := cfg.ResolveModelRef(ref); err == nil && ok {
		return resolved.BaseURL
	}
	providerKey, _, _ := strings.Cut(ref, "/")
	if provider, ok := cfg.Provider[providerKey]; ok && provider.Options.BaseURL != nil {
		return *provider.Options.BaseURL
	}
	return ""
}

// confirmCloudModelSwitch asks, through the session's permission prompt,
// before a mid-session switch sends an uncovered workspace to a cloud model.
// Without someone to answer, it warns and allows the switch.
func confirmCloudModelSwitch(ctx context.Context, session *agent.AgentSession, cfg config.Config, policy *config.ModelPolicy, ruleFile, workspace, ref, baseURL string, warn func(string)) error {
	if notice := hostedModelRuleNotice(cfg, *policy, ref, baseURL); notice != "" {
		warn(notice)
	}
	if !needsCloudConsent(cfg, *policy, ref, baseURL) {
		return nil
	}
	if !agent.PermissionPromptsAvailable(ctx) {
		warn(cloudWorkspaceWarning(workspace, ref, baseURL))
		return nil
	}
	if policy.Workspace == "" {
		policy.Workspace = workspace
	}
	q := newCloudConsentQuestion(cfg, policy.Workspace, ref, baseURL, ruleFile, true)
	resolution, err := session.AskPermission(ctx, eventing.PermissionRequest{
		ToolName:   "/models",
		Capability: cloudModelCapability,
		Target:     policy.Workspace,
		Command:    fmt.Sprintf("send this directory to %s (%s)", ref, q.Host),
		Reason:     "no model rule covers this directory",
		Details:    q.details(),
	})
	if err != nil {
		return err
	}
	answer := cloudAnswer(resolution.Metadata[cloudAnswerKey])
	if answer == "" {
		// Clients without the cloud prompt answer with a plain decision.
		answer = cloudLocalOnce
		if resolution.Decision == eventing.PermissionDecisionAllow || resolution.Decision == eventing.PermissionDecisionAllowSession {
			answer = cloudAllowOnce
		}
	}
	if err := applyCloudAnswer(answer, cfg, policy, ruleFile, true, noticeWriter(warn)); err != nil {
		return err
	}
	switch answer {
	case cloudAllowOnce, cloudAllowAlways:
		return nil
	case cloudLocalAlways:
		return fmt.Errorf("switch to %s refused; current model kept; saved local-only model rule for %s to %s", ref, policy.Workspace, ruleFile)
	default:
		return fmt.Errorf("switch to %s refused; current model kept; no model rule covers %s", ref, workspace)
	}
}

// confirmModelOverride asks, through the session's permission prompt, before
// a mid-session switch to a model the workspace's rule blocks. "Always allow
// here" saves a rule for the workspace that keeps the blocking rule's allow
// list and adds ref. Without someone to answer, the switch is refused.
func confirmModelOverride(ctx context.Context, session *agent.AgentSession, cfg config.Config, policy *config.ModelPolicy, ruleFile, ref, baseURL string, notify func(string)) error {
	if policy.Allows(cfg, ref) {
		return nil
	}
	if !agent.PermissionPromptsAvailable(ctx) {
		return policy.Check(cfg, ref)
	}
	q := cloudConsentQuestion{
		Workspace: policy.Workspace, Ref: ref, Host: config.EndpointHost(baseURL), RuleFile: ruleFile, MidSession: true,
		RulePath: policy.Rule.Path, RuleAllow: strings.Join(policy.Rule.Allow, ", "),
	}
	resolution, err := session.AskPermission(ctx, eventing.PermissionRequest{
		ToolName:   "/models",
		Capability: modelOverrideCapability,
		Target:     policy.Workspace,
		Command:    fmt.Sprintf("switch to %s (%s), which model rule %s does not allow", ref, q.Host, policy.Rule.Path),
		Reason:     policy.Description(),
		Details:    q.details(),
	})
	if err != nil {
		return err
	}
	answer := cloudAnswer(resolution.Metadata[cloudAnswerKey])
	if answer == "" {
		answer = cloudLocalOnce
		if resolution.Decision == eventing.PermissionDecisionAllow || resolution.Decision == eventing.PermissionDecisionAllowSession {
			answer = cloudAllowOnce
		}
	}
	switch answer {
	case cloudAllowOnce:
		return nil
	case cloudAllowAlways:
		rule, err := allowModelHere(policy, ruleFile, ref)
		if err != nil {
			return err
		}
		notify(fmt.Sprintf("Saved model rule for %s to %s: allow [%s].", rule.Path, ruleFile, strings.Join(rule.Allow, ", ")))
		return nil
	default:
		return fmt.Errorf("switch to %s refused; current model kept; %s", ref, policy.Description())
	}
}

// cloudConsentResolution answers a cloud-model permission request in the TUI.
func cloudConsentResolution(terminal *ansi.Terminal, request eventing.PermissionRequest) (eventing.PermissionResolution, error) {
	answer, cancelled, err := readCloudConsent(terminal, cloudConsentQuestionFromRequest(request), request.ExpiresAt)
	if cancelled {
		return eventing.PermissionResolution{}, context.Canceled
	}
	if err != nil {
		return eventing.PermissionResolution{}, err
	}
	decision := eventing.PermissionDecisionDeny
	if answer == cloudAllowOnce || answer == cloudAllowAlways {
		decision = eventing.PermissionDecisionAllow
	}
	return eventing.PermissionResolution{RequestID: request.RequestID, Decision: decision, Resolver: "tui", Metadata: map[string]string{cloudAnswerKey: string(answer)}}, nil
}

// noticeWriter sends each write to a notice function as one line.
type noticeWriter func(string)

func (w noticeWriter) Write(p []byte) (int, error) {
	w(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// isTerminalWriter reports whether w is an *os.File backed by a terminal.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

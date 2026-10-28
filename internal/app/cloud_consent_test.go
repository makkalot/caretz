package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/ansi"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
	libruntime "github.com/makkalot/caretz/lib/runtime"
)

const cloudConsentURL = "https://api.example.com/v1"

func cloudConsentConfig(withLocal bool) config.Config {
	cloud, local := cloudConsentURL, "http://127.0.0.1:8080/v1"
	cfg := config.Config{ActiveModel: "cloud/x", Provider: map[string]config.Provider{
		"cloud": {Options: config.ProviderOptions{BaseURL: &cloud}, Models: map[string]config.Model{"x": {Model: "cloud-x"}}},
	}}
	if withLocal {
		cfg.Provider["local"] = config.Provider{Options: config.ProviderOptions{BaseURL: &local}, Models: map[string]config.Model{"a": {Model: "qwen-a"}}}
	}
	return cfg
}

// answer returns a chooser that records the question and gives answer.
func answer(a cloudAnswer, asked *cloudConsentQuestion) cloudConsentChooser {
	return func(q cloudConsentQuestion) (cloudAnswer, error) {
		*asked = q
		return a, nil
	}
}

// savedRules reads the modelRules written to path.
func savedRules(t *testing.T, path string) []config.ModelRule {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.ModelRules
}

func TestReadCloudConsent(t *testing.T) {
	for _, tt := range []struct {
		name, input   string
		midSession    bool
		want          cloudAnswer
		wantCancelled bool
		wantLabel     string
	}{
		{name: "enter picks the safe default", input: "\r", want: cloudLocalOnce, wantLabel: "[>Use local<]"},
		{name: "mid-session safe default keeps current", input: "\r", midSession: true, want: cloudLocalOnce, wantLabel: "[>Keep current<]"},
		{name: "right arrow to always local", input: "\x1b[C\r", want: cloudLocalAlways},
		{name: "left arrows to allow once", input: "\x1b[D\x1b[D\r", want: cloudAllowOnce},
		{name: "shortcut always allow", input: "a", want: cloudAllowAlways},
		{name: "ctrl-c cancels", input: "\x03", want: cloudLocalOnce, wantCancelled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			terminal := ansi.NewTerminal(strings.NewReader(tt.input), &out)
			q := cloudConsentQuestion{Workspace: "/w", Ref: "cloud/x", Host: "api.example.com", Local: "local/a", RuleFile: "/home/config.json", MidSession: tt.midSession}
			got, cancelled, err := readCloudConsent(terminal, q, time.Time{})
			if err != nil || got != tt.want || cancelled != tt.wantCancelled {
				t.Fatalf("answer=%q cancelled=%v err=%v, want %q cancelled=%v", got, cancelled, err, tt.want, tt.wantCancelled)
			}
			for _, want := range []string{"/w", "cloud/x (api.example.com)", "local/a", "/home/config.json", "Always allow here", "Always local here", tt.wantLabel} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("prompt missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestConfirmCloudWorkspace(t *testing.T) {
	for _, tt := range []struct {
		name      string
		answer    cloudAnswer
		wantModel string
		noLocal   bool
		wantErr   string
		wantAllow []string
		wantSaved bool
	}{
		{name: "allow once", answer: cloudAllowOnce, wantModel: "cloud/x"},
		{name: "always allow", answer: cloudAllowAlways, wantModel: "cloud/x", wantAllow: []string{"*"}, wantSaved: true},
		{name: "use local", answer: cloudLocalOnce, wantModel: "local/a", wantAllow: []string{"local/*"}},
		{name: "always local", answer: cloudLocalAlways, wantModel: "local/a", wantAllow: []string{"local/*"}, wantSaved: true},
		{name: "no local model", answer: cloudLocalOnce, noLocal: true, wantErr: "no local model"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			ruleFile := filepath.Join(t.TempDir(), "config.json")
			cfg := cloudConsentConfig(!tt.noLocal)
			policy := config.ModelPolicy{Workspace: workspace}
			var asked cloudConsentQuestion
			summary, err := confirmCloudWorkspace(answer(tt.answer, &asked), cfg, &policy, "cloud/x", cloudConsentURL, ruleFile)
			if asked.Host != "api.example.com" || asked.MidSession || (!tt.noLocal && asked.Local != "local/a") {
				t.Fatalf("question=%+v", asked)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err=%v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(summary, "using "+tt.wantModel) || strings.Contains(summary, ruleFile) != tt.wantSaved {
				t.Fatalf("summary=%q, want model %s saved=%v", summary, tt.wantModel, tt.wantSaved)
			}
			if tt.wantAllow == nil {
				if policy.Rule != nil {
					t.Fatalf("unexpected rule %+v", policy.Rule)
				}
			} else if policy.Rule == nil || !reflect.DeepEqual(policy.Rule.Allow, tt.wantAllow) {
				t.Fatalf("rule=%+v, want allow %v", policy.Rule, tt.wantAllow)
			}
			rules := savedRules(t, ruleFile)
			if saved := len(rules) > 0; saved != tt.wantSaved {
				t.Fatalf("saved=%v, want %v", saved, tt.wantSaved)
			}
			if tt.wantSaved {
				// The saved rule covers the workspace on the next run.
				next, err := (config.Config{ModelRules: rules}).PolicyForWorkspace(workspace)
				if err != nil || next.Rule == nil || policy.Rule.Source != ruleFile {
					t.Fatalf("saved rule does not cover workspace: %+v err=%v", next, err)
				}
			}
		})
	}
}

func TestConfirmCloudWorkspaceStops(t *testing.T) {
	policy := config.ModelPolicy{Workspace: t.TempDir()}
	stop := func(cloudConsentQuestion) (cloudAnswer, error) { return "", errCloudNotConfirmed }
	_, err := confirmCloudWorkspace(stop, cloudConsentConfig(true), &policy, "cloud/x", cloudConsentURL, filepath.Join(t.TempDir(), "c.json"))
	if !errors.Is(err, errCloudNotConfirmed) || policy.Rule != nil {
		t.Fatalf("err=%v rule=%+v", err, policy.Rule)
	}
}

func TestConfirmCloudWorkspaceSkips(t *testing.T) {
	workspace := t.TempDir()
	cfg := cloudConsentConfig(true)
	covered := config.ModelPolicy{Workspace: workspace, Rule: &config.ModelRule{Path: workspace, Allow: []string{"*"}}}
	never := func(cloudConsentQuestion) (cloudAnswer, error) {
		t.Fatal("asked unexpectedly")
		return "", nil
	}
	for name, tt := range map[string]struct {
		policy  config.ModelPolicy
		baseURL string
	}{
		"rule covers workspace": {covered, cloudConsentURL},
		"local endpoint":        {config.ModelPolicy{Workspace: workspace}, "http://localhost:11434/v1"},
	} {
		t.Run(name, func(t *testing.T) {
			if summary, err := confirmCloudWorkspace(never, cfg, &tt.policy, "cloud/x", tt.baseURL, filepath.Join(t.TempDir(), "c.json")); err != nil || summary != "" {
				t.Fatalf("summary=%q err=%v", summary, err)
			}
		})
	}
}

func TestCloudConsentLocalAnswerFallsBack(t *testing.T) {
	cfg := cloudConsentConfig(true)
	policy := config.ModelPolicy{Workspace: t.TempDir()}
	var asked cloudConsentQuestion
	if _, err := confirmCloudWorkspace(answer(cloudLocalOnce, &asked), cfg, &policy, "cloud/x", cloudConsentURL, filepath.Join(t.TempDir(), "c.json")); err != nil {
		t.Fatal(err)
	}
	settings, notice, err := enforceModelSettings(cfg, policy, runtimeSettings{ActiveModelRef: "cloud/x", Model: "cloud-x", BaseURL: cloudConsentURL}, nil)
	if err != nil || settings.ActiveModelRef != "local/a" || !config.IsLocalEndpoint(settings.BaseURL) || !strings.Contains(notice, "using local/a") {
		t.Fatalf("settings=%+v notice=%q err=%v", settings, notice, err)
	}
	if err := policy.Check(cfg, "cloud/x"); err == nil {
		t.Fatal("cloud model switch allowed after local-only answer")
	}
}

// consentResponder resolves the next permission request like the TUI would.
type consentResponder func(decision string, a cloudAnswer) <-chan eventing.PermissionRequest

// newConsentTestSession returns a session and a responder for its requests.
func newConsentTestSession(t *testing.T) (*agent.AgentSession, consentResponder) {
	t.Helper()
	logger := log.New()
	session, err := agent.NewAgentSession("test", "cloud-switch", agent.SessionParams{
		BaseDir: t.TempDir(), WorkspaceDir: t.TempDir(), Logger: logger,
		Provider: &permissionsCommandProvider{}, ToolRegistry: &tools.Registry{Logger: logger},
		SystemPrompt: "test", ContextWindow: 128000, ReservedTokens: 16384, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	subscription, err := session.SubscribeAuditEvents(eventing.PermissionRequested)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	return session, func(decision string, a cloudAnswer) <-chan eventing.PermissionRequest {
		seen := make(chan eventing.PermissionRequest, 1)
		go func() {
			request, err := eventing.DecodePermissionRequest(<-subscription.Events())
			if err != nil {
				t.Error(err)
				return
			}
			seen <- request
			resolution := eventing.PermissionResolution{RequestID: request.RequestID, Decision: decision}
			if a != "" {
				resolution.Metadata = map[string]string{cloudAnswerKey: string(a)}
			}
			if err := session.ResolvePermission(resolution); err != nil {
				t.Error(err)
			}
		}()
		return seen
	}
}

func TestConfirmCloudModelSwitch(t *testing.T) {
	session, respond := newConsentTestSession(t)
	cfg := cloudConsentConfig(true)
	workspace := session.WorkspaceDir()
	prompts := agent.WithPermissionPrompts(context.Background())
	var notices []string
	notice := func(message string) { notices = append(notices, message) }
	newPolicy := func() (*config.ModelPolicy, string) {
		return &config.ModelPolicy{Workspace: workspace}, filepath.Join(t.TempDir(), "config.json")
	}

	t.Run("keep current refuses", func(t *testing.T) {
		policy, ruleFile := newPolicy()
		seen := respond(eventing.PermissionDecisionDeny, cloudLocalOnce)
		err := confirmCloudModelSwitch(prompts, session, cfg, policy, ruleFile, workspace, "cloud/x", cloudConsentURL, notice)
		if err == nil || !strings.Contains(err.Error(), "current model kept") || policy.Rule != nil {
			t.Fatalf("err=%v rule=%+v", err, policy.Rule)
		}
		request := <-seen
		q := cloudConsentQuestionFromRequest(request)
		if request.Capability != cloudModelCapability || q.Workspace != workspace || q.Ref != "cloud/x" || q.Host != "api.example.com" || q.Local != "local/a" || q.RuleFile != ruleFile || !q.MidSession {
			t.Fatalf("request=%+v question=%+v", request, q)
		}
	})
	t.Run("plain allow is allow once", func(t *testing.T) {
		policy, ruleFile := newPolicy()
		seen := respond(eventing.PermissionDecisionAllow, "")
		if err := confirmCloudModelSwitch(prompts, session, cfg, policy, ruleFile, workspace, "cloud/x", cloudConsentURL, notice); err != nil || policy.Rule != nil {
			t.Fatalf("err=%v rule=%+v", err, policy.Rule)
		}
		<-seen
	})
	t.Run("always allow saves and stops asking", func(t *testing.T) {
		policy, ruleFile := newPolicy()
		seen := respond(eventing.PermissionDecisionAllow, cloudAllowAlways)
		if err := confirmCloudModelSwitch(prompts, session, cfg, policy, ruleFile, workspace, "cloud/x", cloudConsentURL, notice); err != nil {
			t.Fatal(err)
		}
		<-seen
		if rules := savedRules(t, ruleFile); len(rules) != 1 || policy.Rule == nil || !reflect.DeepEqual(rules[0].Allow, []string{"*"}) {
			t.Fatalf("rules=%+v policy=%+v", rules, policy.Rule)
		}
		// The rule now covers the workspace, so no request is raised.
		if err := confirmCloudModelSwitch(prompts, session, cfg, policy, ruleFile, workspace, "cloud/y", cloudConsentURL, notice); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("always local saves and refuses", func(t *testing.T) {
		policy, ruleFile := newPolicy()
		seen := respond(eventing.PermissionDecisionDeny, cloudLocalAlways)
		err := confirmCloudModelSwitch(prompts, session, cfg, policy, ruleFile, workspace, "cloud/x", cloudConsentURL, notice)
		if err == nil || !strings.Contains(err.Error(), "saved local-only model rule") {
			t.Fatalf("err=%v", err)
		}
		<-seen
		if rules := savedRules(t, ruleFile); len(rules) != 1 || !reflect.DeepEqual(rules[0].Allow, []string{"local/*"}) || policy.Check(cfg, "cloud/x") == nil {
			t.Fatalf("rules=%+v policy=%+v", rules, policy.Rule)
		}
	})
	t.Run("local models never ask", func(t *testing.T) {
		policy, ruleFile := newPolicy()
		if err := confirmCloudModelSwitch(prompts, session, cfg, policy, ruleFile, workspace, "local/a", "http://127.0.0.1:8080/v1", notice); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("without prompts it warns", func(t *testing.T) {
		policy, ruleFile := newPolicy()
		notices = nil
		if err := confirmCloudModelSwitch(context.Background(), session, cfg, policy, ruleFile, workspace, "cloud/x", cloudConsentURL, notice); err != nil || len(notices) != 1 || !strings.Contains(notices[0], "no model rule covers") {
			t.Fatalf("err=%v notices=%v", err, notices)
		}
	})
}

func TestReadModelOverride(t *testing.T) {
	var out strings.Builder
	terminal := ansi.NewTerminal(strings.NewReader("\r"), &out)
	q := cloudConsentQuestion{Workspace: "/w", Ref: "cloud/x", Host: "api.example.com", RuleFile: "/home/config.json", MidSession: true, RulePath: "~", RuleAllow: "local/*"}
	got, cancelled, err := readCloudConsent(terminal, q, time.Time{})
	if err != nil || got != cloudLocalOnce || cancelled {
		t.Fatalf("answer=%q cancelled=%v err=%v", got, cancelled, err)
	}
	for _, want := range []string{"Model not allowed here by model rule ~", "allow [local/*]", "cloud/x (api.example.com)", "[>Keep current<]", "Always allow here"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("prompt missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Always local here") {
		t.Fatalf("override prompt offers a local choice:\n%s", out.String())
	}
}

func TestConfirmModelOverride(t *testing.T) {
	session, respond := newConsentTestSession(t)
	cfg := cloudConsentConfig(true)
	workspace := session.WorkspaceDir()
	prompts := agent.WithPermissionPrompts(context.Background())
	var notices []string
	notice := func(message string) { notices = append(notices, message) }
	newPolicy := func(t *testing.T) (*config.ModelPolicy, string) {
		rule, err := config.NewWorkspaceRule(filepath.Dir(workspace), []string{"local/*"}, "personal.json")
		if err != nil {
			t.Fatal(err)
		}
		policy, err := (config.Config{}).PolicyForWorkspace(workspace)
		if err != nil {
			t.Fatal(err)
		}
		policy.Rule = rule
		return &policy, filepath.Join(t.TempDir(), "config.json")
	}

	t.Run("allowed models never ask", func(t *testing.T) {
		policy, ruleFile := newPolicy(t)
		if err := confirmModelOverride(prompts, session, cfg, policy, ruleFile, "local/a", "http://127.0.0.1:8080/v1", notice); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("keep current refuses", func(t *testing.T) {
		policy, ruleFile := newPolicy(t)
		seen := respond(eventing.PermissionDecisionDeny, cloudLocalOnce)
		err := confirmModelOverride(prompts, session, cfg, policy, ruleFile, "cloud/x", cloudConsentURL, notice)
		if err == nil || !strings.Contains(err.Error(), "current model kept") {
			t.Fatalf("err=%v", err)
		}
		request := <-seen
		if q := cloudConsentQuestionFromRequest(request); request.Capability != modelOverrideCapability || q.RulePath != policy.Rule.Path || q.RuleAllow != "local/*" || q.Ref != "cloud/x" {
			t.Fatalf("request=%+v question=%+v", request, q)
		}
	})
	t.Run("allow once leaves the rule alone", func(t *testing.T) {
		policy, ruleFile := newPolicy(t)
		seen := respond(eventing.PermissionDecisionAllow, cloudAllowOnce)
		if err := confirmModelOverride(prompts, session, cfg, policy, ruleFile, "cloud/x", cloudConsentURL, notice); err != nil {
			t.Fatal(err)
		}
		<-seen
		if policy.Allows(cfg, "cloud/x") || savedRules(t, ruleFile) != nil {
			t.Fatalf("allow once changed the rule: %+v", policy.Rule)
		}
	})
	t.Run("always allow adds the model for this directory", func(t *testing.T) {
		policy, ruleFile := newPolicy(t)
		seen := respond(eventing.PermissionDecisionAllow, cloudAllowAlways)
		if err := confirmModelOverride(prompts, session, cfg, policy, ruleFile, "cloud/x", cloudConsentURL, notice); err != nil {
			t.Fatal(err)
		}
		<-seen
		rules := savedRules(t, ruleFile)
		if len(rules) != 1 || !reflect.DeepEqual(rules[0].Allow, []string{"local/*", "cloud/x"}) || !policy.Allows(cfg, "cloud/x") || !policy.Allows(cfg, "local/a") {
			t.Fatalf("rules=%+v policy=%+v", rules, policy.Rule)
		}
		next, err := (config.Config{ModelRules: rules}).PolicyForWorkspace(workspace)
		if err != nil || next.Rule == nil {
			t.Fatalf("saved rule does not cover workspace: %+v err=%v", next, err)
		}
	})
	t.Run("without prompts it refuses", func(t *testing.T) {
		policy, ruleFile := newPolicy(t)
		err := confirmModelOverride(context.Background(), session, cfg, policy, ruleFile, "cloud/x", cloudConsentURL, notice)
		if err == nil || !strings.Contains(err.Error(), "current model kept") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestModelRulesReport(t *testing.T) {
	home := config.ModelRule{Path: "~", Allow: []string{"local/*"}, Source: "home.json"}
	public := config.ModelRule{Path: "~/src/public", Allow: []string{"*"}, Source: "home.json"}
	got := modelRulesReport([]config.ModelRule{home, public}, &home, "/Users/me/src/app")
	for _, want := range []string{"workspace: /Users/me/src/app", "  ~             allow [local/*]  (home.json)  ← active here\n", "  ~/src/public  allow [*]  (home.json)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "active here") != 1 {
		t.Fatalf("want one active rule:\n%s", got)
	}
	runOnly := config.ModelRule{Path: "/Users/me/tmp", Allow: []string{"local/*"}, Source: cloudConsentSource}
	if got := modelRulesReport(nil, &runOnly, "/Users/me/tmp"); !strings.Contains(got, "  /Users/me/tmp  allow [local/*]  (this run)  ← active here") {
		t.Fatalf("this-run rule missing:\n%s", got)
	}
	if got := modelRulesReport(nil, nil, "/w"); !strings.Contains(got, "no model rule covers") || !strings.Contains(got, "no model rules configured") {
		t.Fatalf("empty report:\n%s", got)
	}
}

func TestCloudConsentWarnsWithoutTerminal(t *testing.T) {
	for name, tt := range map[string]struct {
		withLocal, wantWarning bool
		activeModel            string
	}{
		"cloud model": {withLocal: false, wantWarning: true, activeModel: "cloud/x"},
		"local model": {withLocal: true, wantWarning: false, activeModel: "local/a"},
	} {
		t.Run(name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
				t.Setenv(key, "")
			}
			cfg := cloudConsentConfig(tt.withLocal)
			cfg.ActiveModel = tt.activeModel
			writeModelPolicyConfig(t, filepath.Join(home, ".config/caretz/config.json"), cfg)
			runner := func(params libruntime.InteractiveParams) error {
				if params.ModelRef != tt.activeModel {
					t.Fatalf("model=%s, want %s", params.ModelRef, tt.activeModel)
				}
				return nil
			}
			var stderr strings.Builder
			if code := RunWithOptions(t.Context(), []string{"--workspace", root, "--no-model-discovery"}, io.Discard, &stderr, Options{InteractiveRunner: runner}); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			if got := strings.Contains(stderr.String(), "no model rule covers"); got != tt.wantWarning {
				t.Fatalf("warning=%v, want %v: %s", got, tt.wantWarning, stderr.String())
			}
		})
	}
}

func TestExplicitLocalCloudConsent(t *testing.T) {
	for _, endpoint := range []string{"http://100.100.1.2/v1", "https://gpu.example.ts.net/v1", "http://127.0.0.1/v1"} {
		for _, local := range []bool{true, false} {
			cfg := cloudConsentConfig(false)
			p := cfg.Provider["cloud"]
			p.Options.BaseURL, p.Local = &endpoint, &local
			cfg.Provider["cloud"] = p
			policy := config.ModelPolicy{Workspace: t.TempDir()}
			asked := false
			choose := func(cloudConsentQuestion) (cloudAnswer, error) { asked = true; return cloudAllowOnce, nil }
			if _, err := confirmCloudWorkspace(choose, cfg, &policy, "cloud/x", endpoint, "unused"); err != nil || asked == local {
				t.Fatalf("endpoint=%s local=%v asked=%v err=%v", endpoint, local, asked, err)
			}
			q := newCloudConsentQuestion(cfg, policy.Workspace, "cloud/x", endpoint, "unused", false)
			if (q.Local == "cloud/x") != local {
				t.Fatalf("local choice=%s local=%v", q.Local, local)
			}
			var notices []string
			if err := confirmCloudModelSwitch(t.Context(), nil, cfg, &policy, "unused", policy.Workspace, "cloud/x", endpoint, func(s string) { notices = append(notices, s) }); err != nil || (len(notices) == 0) != local {
				t.Fatalf("switch local=%v notices=%v err=%v", local, notices, err)
			}
		}
	}
}

func TestReclassifiedProviderKeepsSavedRuleAndWarns(t *testing.T) {
	cfg := cloudConsentConfig(true)
	path := filepath.Join(t.TempDir(), "config.json")
	policy := config.ModelPolicy{Workspace: t.TempDir()}
	if err := applyCloudAnswer(cloudLocalAlways, cfg, &policy, path, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := cfg.Provider["local"]
	local := false
	p.Local = &local
	cfg.Provider["local"] = p
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var notices []string
	if err := confirmCloudModelSwitch(t.Context(), nil, cfg, &policy, path, policy.Workspace, "local/a", *p.Options.BaseURL, func(s string) { notices = append(notices, s) }); err != nil {
		t.Fatal(err)
	}
	if !policy.Allows(cfg, "local/a") || len(notices) != 1 || !strings.Contains(notices[0], "classified hosted") {
		t.Fatalf("policy=%+v notices=%v", policy, notices)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("saved rule changed: %v", err)
	}
}

func TestClassificationStartupPaths(t *testing.T) {
	for _, tt := range []struct {
		name, endpoint, override string
		local, env, warning      bool
	}{
		{name: "self-hosted tailscale", endpoint: "http://100.100.1.2/v1", local: true},
		{name: "hosted private proxy", endpoint: "http://127.0.0.1/v1", warning: true},
		{name: "CLI override", endpoint: "http://100.100.1.2/v1", local: true, override: cloudConsentURL, warning: true},
		{name: "environment override", endpoint: "http://100.100.1.2/v1", local: true, override: cloudConsentURL, env: true, warning: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			for _, key := range []string{"CARETZ_CONFIG_ROOT", "OPENAI_API_MODEL", "OPENAI_API_BASE_URL", "OPENAI_REASONING", "OPENAI_API_KEY"} {
				t.Setenv(key, "")
			}
			cfg := cloudConsentConfig(false)
			p := cfg.Provider["cloud"]
			p.Local, p.Options.BaseURL = &tt.local, &tt.endpoint
			cfg.Provider["cloud"] = p
			writeModelPolicyConfig(t, filepath.Join(home, ".config/caretz/config.json"), cfg)
			args := []string{"--workspace", root, "--no-model-discovery", "--incognito"}
			if tt.override != "" {
				if tt.env {
					t.Setenv("OPENAI_API_BASE_URL", tt.override)
				} else {
					args = append(args, "--base-url", tt.override)
				}
			}
			var stderr strings.Builder
			// A slash command exercises prompt startup without making an LLM request.
			if code := RunWithOptions(t.Context(), append(args, "-p", "/modelrules"), io.Discard, &stderr, Options{}); code != 0 {
				t.Fatalf("prompt startup=%d: %s", code, stderr.String())
			}
			check := func(output string) {
				t.Helper()
				if strings.Contains(output, "sending it to") != tt.warning {
					t.Fatalf("want warning=%v: %s", tt.warning, output)
				}
			}
			check(stderr.String())
			stderr.Reset()
			rt, code := BuildRuntime(t.Context(), args, &stderr)
			if code != 0 {
				t.Fatalf("runtime startup=%d: %s", code, stderr.String())
			}
			session, err := rt.NewAdapterSession("test", "classification", AdapterSessionOptions{WorkspaceDir: root})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			check(stderr.String())
			constructed, err := NewACPServer(rt, nil)
			if err != nil {
				t.Fatal(err)
			}
			acp, err := constructed.(*acpServer).openSession("classification", root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = acp.session.Close() }()
			check(strings.Join(acp.modelNotices, "\n"))
		})
	}
}

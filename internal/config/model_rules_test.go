package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelRuleDirectoryMatching(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Config{ModelRules: []ModelRule{{Path: "~", Allow: []string{"local/*"}}, {Path: "~/src/a/b", Allow: []string{"*"}}, {Path: "~/src/a/c", Allow: []string{"client/*", "local/qwen"}}}}
	if err := validateModelRules(cfg.ModelRules, "personal.json"); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ workspace, want string }{{"Downloads", "~"}, {"src/a/b/deep", "~/src/a/b"}, {"src/a/c/x", "~/src/a/c"}, {"src/a/cc", "~"}} {
		t.Run(tt.workspace, func(t *testing.T) {
			p, err := cfg.PolicyForWorkspace(filepath.Join(home, tt.workspace))
			if err != nil {
				t.Fatal(err)
			}
			if p.Rule == nil || p.Rule.Path != tt.want {
				t.Fatalf("policy=%+v want %s", p, tt.want)
			}
		})
	}
	p, err := cfg.PolicyForWorkspace(filepath.Dir(home))
	if err != nil || p.Rule != nil {
		t.Fatalf("outside policy=%+v err=%v", p, err)
	}
	real := filepath.Join(home, "src/a/c")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	p, err = cfg.PolicyForWorkspace(filepath.Join(link, "future"))
	if err != nil || p.Rule.Path != "~/src/a/c" {
		t.Fatalf("symlink policy=%+v err=%v", p, err)
	}
	cfg.ModelRules = []ModelRule{{Path: link, Allow: []string{}}}
	if err := validateModelRules(cfg.ModelRules, "personal.json"); err != nil {
		t.Fatal(err)
	}
	p, err = cfg.PolicyForWorkspace(real)
	if err != nil || p.Rule == nil {
		t.Fatalf("symlink rule=%+v err=%v", p, err)
	}
}

func TestModelRulesFailClosed(t *testing.T) {
	root := t.TempDir()
	for _, rules := range [][]ModelRule{
		{{Allow: []string{"*"}}}, {{Path: "relative", Allow: []string{"*"}}}, {{Path: root}},
		{{Path: root, Allow: []string{"provider"}}}, {{Path: root, Allow: []string{"p/foo*"}}}, {{Path: root, Allow: []string{"*/foo"}}},
		{{Path: root, Allow: []string{"*"}}, {Path: root + "/.", Allow: []string{"*"}}},
	} {
		if err := validateModelRules(rules, "personal.json"); err == nil {
			t.Fatalf("accepted invalid rules %+v", rules)
		}
	}
}

func TestModelRuleSelectorsAndFallback(t *testing.T) {
	cfg := Config{Provider: map[string]Provider{"local": {Models: map[string]Model{"alias": {Model: "vendor/qwen"}, "a": {Model: "actual-a"}}}, "cloud": {Models: map[string]Model{"x": {}}}}}
	for _, selector := range []string{"*", "local/*", "local/alias", "local/vendor/qwen"} {
		p := ModelPolicy{Workspace: "/workspace", Rule: &ModelRule{Path: "/workspace", Allow: []string{selector}, Source: "personal"}}
		if !p.Allows(cfg, "local/alias") || p.Allows(cfg, "unknown/alias") {
			t.Fatalf("selector %s incorrectly matched", selector)
		}
	}
	p := ModelPolicy{Workspace: "/workspace", Rule: &ModelRule{Path: "/workspace", Allow: []string{"local/*"}}}
	ref, notice, err := p.Select(cfg, "raw", nil)
	if err != nil || ref != "local/a" || !strings.Contains(notice, "raw") {
		t.Fatalf("fallback=%s %s %v", ref, notice, err)
	}
	ref, _, err = p.Select(cfg, "cloud/x", map[string]bool{"local/alias": true})
	if err != nil || ref != "local/alias" {
		t.Fatalf("intersection=%s %v", ref, err)
	}
	if _, _, err = p.Select(cfg, "cloud/x", map[string]bool{"cloud/x": true}); err == nil {
		t.Fatal("empty intersection accepted")
	}
	p.Rule.Allow = []string{}
	if _, _, err = p.Select(cfg, "local/a", nil); err == nil {
		t.Fatal("empty allow accepted")
	}
}

func TestPersonalAndWorkspaceRuleTrust(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "personal.json")
	project := filepath.Join(root, "workspace.json")
	if err := os.WriteFile(home, []byte(`{"modelRules":[{"path":"/","allow":[]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Even malformed rules in an untrusted workspace are ignored.
	if err := os.WriteFile(project, []byte(`{"modelRules":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProfile(home, project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ModelRules) != 1 || len(cfg.Warnings) != 1 {
		t.Fatalf("config=%+v", cfg)
	}
	if _, err := LoadProfileWithTrust(home, project, "", true); err == nil {
		t.Fatal("invalid trusted rules accepted")
	}
	if err := os.WriteFile(project, []byte(`{"modelRules":[{"path":"/","allow":["*"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadProfileWithTrust(home, project, "", true)
	if err != nil || cfg.ModelRules[0].Allow[0] != "*" || cfg.ModelRules[0].Source != project {
		t.Fatalf("trusted config=%+v err=%v", cfg, err)
	}
	if err := os.WriteFile(home, []byte(`{"modelRules":[{"path":"relative","allow":["*"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfileWithTrust(home, project, "", true); err == nil {
		t.Fatal("invalid home rules masked by config root")
	}
}

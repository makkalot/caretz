package sandbox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// sandboxCapability is one sandbox feature whose implementation differs per
// OS and therefore needs implementation tests on each backend. Shared
// behavior belongs in conformance_test.go instead.
type sandboxCapability struct {
	// only names the sole OS that has this capability; empty means both.
	only string
	// why explains an OS-exclusive capability.
	why string
}

// sandboxCapabilities is the registry that "// parity: <id> ..." tags on tests
// in *_darwin_test.go and *_linux_test.go refer to.
var sandboxCapabilities = map[string]sandboxCapability{
	"policy.filesystem":           {},
	"policy.readable-roots":       {},
	"policy.writable-roots":       {},
	"policy.read-only":            {},
	"policy.hidden-paths":         {},
	"policy.extra-binaries":       {},
	"policy.network-modes":        {},
	"policy.proxy-endpoint":       {},
	"policy.docker-socket":        {},
	"policy.dangerous-operations": {},
	"env.minimal":                 {},
	"env.proxy":                   {},
	"env.go-state":                {},
	"env.docker-host":             {},
	"session.control-channel":     {},
	"policy.path-escaping":        {only: "darwin", why: "Seatbelt profiles embed paths in SBPL text; bubblewrap receives paths as argv"},
	"policy.unix-sockets":         {only: "darwin", why: "Seatbelt gates Unix sockets with network rules; Linux gates them through the filesystem view (shared behavior is in conformance_test.go)"},
	"linux.bwrap-feature-probe":   {only: "linux", why: "bubblewrap options vary by version; sandbox-exec has no optional hardening flags"},
	"linux.container-mode":        {only: "linux", why: "Linux relaxes cgroup/seccomp setup inside containers; macOS never runs in one"},
	"linux.gpu-devices":           {only: "linux", why: "bubblewrap gives commands a minimal /dev, so GPU device nodes must be bound in; macOS reaches the GPU through IOKit, not device nodes"},
	"linux.system-bus":            {only: "linux", why: "macOS has no system D-Bus; Linux masks the bus socket under /run unless allowSystemBus is set for a non-root user"},
	"linux.var-run-link":          {only: "linux", why: "bubblewrap builds a fresh filesystem view and must recreate the host's /var/run -> /run link; Seatbelt filters the real filesystem"},
}

var sandboxPlatforms = []string{"darwin", "linux"}

// TestSandboxTestParity keeps the macOS and Linux test suites in step. It
// parses both platforms' test files regardless of build tags, so a change on
// one side fails here on either OS until the other side has a counterpart.
func TestSandboxTestParity(t *testing.T) {
	covered := map[string]map[string][]string{} // os -> capability -> tests
	for _, goos := range sandboxPlatforms {
		covered[goos] = map[string][]string{}
		files, err := filepath.Glob("*_" + goos + "_test.go")
		if err != nil || len(files) == 0 {
			t.Fatalf("no %s test files found (err=%v)", goos, err)
		}
		for _, path := range files {
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range parsed.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || strings.HasSuffix(fn.Name.Name, "Helper") {
					continue
				}
				ids := parityTags(fn.Doc)
				if len(ids) == 0 {
					t.Errorf("%s: %s has no \"// parity: <capability>\" tag; tag it, or move OS-neutral behavior to conformance_test.go", path, fn.Name.Name)
				}
				for _, id := range ids {
					if _, known := sandboxCapabilities[id]; !known {
						t.Errorf("%s: %s uses unknown capability %q; register it in sandboxCapabilities", path, fn.Name.Name, id)
						continue
					}
					covered[goos][id] = append(covered[goos][id], fn.Name.Name)
				}
			}
		}
	}
	ids := make([]string, 0, len(sandboxCapabilities))
	for id := range sandboxCapabilities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		capability := sandboxCapabilities[id]
		if capability.only != "" && (capability.why == "" || !slices.Contains(sandboxPlatforms, capability.only)) {
			t.Errorf("capability %q: exclusive capabilities need a valid OS and a reason", id)
		}
		for _, goos := range sandboxPlatforms {
			tests := covered[goos][id]
			switch {
			case capability.only == "" && len(tests) == 0:
				t.Errorf("capability %q has no %s test; counterpart tests: %v", id, goos, covered[otherPlatform(goos)][id])
			case capability.only == goos && len(tests) == 0:
				t.Errorf("%s-only capability %q has no test", goos, id)
			case capability.only != "" && capability.only != goos && len(tests) > 0:
				t.Errorf("%s-only capability %q is tagged on %s tests %v", capability.only, id, goos, tests)
			}
		}
	}
}

// TestSandboxConformanceSuiteIsPlatformNeutral keeps OS branches out of the shared
// suite; documented differences go through nativeSandboxBehavior.
func TestSandboxConformanceSuiteIsPlatformNeutral(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "conformance_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "GOOS" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "runtime" {
				t.Error("conformance_test.go branches on runtime.GOOS; add a sandboxBehavior field instead")
			}
		}
		return true
	})
}

func parityTags(doc *ast.CommentGroup) []string {
	if doc == nil {
		return nil
	}
	var ids []string
	for _, comment := range doc.List {
		if rest, ok := strings.CutPrefix(comment.Text, "// parity:"); ok {
			ids = append(ids, strings.Fields(rest)...)
		}
	}
	return ids
}

func otherPlatform(goos string) string {
	if goos == "darwin" {
		return "linux"
	}
	return "darwin"
}

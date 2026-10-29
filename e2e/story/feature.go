package story

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Feature groups scenarios into one generated .feature file. Declare features
// as package variables so every scenario registers into the same one.
type Feature struct {
	Name        string
	Description string

	mu        sync.Mutex
	scenarios []*scenarioRecord
}

var (
	featuresMu sync.Mutex
	features   []*Feature
)

// NewFeature registers a feature for .feature generation.
func NewFeature(name, description string) *Feature {
	f := &Feature{Name: name, Description: description}
	featuresMu.Lock()
	defer featuresMu.Unlock()
	features = append(features, f)
	return f
}

// Scenario starts a story whose steps are recorded under this feature. name is
// the plain-language title readers see in the generated file.
func (f *Feature) Scenario(t testing.TB, name string) *Story {
	t.Helper()
	_, file, line, _ := runtime.Caller(1)
	rec := &scenarioRecord{name: name, test: t.Name(), file: relToRepo(file), line: line}
	f.mu.Lock()
	f.scenarios = append(f.scenarios, rec)
	f.mu.Unlock()
	s := New(t)
	s.rec = rec
	return s
}

type phase string

const (
	phaseGiven phase = "Given"
	phaseWhen  phase = "When"
	phaseThen  phase = "Then"
)

type scenarioRecord struct {
	name string
	test string
	file string
	line int

	mu          sync.Mutex // guards description and steps
	description string
	steps       []recordedStep
}

type recordedStep struct {
	phase phase
	text  string
	doc   *string
}

func (s *Story) record(p phase, text string, doc *string) {
	if s.rec != nil {
		s.rec.mu.Lock()
		defer s.rec.mu.Unlock()
		s.rec.steps = append(s.rec.steps, recordedStep{phase: p, text: text, doc: doc})
	}
}

// Describe adds plain-language context shown under the scenario title.
func (s *Story) Describe(text string) *Story {
	if s.rec != nil {
		s.rec.mu.Lock()
		defer s.rec.mu.Unlock()
		s.rec.description = text
	}
	return s
}

const generatedHeader = "# Generated from the Go tests in e2e/ by `make e2e-features`. Do not edit."

// render returns the .feature file name and content for f.
func (f *Feature) render() (string, string) {
	f.mu.Lock()
	scenarios := append([]*scenarioRecord(nil), f.scenarios...)
	f.mu.Unlock()
	// Source order, so output does not depend on test scheduling.
	sort.Slice(scenarios, func(i, j int) bool {
		if scenarios[i].file != scenarios[j].file {
			return scenarios[i].file < scenarios[j].file
		}
		return scenarios[i].line < scenarios[j].line
	})

	var b strings.Builder
	b.WriteString(generatedHeader + "\n")
	b.WriteString("Feature: " + f.Name + "\n")
	writeIndented(&b, "  ", f.Description)
	for _, sc := range scenarios {
		sc.mu.Lock()
		description, steps := sc.description, append([]recordedStep(nil), sc.steps...)
		sc.mu.Unlock()
		b.WriteString("\n")
		fmt.Fprintf(&b, "  # Test: %s (%s)\n", sc.test, sc.file)
		b.WriteString("  Scenario: " + sc.name + "\n")
		writeIndented(&b, "    ", description)
		var prev phase
		for _, step := range steps {
			keyword := string(step.phase)
			if step.phase == prev {
				keyword = "And"
			}
			prev = step.phase
			b.WriteString("    " + keyword + " " + step.text + "\n")
			if step.doc != nil {
				b.WriteString(`      """` + "\n")
				writeIndented(&b, "      ", *step.doc)
				b.WriteString(`      """` + "\n")
			}
		}
	}
	return featureFileName(f.Name), b.String()
}

func writeIndented(b *strings.Builder, indent, text string) {
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(indent + line + "\n")
	}
}

func featureFileName(name string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, name)
	return slug + ".feature"
}

func renderAll() map[string]string {
	featuresMu.Lock()
	defer featuresMu.Unlock()
	out := map[string]string{}
	for _, f := range features {
		name, content := f.render()
		out[name] = content
	}
	return out
}

// WriteFeatures writes one .feature file per registered feature into dir and
// removes .feature files there that no longer have a feature.
func WriteFeatures(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	want := renderAll()
	existing, err := filepath.Glob(filepath.Join(dir, "*.feature"))
	if err != nil {
		return err
	}
	for _, path := range existing {
		if _, ok := want[filepath.Base(path)]; !ok {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	for name, content := range want {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// CheckFeatures reports an error if the .feature files in dir differ from what
// the tests that just ran would generate.
func CheckFeatures(dir string) error {
	want := renderAll()
	var problems []string
	existing, err := filepath.Glob(filepath.Join(dir, "*.feature"))
	if err != nil {
		return err
	}
	for _, path := range existing {
		if _, ok := want[filepath.Base(path)]; !ok {
			problems = append(problems, fmt.Sprintf("%s: no test generates this file", path))
		}
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: missing", path))
			continue
		}
		if diff := firstDiff(string(got), want[name]); diff != "" {
			problems = append(problems, fmt.Sprintf("%s: %s", path, diff))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("generated .feature files are stale; run `make e2e-features`:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func firstDiff(got, want string) string {
	if got == want {
		return ""
	}
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return fmt.Sprintf("line %d is %q, want %q", i+1, gl, wl)
		}
	}
	return "differs"
}

// relToRepo turns an absolute source path into one relative to the repo root.
func relToRepo(file string) string {
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(self), "..", "..")
	if rel, err := filepath.Rel(root, file); err == nil {
		return filepath.ToSlash(rel)
	}
	return file
}

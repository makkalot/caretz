// Package story is a Given/When/Then DSL for caretz end-to-end tests.
//
//	var echo = story.NewFeature("Echo", "caretz prints the model's reply.")
//
//	func TestEcho(t *testing.T) {
//		s := echo.Scenario(t, "model replies with text")
//		s.Given().Model(story.Replies("hi"))
//		s.When().RunPrompt("say hi")
//		s.Then().
//			Succeeds().
//			Stdout(story.Contains("hi")).
//			Request(1).User(story.Contains("say hi"))
//	}
//
// The DSL is a small set of generic steps rather than one method per test:
// Given scripts the model and prepares files, config, and environment; When
// runs caretz with any arguments; Then combines a subject (stdout, stderr, a
// workspace file, a request caretz sent to the model) with a Matcher
// (Contains, Equals, Matches, Not). For a check the steps cannot express,
// Then().That takes a sentence and a function.
//
// Given, When, and Then return different types, so a setup step cannot be
// written after the run or an assertion before it. Every method calls
// t.Helper, so a failure is reported at the line in the test that made it.
//
// Each step also records a plain-language sentence. Stories started with
// Feature.Scenario are written out as generated .feature files (see
// WriteFeatures), so non-developers can read what the tests cover.
package story

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/e2e/fakellm"
	"github.com/makkalot/caretz/e2e/harness"
)

// Story is one scenario: a scripted model, an isolated caretz environment, and
// the result of the latest caretz run.
type Story struct {
	t   testing.TB
	LLM *fakellm.Server
	Env *harness.Env
	Res *harness.Result

	rec *scenarioRecord // nil when the story is not part of a Feature
}

// New starts a fake model and an isolated environment for one scenario that
// is not recorded into any generated .feature file.
func New(t testing.TB) *Story {
	t.Helper()
	llm := fakellm.New(t)
	return &Story{t: t, LLM: llm, Env: harness.New(t, llm.URL, fakellm.Model)}
}

// Given starts the setup phase.
func (s *Story) Given() *Given { return &Given{s} }

// When starts the action phase.
func (s *Story) When() *When { return &When{s} }

// Then starts the assertion phase. It fails the test if caretz has not run.
func (s *Story) Then() *Then {
	s.t.Helper()
	if s.Res == nil {
		s.t.Fatal("story: Then() before caretz ran; call When().Run first")
	}
	return &Then{s}
}

// Part is one piece of a scripted model reply: text or a tool call.
type Part struct {
	text string
	call *fakellm.ToolCall
}

// Replies is reply text.
func Replies(text string) Part { return Part{text: text} }

// Calls is a request for caretz to run tool with JSON arguments. The fake
// model assigns a call ID unless ID sets one.
func Calls(tool, args string) Part {
	return Part{call: &fakellm.ToolCall{Name: tool, Arguments: args}}
}

// ID sets the tool call's ID, so Then().Request(n).ToolResult can find the
// result caretz sends back for it.
func (p Part) ID(id string) Part {
	if p.call == nil {
		panic("story: ID on a text part; use it on Calls")
	}
	call := *p.call
	call.ID = id
	p.call = &call
	return p
}

// Given scripts the model and prepares the environment.
type Given struct{ s *Story }

// Model queues one model reply made of parts. Call it once per reply, in the
// order caretz will request them.
func (g *Given) Model(parts ...Part) *Given {
	g.s.t.Helper()
	if len(parts) == 0 {
		g.s.t.Fatal("story: Model needs at least one part")
	}
	var turn fakellm.Turn
	for i, p := range parts {
		subject := "the model"
		if i > 0 {
			subject = "in the same reply the model"
		}
		switch {
		case p.call != nil:
			text := fmt.Sprintf("%s calls %q with:", subject, p.call.Name)
			if p.call.ID != "" {
				text = fmt.Sprintf("%s calls %q as %q with:", subject, p.call.Name, p.call.ID)
			}
			args := p.call.Arguments
			g.s.record(phaseGiven, text, &args)
			turn.ToolCalls = append(turn.ToolCalls, *p.call)
		case strings.Contains(p.text, "\n"):
			text := p.text
			g.s.record(phaseGiven, subject+" replies with:", &text)
			turn.Text += p.text
		default:
			g.s.record(phaseGiven, fmt.Sprintf("%s replies %q", subject, p.text), nil)
			turn.Text += p.text
		}
	}
	g.s.LLM.Push(turn)
	return g
}

// File creates a file relative to the workspace.
func (g *Given) File(rel, content string) *Given {
	g.s.t.Helper()
	g.s.record(phaseGiven, fmt.Sprintf("workspace file %q with:", rel), &content)
	writeFile(g.s.t, filepath.Join(g.s.Env.Workspace, rel), content)
	return g
}

// HomeFile creates a file relative to caretz's isolated HOME, for example
// ".config/caretz/skills/x/SKILL.md".
func (g *Given) HomeFile(rel, content string) *Given {
	g.s.t.Helper()
	g.s.record(phaseGiven, fmt.Sprintf("home file %q with:", rel), &content)
	writeFile(g.s.t, filepath.Join(g.s.Env.Home, rel), content)
	return g
}

// Config writes the config root's config.json.
func (g *Given) Config(json string) *Given {
	g.s.t.Helper()
	g.s.record(phaseGiven, "the caretz config:", &json)
	writeFile(g.s.t, filepath.Join(g.s.Env.ConfigRoot, "config.json"), json)
	return g
}

// Env sets an environment variable for every later run.
func (g *Given) Env(key, value string) *Given {
	g.s.record(phaseGiven, fmt.Sprintf("environment variable %q is %q", key, value), nil)
	g.s.Env.ExtraEnv = append(g.s.Env.ExtraEnv, key+"="+value)
	return g
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// When runs caretz.
type When struct{ s *Story }

// Run runs caretz with args after the harness's endpoint, workspace, and
// config-root flags. Then asserts on the latest run.
func (w *When) Run(args ...string) *When {
	w.s.t.Helper()
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	w.s.record(phaseWhen, "I run caretz with arguments "+strings.Join(quoted, " "), nil)
	w.s.Res = w.s.Env.Run(args...)
	return w
}

// RunPrompt runs caretz -p prompt.
func (w *When) RunPrompt(prompt string) *When {
	w.s.t.Helper()
	w.s.record(phaseWhen, fmt.Sprintf("I run caretz with prompt %q", prompt), nil)
	w.s.Res = w.s.Env.Run("-p", prompt)
	return w
}

// Then asserts on caretz's output, the filesystem, and what caretz sent to the
// model.
type Then struct{ s *Story }

// assert records "subject matcher" and fails unless m matches got.
func (th *Then) assert(subject, got string, m Matcher) {
	th.s.t.Helper()
	th.s.record(phaseThen, subject+" "+m.desc, nil)
	if !m.match(got) {
		th.s.t.Fatalf("expected %s %s\ngot: %q\n%s", subject, m.desc, got, th.s.Res.Dump())
	}
}

// Succeeds fails unless caretz exited 0.
func (th *Then) Succeeds() *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, "it succeeds", nil)
	th.s.Res.Success()
	return th
}

// ExitCode fails unless caretz exited with code.
func (th *Then) ExitCode(code int) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("it exits with code %d", code), nil)
	if th.s.Res.ExitCode != code {
		th.s.t.Fatalf("exit code = %d, want %d\n%s", th.s.Res.ExitCode, code, th.s.Res.Dump())
	}
	return th
}

// Stdout checks caretz's standard output.
func (th *Then) Stdout(m Matcher) *Then {
	th.s.t.Helper()
	th.assert("stdout", th.s.Res.Stdout, m)
	return th
}

// Stderr checks caretz's standard error.
func (th *Then) Stderr(m Matcher) *Then {
	th.s.t.Helper()
	th.assert("stderr", th.s.Res.Stderr, m)
	return th
}

// File checks the content of a file relative to the workspace, failing if it
// does not exist.
func (th *Then) File(rel string, m Matcher) *Then {
	th.s.t.Helper()
	subject := fmt.Sprintf("workspace file %q", rel)
	data, err := os.ReadFile(filepath.Join(th.s.Env.Workspace, rel))
	if err != nil {
		th.s.record(phaseThen, subject+" "+m.desc, nil)
		th.s.t.Fatalf("expected %s %s: %v", subject, m.desc, err)
	}
	th.assert(subject, string(data), m)
	return th
}

// Exists fails unless a path relative to the workspace exists. It may point
// outside the workspace, as in "../escaped.txt".
func (th *Then) Exists(rel string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("path %q exists", rel), nil)
	if _, err := os.Stat(filepath.Join(th.s.Env.Workspace, rel)); err != nil {
		th.s.t.Fatalf("expected path %s to exist: %v", rel, err)
	}
	return th
}

// Missing fails if a path relative to the workspace exists. It may point
// outside the workspace, as in "../escaped.txt".
func (th *Then) Missing(rel string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("path %q does not exist", rel), nil)
	if _, err := os.Stat(filepath.Join(th.s.Env.Workspace, rel)); err == nil {
		th.s.t.Fatalf("expected path %s not to exist", rel)
	}
	return th
}

// RequestCount fails unless caretz sent exactly n requests to the model.
func (th *Then) RequestCount(n int) *Then {
	th.s.t.Helper()
	noun := "requests"
	if n == 1 {
		noun = "request"
	}
	th.s.record(phaseThen, fmt.Sprintf("the model received %d %s", n, noun), nil)
	if got := len(th.s.LLM.Requests()); got != n {
		th.s.t.Fatalf("model received %d request(s), want %d", got, n)
	}
	return th
}

// That is the escape hatch for a check the generic steps cannot express:
// sentence is recorded as written and check fails the test by returning an
// error.
func (th *Then) That(sentence string, check func(s *Story) error) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, sentence, nil)
	if err := check(th.s); err != nil {
		th.s.t.Fatalf("%s: %v", sentence, err)
	}
	return th
}

// Request selects the n-th request caretz sent to the model, counting from 1
// as the .feature file does.
func (th *Then) Request(n int) *RequestThen {
	th.s.t.Helper()
	if n < 1 {
		th.s.t.Fatalf("story: Request(%d): requests are numbered from 1", n)
	}
	return &RequestThen{Then: th, n: n}
}

// LastRequest selects the last request caretz sent to the model.
func (th *Then) LastRequest() *RequestThen { return &RequestThen{Then: th} }

// RequestThen asserts on one request caretz sent to the model. It embeds Then,
// so the chain can continue with any other assertion.
type RequestThen struct {
	*Then
	n int // 1-based; 0 means the last request
}

func (r *RequestThen) name() string {
	if r.n == 0 {
		return "the last request"
	}
	return fmt.Sprintf("request %d", r.n)
}

func (r *RequestThen) request() fakellm.Request {
	r.s.t.Helper()
	reqs := r.s.LLM.Requests()
	switch {
	case len(reqs) == 0:
		r.s.t.Fatal("caretz sent no requests to the model")
	case r.n == 0:
		return reqs[len(reqs)-1]
	case r.n > len(reqs):
		r.s.t.Fatalf("%s not received; the model got %d request(s)", r.name(), len(reqs))
	}
	return reqs[r.n-1]
}

// User checks the request's latest user message.
func (r *RequestThen) User(m Matcher) *RequestThen {
	r.s.t.Helper()
	r.assert("the user message in "+r.name(), r.request().LastUserMessage().Content, m)
	return r
}

// System checks the request's system prompt.
func (r *RequestThen) System(m Matcher) *RequestThen {
	r.s.t.Helper()
	r.assert("the system prompt in "+r.name(), r.request().SystemPrompt(), m)
	return r
}

// OffersTool fails unless the request advertised tool.
func (r *RequestThen) OffersTool(tool string) *RequestThen {
	r.s.t.Helper()
	r.s.record(phaseThen, fmt.Sprintf("%s offers the %q tool", r.name(), tool), nil)
	if !r.request().HasTool(tool) {
		r.s.t.Fatalf("%s does not offer tool %s", r.name(), tool)
	}
	return r
}

// HasToolResult fails unless the request carries the result for callID.
func (r *RequestThen) HasToolResult(callID string) *RequestThen {
	r.s.t.Helper()
	r.s.record(phaseThen, fmt.Sprintf("%s has a tool result for %q", r.name(), callID), nil)
	r.toolResult(callID)
	return r
}

// ToolResult checks the result caretz sent back for callID.
func (r *RequestThen) ToolResult(callID string, m Matcher) *RequestThen {
	r.s.t.Helper()
	r.assert(fmt.Sprintf("the tool result for %q in %s", callID, r.name()), r.toolResult(callID).Content, m)
	return r
}

func (r *RequestThen) toolResult(callID string) fakellm.Message {
	r.s.t.Helper()
	req := r.request()
	msg, ok := req.ToolResult(callID)
	if !ok {
		r.s.t.Fatalf("%s has no tool result for %q; last message: %s", r.name(), callID, req.LastMessage().Describe())
	}
	return msg
}

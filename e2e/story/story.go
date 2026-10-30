// Package story is a Given/When/Then DSL for caretz end-to-end tests, written
// so the generated .feature files read as user stories.
//
//	var asking = story.NewFeature("Asking caretz",
//		"As a developer\nI want to ask caretz for help\nSo that ...")
//
//	func TestAnswer(t *testing.T) {
//		s := asking.Scenario(t, "caretz answers a question")
//		s.Given().Model(story.Replies("hi"))
//		s.When().Ask("say hi")
//		s.Then().Succeeds().Stdout(story.Contains("hi"))
//	}
//
// The DSL is a small set of generic steps rather than one method per test:
// Given scripts the assistant (the fake model) and prepares files, config, and
// environment; When asks caretz something or runs it with any arguments; Then
// combines a subject (caretz's answer, a file, what the assistant saw) with a
// Matcher (Contains, Equals, Matches, Not). For a check the steps cannot
// express, Then().That takes a sentence and a function.
//
// Step sentences describe what a user sees, not how caretz is built: "the
// assistant runs:" rather than a tool name and JSON arguments. Request(n) is
// there for the rare scenario that needs to inspect the model traffic.
//
// Given, When, and Then return different types, so a setup step cannot be
// written after the run or an assertion before it. Every method calls
// t.Helper, so a failure is reported at the line in the test that made it.
//
// Stories started with Feature.Scenario are written out as generated .feature
// files (see WriteFeatures), so non-developers can read what the tests cover.
package story

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

	calls []scriptedCall // tool calls scripted so far, in order
	sites map[string]*website
	rec   *scenarioRecord
}

type scriptedCall struct {
	id     string
	source string // "the command" or "the \"Read\" tool", for Then().Saw
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
		s.t.Fatal("story: Then() before caretz ran; call When().Ask or When().Run first")
	}
	return &Then{s}
}

// Part is one piece of a scripted assistant reply: text or a tool call. Each
// part carries the sentence the .feature file shows for it, so tool names and
// JSON arguments stay out of the user story.
type Part struct {
	text string
	call *fakellm.ToolCall

	speaker string  // "the assistant" unless As names an agent
	verb    string  // what the speaker does: "runs:", "reads \"x\""
	doc     *string // doc string shown under the sentence
	source  string  // what a tool result came from, for Then().Saw
}

// Replies is reply text.
func Replies(text string) Part {
	if strings.Contains(text, "\n") {
		return Part{text: text, verb: "replies:", doc: &text}
	}
	return Part{text: text, verb: fmt.Sprintf("replies %q", text)}
}

// As attributes a reply to a subagent, for scripting the turns a child agent
// takes after Delegates: `the "reviewer" agent replies "..."`.
func (p Part) As(agent string) Part {
	p.speaker = fmt.Sprintf("the %q agent", agent)
	return p
}

// RunsCommand is a shell command the assistant runs with the Bash tool.
func RunsCommand(command string) Part {
	return toolPart("Bash", map[string]any{"command": command}, "runs:", &command, "the command")
}

// RunsInBackground is a shell command the assistant starts in the background,
// so it keeps running while later commands execute.
func RunsInBackground(command string) Part {
	return toolPart("Bash", map[string]any{"command": command, "background": true}, "starts in the background:", &command, "the background command")
}

// Reads is the assistant reading a file with the Read tool.
func Reads(path string) Part {
	return toolPart("Read", map[string]any{"file_path": path}, fmt.Sprintf("reads %q", path), nil, fmt.Sprintf("reading %q", path))
}

// Writes is the assistant creating or replacing a file with the Write tool.
func Writes(path, content string) Part {
	return toolPart("Write", map[string]any{"file_path": path, "content": content}, fmt.Sprintf("writes %q:", path), &content, fmt.Sprintf("writing %q", path))
}

// Edits is the assistant replacing old with new in a file with the Edit tool.
func Edits(path, old, new string) Part {
	args := map[string]any{"file_path": path, "edits": []map[string]string{{"old_text": old, "new_text": new}}}
	return toolPart("Edit", args, fmt.Sprintf("changes %q to %q in %q", old, new, path), nil, fmt.Sprintf("editing %q", path))
}

// Delegates is the assistant handing task to a subagent with CallSubagent.
// Script the child's turns next, with Replies(...).As(agent).
func Delegates(agent, task string) Part {
	args := map[string]any{"agent": agent, "task": task}
	return toolPart("CallSubagent", args, fmt.Sprintf("hands %q to the %q agent", task, agent), nil, fmt.Sprintf("the %q agent", agent))
}

// Calls is a request for caretz to run any tool with JSON arguments. Prefer a
// task-shaped part such as RunsCommand when one fits; it reads better.
func Calls(tool, args string) Part {
	return Part{
		call:   &fakellm.ToolCall{Name: tool, Arguments: args},
		verb:   fmt.Sprintf("uses the %q tool with:", tool),
		doc:    &args,
		source: fmt.Sprintf("the %q tool", tool),
	}
}

func toolPart(tool string, args map[string]any, verb string, doc *string, source string) Part {
	data, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return Part{call: &fakellm.ToolCall{Name: tool, Arguments: string(data)}, verb: verb, doc: doc, source: source}
}

// ID sets the tool call's ID for Then().Request(n).ToolResult. Story assigns
// one otherwise; it never appears in the .feature file.
func (p Part) ID(id string) Part {
	if p.call == nil {
		panic("story: ID on a text part; use it on a tool part")
	}
	call := *p.call
	call.ID = id
	p.call = &call
	return p
}

// Given scripts the assistant and prepares the environment.
type Given struct{ s *Story }

// Model queues one assistant reply made of parts. Call it once per reply, in
// the order caretz will request them.
func (g *Given) Model(parts ...Part) *Given {
	g.s.t.Helper()
	if len(parts) == 0 {
		g.s.t.Fatal("story: Model needs at least one part")
	}
	var turn fakellm.Turn
	for i, p := range parts {
		subject := p.speaker
		if subject == "" {
			subject = "the assistant"
		}
		if i > 0 {
			subject = "in the same reply " + subject
		}
		g.s.record(phaseGiven, subject+" "+p.verb, p.doc)
		if p.call == nil {
			turn.Text += p.text
			continue
		}
		call := *p.call
		call.Arguments = g.s.expand(call.Arguments)
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d", len(g.s.calls)+1)
		}
		g.s.calls = append(g.s.calls, scriptedCall{id: call.ID, source: p.source})
		turn.ToolCalls = append(turn.ToolCalls, call)
	}
	g.s.LLM.Push(turn)
	return g
}

// File creates a file relative to the project (the workspace). It may point
// next to the project, as in "../docs/guide.md".
func (g *Given) File(rel, content string) *Given {
	g.s.t.Helper()
	g.s.record(phaseGiven, fmt.Sprintf("a file %q containing:", rel), &content)
	writeFile(g.s.t, filepath.Join(g.s.Env.Workspace, rel), content)
	return g
}

// Folder creates an empty folder relative to the project.
func (g *Given) Folder(rel string) *Given {
	g.s.t.Helper()
	g.s.record(phaseGiven, fmt.Sprintf("a folder %q", rel), nil)
	if err := os.MkdirAll(filepath.Join(g.s.Env.Workspace, rel), 0o755); err != nil {
		g.s.t.Fatal(err)
	}
	return g
}

// HomeFile creates a file relative to caretz's isolated HOME, for example
// ".config/caretz/skills/x/SKILL.md".
func (g *Given) HomeFile(rel, content string) *Given {
	g.s.t.Helper()
	g.s.record(phaseGiven, fmt.Sprintf("a file \"~/%s\" containing:", rel), &content)
	writeFile(g.s.t, filepath.Join(g.s.Env.Home, rel), content)
	return g
}

// Config writes the config root's config.json.
func (g *Given) Config(json string) *Given {
	g.s.t.Helper()
	g.s.record(phaseGiven, "my caretz config is:", &json)
	writeFile(g.s.t, filepath.Join(g.s.Env.ConfigRoot, "config.json"), g.s.expand(json))
	return g
}

// Website starts a local web server that serves body and stands in for host.
// Declare it before the steps that mention host: Model, Config, and the flags
// given to Ask or Run have host replaced by the server's local address, so
// the .feature file reads "curl http://docs.test/" with no random port. Use a
// .test name so nothing ever resolves it for real.
func (g *Given) Website(host, body string) *Given {
	g.s.t.Helper()
	g.s.record(phaseGiven, fmt.Sprintf("a website %q serving:", host), &body)
	site := &website{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		site.visits.Add(1)
		_, _ = io.WriteString(w, body)
	}))
	// The Linux sandbox sends localhost and 127.0.0.1 straight to its private
	// loopback (NO_PROXY), so serve on 127.0.0.2 to go through the proxy like a
	// real website. macOS has only 127.0.0.1 and sets no NO_PROXY.
	if ln, err := net.Listen("tcp4", "127.0.0.2:0"); err == nil {
		_ = srv.Listener.Close()
		srv.Listener = ln
	}
	srv.Start()
	g.s.t.Cleanup(srv.Close)
	site.addr = srv.Listener.Addr().String()
	if g.s.sites == nil {
		g.s.sites = map[string]*website{}
	}
	g.s.sites[host] = site
	return g
}

type website struct {
	addr   string // 127.0.0.2:port, or 127.0.0.1:port where 127.0.0.2 is unavailable
	visits atomic.Int32
}

// expand replaces each website's host name with its real address.
func (s *Story) expand(text string) string {
	for host, site := range s.sites {
		text = strings.ReplaceAll(text, host, site.addr)
	}
	return text
}

// unexpand is the reverse of expand, for output caretz produced.
func (s *Story) unexpand(text string) string {
	for host, site := range s.sites {
		text = strings.ReplaceAll(text, site.addr, host)
	}
	return text
}

func (s *Story) expandAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = s.expand(a)
	}
	return out
}

// Env sets an environment variable for every later run.
func (g *Given) Env(key, value string) *Given {
	g.s.record(phaseGiven, fmt.Sprintf("my shell has %s set to %q", key, value), nil)
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

// Ask runs caretz -p prompt, with any extra flags after it.
func (w *When) Ask(prompt string, flags ...string) *When {
	w.s.t.Helper()
	text := fmt.Sprintf("I ask caretz %q", prompt)
	if len(flags) > 0 {
		text += " with " + shellJoin(flags)
	}
	w.s.record(phaseWhen, text, nil)
	w.s.Res = w.s.Env.Run(append([]string{"-p", prompt}, w.s.expandAll(flags)...)...)
	return w
}

// Continue resumes the session the previous run saved, as `caretz -s <id>
// -p prompt` does, with any extra flags after it.
func (w *When) Continue(prompt string, flags ...string) *When {
	w.s.t.Helper()
	text := fmt.Sprintf("I continue that session with %q", prompt)
	if len(flags) > 0 {
		text += " with " + shellJoin(flags)
	}
	w.s.record(phaseWhen, text, nil)
	args := append([]string{"-s", w.s.lastSession(), "-p", prompt}, w.s.expandAll(flags)...)
	w.s.Res = w.s.Env.Run(args...)
	return w
}

// lastSession returns the ID of the most recently written prompt-mode
// session. Caretz does not print it in prompt mode, so it is read from the
// config root's session store.
func (s *Story) lastSession() string {
	s.t.Helper()
	dir := filepath.Join(s.Env.ConfigRoot, "sessions", "cli")
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.t.Fatalf("story: no saved session to continue: %v", err)
	}
	var latest string
	var latestTime int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() {
			continue
		}
		if mod := info.ModTime().UnixNano(); latest == "" || mod > latestTime {
			latest, latestTime = e.Name(), mod
		}
	}
	if latest == "" {
		s.t.Fatalf("story: no saved session in %s", dir)
	}
	return latest
}

// Run runs caretz with args after the harness's endpoint, workspace, and
// config-root flags. Then asserts on the latest run.
func (w *When) Run(args ...string) *When {
	w.s.t.Helper()
	w.s.record(phaseWhen, "I run `caretz "+shellJoin(args)+"`", nil)
	w.s.Res = w.s.Env.Run(w.s.expandAll(args)...)
	return w
}

// shellJoin renders args as a user would type them.
func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'$`\\") {
			a = fmt.Sprintf("%q", a)
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// Then asserts on what caretz did, from the user's point of view.
type Then struct{ s *Story }

// assert records "subject matcher" and fails unless m matches got.
// Website addresses in got are shown by their host names first, so matchers
// can say "docs.test" as the rest of the story does.
func (th *Then) assert(subject, got string, m Matcher) {
	th.s.t.Helper()
	th.s.record(phaseThen, subject+" "+m.desc, nil)
	got = th.s.unexpand(got)
	if !m.match(got) {
		th.s.t.Fatalf("expected %s %s\ngot: %q\n%s", subject, m.desc, got, th.s.Res.Dump())
	}
}

// Succeeds fails unless caretz exited 0.
func (th *Then) Succeeds() *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, "caretz finishes successfully", nil)
	th.s.Res.Success()
	return th
}

// ExitCode fails unless caretz exited with code.
func (th *Then) ExitCode(code int) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("caretz exits with code %d", code), nil)
	if th.s.Res.ExitCode != code {
		th.s.t.Fatalf("exit code = %d, want %d\n%s", th.s.Res.ExitCode, code, th.s.Res.Dump())
	}
	return th
}

// Stdout checks what caretz printed: in prompt mode, its answer.
func (th *Then) Stdout(m Matcher) *Then {
	th.s.t.Helper()
	th.assert("caretz's answer", th.s.Res.Stdout, m)
	return th
}

// Stderr checks caretz's error output.
func (th *Then) Stderr(m Matcher) *Then {
	th.s.t.Helper()
	th.assert("caretz's error output", th.s.Res.Stderr, m)
	return th
}

// File checks the content of a file relative to the project, failing if it
// does not exist. It may point next to the project, as in "../out/x.txt".
func (th *Then) File(rel string, m Matcher) *Then {
	th.s.t.Helper()
	subject := fmt.Sprintf("the file %q", rel)
	data, err := os.ReadFile(filepath.Join(th.s.Env.Workspace, rel))
	if err != nil {
		th.s.record(phaseThen, subject+" "+m.desc, nil)
		th.s.t.Fatalf("expected %s %s: %v", subject, m.desc, err)
	}
	th.assert(subject, string(data), m)
	return th
}

// Exists fails unless a path relative to the project exists.
func (th *Then) Exists(rel string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("the file %q exists", rel), nil)
	if _, err := os.Stat(filepath.Join(th.s.Env.Workspace, rel)); err != nil {
		th.s.t.Fatalf("expected %s to exist: %v", rel, err)
	}
	return th
}

// Missing fails if a path relative to the project exists.
func (th *Then) Missing(rel string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("the file %q does not exist", rel), nil)
	if _, err := os.Stat(filepath.Join(th.s.Env.Workspace, rel)); err == nil {
		th.s.t.Fatalf("expected %s not to exist", rel)
	}
	return th
}

// Visited fails unless the website declared with Given().Website was visited.
func (th *Then) Visited(host string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("the website %q was visited", host), nil)
	if th.site(host).visits.Load() == 0 {
		th.s.t.Fatalf("website %s was never visited\n%s", host, th.s.Res.Dump())
	}
	return th
}

// NotVisited fails if the website declared with Given().Website was visited.
func (th *Then) NotVisited(host string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("the website %q was not visited", host), nil)
	if n := th.site(host).visits.Load(); n > 0 {
		th.s.t.Fatalf("website %s was visited %d time(s)", host, n)
	}
	return th
}

func (th *Then) site(host string) *website {
	th.s.t.Helper()
	site, ok := th.s.sites[host]
	if !ok {
		th.s.t.Fatalf("story: no website %q; declare it with Given().Website", host)
	}
	return site
}

// Prompt checks the prompt the assistant received in caretz's first request.
func (th *Then) Prompt(m Matcher) *Then {
	th.s.t.Helper()
	th.assert("the prompt the assistant received", th.s.LLM.Request(0).LastUserMessage().Content, m)
	return th
}

// Instructions checks the system prompt caretz sent with its first request:
// project rules, skills, and the base prompt.
func (th *Then) Instructions(m Matcher) *Then {
	th.s.t.Helper()
	th.assert("the instructions the assistant received", th.s.LLM.Request(0).SystemPrompt(), m)
	return th
}

// Conversation checks every message caretz sent in its last request, for
// stories about what the assistant remembers.
func (th *Then) Conversation(m Matcher) *Then {
	th.s.t.Helper()
	reqs := th.s.LLM.Requests()
	if len(reqs) == 0 {
		th.s.t.Fatal("caretz sent no requests to the model")
	}
	var b strings.Builder
	for _, msg := range reqs[len(reqs)-1].Messages {
		if msg.Role != "system" {
			b.WriteString(msg.Content + "\n")
		}
	}
	th.assert("the conversation the assistant received", b.String(), m)
	return th
}

// RequestCount fails unless caretz sent exactly n requests to the model.
func (th *Then) RequestCount(n int) *Then {
	th.s.t.Helper()
	text := fmt.Sprintf("caretz contacted the assistant %d times", n)
	switch n {
	case 0:
		text = "caretz did not contact the assistant"
	case 1:
		text = "caretz contacted the assistant once"
	}
	th.s.record(phaseThen, text, nil)
	if got := len(th.s.LLM.Requests()); got != n {
		th.s.t.Fatalf("model received %d request(s), want %d", got, n)
	}
	return th
}

// CannotUse fails if caretz offered tool to the assistant.
func (th *Then) CannotUse(tool string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("the assistant cannot use the %q tool", tool), nil)
	if th.s.LLM.Request(0).HasTool(tool) {
		th.s.t.Fatalf("caretz offered the %s tool", tool)
	}
	return th
}

// CanUse fails unless caretz offered tool to the assistant.
func (th *Then) CanUse(tool string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("the assistant can use the %q tool", tool), nil)
	if !th.s.LLM.Request(0).HasTool(tool) {
		th.s.t.Fatalf("caretz did not offer the %s tool", tool)
	}
	return th
}

// Saw checks the result caretz sent back to the assistant for the last
// scripted tool call: for RunsCommand, the command's output.
func (th *Then) Saw(m Matcher) *Then {
	th.s.t.Helper()
	if len(th.s.calls) == 0 {
		th.s.t.Fatal("story: Saw needs a scripted RunsCommand or Calls")
	}
	call := th.s.calls[len(th.s.calls)-1]
	subject := "what the assistant saw from " + call.source
	for _, req := range th.s.LLM.Requests() {
		if msg, ok := req.ToolResult(call.id); ok {
			th.assert(subject, msg.Content, m)
			return th
		}
	}
	th.s.record(phaseThen, subject+" "+m.desc, nil)
	th.s.t.Fatalf("caretz never sent the assistant a result for %s (%s)", call.source, call.id)
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

// Request selects the n-th request caretz sent to the model, counting from 1.
// It is for scenarios about the model traffic itself; user stories should
// prefer Prompt, CanUse, and Saw.
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

// ToolResult checks the result caretz sent back for callID (set with Part.ID).
func (r *RequestThen) ToolResult(callID string, m Matcher) *RequestThen {
	r.s.t.Helper()
	req := r.request()
	msg, ok := req.ToolResult(callID)
	if !ok {
		r.s.t.Fatalf("%s has no tool result for %q; last message: %s", r.name(), callID, req.LastMessage().Describe())
	}
	r.assert(fmt.Sprintf("the tool result for %q in %s", callID, r.name()), msg.Content, m)
	return r
}

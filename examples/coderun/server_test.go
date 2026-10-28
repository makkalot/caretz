package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makkalot/caretz"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

type modelRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func modelResponse(w http.ResponseWriter, content string, call *llm.ToolCall) {
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"content": content}
	if call != nil {
		delta["tool_calls"] = []any{map[string]any{"index": 0, "id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments}}}
	}
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
}

func testApp(t *testing.T, handler http.HandlerFunc, configure ...func(*caretz.ClientOptions)) (*app, *httptest.Server, caretz.ClientOptions) {
	t.Helper()
	model := httptest.NewServer(handler)
	t.Cleanup(model.Close)
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	options := clientOptions(data, caretz.ModelOptions{Model: "fake", BaseURL: model.URL + "/v1", APIKey: "test", Protocol: "chat-completions", MaxAttempts: 1}, false)
	options.Sandbox.Disabled = true // Most tests use file tools only; native tests opt in below.
	for _, apply := range configure {
		apply(&options)
	}
	client, err := caretz.NewClient(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newApp(ctx, data, client)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.routes())
	t.Cleanup(func() { cancel(); a.close(); _ = client.Close(); server.Close() })
	return a, server, options
}

func request(t *testing.T, handler http.Handler, method, target, body string, want int) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, target, w.Code, want, w.Body.String())
	}
	return w.Body.Bytes()
}

func createProject(t *testing.T, handler http.Handler) Project {
	t.Helper()
	var p Project
	if err := json.Unmarshal(request(t, handler, "POST", "/api/projects", `{"name":"Water tracker"}`, 201), &p); err != nil {
		t.Fatal(err)
	}
	if !validID.MatchString(p.ID) {
		t.Fatalf("unsafe generated ID %q", p.ID)
	}
	return p
}

func readStream(t *testing.T, url string) <-chan map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	r, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE response: %s", response.Status)
	}
	t.Cleanup(func() { cancel(); _ = response.Body.Close() })
	frames := make(chan map[string]json.RawMessage, 100)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 2*1024*1024)
		for scanner.Scan() {
			if !strings.HasPrefix(scanner.Text(), "data: ") {
				continue
			}
			var f map[string]json.RawMessage
			if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &f) == nil {
				select {
				case frames <- f:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return frames
}

func waitFrame(t *testing.T, frames <-chan map[string]json.RawMessage, kind string) map[string]json.RawMessage {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatalf("SSE closed waiting for %s", kind)
			}
			if string(frame["kind"]) == `"`+kind+`"` {
				return frame
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

func TestMessageStreamsWritesAndResumes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a, server, options := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected model path %s", r.URL.Path)
		}
		var input modelRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		last := input.Messages[len(input.Messages)-1]
		if last.Role == "tool" {
			modelResponse(w, "Created main.py.\n```text\nHello water!\n```", nil)
			return
		}
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		modelResponse(w, "Writing the app.", &llm.ToolCall{ID: "write-1", Name: "Write", Arguments: `{"file_path":"main.py","content":"print('Hello water!')\n"}`})
	})
	handler := a.routes()
	p := createProject(t, handler)
	base := "/api/projects/" + p.ID
	frames := readStream(t, server.URL+base+"/events")
	waitFrame(t, frames, "history")
	request(t, handler, "POST", base+"/messages", `{"text":"Build a water tracker"}`, 202)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	request(t, handler, "POST", base+"/run", "", 409)
	request(t, handler, "POST", base+"/messages", `{"text":"another turn"}`, 409)
	close(release)
	waitFrame(t, frames, "user")
	waitFrame(t, frames, "tool_call")
	result := waitFrame(t, frames, "tool_result")
	if string(result["tool"]) != `"Write"` {
		t.Fatalf("tool result = %s", result["tool"])
	}
	waitFrame(t, frames, "turn_done")
	if got := string(request(t, handler, "GET", base+"/files", "", 200)); !strings.Contains(got, "main.py") {
		t.Fatalf("files: %s", got)
	}
	if got := string(request(t, handler, "GET", base+"/files/main.py", "", 200)); got != "print('Hello water!')\n" {
		t.Fatalf("file content = %q", got)
	}
	history := request(t, handler, "GET", base+"/history", "", 200)
	if !bytes.Contains(history, []byte("Build a water tracker")) || !bytes.Contains(history, []byte("Created main.py")) {
		t.Fatalf("history: %s", history)
	}
	a.close()
	if err := a.client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := caretz.NewClient(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	restored, err := newApp(context.Background(), a.data, client)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	if got := request(t, restored.routes(), "GET", base+"/history", "", 200); !bytes.Equal(got, history) {
		t.Fatalf("restored history differs: %s", got)
	}
	if got := request(t, restored.routes(), "GET", "/api/projects", "", 200); !bytes.Contains(got, []byte(p.ID)) {
		t.Fatalf("restored projects: %s", got)
	}
}

func TestPermissionsDenyAndCancel(t *testing.T) {
	var target string
	a, server, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		var input modelRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.Messages[len(input.Messages)-1].Role == "tool" {
			modelResponse(w, "Permission was denied.", nil)
			return
		}
		args, _ := json.Marshal(map[string]string{"file_path": target, "content": "escaped"})
		modelResponse(w, "", &llm.ToolCall{ID: "escape", Name: "Write", Arguments: string(args)})
	})
	target = filepath.Join(a.data, "escape.py")
	handler := a.routes()
	p := createProject(t, handler)
	base := "/api/projects/" + p.ID
	frames := readStream(t, server.URL+base+"/events")
	waitFrame(t, frames, "history")
	request(t, handler, "POST", base+"/messages", `{"text":"write outside"}`, 202)
	f := waitFrame(t, frames, "permission")
	var permission eventing.PermissionRequest
	if err := json.Unmarshal(f["permission"], &permission); err != nil {
		t.Fatal(err)
	}
	if permission.Target != target {
		t.Fatalf("permission target = %s", permission.Target)
	}
	url := base + "/permissions/" + permission.RequestID
	request(t, handler, "POST", url, `{"decision":"invalid"}`, 400)
	if got := request(t, handler, "GET", base+"/permissions", "", 200); !bytes.Contains(got, []byte(permission.RequestID)) {
		t.Fatal("pending permission missing")
	}
	request(t, handler, "POST", url, `{"decision":"deny"}`, 200)
	waitFrame(t, frames, "permission_resolved")
	waitFrame(t, frames, "turn_done")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("denied write happened: %v", err)
	}
	request(t, handler, "POST", base+"/messages", `{"text":"try again"}`, 202)
	waitFrame(t, frames, "permission")
	request(t, handler, "POST", base+"/cancel", "", 200)
	request(t, handler, "POST", base+"/cancel", "", 200)
	waitFrame(t, frames, "turn_done")
	if got := request(t, handler, "GET", base+"/permissions", "", 200); bytes.Contains(got, []byte("request_id")) {
		t.Fatalf("cancel left permission pending: %s", got)
	}
}

func TestCancelInterruptsModelRequest(t *testing.T) {
	started := make(chan struct{})
	a, server, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	handler := a.routes()
	p := createProject(t, handler)
	base := "/api/projects/" + p.ID
	frames := readStream(t, server.URL+base+"/events")
	waitFrame(t, frames, "history")
	request(t, handler, "POST", base+"/messages", `{"text":"wait"}`, 202)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	request(t, handler, "POST", base+"/cancel", "", 200)
	waitFrame(t, frames, "turn_done")
}

func TestLiveProgressBeforeModelCompletes(t *testing.T) {
	sendText, finish := make(chan struct{}), make(chan struct{})
	a, server, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Planning the app\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-sendText:
		case <-r.Context().Done():
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello from the live response\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	})
	handler := a.routes()
	p := createProject(t, handler)
	base := "/api/projects/" + p.ID
	frames := readStream(t, server.URL+base+"/events")
	waitFrame(t, frames, "history")
	request(t, handler, "POST", base+"/messages", `{"text":"show progress"}`, 202)
	for {
		f := waitFrame(t, frames, "reasoning")
		if bytes.Contains(f["delta"], []byte("Planning")) {
			break
		}
	}
	close(sendText)
	for {
		f := waitFrame(t, frames, "assistant_stream")
		if bytes.Contains(f["text"], []byte("Hello from the live response")) {
			break
		}
	}
	opened, err := a.open(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !opened.snapshot().Running {
		t.Fatal("model completed before live output was received")
	}
	// A second browser can recover provisional text while the model is still busy.
	reconnected := readStream(t, server.URL+base+"/events")
	f := waitFrame(t, reconnected, "assistant_stream")
	if !bytes.Contains(f["text"], []byte("Hello from the live response")) {
		t.Fatalf("reconnect lost live text: %s", f)
	}
	close(finish)
	waitFrame(t, frames, "assistant")
	waitFrame(t, frames, "turn_done")
	live, _ := opened.liveSnapshot()
	if live.Text != "" {
		t.Fatalf("completed response left provisional text: %s", live.Text)
	}
}

func TestRunExecutesMainDirectly(t *testing.T) {
	a, server, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Run main.py unexpectedly called the model")
		http.Error(w, "no model allowed", 500)
	})
	handler := a.routes()
	p := createProject(t, handler)
	if err := os.WriteFile(filepath.Join(a.data, "projects", p.ID, "main.py"), []byte("print(42)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	base := "/api/projects/" + p.ID
	frames := readStream(t, server.URL+base+"/events")
	waitFrame(t, frames, "history")
	request(t, handler, "POST", base+"/run", "", 202)
	f := waitFrame(t, frames, "turn_done")
	var result struct {
		Stdout   string
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal(f["result"], &result); err != nil {
		t.Fatal(err)
	}
	if string(f["command"]) != `"python3 main.py"` || result.Stdout != "42\n" || result.ExitCode != 0 {
		t.Fatalf("run completion = %s", f)
	}
	if got := request(t, handler, "GET", base+"/history", "", 200); string(got) != "[]\n" {
		t.Fatalf("Run changed conversation history: %s", got)
	}
}

func TestDirectCommandOutputAndValidation(t *testing.T) {
	a, server, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("manual command unexpectedly called the model")
		http.Error(w, "no model allowed", 500)
	})
	handler := a.routes()
	p := createProject(t, handler)
	base := "/api/projects/" + p.ID
	frames := readStream(t, server.URL+base+"/events")
	waitFrame(t, frames, "history")
	request(t, handler, "POST", base+"/commands", `{"command":"printf hello; printf problem >&2; exit 7"}`, 202)
	f := waitFrame(t, frames, "turn_done")
	var result struct {
		Stdout, Stderr string
		ExitCode       int `json:"exit_code"`
	}
	if err := json.Unmarshal(f["result"], &result); err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "hello" || result.Stderr != "problem" || result.ExitCode != 7 {
		t.Fatalf("direct command result: %s", f)
	}
	if got := request(t, handler, "GET", base+"/history", "", 200); string(got) != "[]\n" {
		t.Fatalf("manual execution changed model history: %s", got)
	}
	for _, command := range []string{"", "  ", strings.Repeat("x", 8193), "echo\x00bad"} {
		body, _ := json.Marshal(map[string]string{"command": command})
		request(t, handler, "POST", base+"/commands", string(body), 400)
	}
	// The last result is also available to a reconnecting browser.
	reconnected := readStream(t, server.URL+base+"/events")
	f = waitFrame(t, reconnected, "turn_done")
	if !bytes.Contains(f["result"], []byte("hello")) {
		t.Fatalf("reconnect lost command output: %s", f)
	}
}

func TestDirectCommandCancellationAndConcurrency(t *testing.T) {
	a, server, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) { t.Error("manual command called model") })
	handler := a.routes()
	p := createProject(t, handler)
	base := "/api/projects/" + p.ID
	frames := readStream(t, server.URL+base+"/events")
	waitFrame(t, frames, "history")
	request(t, handler, "POST", base+"/commands", `{"command":"printf started > command-started; sleep 30"}`, 202)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(a.data, "projects", p.ID, "command-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	request(t, handler, "POST", base+"/messages", `{"text":"conflicting chat"}`, 409)
	request(t, handler, "POST", base+"/commands", `{"command":"true"}`, 409)
	request(t, handler, "POST", base+"/cancel", "", 200)
	request(t, handler, "POST", base+"/cancel", "", 200)
	f := waitFrame(t, frames, "turn_done")
	if string(f["text"]) != `"Stopped."` {
		t.Fatalf("cancelled command: %s", f)
	}
}

func TestPermissionApproval(t *testing.T) {
	var target string
	a, server, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		var input modelRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.Messages[len(input.Messages)-1].Role == "tool" {
			modelResponse(w, "Written with permission.", nil)
			return
		}
		args, _ := json.Marshal(map[string]string{"file_path": target, "content": "approved"})
		modelResponse(w, "", &llm.ToolCall{ID: "approve-write", Name: "Write", Arguments: string(args)})
	})
	target = filepath.Join(a.data, "approved.txt")
	handler := a.routes()
	p := createProject(t, handler)
	base := "/api/projects/" + p.ID
	frames := readStream(t, server.URL+base+"/events")
	waitFrame(t, frames, "history")
	request(t, handler, "POST", base+"/messages", `{"text":"write with permission"}`, 202)
	f := waitFrame(t, frames, "permission")
	var permission eventing.PermissionRequest
	if err := json.Unmarshal(f["permission"], &permission); err != nil {
		t.Fatal(err)
	}
	request(t, handler, "POST", base+"/permissions/"+permission.RequestID, `{"decision":"allow_session"}`, 200)
	waitFrame(t, frames, "turn_done")
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "approved" {
		t.Fatalf("approved write: %q, %v", contents, err)
	}
}

func TestFilesAndRequestValidation(t *testing.T) {
	a, _, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) { modelResponse(w, "ok", nil) })
	handler := a.routes()
	p := createProject(t, handler)
	base := "/api/projects/" + p.ID
	dir := filepath.Join(a.data, "projects", p.ID)
	for name, content := range map[string][]byte{"hello.py": []byte("print('hi')"), "large.txt": make([]byte, maxFileBytes+1), "binary.dat": {0, 1, 2}, ".secret": []byte("secret")} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(a.data, "projects.json"), filepath.Join(dir, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	request(t, handler, "GET", base+"/files/hello.py", "", 200)
	for _, name := range []string{"../projects.json", "%2e%2e%2fprojects.json", "%2fetc%2fpasswd", "escape.txt", ".secret"} {
		request(t, handler, "GET", base+"/files/"+name, "", 400)
	}
	request(t, handler, "GET", base+"/files/large.txt", "", 413)
	request(t, handler, "GET", base+"/files/binary.dat", "", 415)
	request(t, handler, "GET", base+"/files/missing.py", "", 404)
	files := request(t, handler, "GET", base+"/files", "", 200)
	if bytes.Contains(files, []byte(".secret")) || bytes.Contains(files, []byte("escape.txt")) {
		t.Fatalf("unsafe file listed: %s", files)
	}
	request(t, handler, "POST", "/api/projects", `{"name":" "}`, 400)
	request(t, handler, "POST", "/api/projects", `{"name":"x"} {}`, 400)
	request(t, handler, "POST", base+"/messages", `{"text":""}`, 400)
	request(t, handler, "GET", "/api/projects/unknown/history", "", 404)
	request(t, handler, "GET", "/api/projects/UPPER/history", "", 400)
	request(t, handler, "GET", "/", "", 200)
	r := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"attack"}`))
	r.Header.Set("Origin", "https://elsewhere.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin create: %d", w.Code)
	}
}

func TestFileListSkipsGeneratedContentBeforeLimit(t *testing.T) {
	a, _, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) { t.Error("file listing called the model") })
	handler := a.routes()
	p := createProject(t, handler)
	dir := filepath.Join(a.data, "projects", p.ID)
	write := func(name string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Library/notes.md", "README.md", "data/history.json", "logs/app.log", "main.py", "src/env.py", "src/venv_settings.py", "templates/tmp.html"}
	for _, name := range want {
		write(name)
	}
	for _, name := range []string{
		".venv/bin/python", "venv/lib/package.py", "env/bin/python",
		"custom-python/pyvenv.cfg", "custom-python/lib/package.py",
		"src/__pycache__/app.pyc", "src/module.pyo", "module.pyc", "module.py~",
		"scratch.tmp", "scratch.temp", "editor.swp", "editor.swo",
		"node_modules/package/index.js", "build/generated.py", "dist/app.whl",
		"tmp/scratch.txt", "temp/scratch.txt", "htmlcov/index.html",
		"__pypackages__/3.12/lib/package.py", "site-packages/package.py",
		"app.egg-info/PKG-INFO", "app.dist-info/METADATA",
		"pip-cache/download", "pip_cache/download", "pip-wheel-metadata/metadata",
	} {
		write(name)
	}
	// These sort before main.py and used to consume the entire 500-file cap.
	for i := range 600 {
		write(fmt.Sprintf("Library/Caches/pip/http-v2/%04d.body", i))
	}
	var got []string
	if err := json.Unmarshal(request(t, handler, "GET", "/api/projects/"+p.ID+"/files", "", 200), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("visible files = %v, want %v", got, want)
	}
	// Filtering is a presentation choice: generated files remain on disk.
	if _, err := os.Stat(filepath.Join(dir, "Library/Caches/pip/http-v2/0000.body")); err != nil {
		t.Fatal(err)
	}
}

func TestEventFrames(t *testing.T) {
	for _, tc := range []struct {
		name, eventType, kind string
		payload               any
	}{
		{"user", eventing.LLMUserMessageCreated, "user", map[string]any{"message": llm.Message{Role: llm.RoleUser, Content: "hi"}}},
		{"assistant", eventing.LLMAgentResponseCreated, "assistant", map[string]any{"message": llm.Message{Role: llm.RoleAssistant, Content: "done"}}},
		{"call", eventing.LLMAgentMessageCreated, "tool_call", map[string]any{"message": llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "one", Name: "Bash", Arguments: `{}`}}}}},
		{"result", eventing.LLMToolCallResultCreated, "tool_result", eventing.LLMToolCallResult{Tool: llm.ToolCall{Name: "Write"}, Message: llm.Message{Role: llm.RoleTool, Content: "written"}}},
		{"permission", eventing.PermissionRequested, "permission", eventing.PermissionRequest{RequestID: "one"}},
		{"resolved", eventing.PermissionResolved, "permission_resolved", eventing.PermissionResolution{RequestID: "one", Decision: "deny"}},
		{"status", eventing.StatusUpdated, "status", eventing.Status{Message: "thinking"}},
		{"reasoning", eventing.LLMReasoningDeltaCreated, "reasoning", eventing.LLMReasoningDelta{Delta: "planning", Done: false}},
		{"retry", eventing.LLMRequestRetryScheduled, "status", eventing.LLMRequestRetry{Attempt: 2, DelayMillis: 1000, Message: "retrying", Error: "busy"}},
		{"unknown", "Unknown.Event", "", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(tc.payload)
			frames := eventFrames(&types.Event{EventType: tc.eventType, Payload: string(payload)})
			if tc.kind == "" {
				if len(frames) != 0 {
					t.Fatal(frames)
				}
				return
			}
			if len(frames) != 1 || frames[0].Kind != tc.kind {
				t.Fatalf("frames = %#v", frames)
			}
			if got := eventFrames(&types.Event{EventType: tc.eventType, Payload: "{"}); len(got) != 0 {
				t.Fatalf("malformed payload accepted: %#v", got)
			}
		})
	}
}

func TestClientOptionsKeepSandboxEnabled(t *testing.T) {
	for _, allowPyPI := range []bool{false, true} {
		options := clientOptions(t.TempDir(), caretz.ModelOptions{}, allowPyPI)
		if options.Sandbox.Disabled || options.Sandbox.AskSandboxOff || options.Sandbox.UnrestrictedNetwork {
			t.Fatalf("allow-pypi=%v relaxes sandbox execution: %#v", allowPyPI, options.Sandbox)
		}
		want := []string(nil)
		if allowPyPI {
			want = []string{"pypi.org", "files.pythonhosted.org"}
		}
		if !slices.Equal(options.Sandbox.NetworkAllowlist, want) {
			t.Fatalf("allow-pypi=%v: network allowlist = %v, want %v", allowPyPI, options.Sandbox.NetworkAllowlist, want)
		}
	}
}

func TestProxyNetworkPermissionIntegration(t *testing.T) {
	if os.Getenv("CARETZ_RUN_SANDBOX_INTEGRATION") != "1" {
		t.Skip("set CARETZ_RUN_SANDBOX_INTEGRATION=1 to exercise the native sandbox proxy")
	}
	for _, tc := range []struct {
		decision string
		direct   bool
	}{{"allow", false}, {"deny", false}, {"allow", true}, {"deny", true}} {
		t.Run(fmt.Sprintf("%s/direct=%v", tc.decision, tc.direct), func(t *testing.T) {
			decision := tc.decision
			reached := make(chan struct{}, 4)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached <- struct{}{}
				_, _ = fmt.Fprint(w, "network-approved")
			}))
			defer upstream.Close()
			command := "curl -fsS --max-time 15 --noproxy '' " + upstream.URL
			a, server, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.direct {
					t.Error("direct command called the model")
					return
				}
				var input modelRequest
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					return
				}
				if input.Messages[len(input.Messages)-1].Role == "tool" {
					modelResponse(w, "Command complete.", nil)
					return
				}
				args, _ := json.Marshal(map[string]string{"command": command})
				modelResponse(w, "", &llm.ToolCall{ID: "proxy-request", Name: "Bash", Arguments: string(args)})
			}, func(options *caretz.ClientOptions) { options.Sandbox.Disabled = false })
			handler := a.routes()
			p := createProject(t, handler)
			base := "/api/projects/" + p.ID
			frames := readStream(t, server.URL+base+"/events")
			waitFrame(t, frames, "history")
			if tc.direct {
				body, _ := json.Marshal(map[string]string{"command": command})
				request(t, handler, "POST", base+"/commands", string(body), 202)
			} else {
				request(t, handler, "POST", base+"/messages", `{"text":"fetch through proxy"}`, 202)
			}
			f := waitFrame(t, frames, "permission")
			var permission eventing.PermissionRequest
			if err := json.Unmarshal(f["permission"], &permission); err != nil {
				t.Fatal(err)
			}
			if permission.Capability != "network" || permission.Target != strings.TrimPrefix(upstream.URL, "http://") {
				t.Fatalf("expected a proxy destination permission, got %#v", permission)
			}
			if len(reached) != 0 {
				t.Fatal("upstream reached before approval")
			}
			request(t, handler, "POST", base+"/permissions/"+permission.RequestID, `{"decision":"`+decision+`"}`, 200)
			var output json.RawMessage
			if tc.direct {
				result := waitFrame(t, frames, "turn_done")
				output = result["result"]
			} else {
				result := waitFrame(t, frames, "tool_result")
				output = result["text"]
				waitFrame(t, frames, "turn_done")
			}
			if decision == "allow" {
				if !bytes.Contains(output, []byte("network-approved")) || len(reached) != 1 {
					t.Fatalf("approved proxy request did not reach upstream: %s", output)
				}
			} else if len(reached) != 0 {
				t.Fatal("denied proxy request reached upstream")
			}
		})
	}
}

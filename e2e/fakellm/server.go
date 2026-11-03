// Package fakellm is a scripted OpenAI-compatible server for end-to-end tests.
//
// The real caretz binary is pointed at it with --base-url. Each request to
// /v1/chat/completions consumes the next scripted Turn, and every request is
// recorded so tests can assert on what caretz sent to the model.
package fakellm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Model is the only model the fake server advertises.
const Model = "fake-model"

// Turn is one scripted model reply.
type Turn struct {
	Text      string
	ToolCalls []ToolCall
}

// ToolCall is a function call the fake model asks caretz to run.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Text scripts a plain assistant reply.
func Text(text string) Turn { return Turn{Text: text} }

// Call scripts a reply that asks caretz to run a single tool.
func Call(name, arguments string) Turn {
	return Turn{ToolCalls: []ToolCall{{Name: name, Arguments: arguments}}}
}

// Server is a running fake LLM endpoint.
type Server struct {
	// URL is the base URL to pass to caretz --base-url (it ends in /v1).
	URL string

	t        testing.TB
	srv      *httptest.Server
	mu       sync.Mutex
	turns    []Turn
	requests []Request
}

// New starts a fake server that replies with turns in order. The server is
// closed when the test ends, and the test fails if any turn was not consumed.
func New(t testing.TB, turns ...Turn) *Server {
	t.Helper()
	s := &Server{t: t, turns: turns}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.t.Errorf("fakellm: unhandled %s %s", r.Method, r.URL.Path)
		http.Error(w, "fakellm: unhandled endpoint", http.StatusNotFound)
	})
	s.srv = httptest.NewServer(mux)
	s.URL = s.srv.URL + "/v1"
	t.Cleanup(func() {
		s.srv.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.turns) > 0 {
			t.Errorf("fakellm: %d scripted turn(s) never requested; got %d request(s)", len(s.turns), len(s.requests))
		}
	})
	return s
}

// Push appends turns to the end of the script.
func (s *Server) Push(turns ...Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = append(s.turns, turns...)
}

// Requests returns every chat request received so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Request returns the i-th chat request (0-based), failing the test if it is missing.
func (s *Server) Request(i int) Request {
	s.t.Helper()
	reqs := s.Requests()
	if i >= len(reqs) {
		s.t.Fatalf("fakellm: request %d not received; got %d request(s)", i, len(reqs))
	}
	return reqs[i]
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"object": "list",
		"data":   []map[string]any{{"id": Model, "object": "model", "owned_by": "fakellm"}},
	})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		s.t.Errorf("fakellm: decode request: %v\n%s", err, body)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Raw = body

	s.mu.Lock()
	s.requests = append(s.requests, req)
	n := len(s.requests)
	if len(s.turns) == 0 {
		s.mu.Unlock()
		s.t.Errorf("fakellm: unexpected request #%d with no scripted turn left; last message: %s", n, req.LastMessage().Describe())
		// 400 is not retried by caretz, so the run fails fast.
		http.Error(w, "fakellm: no scripted turn left", http.StatusBadRequest)
		return
	}
	turn := s.turns[0]
	s.turns = s.turns[1:]
	s.mu.Unlock()

	for i := range turn.ToolCalls {
		if turn.ToolCalls[i].ID == "" {
			turn.ToolCalls[i].ID = fmt.Sprintf("call_%d_%d", n, i)
		}
	}
	if req.Stream {
		writeStream(w, turn, req.StreamOptions != nil && req.StreamOptions.IncludeUsage)
		return
	}
	writeCompletion(w, turn)
}

func writeCompletion(w http.ResponseWriter, turn Turn) {
	msg := map[string]any{"role": "assistant", "content": turn.Text}
	if len(turn.ToolCalls) > 0 {
		calls := make([]map[string]any, len(turn.ToolCalls))
		for i, c := range turn.ToolCalls {
			calls[i] = map[string]any{
				"id":       c.ID,
				"type":     "function",
				"function": map[string]any{"name": c.Name, "arguments": c.Arguments},
			}
		}
		msg["tool_calls"] = calls
	}
	writeJSON(w, map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion",
		"model":   Model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finishReason(turn)}},
		"usage":   fakeUsage(),
	})
}

// writeStream sends the turn as SSE chunks, splitting text and tool arguments
// so caretz's delta accumulation is exercised.
func writeStream(w http.ResponseWriter, turn Turn, includeUsage bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	send := func(payload any) {
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id":      "chatcmpl-fake",
			"object":  "chat.completion.chunk",
			"model":   Model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}

	send(chunk(map[string]any{"role": "assistant"}, nil))
	for _, part := range splitChunks(turn.Text) {
		send(chunk(map[string]any{"content": part}, nil))
	}
	for i, c := range turn.ToolCalls {
		send(chunk(map[string]any{"tool_calls": []map[string]any{{
			"index": i, "id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": ""},
		}}}, nil))
		for _, part := range splitChunks(c.Arguments) {
			send(chunk(map[string]any{"tool_calls": []map[string]any{{
				"index": i, "function": map[string]any{"arguments": part},
			}}}, nil))
		}
	}
	send(chunk(map[string]any{}, finishReason(turn)))
	if includeUsage {
		send(map[string]any{"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": Model, "choices": []any{}, "usage": fakeUsage()})
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func splitChunks(s string) []string {
	const size = 8
	var parts []string
	for len(s) > size {
		parts = append(parts, s[:size])
		s = s[size:]
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}

func finishReason(turn Turn) string {
	if len(turn.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func fakeUsage() map[string]any {
	return map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Request is a chat completion request as caretz sent it.
type Request struct {
	Model         string    `json:"model"`
	Messages      []Message `json:"messages"`
	Tools         []Tool    `json:"tools"`
	Stream        bool      `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Raw []byte `json:"-"`
}

// Message is one chat message in a request.
type Message struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// Tool is a tool definition advertised by caretz.
type Tool struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

// Describe renders the message compactly for failure output.
func (m Message) Describe() string {
	if m.Role == "" {
		return "<none>"
	}
	return fmt.Sprintf("[%s] %q", m.Role, truncate(m.Content, 200))
}

// LastMessage returns the final message of the request.
func (r Request) LastMessage() Message {
	if len(r.Messages) == 0 {
		return Message{}
	}
	return r.Messages[len(r.Messages)-1]
}

// LastUserMessage returns the most recent user message.
func (r Request) LastUserMessage() Message {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.Messages[i]
		}
	}
	return Message{}
}

// ToolResult returns the tool message answering callID.
func (r Request) ToolResult(callID string) (Message, bool) {
	for _, m := range r.Messages {
		if m.Role == "tool" && m.ToolCallID == callID {
			return m, true
		}
	}
	return Message{}, false
}

// LastToolResult returns the most recent tool message in the request.
func (r Request) LastToolResult() (Message, bool) {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "tool" {
			return r.Messages[i], true
		}
	}
	return Message{}, false
}

// HasTool reports whether caretz advertised a tool with this name.
func (r Request) HasTool(name string) bool {
	for _, t := range r.Tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// SystemPrompt returns the concatenated system messages.
func (r Request) SystemPrompt() string {
	var parts []string
	for _, m := range r.Messages {
		if m.Role == "system" {
			parts = append(parts, m.Content)
		}
	}
	return strings.Join(parts, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

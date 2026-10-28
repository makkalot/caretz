package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	caretz "github.com/makkalot/caretz"
	"github.com/makkalot/caretz/lib/llm"
)

type fakeModel struct {
	prompts []string
}

func (f *fakeModel) GetModel() string   { return "fake" }
func (f *fakeModel) GetBaseURL() string { return "" }
func (f *fakeModel) Chat(_ string, messages []llm.Message, _ []llm.ToolDefinition, _ func(string)) (llm.Response, error) {
	f.prompts = append(f.prompts, messages[len(messages)-1].Content)
	return llm.Response{Content: "Oak worktop — Home/Kitchen.md"}, nil
}

// Exercise the public session API rather than only checking prompt strings:
// audit append, user-event processing, response delivery, and cancellation.
func TestSessionRoundTrip(t *testing.T) {
	for _, command := range []string{"capture", "ask", "serve"} {
		t.Run(command, func(t *testing.T) {
			model := &fakeModel{}
			client, err := caretz.NewClient(t.Context(), caretz.ClientOptions{
				Provider: model, WorkspaceDir: t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			session, err := client.NewSession("test", caretz.SessionOptions{Adapter: "vault"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			out := &cancelWriter{cancel: cancel, cancelOnWrite: command == "serve"}
			if err := execute(ctx, session, options{
				command: command, text: "worktop?", daily: "08:00", weekly: "Sun 18:00",
			}, out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Oak worktop — Home/Kitchen.md") {
				t.Fatalf("response not delivered: %q", out.String())
			}
			if len(model.prompts) != 1 {
				t.Fatalf("got %d model calls", len(model.prompts))
			}
			want := "worktop?"
			if command == "serve" {
				want = "ListSchedules"
			}
			if !strings.Contains(model.prompts[0], want) {
				t.Fatalf("model did not receive %q", want)
			}
		})
	}
}

type cancelWriter struct {
	bytes.Buffer
	cancel        context.CancelFunc
	cancelOnWrite bool
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if w.cancelOnWrite {
		w.cancel()
	}
	return n, err
}

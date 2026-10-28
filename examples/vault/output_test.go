package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/lib/tui"
)

const exampleReply = "## Latest entry\n\n**Idea:** see `Daily/2026-10-27.md`.\n\n- Call dentist\n\n> A thought\n\n```text\n# Literal heading\n**literal stars**\n```"

func TestRedirectedResponsePreservesMarkdown(t *testing.T) {
	var out bytes.Buffer
	if err := newResponseOutput(&out).print(exampleReply, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != exampleReply+"\n" {
		t.Fatalf("redirected Markdown changed: %q", got)
	}
}

func TestTerminalResponseRendersMarkdown(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		var out bytes.Buffer
		output := responseOutputWithStyle(&out, tui.NewStyleEnabled(true))
		var timestamp time.Time
		if scheduled {
			timestamp = time.Date(2026, 10, 27, 8, 0, 0, 0, time.Local)
		}
		if err := output.print(exampleReply, timestamp); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !strings.Contains(got, "\x1b[") {
			t.Fatal("terminal response has no styling")
		}
		plain := tui.Strip(got)
		for _, marker := range []string{"## Latest entry", "**Idea:**", "`Daily/2026-10-27.md`", "```"} {
			if strings.Contains(plain, marker) {
				t.Errorf("unrendered Markdown %q in %q", marker, plain)
			}
		}
		for _, content := range []string{"Latest entry", "Idea:", "Daily/2026-10-27.md", "Call dentist", "A thought", "# Literal heading\n**literal stars**"} {
			if !strings.Contains(plain, content) {
				t.Errorf("lost content %q in %q", content, plain)
			}
		}
		if scheduled && !strings.HasPrefix(plain, "[08:00:00] ") {
			t.Fatalf("missing schedule timestamp: %q", plain)
		}
	}
}

func TestDisabledStylePreservesMarkdown(t *testing.T) {
	var out bytes.Buffer
	if err := responseOutputWithStyle(&out, tui.NewStyleEnabled(false)).print(exampleReply, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != exampleReply+"\n" {
		t.Fatal("disabled styling changed Markdown")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestResponseReportsWriteFailure(t *testing.T) {
	if err := newResponseOutput(failingWriter{}).print("reply", time.Time{}); err == nil {
		t.Fatal("output error was ignored")
	}
}

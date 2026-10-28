package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseArgs(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		flags   []string
		text    string
		wantErr bool
	}{
		{"capture", []string{"capture", "-workspace", "notes", "--", "buy", "milk"}, []string{"-workspace", "notes"}, "buy milk", false},
		{"quoted", []string{"capture", "-v", "buy milk"}, []string{"-v"}, "buy milk", false},
		{"readonly", []string{"ask", "--plan=false", "--", "why?"}, []string{"--plan=false", "--plan=true"}, "why?", false},
		{"dash text", []string{"capture", "--", "- [ ] milk"}, nil, "- [ ] milk", false},
		{"serve", []string{"serve", "--daily=09:30", "--weekly", "Fri 17:00", "--demo"}, []string{"--incognito=false"}, "", false},
		{"missing command", nil, nil, "", true},
		{"unknown command", []string{"delete"}, nil, "", true},
		{"missing text", []string{"ask", "-workspace", "notes"}, nil, "", true},
		{"empty text", []string{"capture", "--", " "}, nil, "", true},
		{"missing value", []string{"capture", "-workspace", "--", "hello"}, nil, "", true},
		{"unquoted", []string{"capture", "buy", "milk"}, nil, "", true},
		{"serve text", []string{"serve", "hello"}, nil, "", true},
		{"bad clock", []string{"serve", "--daily", "25:00"}, nil, "", true},
		{"bad weekday", []string{"serve", "--weekly", "Sunday 18:00"}, nil, "", true},
		{"missing daily", []string{"serve", "--daily"}, nil, "", true},
		{"wrong command flag", []string{"ask", "--demo", "hello"}, nil, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, err := parseArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseArgs error = %v", err)
			}
			if err == nil && (!reflect.DeepEqual(o.flags, tt.flags) || o.text != tt.text) {
				t.Fatalf("got flags %v, text %q", o.flags, o.text)
			}
		})
	}
}

func TestPrompts(t *testing.T) {
	now := time.Date(2026, 12, 31, 11, 30, 0, 0, time.UTC)
	for _, command := range []string{"capture", "ask"} {
		prompt := commandPrompt(command, "oak worktop?", now)
		for _, want := range []string{"2026-12-31", "Thursday", "oak worktop?"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s prompt missing %q", command, want)
			}
		}
	}
	o := options{daily: "09:15", weekly: "Fri 17:30"}
	for _, want := range []string{"2026-12-31", "ListSchedules", "RemoveSchedule", "UpdateSchedule", "daily:09:15", "daily:17:30", "weekday is Fri", "[vault:daily-plan]", "[vault:weekly-review]"} {
		if !strings.Contains(setupPrompt(o, now), want) {
			t.Errorf("setup prompt missing %q", want)
		}
	}
	o.demo = true
	prompt := setupPrompt(o, now)
	if strings.Count(prompt, "every:1m") != 2 || strings.Contains(prompt, "weekday is Fri") {
		t.Fatal("demo should schedule both jobs every minute without a weekday guard")
	}
}

func TestHelpAndSandboxDisabled(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("help: code %d, output %q", code, out.String())
	}
	t.Setenv("CARETZ_DISABLE_SANDBOX", "true")
	if code := run(context.Background(), []string{"serve"}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "requires the sandbox") {
		t.Fatalf("serve: code %d, error %q", code, stderr.String())
	}
}

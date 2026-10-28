package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/eventing"
)

func TestNewSubagentChildSessionAppliesContextLimits(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		limits  eventing.ContextLimits
		want    [4]int
		wantErr string
	}{
		{
			name:   "inherited model keeps unset limits",
			limits: eventing.ContextLimits{Context: 64000},
			want:   [4]int{64000, 16384, 20000, 0},
		},
		{
			name:   "configured model",
			model:  "local/fast",
			limits: eventing.ContextLimits{Context: 32000, ReservedTokens: 3000, SummaryTokens: 1000},
			want:   [4]int{32000, 3000, 8000, 1000},
		},
		{
			name:    "larger than the model's window",
			model:   "local/fast",
			limits:  eventing.ContextLimits{Context: 128000},
			wantErr: "exceeds the model's context window 64000",
		},
		{
			name:    "reserved tokens fill the window",
			limits:  eventing.ContextLimits{Context: 16000},
			wantErr: "reserved tokens 16384 must be below the context limit 16000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := log.New()
			session, err := NewAgentSession("test", "parent", SessionParams{
				BaseDir:          filepath.Join(t.TempDir(), "sessions"),
				WorkspaceDir:     t.TempDir(),
				Logger:           logger,
				Provider:         &subagentModelTestProvider{model: "parent-model"},
				ToolRegistry:     &tools.Registry{Logger: logger},
				SystemPrompt:     "parent prompt",
				ContextWindow:    128000,
				ReservedTokens:   16384,
				KeepRecentTokens: 20000,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			session.SetSubagentModelResolver(subagentModelTestResolver{models: map[string]SubagentModelResolution{
				"local/fast": {Model: "fast-model", Provider: &subagentModelTestProvider{model: "fast-model"}, ContextWindow: 64000, ReservedTokens: 4000, KeepRecentTokens: 8000, SummaryTokens: 2000},
			}})

			limits := tt.limits
			child, err := session.NewSubagentChildSession(context.Background(), subagents.Definition{Name: "background"}, SubagentChildOptions{Prefix: "background", ModelRefOverride: tt.model, Limits: &limits})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Close() }()
			got := [4]int{child.ContextWindow(), child.ReservedTokens(), child.KeepRecentTokens(), child.SummaryTokens()}
			if tt.want[3] == 0 {
				got[3] = 0 // The inherited summary size is derived; only the others are checked.
			}
			if got != tt.want {
				t.Fatalf("limits = %v, want %v", got, tt.want)
			}
		})
	}
}

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/lib/llm"
)

type structuredExpense struct {
	Category string    `json:"category" validate:"required"`
	Amount   float64   `json:"amount" validate:"required"`
	Date     time.Time `json:"date" validate:"required"`
	Notes    string    `json:"notes" validate:"required"`
}

type conditionallyStructured struct {
	Kind  string `json:"kind" validate:"required"`
	Value string `json:"value"`
}

type contextualStructured struct {
	Value   string          `json:"value"`
	Allowed map[string]bool `json:"-"`
}

func (s *contextualStructured) ValidateStructuredCandidate(candidate any) error {
	decoded := candidate.(*contextualStructured)
	if !s.Allowed[decoded.Value] {
		return fmt.Errorf("value %q is not allowed", decoded.Value)
	}
	return nil
}

func (s *conditionallyStructured) ValidateStructured() error {
	if s.Kind == "valued" && strings.TrimSpace(s.Value) == "" {
		return fmt.Errorf("value is required when kind is valued")
	}
	return nil
}

func structuredTestSession(t *testing.T, provider llm.Provider) *AgentSession {
	t.Helper()
	logger := log.New()
	session, err := NewAgentSession("test", "structured", SessionParams{
		BaseDir: t.TempDir(), WorkspaceDir: t.TempDir(), Logger: logger,
		Provider: provider, ToolRegistry: &tools.Registry{Logger: logger},
		SystemPrompt: "test", ContextWindow: 128000, ReservedTokens: 16000, KeepRecentTokens: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestRunStructuredCorrectsMissingRequiredField(t *testing.T) {
	provider := &cacheGuardProvider{responses: []llm.Response{
		{Content: `{"category":"food","amount":0,"date":"2026-09-17T00:00:00Z","notes":"lunch"}`},
		{Content: `{"category":"food","amount":12.5,"date":"2026-09-17T00:00:00Z","notes":"lunch"}`},
	}}
	session := structuredTestSession(t, provider)
	expense := structuredExpense{Category: "original"}
	response, err := session.RunStructured(context.Background(), "Extract the expense", &expense)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || expense.Category != "food" || expense.Amount != 12.5 || expense.Date.IsZero() || expense.Notes != "lunch" {
		t.Fatalf("expense = %+v, response = %+v", expense, response)
	}
	if len(provider.requests) != 2 || !strings.Contains(provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content, "amount is required") {
		t.Fatalf("correction request = %+v", provider.requests)
	}
	if !strings.Contains(provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content, "date (RFC3339 timestamp, required)") {
		t.Fatal("initial request omitted the target field types")
	}
}

func TestRunStructuredCorrectsSemanticValidationFailure(t *testing.T) {
	provider := &cacheGuardProvider{responses: []llm.Response{
		{Content: `{"kind":"valued"}`},
		{Content: `{"kind":"valued","value":"present"}`},
	}}
	session := structuredTestSession(t, provider)
	var result conditionallyStructured
	if _, err := session.RunStructured(context.Background(), "Extract the value", &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != "present" || len(provider.requests) != 2 {
		t.Fatalf("result = %+v, calls = %d", result, len(provider.requests))
	}
	if !strings.Contains(provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content, "value is required when kind is valued") {
		t.Fatalf("correction request = %+v", provider.requests)
	}
}

func TestRunStructuredCorrectsContextualValidationFailure(t *testing.T) {
	provider := &cacheGuardProvider{responses: []llm.Response{
		{Content: `{"value":"invented"}`},
		{Content: `{"value":"known"}`},
	}}
	session := structuredTestSession(t, provider)
	result := contextualStructured{Allowed: map[string]bool{"known": true}}
	if _, err := session.RunStructured(context.Background(), "Choose a value", &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != "known" || len(provider.requests) != 2 {
		t.Fatalf("result = %+v, calls = %d", result, len(provider.requests))
	}
}

func TestRunStructuredRetriesMalformedJSONWithoutChangingTarget(t *testing.T) {
	provider := &cacheGuardProvider{responses: []llm.Response{
		{Content: `{"category":`},
		{Content: `{"category":`},
		{Content: `{"category":`},
		{Content: `{"category":`},
		{Content: `{"category":"food","amount":8,"date":"2026-09-17T00:00:00Z","notes":"tea"}`},
	}}
	session := structuredTestSession(t, provider)
	expense := structuredExpense{Category: "original"}
	if _, err := session.RunStructured(context.Background(), "Extract the expense", &expense); err != nil {
		t.Fatal(err)
	}
	if expense.Category != "food" || len(provider.requests) != 5 {
		t.Fatalf("expense = %+v, calls = %d", expense, len(provider.requests))
	}
}

func TestRunStructuredExhaustsRetryAndPreservesTarget(t *testing.T) {
	provider := &mockProvider{responses: []llm.Response{{Content: `{}`}, {Content: `{}`}, {Content: `{}`}, {Content: `{}`}, {Content: `{}`}}}
	session := structuredTestSession(t, provider)
	expense := structuredExpense{Category: "original"}
	if _, err := session.RunStructured(context.Background(), "Extract the expense", &expense); err == nil || !strings.Contains(err.Error(), "structured output invalid after 5 attempts: category is required") {
		t.Fatalf("error = %v", err)
	}
	if expense.Category != "original" || provider.callIndex != 5 {
		t.Fatalf("expense = %+v, calls = %d", expense, provider.callIndex)
	}
}

func TestRunStructuredRejectsNilTargetBeforeCalling(t *testing.T) {
	provider := &mockProvider{}
	session := structuredTestSession(t, provider)
	var expense *structuredExpense
	if _, err := session.RunStructured(context.Background(), "Extract the expense", expense); err == nil {
		t.Fatal("nil target was accepted")
	}
	if provider.callIndex != 0 {
		t.Fatal("model was called for an invalid target")
	}
}

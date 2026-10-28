package slashcmd

import (
	"context"
	"strings"
	"testing"
)

type contextTestProvider struct {
	*loopMockProvider
	model string
}

func (p *contextTestProvider) GetModel() string { return p.model }

func TestContextCommandShowsCurrentModelAndProfile(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	t.Cleanup(func() { _ = session.Close() })
	cmd := ContextCommand{Profile: "coding"}

	check := func(wantModel string) {
		t.Helper()
		result, err := cmd.Run(context.Background(), session, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"model: " + wantModel, "profile: coding", "context: ", "reserved: ", "messages: "} {
			if !strings.Contains(result.Message, want) {
				t.Fatalf("context output missing %q: %q", want, result.Message)
			}
		}
	}

	check("mock-model")
	if err := session.SwitchModel(&contextTestProvider{loopMockProvider: &loopMockProvider{}, model: "switched-model"}, 64000, 8192, 12000); err != nil {
		t.Fatal(err)
	}
	check("switched-model")
}

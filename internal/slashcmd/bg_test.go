package slashcmd

import (
	"context"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/eventing"
)

func TestBackgroundCommandListsRuns(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	t.Cleanup(func() { _ = session.Close() })
	command := BackgroundCommand{}

	result, err := command.Run(context.Background(), session, "")
	if err != nil || result.Message != "No subagent runs in this session." {
		t.Fatalf("empty /bg = %q, %v", result.Message, err)
	}

	id := session.ChannelID()
	child := id + "-subagent-search-1"
	if err := session.AppendAuditEvent(eventing.NewSubagentInvocationStarted(id, "search", child, id, "find callers", 128000)); err != nil {
		t.Fatal(err)
	}
	if err := session.AppendAuditEvent(eventing.NewSubagentInvocationStopped(id, "search", child, id, "context canceled", eventing.SubagentStopUserCancelled, 0, 128000)); err != nil {
		t.Fatal(err)
	}
	result, err = command.Run(context.Background(), session, "")
	if err != nil || !strings.Contains(result.Message, "search: cancelled (user_cancelled)") || !strings.Contains(result.Message, "find callers") {
		t.Fatalf("/bg = %q, %v", result.Message, err)
	}
}

func TestBackgroundCommandRejectsBadArguments(t *testing.T) {
	session := newLoopTestSession(t, &loopMockProvider{}, false)
	t.Cleanup(func() { _ = session.Close() })
	command := BackgroundCommand{}
	for _, args := range []string{"cancel", "stop search", "cancel a b"} {
		if _, err := command.Run(context.Background(), session, args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("/bg %s: %v", args, err)
		}
	}
	if _, err := command.Run(context.Background(), session, "cancel search"); err == nil || !strings.Contains(err.Error(), "no subagent run matches") {
		t.Fatalf("/bg cancel search: %v", err)
	}
}

package herdr

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type commandCall struct {
	path string
	args []string
}

func validEnvironment(key string) string {
	return map[string]string{
		"HERDR_ENV":         "1",
		"HERDR_BIN_PATH":    "/usr/local/bin/herdr",
		"HERDR_PANE_ID":     "w1:p2",
		"HERDR_SOCKET_PATH": "/tmp/herdr.sock",
	}[key]
}

func TestNewFromEnvironmentRequiresCompleteHerdrEnvironment(t *testing.T) {
	if got := NewFromEnvironment(Options{Getenv: func(string) string { return "" }}); got != nil {
		t.Fatal("reporter enabled outside Herdr")
	}
	if got := NewFromEnvironment(Options{Getenv: func(key string) string {
		if key == "HERDR_BIN_PATH" {
			return "relative/herdr"
		}
		return validEnvironment(key)
	}}); got != nil {
		t.Fatal("reporter accepted a relative HERDR_BIN_PATH")
	}
	reporter := NewFromEnvironment(Options{Getenv: validEnvironment, Run: func(context.Context, string, []string) error { return nil }})
	if reporter == nil {
		t.Fatal("reporter not enabled in a valid Herdr pane")
	}
	reporter.Close()
}

func TestReporterSendsStateResumeAndRelease(t *testing.T) {
	calls := make(chan commandCall, 2)
	now := time.Unix(100, 7)
	reporter := NewFromEnvironment(Options{
		Adapter: "tui", ConfigRoot: "/work/config", ConfigRootSupplied: true, Resumable: true,
		Getenv: validEnvironment, Now: func() time.Time { return now },
		Run: func(_ context.Context, path string, args []string) error {
			calls <- commandCall{path: path, args: append([]string(nil), args...)}
			return nil
		},
	})
	reporter.Report(StateBlocked, "Permission required by Bash", "session-1")
	state := receiveCall(t, calls)
	want := []string{
		"pane", "report-agent", "w1:p2", "--source", "caretz", "--agent", "caretz",
		"--state", "blocked", "--seq", "100000000007", "--message", "Permission required by Bash",
		"--agent-session-id", "session-1", "--", "caretz", "-s", "session-1", "--adapter", "tui",
		"--config-root", "/work/config",
	}
	if state.path != "/usr/local/bin/herdr" || !reflect.DeepEqual(state.args, want) {
		t.Fatalf("state call = %q %#v, want %q %#v", state.path, state.args, "/usr/local/bin/herdr", want)
	}

	reporter.Close()
	release := receiveCall(t, calls)
	if got, want := release.args, []string{"pane", "release-agent", "w1:p2", "--source", "caretz", "--agent", "caretz", "--seq", "100000000008"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("release args = %#v, want %#v", got, want)
	}
}

func TestReporterOmitsInvalidOrDisabledResume(t *testing.T) {
	for _, test := range []struct {
		name      string
		resumable bool
		root      string
	}{
		{name: "incognito", resumable: false},
		{name: "invalid apostrophe", resumable: true, root: "/work/user's/config"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := make(chan commandCall, 2)
			reporter := NewFromEnvironment(Options{
				Adapter: "tui", ConfigRoot: test.root, ConfigRootSupplied: test.root != "", Resumable: test.resumable,
				Getenv: validEnvironment,
				Run: func(_ context.Context, path string, args []string) error {
					calls <- commandCall{path: path, args: append([]string(nil), args...)}
					return nil
				},
			})
			reporter.Report(StateIdle, "", "session-1")
			call := receiveCall(t, calls)
			reporter.Close()
			_ = receiveCall(t, calls)
			if strings.Contains(strings.Join(call.args, " "), "--agent-session-id") {
				t.Fatalf("resume was included: %#v", call.args)
			}
		})
	}
}

func TestReporterKeepsLatestQueuedState(t *testing.T) {
	entered := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	calls := make(chan commandCall, 4)
	reporter := NewFromEnvironment(Options{
		Getenv: validEnvironment,
		Run: func(_ context.Context, path string, args []string) error {
			once.Do(func() { close(entered); <-unblock })
			calls <- commandCall{path: path, args: append([]string(nil), args...)}
			return nil
		},
	})
	reporter.Report(StateWorking, "", "session-1")
	<-entered
	reporter.Report(StateBlocked, "first", "session-1")
	reporter.Report(StateIdle, "", "session-1")
	close(unblock)
	first := receiveCall(t, calls)
	latest := receiveCall(t, calls)
	reporter.Close()
	_ = receiveCall(t, calls)
	if first.args[8] != StateWorking || latest.args[8] != StateIdle {
		t.Fatalf("states = %q then %q, want working then idle", first.args[8], latest.args[8])
	}
}

func receiveCall(t *testing.T, calls <-chan commandCall) commandCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Herdr call")
		return commandCall{}
	}
}

func TestReporterNamesAndUnnamesBotPane(t *testing.T) {
	calls := make(chan commandCall, 4)
	reporter := NewFromEnvironment(Options{
		Adapter: "peer", Resumable: true, DisplayName: "coordinator",
		Getenv: validEnvironment,
		Run: func(_ context.Context, path string, args []string) error {
			calls <- commandCall{path: path, args: append([]string(nil), args...)}
			return nil
		},
	})
	reporter.Report(StateIdle, "", "coordinator")
	if state := receiveCall(t, calls); state.args[1] != "report-agent" {
		t.Fatalf("first call = %#v, want report-agent", state.args)
	}
	if got, want := receiveCall(t, calls).args, []string{"pane", "report-metadata", "w1:p2", "--source", "caretz", "--display-agent", "coordinator"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata args = %#v, want %#v", got, want)
	}
	reporter.Report(StateWorking, "", "coordinator")
	if state := receiveCall(t, calls); state.args[1] != "report-agent" {
		t.Fatalf("later call = %#v, want only report-agent", state.args)
	}
	reporter.Close()
	if got, want := receiveCall(t, calls).args, []string{"pane", "report-metadata", "w1:p2", "--source", "caretz", "--clear-display-agent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("clear args = %#v, want %#v", got, want)
	}
	if release := receiveCall(t, calls); release.args[1] != "release-agent" {
		t.Fatalf("last call = %#v, want release-agent", release.args)
	}
}

package ansi

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminalInputLifecycleOrder(t *testing.T) {
	var out bytes.Buffer
	terminal := NewTerminal(&bytes.Buffer{}, &out)
	var calls []string
	terminal.rawMode = func() (func(), error) {
		calls = append(calls, "raw")
		return func() { calls = append(calls, "restore") }, nil
	}
	restore, err := terminal.BeginInput()
	if err != nil {
		t.Fatal(err)
	}
	restore()
	if !reflect.DeepEqual(calls, []string{"raw", "restore"}) {
		t.Fatalf("calls = %v", calls)
	}
	if out.String() != enableBracketedPaste+disableBracketedPaste {
		t.Fatalf("output = %q", out.String())
	}
}

func TestTerminalReadEventWithinReturnsLoneEscapeWithoutAnotherByte(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	terminal := NewTerminal(reader, &bytes.Buffer{})

	if _, ready, err := terminal.ReadEventWithin(10 * time.Millisecond); err != nil || ready {
		t.Fatalf("empty poll = ready %v, err %v; want timeout", ready, err)
	}
	if _, err := writer.Write([]byte{'\x1b'}); err != nil {
		t.Fatal(err)
	}
	event, ready, err := terminal.ReadEventWithin(100 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !ready || event.Kind != EventEscape {
		t.Fatalf("event = %#v, ready %v; want lone Escape", event, ready)
	}
}

func TestTerminalReadEventWithinRetriesInterruptedWait(t *testing.T) {
	terminal := NewTerminal(strings.NewReader("x"), io.Discard)
	calls := 0
	terminal.decoder.waitInput = func(time.Duration) (bool, error) {
		calls++
		if calls == 1 {
			return false, syscall.EINTR
		}
		return true, nil
	}
	event, ready, err := terminal.ReadEventWithin(time.Second)
	if err != nil {
		t.Fatalf("ReadEventWithin() error = %v", err)
	}
	if !ready || event.Kind != EventRune || event.Rune != 'x' {
		t.Fatalf("event = %#v, ready = %v", event, ready)
	}
	if calls != 2 {
		t.Fatalf("wait calls = %d, want 2", calls)
	}
}

func TestTerminalRawModeFailureDoesNotEnablePaste(t *testing.T) {
	var out bytes.Buffer
	terminal := NewTerminal(&bytes.Buffer{}, &out)
	terminal.rawMode = func() (func(), error) { return nil, errors.New("raw failed") }
	if _, err := terminal.BeginInput(); err == nil {
		t.Fatal("expected error")
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q", out.String())
	}
}

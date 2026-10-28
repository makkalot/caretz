package ansi

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestDecoderReadsPersistentEventStream(t *testing.T) {
	decoder := NewDecoder(bytes.NewBufferString("a\nβ\n"))
	want := []Event{
		{Kind: EventRune, Rune: 'a'},
		{Kind: EventEnter},
		{Kind: EventRune, Rune: 'β'},
		{Kind: EventEnter},
	}
	for i, expected := range want {
		got, err := decoder.Read()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if got != expected {
			t.Fatalf("event %d = %#v, want %#v", i, got, expected)
		}
	}
}

func TestDecoderReportsIncompletePaste(t *testing.T) {
	decoder := NewDecoder(bytes.NewBufferString("\x1b[200~unfinished"))
	if _, err := decoder.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v, want EOF", err)
	}
}

func TestDecoderReturnsWholeNormalizedPaste(t *testing.T) {
	decoder := NewDecoder(bytes.NewBufferString("\x1b[200~one\r\ntwo\tthree\x1b[201~"))
	event, err := decoder.Read()
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventPaste || event.Text != "one\ntwo\tthree" {
		t.Fatalf("event = %#v", event)
	}
}

func TestDecoderNavigationAndEditingKeys(t *testing.T) {
	decoder := NewDecoder(bytes.NewBufferString("\x1b[A\x1b[B\x1b[C\x1b[D\x1b[Z\x1b[3~\x7f\x01\x03\x04\x05\x0c\x17"))
	want := []EventKind{
		EventUp, EventDown, EventRight, EventLeft, EventShiftTab, EventDelete, EventBackspace,
		EventCtrlA, EventCtrlC, EventCtrlD, EventCtrlE, EventCtrlL, EventCtrlW,
	}
	for i, kind := range want {
		event, err := decoder.Read()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if event.Kind != kind {
			t.Fatalf("event %d kind = %v, want %v", i, event.Kind, kind)
		}
	}
}

func TestDecoderPagingKeys(t *testing.T) {
	decoder := NewDecoder(bytes.NewBufferString("\x1b[5~\x1b[6~\x1b[H\x1b[F\x1b[1~\x1b[4~\x1bOH\x1bOF\x15\x13"))
	want := []EventKind{
		EventPageUp, EventPageDown, EventHome, EventEnd, EventHome, EventEnd,
		EventHome, EventEnd, EventCtrlU, EventCtrlS,
	}
	for i, kind := range want {
		event, err := decoder.Read()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if event.Kind != kind {
			t.Fatalf("event %d kind = %v, want %v", i, event.Kind, kind)
		}
	}
}

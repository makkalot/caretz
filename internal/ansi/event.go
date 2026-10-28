package ansi

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

type EventKind int

const (
	EventRune EventKind = iota
	EventEnter
	EventBackspace
	EventDelete
	EventLeft
	EventRight
	EventUp
	EventDown
	EventTab
	EventShiftTab
	EventEscape
	EventCtrlA
	EventCtrlC
	EventCtrlD
	EventCtrlE
	EventCtrlL
	EventCtrlW
	EventPaste
	EventUnknown
	EventPageUp
	EventPageDown
	EventHome
	EventEnd
	EventCtrlU
	EventCtrlS
)

type Event struct {
	Kind EventKind
	Rune rune
	Text string
}

type Decoder struct {
	reader      *bufio.Reader
	setDeadline func(time.Time) error
	waitInput   func(time.Duration) (bool, error)
	escapeWait  time.Duration
}

func NewDecoder(reader io.Reader) *Decoder {
	buffered, ok := reader.(*bufio.Reader)
	if !ok {
		buffered = bufio.NewReader(reader)
	}
	return &Decoder{reader: buffered, escapeWait: 20 * time.Millisecond}
}

func (d *Decoder) Read() (Event, error) {
	b, err := d.reader.ReadByte()
	if err != nil {
		return Event{}, err
	}
	switch b {
	case 0x01:
		return Event{Kind: EventCtrlA}, nil
	case '\r', '\n':
		return Event{Kind: EventEnter}, nil
	case '\t':
		return Event{Kind: EventTab}, nil
	case 0x7f, 0x08:
		return Event{Kind: EventBackspace}, nil
	case 0x03:
		return Event{Kind: EventCtrlC}, nil
	case 0x04:
		return Event{Kind: EventCtrlD}, nil
	case 0x05:
		return Event{Kind: EventCtrlE}, nil
	case 0x0c:
		return Event{Kind: EventCtrlL}, nil
	case 0x13:
		return Event{Kind: EventCtrlS}, nil
	case 0x15:
		return Event{Kind: EventCtrlU}, nil
	case 0x17:
		return Event{Kind: EventCtrlW}, nil
	case 0x1b:
		return d.readEscape()
	}
	if b < 0x20 {
		return Event{Kind: EventUnknown}, nil
	}
	if b < utf8.RuneSelf {
		return Event{Kind: EventRune, Rune: rune(b)}, nil
	}
	if err := d.reader.UnreadByte(); err != nil {
		return Event{}, err
	}
	r, _, err := d.reader.ReadRune()
	if err != nil {
		return Event{}, err
	}
	if r == utf8.RuneError {
		return Event{Kind: EventUnknown}, nil
	}
	return Event{Kind: EventRune, Rune: r}, nil
}

func (d *Decoder) readEscape() (Event, error) {
	if d.reader.Buffered() == 0 && d.waitInput != nil {
		ready, err := d.waitInput(d.escapeWait)
		if err != nil || !ready {
			return Event{Kind: EventEscape}, nil
		}
	}
	if d.escapeWait > 0 && d.setDeadline != nil {
		if err := d.setDeadline(time.Now().Add(d.escapeWait)); err == nil {
			defer func() { _ = d.setDeadline(time.Time{}) }()
		}
	}
	b, err := d.reader.ReadByte()
	if err != nil {
		return Event{Kind: EventEscape}, nil
	}
	if b != '[' && b != 'O' {
		return Event{Kind: EventEscape}, nil
	}
	seq, err := d.reader.ReadByte()
	if err != nil {
		return Event{Kind: EventEscape}, nil
	}
	switch seq {
	case 'A':
		return Event{Kind: EventUp}, nil
	case 'B':
		return Event{Kind: EventDown}, nil
	case 'C':
		return Event{Kind: EventRight}, nil
	case 'D':
		return Event{Kind: EventLeft}, nil
	case 'Z':
		return Event{Kind: EventShiftTab}, nil
	case 'H':
		return Event{Kind: EventHome}, nil
	case 'F':
		return Event{Kind: EventEnd}, nil
	case '1', '4', '5', '6', '7', '8':
		if end, err := d.reader.ReadByte(); err == nil && end == '~' {
			return Event{Kind: tildeKeys[seq]}, nil
		}
	case '3':
		if end, err := d.reader.ReadByte(); err == nil && end == '~' {
			return Event{Kind: EventDelete}, nil
		}
	case '2':
		if suffix, err := d.reader.Peek(3); err == nil && string(suffix) == "00~" {
			_, _ = d.reader.Discard(3)
			text, err := d.readPaste()
			if err != nil {
				return Event{}, err
			}
			return Event{Kind: EventPaste, Text: text}, nil
		}
	}
	return Event{Kind: EventUnknown}, nil
}

// tildeKeys maps the digit of an ESC[<n>~ sequence to its key.
var tildeKeys = map[byte]EventKind{
	'1': EventHome, '7': EventHome,
	'4': EventEnd, '8': EventEnd,
	'5': EventPageUp, '6': EventPageDown,
}

func (d *Decoder) readPaste() (string, error) {
	marker := []byte("\x1b[201~")
	var content []byte
	for {
		b, err := d.reader.ReadByte()
		if err != nil {
			return "", err
		}
		content = append(content, b)
		if bytes.HasSuffix(content, marker) {
			text := string(content[:len(content)-len(marker)])
			text = strings.ReplaceAll(text, "\r\n", "\n")
			return strings.ReplaceAll(text, "\r", "\n"), nil
		}
	}
}

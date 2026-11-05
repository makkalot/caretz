package story

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz/e2e/fakellm"
	"github.com/makkalot/caretz/internal/loopguard"
)

// The loop guard's judge runs on the session's model unless one is
// configured, so its calls reach the fake model like the assistant's. These
// steps script its verdicts and tell its requests apart from the assistant's.

const judgeSpeaker = "the loop guard's judge"

// JudgeFindsStuck is the judge deciding the turn is stuck.
func JudgeFindsStuck(confidence float64, reason, alternative string) Part {
	doc := "reason: " + reason + "\ntry instead: " + alternative
	return Part{
		text:    verdictJSON(true, confidence, reason, alternative),
		speaker: judgeSpeaker,
		verb:    fmt.Sprintf("finds the turn stuck (confidence %.2f):", confidence),
		doc:     &doc,
	}
}

// JudgeFindsNotStuck is the judge deciding the turn is making progress.
func JudgeFindsNotStuck(confidence float64, reason string) Part {
	return Part{
		text:    verdictJSON(false, confidence, reason, ""),
		speaker: judgeSpeaker,
		verb:    fmt.Sprintf("finds the turn making progress (confidence %.2f): %q", confidence, reason),
	}
}

// JudgeReplies is a raw judge reply, for replies that are not a verdict.
func JudgeReplies(text string) Part {
	p := Replies(text)
	p.speaker = judgeSpeaker
	return p
}

func verdictJSON(stuck bool, confidence float64, reason, alternative string) string {
	data, err := json.Marshal(loopguard.Verdict{Stuck: stuck, Confidence: confidence, Reason: reason, Alternative: alternative})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func isJudgeRequest(req fakellm.Request) bool {
	return req.SystemPrompt() == loopguard.JudgeSystemPrompt
}

func (s *Story) judgeRequests() []fakellm.Request {
	var out []fakellm.Request
	for _, req := range s.LLM.Requests() {
		if isJudgeRequest(req) {
			out = append(out, req)
		}
	}
	return out
}

// JudgeAsked fails unless caretz asked the loop guard's judge exactly n times.
func (th *Then) JudgeAsked(n int) *Then {
	th.s.t.Helper()
	text := fmt.Sprintf("caretz asked the loop guard's judge %d times", n)
	switch n {
	case 0:
		text = "caretz never asked the loop guard's judge"
	case 1:
		text = "caretz asked the loop guard's judge once"
	}
	th.s.record(phaseThen, text, nil)
	if got := len(th.s.judgeRequests()); got != n {
		th.s.t.Fatalf("judge asked %d time(s), want %d\n%s", got, n, th.s.Res.Dump())
	}
	return th
}

// JudgeSaw checks the summary of the turn caretz showed the judge the last
// time it asked.
func (th *Then) JudgeSaw(m Matcher) *Then {
	th.s.t.Helper()
	reqs := th.s.judgeRequests()
	if len(reqs) == 0 {
		th.s.record(phaseThen, "what the loop guard's judge was shown "+m.desc, nil)
		th.s.t.Fatalf("caretz never asked the loop guard's judge\n%s", th.s.Res.Dump())
	}
	th.assert("what the loop guard's judge was shown", reqs[len(reqs)-1].LastUserMessage().Content, m)
	return th
}

// latestNote returns the last note caretz added for the assistant: a user
// message that ends an assistant request right after a tool result, rather
// than text inside the tool result itself.
func (s *Story) latestNote() (string, bool) {
	var note string
	found := false
	for _, req := range s.LLM.Requests() {
		if isJudgeRequest(req) || len(req.Messages) < 2 {
			continue
		}
		last, prev := req.Messages[len(req.Messages)-1], req.Messages[len(req.Messages)-2]
		if last.Role == "user" && prev.Role == "tool" {
			note, found = last.Content, true
		}
	}
	return note, found
}

// Note checks the last note caretz added for the assistant after a tool
// result, failing if it never added one.
func (th *Then) Note(m Matcher) *Then {
	th.s.t.Helper()
	note, ok := th.s.latestNote()
	if !ok {
		th.s.record(phaseThen, "the note caretz added for the assistant "+m.desc, nil)
		th.s.t.Fatalf("caretz never added a note for the assistant\n%s", th.s.Res.Dump())
	}
	th.assert("the note caretz added for the assistant", note, m)
	return th
}

// NoNote fails if caretz added a note for the assistant after a tool result.
func (th *Then) NoNote() *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, "caretz added no note for the assistant", nil)
	if note, ok := th.s.latestNote(); ok {
		th.s.t.Fatalf("caretz added a note for the assistant: %q", note)
	}
	return th
}

// OffersNoTools fails unless the request offered the assistant no tools.
func (r *RequestThen) OffersNoTools() *RequestThen {
	r.s.t.Helper()
	r.s.record(phaseThen, r.name()+" offers the assistant no tools", nil)
	if tools := r.request().Tools; len(tools) > 0 {
		r.s.t.Fatalf("%s offered %d tool(s), first %q", r.name(), len(tools), tools[0].Function.Name)
	}
	return r
}

// RecordedEvent fails unless the latest saved session's events.jsonpl has an
// event of eventType.
func (th *Then) RecordedEvent(eventType string) *Then {
	th.s.t.Helper()
	th.s.record(phaseThen, fmt.Sprintf("the session recorded a %q event", eventType), nil)
	path := filepath.Join(th.s.Env.ConfigRoot, "sessions", "cli", th.s.lastSession(), "events.jsonpl")
	f, err := os.Open(path)
	if err != nil {
		th.s.t.Fatalf("read session events: %v", err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var seen []string
	for scanner.Scan() {
		var event struct {
			Type string `json:"event_type"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == eventType {
			return th
		}
		seen = append(seen, event.Type)
	}
	if err := scanner.Err(); err != nil {
		th.s.t.Fatalf("read session events: %v", err)
	}
	th.s.t.Fatalf("no %s event in %s; saw: %s", eventType, path, strings.Join(seen, ", "))
	return th
}

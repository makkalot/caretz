package app

import "slices"

type slashSuggestionMenu struct {
	start      int
	end        int
	candidates []string
	selected   int
	active     bool
}

func (m *slashSuggestionMenu) show(match slashCompletionMatch) {
	m.start = match.start
	m.end = match.end
	m.candidates = append(m.candidates[:0], match.candidates...)
	m.selected = 0
	m.active = len(m.candidates) > 0
}

func (m *slashSuggestionMenu) dismiss() {
	m.start = 0
	m.end = 0
	m.candidates = nil
	m.selected = 0
	m.active = false
}

func (m slashSuggestionMenu) matches(match slashCompletionMatch) bool {
	return m.active && m.start == match.start && m.end == match.end && slices.Equal(m.candidates, match.candidates)
}

func (m *slashSuggestionMenu) move(delta int) bool {
	if !m.active || len(m.candidates) == 0 {
		return false
	}
	m.selected = (m.selected + delta + len(m.candidates)) % len(m.candidates)
	return true
}

func (m slashSuggestionMenu) selection() (string, bool) {
	if !m.active || m.selected < 0 || m.selected >= len(m.candidates) {
		return "", false
	}
	return m.candidates[m.selected], true
}

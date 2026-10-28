package app

import (
	"strings"

	"github.com/makkalot/caretz/internal/slashcmd"
)

type commandHistory struct {
	entries []string
	index   int
	draft   string
}

func newCommandHistory() commandHistory {
	return commandHistory{index: -1}
}

func (h *commandHistory) add(input string) {
	h.entries = append(h.entries, input)
	h.index = -1
	h.draft = ""
}

func (h *commandHistory) previous(current string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	if h.index < 0 {
		h.draft = current
		h.index = len(h.entries) - 1
	} else if h.index > 0 {
		h.index--
	} else {
		return "", false
	}
	return h.entries[h.index], true
}

func (h *commandHistory) next() (string, bool) {
	if h.index < 0 {
		return "", false
	}
	if h.index < len(h.entries)-1 {
		h.index++
		return h.entries[h.index], true
	}
	h.index = -1
	return h.draft, true
}

func (h *commandHistory) edited(current string) {
	if h.index >= 0 {
		h.index = -1
		h.draft = current
	}
}

func isRegisteredSlashCommand(input string, registry *slashcmd.Registry) bool {
	if registry == nil {
		return false
	}
	name, _, ok := slashcmd.Parse(input)
	if !ok || name == "" {
		return false
	}
	for _, command := range registry.Commands() {
		if strings.EqualFold(command.Name(), name) {
			return true
		}
		for _, alias := range command.Aliases() {
			if strings.EqualFold(alias, name) {
				return true
			}
		}
	}
	return false
}

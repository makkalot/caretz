package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/makkalot/caretz/lib/llm"
)

type replayMockProvider struct {
	normalResponse  llm.Response
	summaryResponse llm.Response
}

func toolDefinitionNamesMain(defs []llm.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

func hasToolDefinitionMain(defs []llm.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

func helpFlagGroups(help string) map[string][]string {
	groups := make(map[string][]string)
	current := ""
	for _, line := range strings.Split(help, "\n") {
		if line != "" && !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":") {
			current = strings.TrimSuffix(line, ":")
			continue
		}
		if current == "" || !strings.HasPrefix(line, "  -") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			groups[current] = append(groups[current], strings.TrimLeft(fields[0], "-"))
		}
	}
	return groups
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func mapGetenv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func intPtrMain(value int) *int {
	return &value
}

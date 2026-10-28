package frontmatter

import (
	"fmt"
	"strconv"
	"strings"
)

type Parsed struct {
	Fields map[string]string
	Maps   map[string]map[string]string
	Body   string
}

func Parse(content []byte) (Parsed, error) {
	s := strings.TrimSpace(string(content))
	if !strings.HasPrefix(s, "---") {
		return Parsed{}, fmt.Errorf("no opening frontmatter delimiter")
	}
	afterOpen := s[3:]
	closeIdx := strings.Index(afterOpen, "\n---")
	if closeIdx < 0 {
		return Parsed{}, fmt.Errorf("no closing frontmatter delimiter")
	}
	block := afterOpen[:closeIdx]
	body := strings.TrimSpace(afterOpen[closeIdx+len("\n---"):])

	fields, maps, err := parseBlock(block)
	if err != nil {
		return Parsed{}, err
	}
	return Parsed{Fields: fields, Maps: maps, Body: body}, nil
}

func parseBlock(block string) (map[string]string, map[string]map[string]string, error) {
	fields := map[string]string{}
	maps := map[string]map[string]string{}
	var currentMap string

	for lineNo, rawLine := range strings.Split(block, "\n") {
		if strings.TrimSpace(rawLine) == "" || strings.HasPrefix(strings.TrimSpace(rawLine), "#") {
			continue
		}
		indent := leadingSpaces(rawLine)
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "-") {
			return nil, nil, fmt.Errorf("unsupported frontmatter sequence on line %d", lineNo+1)
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, nil, fmt.Errorf("invalid frontmatter line %d", lineNo+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, nil, fmt.Errorf("invalid empty frontmatter key on line %d", lineNo+1)
		}

		if indent > 0 {
			if currentMap == "" {
				return nil, nil, fmt.Errorf("nested frontmatter field %q has no parent", key)
			}
			parsedValue, err := parseScalar(value)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid frontmatter value for %s.%s: %w", currentMap, key, err)
			}
			maps[currentMap][key] = parsedValue
			continue
		}

		currentMap = ""
		if value == "" {
			currentMap = key
			if maps[currentMap] == nil {
				maps[currentMap] = map[string]string{}
			}
			continue
		}
		parsedValue, err := parseScalar(value)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid frontmatter value for %s: %w", key, err)
		}
		fields[key] = parsedValue
	}
	return fields, maps, nil
}

func parseScalar(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, `"`) {
		return strconv.Unquote(value)
	}
	if strings.HasPrefix(value, "'") {
		if !strings.HasSuffix(value, "'") || len(value) < 2 {
			return "", fmt.Errorf("unterminated single-quoted scalar")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	if strings.Contains(value, " #") {
		value = strings.TrimSpace(strings.SplitN(value, " #", 2)[0])
	}
	return value, nil
}

func leadingSpaces(s string) int {
	count := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		count++
	}
	return count
}

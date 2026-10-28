package slashcmd

import (
	"fmt"
	"strings"
	"unicode"
)

type optionDef struct {
	Name       string
	Logical    string
	TakesValue bool
	Value      string
}

type parsedArgs struct {
	Positionals []string
	Options     map[string]string
}

func parseCommandArgs(input string, defs []optionDef) (parsedArgs, error) {
	tokens, err := splitArgs(input)
	if err != nil {
		return parsedArgs{}, err
	}
	byName := make(map[string]optionDef, len(defs))
	legacy := make(map[string]string)
	for _, def := range defs {
		byName[def.Name] = def
		if strings.Contains(def.Name, "-") {
			legacy[strings.ReplaceAll(def.Name, "-", "_")] = "--" + def.Name
		}
	}
	out := parsedArgs{Options: make(map[string]string)}
	seen := make(map[string]string)
	positional := false
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if !positional && token == "--" {
			positional = true
			continue
		}
		if positional {
			if replacement, ok := legacyTokenReplacement(token, byName, legacy); ok {
				return parsedArgs{}, fmt.Errorf("options must precede arguments; use %s before positional arguments", replacement)
			}
			out.Positionals = append(out.Positionals, token)
			continue
		}
		if !strings.HasPrefix(token, "--") {
			if replacement, ok := legacyTokenReplacement(token, byName, legacy); ok {
				return parsedArgs{}, fmt.Errorf("legacy option %q is not supported; use %s", token, replacement)
			}
			positional = true
			out.Positionals = append(out.Positionals, token)
			continue
		}
		nameValue := strings.TrimPrefix(token, "--")
		name, inline, hasInline := strings.Cut(nameValue, "=")
		def, ok := byName[name]
		if !ok {
			if replacement, legacyOK := legacyTokenReplacement(token, byName, legacy); legacyOK {
				return parsedArgs{}, fmt.Errorf("legacy option %q is not supported; use %s", "--"+name, replacement)
			}
			return parsedArgs{}, fmt.Errorf("unknown option %q", "--"+name)
		}
		logical := def.Logical
		if logical == "" {
			logical = def.Name
		}
		if previous, exists := seen[logical]; exists {
			return parsedArgs{}, fmt.Errorf("duplicate or conflicting options %s and --%s", previous, name)
		}
		value := def.Value
		if def.TakesValue {
			if hasInline {
				value = inline
			} else {
				if i+1 >= len(tokens) || tokens[i+1] == "--" || strings.HasPrefix(tokens[i+1], "--") {
					return parsedArgs{}, fmt.Errorf("--%s requires a value", name)
				}
				i++
				value = tokens[i]
			}
			if strings.TrimSpace(value) == "" {
				return parsedArgs{}, fmt.Errorf("--%s requires a value", name)
			}
		} else if hasInline {
			return parsedArgs{}, fmt.Errorf("--%s does not accept a value", name)
		}
		seen[logical] = "--" + name
		out.Options[logical] = value
	}
	return out, nil
}

func legacyTokenReplacement(token string, defs map[string]optionDef, legacy map[string]string) (string, bool) {
	name := strings.TrimPrefix(token, "--")
	name, value, hasEquals := strings.Cut(name, "=")
	switch name {
	case "fresh", "new_session":
		if hasEquals && value == "false" {
			return "--no-new-session", true
		}
		return "--new-session", true
	case "final_review":
		if hasEquals && value == "false" {
			return "--no-review", true
		}
		return "--review", true
	case "stop":
		return "--done", true
	}
	if def, ok := defs[name]; ok {
		return "--" + def.Name, true
	}
	if replacement, ok := legacy[name]; ok {
		return replacement, true
	}
	if hasEquals {
		return "--" + strings.ReplaceAll(name, "_", "-"), true
	}
	return "", false
}

func rejectArguments(command, args string) error {
	if strings.TrimSpace(args) != "" {
		return fmt.Errorf("/%s does not accept arguments", command)
	}
	return nil
}

func splitArgs(input string) ([]string, error) {
	var tokens []string
	var b strings.Builder
	var quote rune
	escaped := false
	inToken := false

	for _, r := range input {
		if escaped {
			b.WriteRune(r)
			inToken = true
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			inToken = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			inToken = true
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			inToken = true
			continue
		}
		if unicode.IsSpace(r) {
			if inToken {
				tokens = append(tokens, b.String())
				b.Reset()
				inToken = false
			}
			continue
		}
		b.WriteRune(r)
		inToken = true
	}

	if escaped {
		b.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted argument")
	}
	if inToken {
		tokens = append(tokens, b.String())
	}
	return tokens, nil
}

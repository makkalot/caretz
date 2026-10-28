package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/makkalot/caretz/internal/frontmatter"
)

type Frontmatter struct {
	Description string
	Mode        string
	Model       string
	Reasoning   string
	Temperature *float64
	Permission  map[string]string
}

type Definition struct {
	Name         string
	Description  string
	Mode         string
	Model        string
	Reasoning    string
	Temperature  *float64
	Permission   map[string]string
	SystemPrompt string
	Path         string
}

func AgentsDir(cwd string) string {
	return filepath.Join(cwd, ".caretz", "agents")
}

func Discover(cwd string) ([]Definition, error) {
	return DiscoverFromDir(AgentsDir(cwd))
}

func DiscoverFromDirs(dirs []string) ([]Definition, error) {
	merged := map[string]Definition{}
	for _, dir := range dirs {
		defs, err := DiscoverFromDir(dir)
		if err != nil {
			return nil, err
		}
		for _, def := range defs {
			merged[def.Name] = def
		}
	}

	defs := make([]Definition, 0, len(merged))
	for _, def := range merged {
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func DiscoverFromDir(dir string) ([]Definition, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading agents directory: %w", err)
	}

	var defs []Definition
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		def, err := LoadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func Load(cwd, name string) (Definition, error) {
	return LoadFromDir(AgentsDir(cwd), name)
}

func LoadFromDirs(dirs []string, name string) (Definition, error) {
	if name = strings.TrimSpace(name); name == "" {
		return Definition{}, fmt.Errorf("subagent name must not be empty")
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return Definition{}, fmt.Errorf("invalid subagent name %q", name)
	}
	var firstErr error
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := strings.TrimSpace(dirs[i])
		if dir == "" {
			continue
		}
		def, err := LoadFile(filepath.Join(dir, name+".md"))
		if err == nil {
			return def, nil
		}
		if !os.IsNotExist(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
	}
	if firstErr != nil {
		return Definition{}, firstErr
	}
	return Definition{}, fmt.Errorf("reading subagent %s: %w", name, os.ErrNotExist)
}

// Snapshot maps each subagent name to the file that defines it, with later
// directories overriding earlier ones. Files are parsed when they run, so
// edits to a listed file apply, but a definition added after the snapshot is
// never run.
func Snapshot(dirs []string) map[string]string {
	paths := map[string]string{}
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			paths[strings.TrimSuffix(entry.Name(), ".md")] = filepath.Join(dir, entry.Name())
		}
	}
	return paths
}

func LoadFromSnapshot(paths map[string]string, name string) (Definition, error) {
	if name = strings.TrimSpace(name); name == "" {
		return Definition{}, fmt.Errorf("subagent name must not be empty")
	}
	path, ok := paths[name]
	if !ok {
		return Definition{}, fmt.Errorf("subagent %q not found; agents added after the session started need a new session: %w", name, os.ErrNotExist)
	}
	return LoadFile(path)
}

func LoadFromDir(dir, name string) (Definition, error) {
	if name = strings.TrimSpace(name); name == "" {
		return Definition{}, fmt.Errorf("subagent name must not be empty")
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return Definition{}, fmt.Errorf("invalid subagent name %q", name)
	}
	return LoadFile(filepath.Join(dir, name+".md"))
}

func LoadFile(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("reading subagent %s: %w", path, err)
	}
	fm, body, err := Parse(data)
	if err != nil {
		return Definition{}, fmt.Errorf("parsing subagent %s: %w", path, err)
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	absPath, _ := filepath.Abs(path)
	return Definition{
		Name:         name,
		Description:  fm.Description,
		Mode:         fm.Mode,
		Model:        fm.Model,
		Reasoning:    fm.Reasoning,
		Temperature:  fm.Temperature,
		Permission:   fm.Permission,
		SystemPrompt: body,
		Path:         absPath,
	}, nil
}

func Parse(content []byte) (Frontmatter, string, error) {
	parsed, err := frontmatter.Parse(content)
	if err != nil {
		return Frontmatter{}, "", err
	}
	fm := Frontmatter{
		Description: strings.TrimSpace(parsed.Fields["description"]),
		Mode:        strings.TrimSpace(parsed.Fields["mode"]),
		Model:       strings.TrimSpace(parsed.Fields["model"]),
		Reasoning:   strings.TrimSpace(parsed.Fields["reasoning"]),
		Permission:  parsed.Maps["permission"],
	}
	if temperature := strings.TrimSpace(parsed.Fields["temperature"]); temperature != "" {
		value, err := strconv.ParseFloat(temperature, 64)
		if err != nil {
			return Frontmatter{}, "", fmt.Errorf("invalid frontmatter temperature: %w", err)
		}
		fm.Temperature = &value
	}
	if fm.Description == "" {
		return Frontmatter{}, "", fmt.Errorf("frontmatter missing required field: description")
	}
	if fm.Mode != "subagent" {
		return Frontmatter{}, "", fmt.Errorf("frontmatter mode must be subagent")
	}
	if parsed.Body == "" {
		return Frontmatter{}, "", fmt.Errorf("subagent prompt body must not be empty")
	}
	return fm, parsed.Body, nil
}

func DisabledTools(permission map[string]string) map[string]bool {
	disabled := map[string]bool{}
	for key, value := range permission {
		if strings.ToLower(strings.TrimSpace(value)) != "deny" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "edit":
			disabled["Edit"] = true
			disabled["Write"] = true
		case "bash":
			disabled["Bash"] = true
		}
	}
	return disabled
}

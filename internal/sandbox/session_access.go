package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// AccessPath resolves existing ancestors as well as paths not yet created.
func AccessPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	cursor := filepath.Clean(abs)
	for {
		resolved, err := filepath.EvalSymlinks(cursor)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || cursor == filepath.Dir(cursor) {
			return "", err
		}
		tail = append(tail, filepath.Base(cursor))
		cursor = filepath.Dir(cursor)
	}
}

func PathContains(root, path string) bool {
	root, err := AccessPath(root)
	if err != nil {
		return false
	}
	path, err = AccessPath(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ForWorkspace keeps explicit restrictions and classifies recognized session
// stores by resolved location, never by whether --config-root was supplied.
// Subagent definitions are read-only so a session cannot write an agent with
// its own system prompt and then call it.
func (c Config) ForWorkspace(workspace string) Config {
	c.HiddenPaths = append([]string(nil), c.HiddenPaths...)
	c.ReadOnlyPaths = append([]string(nil), c.ReadOnlyPaths...)
	c.HiddenPaths = append(c.HiddenPaths, filepath.Join(workspace, ".caretz", "config.json"))
	c.ReadOnlyPaths = append(c.ReadOnlyPaths, filepath.Join(workspace, ".caretz", "agents"))
	stores := append([]string{filepath.Join(workspace, ".caretz", "sessions")}, c.SessionPaths...)
	for _, path := range stores {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if PathContains(workspace, path) {
			c.ReadOnlyPaths = append(c.ReadOnlyPaths, path)
		} else {
			c.HiddenPaths = append(c.HiddenPaths, path)
		}
	}
	return c
}

package slashcmd

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/makkalot/caretz/internal/goal"
)

// staticGoalScan gives the first planning step the project's build files,
// verification scripts and available runners, so it starts from facts.
func staticGoalScan(workspace string) map[string]any {
	files := map[string]string{}
	for _, name := range []string{"Makefile", "package.json", "pyproject.toml", "pytest.ini", "go.mod"} {
		data, err := os.ReadFile(filepath.Join(workspace, name))
		if err == nil {
			files[name] = goal.Compact(string(data), 4000)
		}
	}
	var scripts []string
	matches, _ := filepath.Glob(filepath.Join(workspace, "scripts", "verify_*"))
	for _, match := range matches {
		if rel, err := filepath.Rel(workspace, match); err == nil {
			scripts = append(scripts, rel)
		}
	}
	var runners []string
	for _, name := range []string{"go", "make", "npm", "pytest", "python", "python3", "rg", "jq"} {
		if _, err := exec.LookPath(name); err == nil {
			runners = append(runners, name)
		}
	}
	return map[string]any{"files": files, "verify_scripts": scripts, "available_binaries": runners}
}

package sandbox

import (
	"os"
	"path/filepath"
)

// ScratchDir is where sandboxed commands keep reusable tool caches, such as
// Go's build and module caches, inside the workspace.
func ScratchDir(workspace string) string {
	return filepath.Join(workspace, ".caretz", "scratch")
}

// readOnlyScratchDir returns the workspace's scratch directory, creating it,
// when a read-only command may still write there. It holds only caches, so
// keeping it writable lets builds and tests reuse them while the rest of the
// workspace stays read-only.
//
// The directory must be a real directory inside the workspace. An earlier
// writable command could have replaced .caretz or .caretz/scratch with a
// symlink, and granting write access through it would open the link's
// target instead, so any symlink disables the exception.
func readOnlyScratchDir(workspace string) (string, bool) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", false
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", false
	}
	dir := ScratchDir(workspace)
	for _, path := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if err := os.Mkdir(path, 0o755); err != nil {
				return "", false
			}
			info, err = os.Lstat(path)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != ScratchDir(resolvedWorkspace) {
		return "", false
	}
	return dir, true
}

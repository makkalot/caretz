//go:build unix

// Package peers lets caretz sessions in one workspace message long-lived peer
// bots. Each endpoint (a bot or an interactive root session) is owned by one
// process. Messages are durable, signed files in a per-endpoint spool, and a
// unix socket per endpoint only carries wake-ups.
package peers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const keySize = 32

// RunDir is the private per-workspace directory holding spools, locks,
// sockets, and the message signing key. Sandboxed tools must not see it.
type RunDir struct {
	path string
	key  []byte
}

// RunRoot is the directory, under the config root, that holds every
// workspace's run directory. It belongs on the sandbox hidden-path list.
func RunRoot(configRoot string) string {
	return filepath.Join(configRoot, "run")
}

// OpenRunDir opens or creates the run directory for workspace.
func OpenRunDir(configRoot, workspace string) (*RunDir, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	// Every path to the same workspace must reach the same endpoints.
	if resolved, err := filepath.EvalSymlinks(absWorkspace); err == nil {
		absWorkspace = resolved
	}
	sum := sha256.Sum256([]byte(absWorkspace))
	root := RunRoot(configRoot)
	path := filepath.Join(root, hex.EncodeToString(sum[:4]))
	for _, dir := range []string{root, path, filepath.Join(path, "inbox"), filepath.Join(path, "locks"), filepath.Join(path, "s")} {
		if err := ensurePrivateDir(dir); err != nil {
			return nil, err
		}
	}
	key, err := loadOrCreateKey(filepath.Join(path, "key"))
	if err != nil {
		return nil, err
	}
	return &RunDir{path: path, key: key}, nil
}

// Path returns the run directory path.
func (r *RunDir) Path() string { return r.path }

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return checkPrivate(dir, true)
}

// checkPrivate refuses paths that another user owns or that group or other
// users can access.
func checkPrivate(path string, dir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("peer run path %s must not be a symlink", path)
	}
	if dir != info.IsDir() {
		return fmt.Errorf("peer run path %s has an unexpected type", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("peer run path %s is owned by uid %d, not the current user", path, stat.Uid)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("peer run path %s has mode %v; it must not be accessible to group or other users", path, info.Mode().Perm())
	}
	return nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		key := make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			_ = file.Close()
			return nil, err
		}
		if _, err := file.Write(key); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := checkPrivate(path, false); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("peer signing key %s is corrupt", path)
	}
	return key, nil
}

// ErrLocked reports that another process owns an endpoint.
type ErrLocked struct {
	Endpoint string
	PID      int
}

func (e *ErrLocked) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("%s is already running (pid %d)", e.Endpoint, e.PID)
	}
	return fmt.Sprintf("%s is already running", e.Endpoint)
}

// Lock is an exclusive, process-held claim on an endpoint.
type Lock struct {
	file *os.File
}

// Release drops the claim.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (r *RunDir) lockPath(endpoint string) string {
	return filepath.Join(r.path, "locks", endpoint+".lock")
}

// Lock claims endpoint for this process. It fails with *ErrLocked when a
// live process already holds it; locks of dead processes are released by the
// kernel, so stale files never block.
func (r *RunDir) Lock(endpoint string) (*Lock, error) {
	if err := validEndpoint(endpoint); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(r.lockPath(endpoint), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &ErrLocked{Endpoint: endpoint, PID: r.lockPID(endpoint)}
		}
		return nil, err
	}
	_ = file.Truncate(0)
	_, _ = file.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	return &Lock{file: file}, nil
}

// Live reports whether a process currently holds endpoint, and its pid.
func (r *RunDir) Live(endpoint string) (int, bool) {
	if validEndpoint(endpoint) != nil {
		return 0, false
	}
	file, err := os.OpenFile(r.lockPath(endpoint), os.O_RDONLY, 0)
	if err != nil {
		return 0, false
	}
	defer func() { _ = file.Close() }()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return r.lockPID(endpoint), true
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return 0, false
}

func (r *RunDir) lockPID(endpoint string) int {
	data, err := os.ReadFile(r.lockPath(endpoint))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

// BotEndpoint is the endpoint name of the peer bot called name.
func BotEndpoint(name string) string { return "bot." + name }

// SessionEndpoint is the endpoint name of an interactive root session.
func SessionEndpoint(adapter, sessionID string) string {
	return "session." + adapter + "." + sessionID
}

func validEndpoint(endpoint string) error {
	if endpoint == "" || strings.ContainsAny(endpoint, `/\`) || strings.HasPrefix(endpoint, ".") {
		return fmt.Errorf("invalid peer endpoint %q", endpoint)
	}
	return nil
}

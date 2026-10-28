package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
)

const (
	DefaultTimeoutSeconds = 120
	MaxTimeoutSeconds     = 10 * 60
	sandboxMaxOutput      = 1 * 1024 * 1024
)

// makeSandboxTempDir creates a private temporary directory under
// sandboxTempRoot. When that root is not writable, as when Caretz itself runs
// inside a Caretz sandbox whose TMPDIR is already short, it falls back to the
// default temporary directory.
func makeSandboxTempDir(pattern string) (string, error) {
	if sandboxTempRoot != "" {
		if dir, err := os.MkdirTemp(sandboxTempRoot, pattern); err == nil {
			return dir, nil
		}
	}
	return os.MkdirTemp("", pattern)
}

type Config struct {
	UnrestrictedNetwork bool
	AllowLoopback       bool
	NetworkAllowlist    []string
	ExtraBinaries       []string
	ReadOnly            bool
	ReadableRoots       []string
	WritableRoots       []string
	AllowDocker         bool
	AllowSystemBus      bool
	AllowGPU            bool
	HiddenPaths         []string
	ReadOnlyPaths       []string
	SessionPaths        []string
	Environment         []string
}

type RunOptions struct {
	TimeoutSeconds int
	Env            map[string]string
	Background     bool
	// ReadOnly runs this one command with the workspace and writable roots
	// read-only, as if the runner were configured ReadOnly. A persistent
	// Session cannot change its policy per command and rejects it.
	ReadOnly         bool
	ReadableRoots    []string
	WritableRoots    []string
	NetworkAllowlist []string
	AuthorizeNetwork func(context.Context, string) (bool, error)
}

type Runner struct {
	config Config
}

func NewRunner(config Config) *Runner {
	networkAllowlist := append([]string(nil), config.NetworkAllowlist...)
	config.NetworkAllowlist = networkAllowlist
	extraBinaries := append([]string(nil), config.ExtraBinaries...)
	config.ExtraBinaries = extraBinaries
	readableRoots := append([]string(nil), config.ReadableRoots...)
	config.ReadableRoots = readableRoots
	writableRoots := append([]string(nil), config.WritableRoots...)
	config.WritableRoots = writableRoots
	config.HiddenPaths = append([]string(nil), config.HiddenPaths...)
	config.ReadOnlyPaths = append([]string(nil), config.ReadOnlyPaths...)
	config.SessionPaths = append([]string(nil), config.SessionPaths...)
	config.Environment = append([]string(nil), config.Environment...)

	return &Runner{config: config}
}

// Environment resolves the environment granted to sandboxed commands, so a
// command approved to leave the sandbox runs with the same variables.
func (r *Runner) Environment() (map[string]string, error) {
	return resolveEnvironment(r.config.Environment)
}

func resolveEnvironment(specs []string) (map[string]string, error) {
	result := make(map[string]string, len(specs))
	for _, spec := range specs {
		name, value, hasValue := strings.Cut(spec, "=")
		if !validEnvironmentName(name) {
			return nil, fmt.Errorf("invalid sandbox environment entry %q", spec)
		}
		if reservedEnvironmentName(name) {
			return nil, fmt.Errorf("sandbox environment variable %s is controlled by Caretz", name)
		}
		if !hasValue {
			var ok bool
			value, ok = os.LookupEnv(name)
			if !ok {
				delete(result, name)
				continue
			}
		}
		result[name] = value
	}
	return result, nil
}

// ValidateEnvironment checks Docker-style NAME and NAME=value entries without
// resolving NAME from the host environment.
func ValidateEnvironment(specs []string) error {
	for _, spec := range specs {
		name, _, _ := strings.Cut(spec, "=")
		if !validEnvironmentName(name) {
			return fmt.Errorf("invalid sandbox environment entry %q", spec)
		}
		if reservedEnvironmentName(name) {
			return fmt.Errorf("sandbox environment variable %s is controlled by Caretz", name)
		}
	}
	return nil
}

func validateRequestEnvironment(env map[string]string) error {
	for name := range env {
		if !validEnvironmentName(name) {
			return fmt.Errorf("invalid command environment variable %q", name)
		}
		if reservedEnvironmentName(name) {
			return fmt.Errorf("command environment variable %s is controlled by Caretz", name)
		}
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" || !environmentNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !environmentNamePart(name[i]) {
			return false
		}
	}
	return true
}

func environmentNameStart(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
}

func environmentNamePart(c byte) bool {
	return environmentNameStart(c) || c >= '0' && c <= '9'
}

func reservedEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	switch upper {
	case "HOME", "TMPDIR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "DOCKER_HOST", socksProxyEnv, sshProxyCommandEnv,
		"BASH_ENV", "ENV", "CDPATH", "IFS", sandboxOwnerEnv:
		return true
	}
	return strings.HasPrefix(upper, "DYLD_") || strings.HasPrefix(upper, "LD_") ||
		strings.HasPrefix(upper, "MALLOC_") || strings.HasPrefix(upper, "_SANDBOX_") ||
		strings.HasPrefix(upper, "_CARETZ_SESSION_")
}

func Run(cwd, command string) (string, string, error) {
	return NewRunner(Config{}).Run(cwd, command)
}

func RunContext(ctx context.Context, cwd, command string) (string, string, error) {
	return NewRunner(Config{}).RunContext(ctx, cwd, command)
}

func resolveTimeoutSeconds(opts RunOptions) int {
	timeout := DefaultTimeoutSeconds
	if opts.TimeoutSeconds > 0 {
		timeout = opts.TimeoutSeconds
	}
	if timeout > MaxTimeoutSeconds {
		timeout = MaxTimeoutSeconds
	}
	return timeout
}

type limitedWriter struct {
	w         *bytes.Buffer
	maxBytes  int
	written   int
	truncated bool
}

func (lw *limitedWriter) Write(p []byte) (n int, err error) {
	if lw.written >= lw.maxBytes {
		lw.truncated = true
		return len(p), nil
	}
	if lw.written+len(p) > lw.maxBytes {
		partial := lw.maxBytes - lw.written
		lw.w.Write(p[:partial])
		lw.written = lw.maxBytes
		lw.truncated = true
		return len(p), nil
	}
	n, err = lw.w.Write(p)
	lw.written += n
	return n, err
}

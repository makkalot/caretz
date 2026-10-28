//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var macOSReadRoots = []string{
	"/System", "/Library", "/usr", "/bin", "/sbin", "/etc", "/private/etc",
	"/private/var/db/timezone", "/private/var/db/dyld", "/private/var/select",
	"/var/db/xcode_select_link", "/private/var/db/xcode_select_link",
	"/Applications", "/opt", "/nix",
}

// proxyEndpoints holds the loopback addresses of the HTTP and SOCKS proxies.
type proxyEndpoints struct {
	HTTP  string
	SOCKS string
}

func sandboxLiteral(path string) string {
	path = strings.ReplaceAll(path, `\`, `\\`)
	return strings.ReplaceAll(path, `"`, `\"`)
}

func profileRule(action, operation, filter, path string) string {
	return fmt.Sprintf("(%s %s (%s \"%s\"))\n", action, operation, filter, sandboxLiteral(path))
}

func pathVariants(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	abs = filepath.Clean(abs)
	result := []string{abs}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != abs {
		result = append(result, resolved)
	} else if err != nil {
		remaining := []string{}
		cursor := abs
		for cursor != filepath.Dir(cursor) {
			if resolved, resolveErr := filepath.EvalSymlinks(cursor); resolveErr == nil {
				for i := len(remaining) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, remaining[i])
				}
				if resolved != abs {
					result = append(result, resolved)
				}
				break
			}
			remaining = append(remaining, filepath.Base(cursor))
			cursor = filepath.Dir(cursor)
		}
	}
	return uniqueStrings(result)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func addPathReadRules(profile *strings.Builder, path string) {
	for _, candidate := range pathVariants(path) {
		profile.WriteString(profileRule("allow", "file-read*", "literal", candidate))
		profile.WriteString(profileRule("allow", "file-read*", "subpath", candidate))
		for parent := filepath.Dir(candidate); parent != candidate; parent = filepath.Dir(parent) {
			profile.WriteString(profileRule("allow", "file-read-metadata", "literal", parent))
			candidate = parent
		}
	}
}

func addPathWriteRules(profile *strings.Builder, path string) {
	for _, candidate := range pathVariants(path) {
		profile.WriteString(profileRule("allow", "file-write*", "literal", candidate))
		profile.WriteString(profileRule("allow", "file-write*", "subpath", candidate))
	}
}

func addHiddenPathRules(profile *strings.Builder, path string) {
	for _, candidate := range pathVariants(path) {
		profile.WriteString(profileRule("deny", "file-read* file-write*", "literal", candidate))
		profile.WriteString(profileRule("deny", "file-read* file-write*", "subpath", candidate))
	}
}

// sandboxTempRoot holds private sandbox temp directories. The per-user
// /var/folders temp directory is long enough that a socket path below it can
// exceed the 104-byte sun_path limit; /private/tmp keeps paths short.
const sandboxTempRoot = "/private/tmp"

// addUnixSocketRules allows or denies binding, accepting, and connecting Unix
// sockets under path. Sockets are allowed only where commands may already
// write, so they add no access beyond the file policy.
func addUnixSocketRules(profile *strings.Builder, action, path string) {
	for _, candidate := range pathVariants(path) {
		_, _ = fmt.Fprintf(profile, "(%s network-bind network-inbound (local unix-socket (subpath \"%s\")))\n", action, sandboxLiteral(candidate))
		_, _ = fmt.Fprintf(profile, "(%s network-outbound (remote unix-socket (subpath \"%s\")))\n", action, sandboxLiteral(candidate))
	}
}

// addXcrunCacheRules grants Apple's xcrun cache, which lives in the system
// temporary directory rather than the sandbox's private one.
func addXcrunCacheRules(profile *strings.Builder, systemTempDir string) {
	for _, candidate := range pathVariants(systemTempDir) {
		profile.WriteString(profileRule("allow", "file-read* file-write*", "literal", filepath.Join(candidate, "xcrun_db")))
		pattern := "^" + regexp.QuoteMeta(candidate) + `/xcrun_db-[^/]+$`
		_, _ = fmt.Fprintf(profile, "(allow file-write* (regex #\"%s\"))\n", sandboxLiteral(pattern))
	}
}

func generateSandboxProfileForRun(cwd, tmpDir string, extraBinaries []string, unrestrictedNetwork, allowLoopback, allowDocker, readOnly bool, readableRoots, writableRoots, hiddenPaths []string, proxy proxyEndpoints, readOnlyPaths ...string) string {
	var profile strings.Builder
	profile.WriteString("(version 1)\n(allow default)\n(deny file-read*)\n(deny file-write*)\n")
	profile.WriteString(profileRule("allow", "file-read*", "literal", "/"))
	for _, root := range append(append([]string{}, macOSReadRoots...), cwd, tmpDir) {
		addPathReadRules(&profile, root)
	}
	for _, root := range append(append([]string{}, readableRoots...), writableRoots...) {
		addPathReadRules(&profile, root)
	}
	for _, binary := range extraBinaries {
		addPathReadRules(&profile, binary)
	}
	if self, err := os.Executable(); err == nil {
		addPathReadRules(&profile, self)
	}
	for _, device := range []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty"} {
		profile.WriteString(profileRule("allow", "file-read* file-write-data", "literal", device))
	}
	addPathReadRules(&profile, "/dev/fd")
	addPathWriteRules(&profile, tmpDir)
	addXcrunCacheRules(&profile, os.TempDir())
	if !readOnly {
		addPathWriteRules(&profile, cwd)
		for _, root := range writableRoots {
			addPathWriteRules(&profile, root)
		}
	}

	if unrestrictedNetwork {
		profile.WriteString("(allow network*)\n")
	} else {
		profile.WriteString("(deny network*)\n")
		addUnixSocketRules(&profile, "allow", tmpDir)
		if !readOnly {
			addUnixSocketRules(&profile, "allow", cwd)
			for _, root := range writableRoots {
				addUnixSocketRules(&profile, "allow", root)
			}
		}
		if allowLoopback {
			profile.WriteString("(allow network-bind (local tcp \"localhost:*\"))\n")
			profile.WriteString("(allow network-inbound (local tcp \"localhost:*\"))\n")
			profile.WriteString("(allow network-outbound (remote tcp \"localhost:*\"))\n")
		}
		for _, addr := range []string{proxy.HTTP, proxy.SOCKS} {
			if _, port, err := net.SplitHostPort(addr); err == nil {
				_, _ = fmt.Fprintf(&profile, "(allow network-outbound (remote tcp \"localhost:%s\"))\n", sandboxLiteral(port))
			}
		}
		if allowDocker {
			for _, path := range dockerSocketPaths() {
				profile.WriteString(profileRule("allow", "network-outbound", "literal", path))
			}
		}
		// An exact socket read grant, such as an SSH agent, must also be
		// connectable. Docker sockets stay behind the AllowDocker gate.
		for _, root := range readableRoots {
			if info, err := os.Stat(root); err == nil && info.Mode()&os.ModeSocket != 0 && (allowDocker || !slices.Contains(dockerSocketPaths(), root)) {
				profile.WriteString(profileRule("allow", "network-outbound", "literal", root))
			}
		}
	}
	for _, binary := range []string{
		"/sbin/mount", "/sbin/umount", "/usr/sbin/mount", "/usr/sbin/umount",
		"/usr/sbin/reboot", "/sbin/reboot", "/usr/sbin/halt", "/sbin/halt",
		"/usr/sbin/shutdown", "/usr/bin/passwd", "/usr/sbin/kextload",
		"/usr/sbin/kextunload", "/usr/bin/launchctl", "/usr/sbin/launchd", "/usr/bin/open",
	} {
		profile.WriteString(profileRule("deny", "process-exec", "literal", binary))
	}
	for _, path := range readOnlyPaths {
		for _, candidate := range pathVariants(path) {
			profile.WriteString(profileRule("deny", "file-write*", "literal", candidate))
			profile.WriteString(profileRule("deny", "file-write*", "subpath", candidate))
		}
		addUnixSocketRules(&profile, "deny", path)
	}
	for _, path := range hiddenPaths {
		addHiddenPathRules(&profile, path)
		addUnixSocketRules(&profile, "deny", path)
	}
	return profile.String()
}

func dockerSocketPaths() []string {
	paths := []string{"/var/run/docker.sock", "/private/var/run/docker.sock"}
	if dockerHost := strings.TrimSpace(os.Getenv("DOCKER_HOST")); dockerHost != "" {
		if parsed, err := url.Parse(dockerHost); err == nil && parsed.Scheme == "unix" && filepath.IsAbs(parsed.Path) {
			paths = append(paths, parsed.Path)
		}
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(homeDir, ".docker/run/docker.sock"), filepath.Join(homeDir, ".orbstack/run/docker.sock"), filepath.Join(homeDir, ".colima/default/docker.sock"), filepath.Join(homeDir, ".rd/docker.sock"))
	}
	var candidates []string
	for _, path := range paths {
		candidates = append(candidates, path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			candidates = append(candidates, resolved)
		}
	}
	return uniqueStrings(candidates)
}

func macOSSandboxEnvironment(cwd, tmpDir string, proxy proxyEndpoints, allowDocker bool, grants map[string]string) []string {
	envMap := environmentMap(os.Environ())
	scratchDir := filepath.Join(cwd, ".caretz", "scratch")
	env := []string{
		"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/nix/var/nix/profiles/default/bin",
		"HOME=" + cwd,
		"GOCACHE=" + filepath.Join(scratchDir, "go-build"),
		"GOPATH=" + filepath.Join(scratchDir, "go"),
		"TMPDIR=" + tmpDir,
		"TERM=" + envMap["TERM"],
		"LANG=" + envMap["LANG"],
	}
	for key, value := range envMap {
		if strings.HasPrefix(key, "LC_") {
			env = append(env, key+"="+value)
		}
	}
	env = mergeEnv(env, grants)
	if proxy.HTTP != "" {
		env = append(env, proxyEnv(proxy.HTTP, proxy.SOCKS, false)...)
	}
	if proxy.SOCKS != "" {
		for _, entry := range sshProxyEnv() {
			// Appended entries win, so keep an explicitly granted value.
			key, _, _ := strings.Cut(entry, "=")
			if _, granted := grants[key]; !granted {
				env = append(env, entry)
			}
		}
	}
	if allowDocker {
		if dockerHost := strings.TrimSpace(envMap["DOCKER_HOST"]); strings.HasPrefix(dockerHost, "unix://") {
			env = append(env, "DOCKER_HOST="+dockerHost)
		}
	}
	return env
}

func environmentMap(env []string) map[string]string {
	result := make(map[string]string, len(env))
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			result[key] = value
		}
	}
	return result
}

func mergeEnvironmentMap(base, overlay map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(overlay))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range overlay {
		result[key] = value
	}
	return result
}

func (r *Runner) Run(cwd, command string) (string, string, error) {
	return r.RunContext(context.Background(), cwd, command)
}

func (r *Runner) RunContext(ctx context.Context, cwd, command string) (string, string, error) {
	return r.RunContextOptions(ctx, cwd, command, RunOptions{})
}

func (r *Runner) RunContextOptions(ctx context.Context, cwd, command string, opts RunOptions) (string, string, error) {
	r = NewRunner(r.config.ForWorkspace(cwd))
	r.config.ReadOnly = r.config.ReadOnly || opts.ReadOnly
	if ctx == nil {
		ctx = context.Background()
	}
	grantedEnv, err := resolveEnvironment(r.config.Environment)
	if err != nil {
		return "", "", err
	}
	if err := validateRequestEnvironment(opts.Env); err != nil {
		return "", "", err
	}
	grantedEnv = mergeEnvironmentMap(grantedEnv, opts.Env)
	tmpDir, err := makeSandboxTempDir("caretz-sandbox-*")
	if err != nil {
		return "", "", fmt.Errorf("creating sandbox temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	if err := os.Chmod(tmpDir, 0700); err != nil {
		return "", "", fmt.Errorf("securing sandbox temp directory: %w", err)
	}
	timeout := resolveTimeoutSeconds(opts)
	if override := os.Getenv("_SANDBOX_TIMEOUT"); override != "" {
		if parsed, parseErr := strconv.Atoi(override); parseErr == nil && parsed > 0 {
			timeout = resolveTimeoutSeconds(RunOptions{TimeoutSeconds: parsed})
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var proxy *sandboxNetworkController
	var endpoints proxyEndpoints
	if !r.config.UnrestrictedNetwork {
		proxy, endpoints.HTTP, endpoints.SOCKS, err = startSandboxTCPProxy(ctx, r.config.NetworkAllowlist)
		if err != nil {
			return "", "", err
		}
		if _, err := proxy.beginCommandNetworkPolicy(ctx, opts.NetworkAllowlist, opts.AuthorizeNetwork); err != nil {
			proxy.Close()
			return "", "", err
		}
		defer proxy.Close()
	}
	readableRoots := append(append([]string{}, r.config.ReadableRoots...), opts.ReadableRoots...)
	writableRoots := append(append([]string{}, r.config.WritableRoots...), opts.WritableRoots...)
	profile := generateSandboxProfileForRun(cwd, tmpDir, r.config.ExtraBinaries, r.config.UnrestrictedNetwork, r.config.AllowLoopback, r.config.AllowDocker, r.config.ReadOnly, readableRoots, writableRoots, r.config.HiddenPaths, endpoints, r.config.ReadOnlyPaths...)
	cmd := exec.CommandContext(ctx, "sandbox-exec", "-p", profile, "--", "/bin/bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = macOSSandboxEnvironment(cwd, tmpDir, endpoints, r.config.AllowDocker, grantedEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	stderrBuf := &bytes.Buffer{}
	stdoutBuf := &bytes.Buffer{}
	lw := &limitedWriter{w: stdoutBuf, maxBytes: sandboxMaxOutput}
	cmd.Stderr, cmd.Stdout = stderrBuf, lw
	oneShotLaunched.Store(true)
	if err = cmd.Start(); err == nil {
		trackSandboxedGroup(cmd.Process.Pid)
		err = cmd.Wait()
	}
	stderr := stderrBuf.String()
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", fmt.Errorf("command timed out after %d seconds", timeout)
	}
	if ctx.Err() == context.Canceled {
		return "", "", ctx.Err()
	}
	if err != nil && strings.Contains(stderr, "sandbox_apply: Operation not permitted") {
		err = fmt.Errorf("macOS refused to apply sandbox profile: %w", err)
	}
	if proxy != nil {
		if target := proxy.takeBlocked(); target != "" {
			return stdoutBuf.String(), stderr, &NetworkBlockedError{Target: target}
		}
	}
	stdout := stdoutBuf.String()
	if lw.truncated {
		stdout += "\n[output truncated - exceeded 1MB limit]"
	}
	return stdout, stderr, err
}

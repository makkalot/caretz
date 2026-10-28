//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// CheckSessionExecution checks the persistent Linux sandbox before an agent
// session starts and reports any compatibility downgrade.
func CheckSessionExecution(direct bool) (string, error) {
	if direct {
		return "", nil
	}
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return "", fmt.Errorf("session execution requires bubblewrap: bwrap was not found in PATH")
	}
	supported, version, err := inspectBubblewrapSessionExecution(func(args ...string) ([]byte, error) {
		return exec.Command(path, args...).CombinedOutput()
	})
	if err != nil {
		return "", err
	}
	if supported {
		return "", nil
	}
	return fmt.Sprintf("session execution is using %s without --disable-userns; nested user namespaces remain available and sandbox isolation is reduced", version), nil
}

func inspectBubblewrapSessionExecution(run func(...string) ([]byte, error)) (bool, string, error) {
	help, err := run("--help")
	if err != nil {
		return false, "", fmt.Errorf("inspect bubblewrap capabilities: %w", err)
	}
	if bytes.Contains(help, []byte("--disable-userns")) {
		return true, "", nil
	}
	version, _ := run("--version")
	found := string(bytes.TrimSpace(version))
	if found == "" {
		found = "an older bubblewrap version"
	}
	return false, found, nil
}

func prepareSessionSupervisor() (func(), error) {
	if os.Getenv(sessionDirectEnv) == "1" {
		return func() {}, nil
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return nil, fmt.Errorf("set non-dumpable: %w", err)
	}
	var bridges []io.Closer
	if socket := os.Getenv("_SANDBOX_PROXY_SOCKET"); socket != "" {
		listener, err := startSandboxProxyBridge(sandboxHTTPProxyAddr, socket)
		if err != nil {
			return nil, err
		}
		bridges = append(bridges, listener)
	}
	if socket := os.Getenv("_SANDBOX_SOCKS_SOCKET"); socket != "" {
		listener, err := startSandboxProxyBridge(sandboxSOCKSProxyAddr, socket)
		if err != nil {
			for _, bridge := range bridges {
				_ = bridge.Close()
			}
			return nil, err
		}
		bridges = append(bridges, listener)
	}
	if err := applySeccomp(); err != nil {
		for _, bridge := range bridges {
			_ = bridge.Close()
		}
		return nil, err
	}
	return func() {
		for _, bridge := range bridges {
			_ = bridge.Close()
		}
	}, nil
}

func startPlatformReaper(known func(int) bool) func() {
	if os.Getenv(sessionDirectEnv) == "1" {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				entries, _ := filepath.Glob("/proc/[0-9]*/status")
				for _, path := range entries {
					data, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					text := string(data)
					if !strings.Contains(text, "\nPPid:\t1\n") || !strings.Contains(text, "\nState:\tZ") {
						continue
					}
					pid, err := strconv.Atoi(filepath.Base(filepath.Dir(path)))
					if err == nil && !known(pid) {
						var status syscall.WaitStatus
						_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
					}
				}
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

func (r *Runner) startSessionSupervisor(cwd string, direct bool, tmpDir string) (*sessionSupervisor, error) {
	baseConfig := r.config
	r = NewRunner(baseConfig.ForWorkspace(cwd))
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	var proxy *linuxSandboxProxy
	var masks []hiddenPathMask
	grantedEnv := map[string]string{}
	if !direct {
		grantedEnv, err = resolveEnvironment(r.config.Environment)
		if err != nil {
			return nil, err
		}
	}
	if direct {
		cmd = exec.Command(self)
	} else {
		if !r.config.UnrestrictedNetwork {
			proxy, err = startSandboxProxy(context.Background(), r.config.NetworkAllowlist)
			if err != nil {
				return nil, err
			}
		}
		proxyDir := ""
		if proxy != nil {
			proxyDir = proxy.socketDir
		}
		bwrapPath, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("session execution requires bubblewrap: bwrap was not found in PATH")
		}
		disableUserNS, _, err := inspectBubblewrapSessionExecution(func(args ...string) ([]byte, error) {
			return exec.Command(bwrapPath, args...).CombinedOutput()
		})
		if err != nil {
			return nil, err
		}
		args := bwrapArgs(cwd, self, r.config.UnrestrictedNetwork, r.config.ExtraBinaries, r.config.ReadOnly, r.config.ReadableRoots, r.config.WritableRoots, proxyDir)
		if !r.config.AllowDocker {
			args = removeBind(args, "/run")
		}
		masks, err = discoverFilesystemMasks(r.config, cwd)
		if err != nil {
			if proxy != nil {
				proxy.Close()
			}
			return nil, err
		}
		args = appendDiscoveredHiddenPathMasks(args, masks)
		args = appendGPUDevices(args, r.config.AllowGPU)
		args = append(sessionBwrapPrefix(disableUserNS), args...)
		args = append(args, self)
		cmd = exec.Command(bwrapPath, args...)
	}
	cmd.Dir = cwd
	allowDocker := r.config.AllowDocker || direct
	env := map[string]string{sessionSupervisorEnv: "1"}
	if allowDocker {
		env[sessionAllowDockerEnv] = "1"
	}
	if direct {
		env["TMPDIR"] = tmpDir
		env[sessionDirectEnv] = "1"
	} else {
		env["TMPDIR"] = "/tmp"
	}
	if proxy != nil {
		env["_SANDBOX_PROXY_SOCKET"] = proxy.httpSocketPath
		env["_SANDBOX_SOCKS_SOCKET"] = proxy.socksSocketPath
	}
	baseEnv := sanitizeSessionEnv(os.Environ(), allowDocker)
	if !direct {
		proxySocket, socksSocket := "", ""
		if proxy != nil {
			proxySocket, socksSocket = proxy.httpSocketPath, proxy.socksSocketPath
		}
		baseEnv = sandboxChildEnv("", baseEnv, os.Stat, r.config.WritableRoots, r.config.AllowDocker, proxySocket, socksSocket)
		baseEnv = sanitizeSessionEnv(baseEnv, allowDocker)
		baseEnv = mergeEnv(baseEnv, grantedEnv)
	}
	cmd.Env = mergeEnv(baseEnv, env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		if proxy != nil {
			proxy.Close()
		}
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		if proxy != nil {
			proxy.Close()
		}
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		if proxy != nil {
			proxy.Close()
		}
		return nil, err
	}
	cleanup := func() {
		if proxy != nil {
			proxy.Close()
		}
	}
	var recorder *sandboxNetworkController
	if proxy != nil {
		recorder = proxy.sandboxNetworkController
	}
	supervisor := &sessionSupervisor{cmd: cmd, in: stdin, out: stdout, cleanup: cleanup, proxy: recorder}
	if !direct {
		supervisor.refresh = func() (bool, error) {
			current, err := discoverFilesystemMasks(baseConfig.ForWorkspace(cwd), cwd)
			return !slices.Equal(masks, current), err
		}
	}
	return supervisor, nil
}

func sessionBwrapPrefix(disableUserNS bool) []string {
	args := []string{"--unshare-user", "--unshare-pid", "--unshare-cgroup", "--as-pid-1", "--die-with-parent", "--new-session", "--cap-drop", "ALL"}
	if disableUserNS {
		args = append(args, "--disable-userns")
	}
	return args
}

func removeBind(args []string, path string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if i+2 < len(args) && (args[i] == "--ro-bind" || args[i] == "--bind") && args[i+1] == path && args[i+2] == path {
			i += 2
			continue
		}
		out = append(out, args[i])
	}
	return out
}

type hiddenPathMask struct {
	path      string
	directory bool
	readOnly  bool
}

// Discovery never creates host paths or overlays a visible parent directory.
// Missing paths are reconsidered before the next persistent command.
func discoverHiddenPathMasks(paths []string, cwd string, readableRoots, writableRoots []string) ([]hiddenPathMask, error) {
	var masks []hiddenPathMask
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("normalizing hidden path %s: %w", path, err)
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolving hidden path %s: %w", abs, err)
		}
		info, err := os.Stat(resolved)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspecting hidden path %s: %w", resolved, err)
		}
		if !sandboxPathMapped(abs, cwd, readableRoots, writableRoots) && !sandboxPathMapped(resolved, cwd, readableRoots, writableRoots) {
			continue
		}
		masks = append(masks, hiddenPathMask{path: resolved, directory: info.IsDir()})
	}
	sort.Slice(masks, func(i, j int) bool { return masks[i].path < masks[j].path })
	var filtered []hiddenPathMask
	for _, candidate := range masks {
		covered := false
		for _, existing := range filtered {
			if candidate.path == existing.path || existing.directory && pathWithin(candidate.path, existing.path) {
				covered = true
				break
			}
		}
		if !covered {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}

func appendDiscoveredHiddenPathMasks(args []string, masks []hiddenPathMask) []string {
	for _, mask := range masks {
		if mask.readOnly {
			args = append(args, "--ro-bind", mask.path, mask.path)
		} else if mask.directory {
			args = append(args, "--perms", "000", "--tmpfs", mask.path)
		} else {
			args = append(args, "--ro-bind", "/dev/null", mask.path)
		}
	}
	return args
}

func discoverFilesystemMasks(config Config, cwd string) ([]hiddenPathMask, error) {
	readOnly, err := discoverHiddenPathMasks(config.ReadOnlyPaths, cwd, config.ReadableRoots, config.WritableRoots)
	if err != nil {
		return nil, err
	}
	for i := range readOnly {
		readOnly[i].readOnly = true
	}
	hidden, err := discoverHiddenPathMasks(config.HiddenPaths, cwd, config.ReadableRoots, config.WritableRoots)
	if err != nil {
		return nil, err
	}
	return append(readOnly, hidden...), nil
}

func appendHiddenPathMasks(args []string, paths []string, cwd string, _ bool, readableRoots, writableRoots []string) ([]string, error) {
	masks, err := discoverHiddenPathMasks(paths, cwd, readableRoots, writableRoots)
	if err != nil {
		return nil, err
	}
	return appendDiscoveredHiddenPathMasks(args, masks), nil
}

func sandboxPathMapped(path, cwd string, readableRoots, writableRoots []string) bool {
	if pathWithin(path, cwd) || withinAnyRoot(path, readableRoots) || withinAnyRoot(path, writableRoots) {
		return true
	}
	return withinAnyRoot(path, defaultReadOnlyBinds)
}

func withinAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithin(path, root) {
			return true
		}
	}
	return false
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

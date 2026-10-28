//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// sandboxTempRoot is empty on Linux: commands get a private /tmp tmpfs, so
// the host temp directory length does not limit socket paths.
const sandboxTempRoot = ""

// init checks for sandbox child environment at package initialization.
// This ensures that sandbox restrictions are applied before main() runs
// in both production binaries and test binaries that re-execute themselves.
// This is the single sentinel check point; no other code should check
// the _SANDBOX_CHILD env var.
func init() {
	if os.Getenv("_SANDBOX_CHILD") == "1" {
		runSandboxChild()
	}
}

var defaultReadOnlyBinds = []string{
	"/sys",
	"/usr",
	"/bin",
	"/lib",
	"/etc",
	"/run",
	"/var/log",
	"/lib64",
	"/sbin",
	"/opt",
	"/usr/local",
}

var (
	containerStat     = os.Stat
	containerReadFile = os.ReadFile
	// hostVarRunLink reports the host's /var/run -> /run link target.
	hostVarRunLink = func() string {
		target, err := os.Readlink("/var/run")
		if err != nil || varRunLinkTarget(target) != "/run" {
			return ""
		}
		return target
	}
	hostDockerSockets = func() []string {
		sockets, _ := filepath.Glob("/run/user/*/docker.sock")
		if _, err := os.Lstat("/run/docker.sock"); err == nil {
			sockets = append([]string{"/run/docker.sock"}, sockets...)
		}
		return sockets
	}
	// hostControlSockets finds daemon sockets that would let a sandboxed
	// command act on the host. A read-only bind does not stop connect(), so
	// each is hidden: the user bus and user systemd run arbitrary commands
	// (systemd-run --user), container engines start privileged containers, and
	// the system bus reaches polkit actions such as reboot.
	hostControlSockets = func() []string {
		var sockets []string
		for _, pattern := range []string{
			"/run/user/*/bus",
			"/run/user/*/systemd/private",
			"/run/user/*/podman/podman.sock",
			"/run/podman/podman.sock",
			"/run/containerd/containerd.sock",
			"/run/dbus/system_bus_socket",
			"/run/systemd/private",
		} {
			matches, _ := filepath.Glob(pattern)
			sockets = append(sockets, matches...)
		}
		return sockets
	}
	// hostGPUDevices finds the GPU device nodes of NVIDIA desktop drivers,
	// Jetson (Tegra) boards, DRM, and AMD ROCm.
	hostGPUDevices = func() []string {
		var devices []string
		for _, pattern := range []string{"/dev/nvidia*", "/dev/nvidia-caps", "/dev/nvmap", "/dev/nvhost-*", "/dev/nvgpu", "/dev/dri", "/dev/kfd"} {
			matches, _ := filepath.Glob(pattern)
			devices = append(devices, matches...)
		}
		return devices
	}
	hostSystemBusSockets = []string{"/run/dbus/system_bus_socket"}
	currentEUID          = os.Geteuid
)

func varRunLinkTarget(target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Clean(filepath.Join("/var", target))
}

// maskHostSockets hides the host-control sockets under the bound /run, and
// the Docker daemon sockets unless Docker access was granted, as session
// execution does by dropping /run entirely. allowSystemBus leaves the system
// bus reachable for a non-root user, whose state-changing calls polkit still
// authorizes; root is never checked by polkit, so its bus stays hidden.
func maskHostSockets(args []string, allowDocker, allowSystemBus bool) []string {
	sockets := hostControlSockets()
	if !allowDocker {
		sockets = append(hostDockerSockets(), sockets...)
	}
	exposeSystemBus := allowSystemBus && currentEUID() != 0
	for _, socket := range sockets {
		if exposeSystemBus && slices.Contains(hostSystemBusSockets, socket) {
			continue
		}
		args = append(args, "--ro-bind", "/dev/null", socket)
	}
	return args
}

// appendGPUDevices binds the host's GPU device nodes into the sandbox's
// minimal /dev. Device permissions and the user's video and render groups
// still decide who may open them.
func appendGPUDevices(args []string, allowGPU bool) []string {
	if !allowGPU {
		return args
	}
	for _, device := range hostGPUDevices() {
		args = append(args, "--dev-bind", device, device)
	}
	return args
}

func pathCoveredByBind(path string, binds []string) bool {
	for _, bind := range binds {
		if path == bind || strings.HasPrefix(path, bind+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func appendReadOnlyRootBinds(args []string, readOnlyRoots []string, coveredRoots []string) ([]string, []string) {
	for _, root := range readOnlyRoots {
		if root == "" || pathCoveredByBind(root, coveredRoots) {
			continue
		}
		args = append(args, "--ro-bind", root, root)
		coveredRoots = append(coveredRoots, root)
	}
	return args, coveredRoots
}

// bwrapArgs returns bubblewrap arguments to set up a sandboxed environment.
// binaryPath is the full path to the sandboxed binary (for read-only access).
func bwrapArgs(workDir, binaryPath string, unrestrictedNetwork bool, extraBinaries []string, readOnly bool, readableRoots []string, writableRoots []string, proxySocketDir string) []string {
	return bwrapArgsForContainerMode(workDir, binaryPath, unrestrictedNetwork, extraBinaries, readOnly, readableRoots, writableRoots, proxySocketDir, runningInContainer())
}

func bwrapArgsForContainerMode(workDir, binaryPath string, unrestrictedNetwork bool, extraBinaries []string, readOnly bool, readableRoots []string, writableRoots []string, proxySocketDir string, inContainer bool) []string {
	binaryDir := filepath.Dir(binaryPath)

	args := []string{
		// PID namespace is left shared for process visibility.
		"--unshare-ipc",
		"--unshare-uts",
	}
	if !inContainer {
		args = append(args, "--unshare-cgroup")
	}

	if !unrestrictedNetwork {
		args = append(args, "--unshare-net")
	}

	args = append(args, []string{
		"--dev", "/dev",
		"--proc", "/proc",
	}...)
	if inContainer {
		args = append(args, "--ro-bind-try", "/sys", "/sys")
	} else {
		args = append(args, "--ro-bind", "/sys", "/sys")
	}
	args = append(args, []string{
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/etc", "/etc",
		"--ro-bind", "/run", "/run",
		"--ro-bind", "/var/log", "/var/log",

		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/sbin", "/sbin",
		"--ro-bind-try", "/opt", "/opt",
		"--ro-bind-try", "/usr/local", "/usr/local",

		"--tmpfs", "/tmp",
	}...)
	// Daemon clients such as tailscale and docker look for sockets under
	// /var/run. Recreate the host's /var/run -> /run link.
	if target := hostVarRunLink(); target != "" {
		args = append(args, "--symlink", target, "/var/run")
	}

	coveredBinds := append([]string{workDir}, defaultReadOnlyBinds...)
	if readOnly {
		args = append(args, "--ro-bind", workDir, workDir)
		readOnlyRoots := append([]string{}, readableRoots...)
		readOnlyRoots = append(readOnlyRoots, writableRoots...)
		args, coveredBinds = appendReadOnlyRootBinds(args, readOnlyRoots, coveredBinds)
	} else {
		args = append(args, "--bind", workDir, workDir)
		for _, root := range writableRoots {
			if root == "" || root == workDir {
				continue
			}
			args = append(args, "--bind", root, root)
			coveredBinds = append(coveredBinds, root)
		}
		args, coveredBinds = appendReadOnlyRootBinds(args, readableRoots, coveredBinds)
	}

	args = append(args, []string{
		"--setenv", "HOME", workDir,
		"--setenv", "GOCACHE", filepath.Join(workDir, ".caretz", "scratch", "go-build"),
		"--setenv", "GOPATH", filepath.Join(workDir, ".caretz", "scratch", "go"),
	}...)

	if proxySocketDir != "" {
		args = append(args, "--bind", proxySocketDir, proxySocketDir)
	}

	// Add read-only bind for the binary's directory if it's not already covered
	// This ensures the binary is accessible from within the sandbox
	if binaryDir != "/" && binaryDir != workDir {
		args = append(args, "--ro-bind-try", binaryDir, binaryDir)
	} else if binaryDir == "/" && workDir != "/" {
		// Special case: binary is in rootdirectory (e.g., /caretz-test-linux in Docker tests)
		// We can't bind-mount / itself, but we can bind-mount the specific binary path
		// Use --ro-bind-try to avoid errors if the path doesn't exist
		args = append(args, "--ro-bind-try", binaryPath, binaryPath)
	}

	coveredBinds = append(coveredBinds, binaryDir)
	for _, extra := range extraBinaries {
		if extra == "" || pathCoveredByBind(extra, coveredBinds) {
			continue
		}
		args = append(args, "--ro-bind-try", extra, extra)
	}

	return args
}

// Run executes command in a sandboxed environment.
// Spawns bwrap which re-executes the binary with sandbox child env vars set.
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
	// Check for timeout override (used for testing)
	timeout := resolveTimeoutSeconds(opts)
	if override := os.Getenv("_SANDBOX_TIMEOUT"); override != "" {
		if parsed, err := strconv.Atoi(override); err == nil && parsed > 0 {
			timeout = resolveTimeoutSeconds(RunOptions{TimeoutSeconds: parsed})
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	grantedEnv, err := resolveEnvironment(r.config.Environment)
	if err != nil {
		return "", "", err
	}
	if err := validateRequestEnvironment(opts.Env); err != nil {
		return "", "", err
	}

	// Resolve /proc/self/exe to the actual binary path before passing to bwrap
	// bwrap doesn't resolve symlinks, so we need to do it in the parent process
	selfExe, readErr := os.Readlink("/proc/self/exe")
	if readErr != nil {
		return "", "", fmt.Errorf("reading /proc/self/exe: %w", readErr)
	}

	var proxy *linuxSandboxProxy
	if !r.config.UnrestrictedNetwork {
		var err error
		proxy, err = startSandboxProxy(ctx, r.config.NetworkAllowlist)
		if err != nil {
			return "", "", err
		}
		if _, err := proxy.beginCommandNetworkPolicy(ctx, opts.NetworkAllowlist, opts.AuthorizeNetwork); err != nil {
			proxy.Close()
			return "", "", err
		}
		defer proxy.Close()
	}

	proxySocketDir := ""
	if proxy != nil {
		proxySocketDir = proxy.socketDir
	}
	readableRoots := append(append([]string{}, r.config.ReadableRoots...), opts.ReadableRoots...)
	writableRoots := append(append([]string{}, r.config.WritableRoots...), opts.WritableRoots...)
	args := bwrapArgs(cwd, selfExe, r.config.UnrestrictedNetwork, r.config.ExtraBinaries, r.config.ReadOnly, readableRoots, writableRoots, proxySocketDir)
	maskConfig := r.config
	maskConfig.ReadableRoots, maskConfig.WritableRoots = readableRoots, writableRoots
	masks, err := discoverFilesystemMasks(maskConfig, cwd)
	if err != nil {
		return "", "", err
	}
	args = appendDiscoveredHiddenPathMasks(args, masks)
	args = maskHostSockets(args, r.config.AllowDocker, r.config.AllowSystemBus)
	args = appendGPUDevices(args, r.config.AllowGPU)
	args = append(args, selfExe)
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	cmd.Dir = cwd
	proxySocketPath, socksSocketPath := "", ""
	if proxy != nil {
		proxySocketPath, socksSocketPath = proxy.httpSocketPath, proxy.socksSocketPath
	}
	cmd.Env = mergeEnv(sandboxChildEnv(command, os.Environ(), os.Stat, writableRoots, r.config.AllowDocker, proxySocketPath, socksSocketPath), grantedEnv)
	cmd.Env = mergeEnv(cmd.Env, opts.Env)
	cmd.Env = mergeEnv(cmd.Env, sandboxOwnerEnvironment())
	oneShotLaunched.Store(true)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // child in its own process group
	}

	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf
	stdoutBuf := &bytes.Buffer{}
	lw := &limitedWriter{w: stdoutBuf, maxBytes: sandboxMaxOutput}
	cmd.Stdout = lw

	// Kill entire process group on context timeout (not just direct child)
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second

	// SIGINT forwarding: if we get Ctrl+C, kill the child's process group
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Signal(syscall.SIGINT))
	defer signal.Stop(sigChan)

	err = cmd.Start()
	if err != nil {
		return "", "", fmt.Errorf("starting sandbox child: %w", err)
	}

	// Goroutine to forward SIGINT to child's process group
	go func() {
		for range sigChan {
			// SIGKILL the entire process group — SIGINT may be caught/ignored
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	err = cmd.Wait()

	if ctx.Err() == context.DeadlineExceeded {
		return "", "", fmt.Errorf("command timed out after %d seconds", timeout)
	}
	if ctx.Err() == context.Canceled {
		return "", "", ctx.Err()
	}

	stdout := stdoutBuf.String()
	if lw.truncated {
		stdout += "\n[output truncated - exceeded 1MB limit]"
	}
	if proxy != nil {
		if target := proxy.takeBlocked(); target != "" {
			return stdout, stderrBuf.String(), &NetworkBlockedError{Target: target}
		}
	}
	return stdout, stderrBuf.String(), err
}

// runSandboxChild applies sandbox restrictions and execs into bash.
// Called only when _SANDBOX_CHILD=1 env var is set. Does not return on success.
func runSandboxChild() {
	cmd := os.Getenv("_SANDBOX_CMD")

	var proxyBridge, socksBridge net.Listener
	if proxySocket := os.Getenv("_SANDBOX_PROXY_SOCKET"); proxySocket != "" {
		var err error
		proxyBridge, err = startSandboxProxyBridge(sandboxHTTPProxyAddr, proxySocket)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "proxy bridge error: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = proxyBridge.Close() }()
	}
	if proxySocket := os.Getenv("_SANDBOX_SOCKS_SOCKET"); proxySocket != "" {
		var err error
		socksBridge, err = startSandboxProxyBridge(sandboxSOCKSProxyAddr, proxySocket)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "SOCKS bridge error: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = socksBridge.Close() }()
	}

	if seccompEnabled() {
		if err := applySeccomp(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "seccomp error: %v\n", err)
			os.Exit(1)
		}
	}

	shell := exec.Command("/bin/bash", "-c", cmd)
	shell.Env = sanitizeEnv(os.Environ())
	shell.Stdin = os.Stdin
	shell.Stdout = os.Stdout
	shell.Stderr = os.Stderr
	if err := shell.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				if status.Signaled() {
					os.Exit(128 + int(status.Signal()))
				}
				os.Exit(status.ExitStatus())
			}
		}
		_, _ = fmt.Fprintf(os.Stderr, "exec error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func seccompEnabled() bool {
	return !runningInContainer()
}

func runningInContainer() bool {
	if _, err := containerStat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := containerStat("/run/.containerenv"); err == nil {
		return true
	}

	containerMarkers := []string{
		"docker",
		"containerd",
		"kubepods",
		"podman",
		"/containers/",
	}
	for _, path := range []string{"/proc/1/cgroup", "/proc/self/cgroup"} {
		if fileContainsAny(path, containerMarkers) {
			return true
		}
	}

	// On cgroup v2 Docker commonly reports "0::/" in cgroup files. The
	// mount table still exposes Docker-managed binds for container-local files.
	return fileContainsAny("/proc/self/mountinfo", []string{
		"/docker/containers/",
		"/containerd/",
		"/kubepods/",
		"/podman/",
	})
}

func fileContainsAny(path string, needles []string) bool {
	data, err := containerReadFile(path)
	if err != nil {
		return false
	}
	haystack := string(data)
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// applySeccomp builds and loads a seccomp BPF filter blocking dangerous syscalls.
func applySeccomp() error {
	// Set PR_SET_NO_NEW_PRIVS first (required before loading seccomp filters
	// as unprivileged user without CAP_SYS_ADMIN)
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl PR_SET_NO_NEW_PRIVS: %w", err)
	}

	filter := buildSeccompFilter(blockedSyscalls())
	prog := unix.SockFprog{
		Len:    uint16(len(filter)),
		Filter: &filter[0],
	}
	result, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER,
		unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog)))
	if errno != 0 {
		return fmt.Errorf("seccomp SECCOMP_FILTER_FLAG_TSYNC: %w", errno)
	}
	if result != 0 {
		return fmt.Errorf("seccomp filter synchronization failed on thread %d", result)
	}
	return nil
}

// blockedSyscalls returns the list of syscall numbers to block.
func blockedSyscalls() []uint32 {
	base := []uint32{
		// Kernel/system operations
		syscall.SYS_REBOOT,
		syscall.SYS_INIT_MODULE,
		unix.SYS_FINIT_MODULE,
		syscall.SYS_DELETE_MODULE,
		syscall.SYS_KEXEC_LOAD,
		unix.SYS_KEXEC_FILE_LOAD,
		syscall.SYS_SWAPON,
		syscall.SYS_SWAPOFF,

		// Mount / namespace
		syscall.SYS_MOUNT,
		syscall.SYS_UMOUNT2,
		syscall.SYS_PIVOT_ROOT,
		syscall.SYS_CHROOT,
		syscall.SYS_UNSHARE,
		unix.SYS_SETNS,

		// UID/GID
		syscall.SYS_SETUID,
		syscall.SYS_SETGID,
		syscall.SYS_SETREUID,
		syscall.SYS_SETREGID,
		syscall.SYS_SETRESUID,
		syscall.SYS_SETRESGID,
		syscall.SYS_SETGROUPS,
		syscall.SYS_SETFSUID,
		syscall.SYS_SETFSGID,

		// Hostname / time
		syscall.SYS_SETHOSTNAME,
		syscall.SYS_SETDOMAINNAME,
		syscall.SYS_CLOCK_SETTIME,
		syscall.SYS_SETTIMEOFDAY,
		syscall.SYS_ADJTIMEX,
		unix.SYS_CLOCK_ADJTIME,

		// Scheduling
		syscall.SYS_SCHED_SETSCHEDULER,
		syscall.SYS_SCHED_SETPARAM,
		syscall.SYS_SCHED_SETAFFINITY,
		syscall.SYS_SETPRIORITY,
		unix.SYS_IOPRIO_SET,

		// Device / I/O ports
		syscall.SYS_MKNODAT,

		// Misc dangerous
		syscall.SYS_ACCT,
		unix.SYS_QUOTACTL,
		unix.SYS_QUOTACTL_FD,
		syscall.SYS_ADD_KEY,
		syscall.SYS_REQUEST_KEY,
		syscall.SYS_KEYCTL,
		unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_PROCESS_VM_READV,
		syscall.SYS_PTRACE,
		unix.SYS_PIDFD_GETFD,
		syscall.SYS_PERSONALITY,
		unix.SYS_USERFAULTFD,
	}

	switch runtime.GOARCH {
	case "amd64":
		// These syscalls only exist on x86_64, not on arm64.
		// SYS_MKNOD=133, SYS_IOPL=172, SYS_IOPERM=173 on amd64.
		base = append(base, 133, 172, 173)
	}

	return base
}

// buildSeccompFilter constructs a BPF filter that blocks specified syscalls.
func buildSeccompFilter(blocked []uint32) []unix.SockFilter {
	var filter []unix.SockFilter

	// Load syscall number: seccomp_data.nr is at offset 0
	filter = append(filter,
		bpfStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, 0),
	)

	for _, nr := range blocked {
		// if syscall == nr, return ERRNO(EPERM)
		filter = append(filter,
			bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, nr, 0, 1),
			bpfStmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ERRNO|(1&unix.SECCOMP_RET_DATA)),
		)
	}

	// Default: allow
	filter = append(filter,
		bpfStmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW),
	)
	return filter
}

// bpfStmt creates a simple BPF statement instruction.
func bpfStmt(code uint16, k uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, K: k}
}

// bpfJump creates a BPF jump instruction.
func bpfJump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// sanitizeEnv strips environment variables that could bypass sandbox or inject behavior.
func sanitizeEnv(env []string) []string {
	blockedPrefixes := []string{"LD_", "DYLD_", "MALLOC_", "_SANDBOX_"}
	blockedNames := map[string]bool{
		"LD_PRELOAD":      true,
		"LD_LIBRARY_PATH": true,
		"GLIBC_TUNABLES":  true,
		"BASH_ENV":        true,
		"ENV":             true,
		"CDPATH":          true,
		"IFS":             true,
	}

	var result []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")

		if blockedNames[key] {
			continue
		}

		skip := false
		for _, prefix := range blockedPrefixes {
			if strings.HasPrefix(e, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		result = append(result, e)
	}
	return result
}

func sandboxChildEnv(command string, parentEnv []string, stat func(string) (os.FileInfo, error), writableRoots []string, allowDocker bool, httpProxySocket, socksProxySocket string) []string {
	envMap := map[string]string{}
	for _, env := range parentEnv {
		key, value, ok := strings.Cut(env, "=")
		if ok {
			envMap[key] = value
		}
	}

	result := []string{
		"_SANDBOX_CHILD=1",
		"_SANDBOX_CMD=" + command,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + envMap["HOME"],
		"TERM=" + envMap["TERM"],
		"LANG=" + envMap["LANG"],
	}

	if allowDocker {
		dockerHost := dockerHostEnv(envMap, stat, writableRoots)
		if dockerHost != "" {
			result = append(result, "DOCKER_HOST="+dockerHost)
		}
	}
	if httpProxySocket != "" {
		result = append(result, "_SANDBOX_PROXY_SOCKET="+httpProxySocket)
		socksAddr := ""
		if socksProxySocket != "" {
			result = append(result, "_SANDBOX_SOCKS_SOCKET="+socksProxySocket)
			socksAddr = sandboxSOCKSProxyAddr
		}
		result = append(result, proxyEnv(sandboxHTTPProxyAddr, socksAddr, true)...)
		if socksAddr != "" {
			result = append(result, sshProxyEnv()...)
		}
	}

	// Propagate LC_* vars from parent env.
	for _, env := range sanitizeEnv(parentEnv) {
		if strings.HasPrefix(env, "LC_") {
			result = append(result, env)
		}
	}
	return result
}

func dockerHostEnv(envMap map[string]string, stat func(string) (os.FileInfo, error), writableRoots []string) string {
	if dockerHost := strings.TrimSpace(envMap["DOCKER_HOST"]); dockerHost != "" {
		return dockerHost
	}
	if hasRoot(writableRoots, "/run") {
		return "unix:///run/docker.sock"
	}
	if stat == nil {
		stat = os.Stat
	}
	if info, err := stat("/run/docker.sock"); err == nil && !info.IsDir() {
		return "unix:///run/docker.sock"
	}
	return ""
}

func hasRoot(roots []string, want string) bool {
	for _, root := range roots {
		if root == want {
			return true
		}
	}
	return false
}

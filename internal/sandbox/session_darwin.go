//go:build darwin

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func CheckSessionExecution(bool) (string, error) { return "", nil }

func prepareSessionSupervisor() (func(), error) { return func() {}, nil }
func startPlatformReaper(func(int) bool) func() { return func() {} }

// markStrayFDCLOEXEC marks any open descriptors beyond 0-2 as close-on-exec
// so the spawned session supervisor inherits only the intended pipes.
func markStrayFDCLOEXEC() {
	for fd := 3; fd < 64; fd++ {
		if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFD), uintptr(syscall.FD_CLOEXEC)); errno != 0 {
			continue
		}
	}
}

func (r *Runner) startSessionSupervisor(cwd string, direct bool, tmpDir string) (*sessionSupervisor, error) {
	r = NewRunner(r.config.ForWorkspace(cwd))
	markStrayFDCLOEXEC()
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	var proxy *sandboxNetworkController
	var endpoints proxyEndpoints
	grantedEnv := map[string]string{}
	if direct {
		cmd = exec.Command(self)
	} else {
		grantedEnv, err = resolveEnvironment(r.config.Environment)
		if err != nil {
			return nil, err
		}
		if !r.config.UnrestrictedNetwork {
			proxy, endpoints.HTTP, endpoints.SOCKS, err = startSandboxTCPProxy(context.Background(), r.config.NetworkAllowlist)
			if err != nil {
				return nil, err
			}
		}
		profile := generateSandboxProfileForRun(cwd, tmpDir, r.config.ExtraBinaries, r.config.UnrestrictedNetwork, r.config.AllowLoopback, r.config.AllowDocker, r.config.ReadOnly, r.config.ReadableRoots, r.config.WritableRoots, r.config.HiddenPaths, endpoints, r.config.ReadOnlyPaths...)
		cmd = exec.Command("sandbox-exec", "-p", profile, "--", self)
	}
	cmd.Dir = cwd
	allowDocker := direct || r.config.AllowDocker
	env := map[string]string{sessionSupervisorEnv: "1", "TMPDIR": tmpDir, "HOME": cwd}
	if allowDocker {
		env[sessionAllowDockerEnv] = "1"
	}
	if direct {
		cmd.Env = mergeEnv(sanitizeSessionEnv(os.Environ(), allowDocker), env)
	} else {
		cmd.Env = mergeEnv(macOSSandboxEnvironment(cwd, tmpDir, endpoints, r.config.AllowDocker, grantedEnv), env)
	}
	if direct {
		cmd.Env = mergeEnv(cmd.Env, map[string]string{sessionDirectEnv: "1"})
	}
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
	if err := cmd.Start(); err != nil {
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
	return &sessionSupervisor{cmd: cmd, in: stdin, out: stdout, cleanup: cleanup, proxy: proxy}, nil
}

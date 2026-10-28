package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/makkalot/caretz/internal/turnscope"
)

const (
	sessionSupervisorEnv  = "_CARETZ_SESSION_SUPERVISOR"
	sessionDirectEnv      = "_CARETZ_SESSION_DIRECT"
	sessionAllowDockerEnv = "_CARETZ_SESSION_ALLOW_DOCKER"
	maxSessionFrame       = 3 * 1024 * 1024
	maxBackgroundJobs     = 8
	backgroundLogLimit    = 1 * 1024 * 1024
)

type sessionRequest struct {
	ID             uint64            `json:"id"`
	CancelID       uint64            `json:"cancel_id,omitempty"`
	Command        string            `json:"command"`
	CWD            string            `json:"cwd"`
	Env            map[string]string `json:"env,omitempty"`
	Background     bool              `json:"background,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type sessionResponse struct {
	ID       uint64 `json:"id"`
	Type     string `json:"type"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code"`
	PID      int    `json:"pid,omitempty"`
}

type tailLogWriter struct {
	mu    sync.Mutex
	file  *os.File
	limit int
}

func (w *tailLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(p)
	if len(p) >= w.limit {
		p = p[len(p)-w.limit:]
		if err := w.file.Truncate(0); err != nil {
			return 0, err
		}
		if _, err := w.file.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
	} else {
		info, err := w.file.Stat()
		if err != nil {
			return 0, err
		}
		overflow := int(info.Size()) + len(p) - w.limit
		if overflow > 0 {
			keep := int(info.Size()) - overflow
			data := make([]byte, keep)
			if keep > 0 {
				if _, err := w.file.ReadAt(data, int64(overflow)); err != nil {
					return 0, err
				}
			}
			if err := w.file.Truncate(0); err != nil {
				return 0, err
			}
			if _, err := w.file.Seek(0, io.SeekStart); err != nil {
				return 0, err
			}
			if _, err := w.file.Write(data); err != nil {
				return 0, err
			}
		}
	}
	if _, err := w.file.Seek(0, io.SeekEnd); err != nil {
		return 0, err
	}
	_, err := w.file.Write(p)
	return original, err
}

// Session is one persistent command supervisor. Calls use fresh bash
// processes while sharing its process namespace, network namespace and /tmp.
type Session struct {
	runner *Runner
	direct bool

	mu         sync.Mutex
	cmd        *exec.Cmd
	waitDone   chan error
	in         io.WriteCloser
	out        *bufio.Reader
	connection *sessionConnection
	failed     bool
	recreated  bool
	tmpDir     string
	cleanup    func()
	proxy      *sandboxNetworkController
	closed     bool
	refresh    func() (bool, error)
	workspace  string
}

// sessionSupervisor groups the process and resources created together by the
// platform launcher. Keeping them in one value makes ownership and cleanup
// explicit at the platform boundary.
type sessionSupervisor struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     io.ReadCloser
	cleanup func()
	proxy   *sandboxNetworkController
	refresh func() (bool, error)
}

func (r *Runner) NewSession(direct bool) *Session { return &Session{runner: r, direct: direct} }

// AddFilesystemRoots widens the filesystem policy for subsequent commands.
// Sandboxes cannot be widened in place, so an active supervisor is stopped
// and recreated lazily by the next Run call.
func (s *Session) AddFilesystemRoots(readable, writable []string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, fmt.Errorf("session execution supervisor is closed")
	}
	config := s.runner.config
	config.ReadableRoots = append([]string(nil), config.ReadableRoots...)
	config.WritableRoots = append([]string(nil), config.WritableRoots...)
	changed := appendMissingRoots(&config.ReadableRoots, readable)
	changed = appendMissingRoots(&config.WritableRoots, writable) || changed
	if !changed {
		return false, nil
	}
	wasRunning := s.cmd != nil
	if wasRunning {
		if err := s.stopLocked(); err != nil {
			return false, err
		}
		s.failed = true
	}
	s.runner = NewRunner(config)
	return wasRunning, nil
}

func appendMissingRoots(dst *[]string, roots []string) bool {
	changed := false
	for _, root := range roots {
		if root == "" || slices.Contains(*dst, root) {
			continue
		}
		*dst = append(*dst, root)
		changed = true
	}
	return changed
}

func (s *Session) Run(ctx context.Context, cwd, command string, opts RunOptions) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if opts.ReadOnly && (s.direct || !s.runner.config.ReadOnly) {
		return "", "", fmt.Errorf("read-only commands cannot run in a writable sandbox session")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if !s.direct {
		if err := validateRequestEnvironment(opts.Env); err != nil {
			return "", "", err
		}
	}
	if err := s.ensureStarted(cwd); err != nil {
		return "", "", err
	}
	if s.proxy != nil {
		policyCtx := ctx
		if !opts.Background {
			var cancel context.CancelFunc
			policyCtx, cancel = context.WithTimeout(ctx, time.Duration(resolveTimeoutSeconds(opts))*time.Second)
			defer cancel()
		}
		proxy := s.proxy
		commandPolicy, err := proxy.beginCommandNetworkPolicy(policyCtx, opts.NetworkAllowlist, opts.AuthorizeNetwork)
		if err != nil {
			return "", "", err
		}
		defer proxy.endCommandNetworkPolicy(commandPolicy)
	}
	connection := s.connection
	id, done, err := connection.prepare()
	if err != nil {
		return s.fail(err)
	}
	defer connection.abandon(id)
	req := sessionRequest{ID: id, Command: command, CWD: cwd, Env: opts.Env, Background: opts.Background, TimeoutSeconds: resolveTimeoutSeconds(opts)}
	if opts.Background {
		req.TimeoutSeconds = 0
	}
	if err := connection.write(req); err != nil {
		return s.fail(err)
	}
	if opts.Background {
		turnscope.From(ctx).Register(func() (string, error) {
			resp, err := connection.cancel(id)
			if err != nil {
				return fmt.Sprintf("Background request %d cleanup failed: %v", id, err), err
			}
			if resp.PID == 0 {
				return "", nil
			}
			return fmt.Sprintf("Stopped background job PID %d (request %d).", resp.PID, id), nil
		})
	}
	var got sessionReply
	select {
	case got = <-done:
	case <-ctx.Done():
		resp, cancelErr := connection.cancel(id)
		select {
		case result := <-done:
			if result.err == nil {
				resp.Stdout, resp.Stderr = result.resp.Stdout, result.resp.Stderr
			}
		default:
		}
		return resp.Stdout, resp.Stderr, errors.Join(ctx.Err(), cancelErr)
	}
	if got.err != nil {
		return s.fail(got.err)
	}
	resp := got.resp
	if resp.ID != id || resp.Type != "result" {
		return s.fail(fmt.Errorf("invalid supervisor response for request %d", id))
	}
	if len(resp.Stdout) > sandboxMaxOutput || len(resp.Stderr) > sandboxMaxOutput || len(resp.Error) > 64*1024 || !utf8.ValidString(resp.Stdout) || !utf8.ValidString(resp.Stderr) || !utf8.ValidString(resp.Error) {
		return s.fail(fmt.Errorf("invalid supervisor output"))
	}
	if resp.ExitCode < -1 || resp.ExitCode > 255 {
		return s.fail(fmt.Errorf("invalid supervisor exit status"))
	}
	if s.recreated {
		resp.Stdout = "[Session execution supervisor was recreated; previous background jobs and temporary state were lost.]\n" + resp.Stdout
		s.recreated = false
	}
	if s.proxy != nil {
		if target := s.proxy.takeBlocked(); target != "" {
			return resp.Stdout, resp.Stderr, &NetworkBlockedError{Target: target}
		}
	}
	if resp.Error != "" {
		return resp.Stdout, resp.Stderr, errors.New(resp.Error)
	}
	if resp.ExitCode != 0 {
		return resp.Stdout, resp.Stderr, fmt.Errorf("command exited with status %d", resp.ExitCode)
	}
	return resp.Stdout, resp.Stderr, nil
}

func (s *Session) fail(err error) (string, string, error) {
	_ = s.stopLocked()
	if s.tmpDir != "" {
		_ = os.RemoveAll(s.tmpDir)
		s.tmpDir = ""
	}
	s.failed = true
	return "", "", fmt.Errorf("session execution supervisor failed: %w", err)
}

func (s *Session) ensureStarted(cwd string) error {
	if s.closed {
		return fmt.Errorf("session execution supervisor is closed")
	}
	if !s.direct && s.cmd != nil && s.workspace != cwd {
		_ = s.stopLocked()
		s.failed = true
	}
	if s.cmd != nil {
		select {
		case <-s.waitDone:
			if s.cleanup != nil {
				s.cleanup()
				s.cleanup = nil
			}
			s.cmd, s.in, s.out, s.waitDone = nil, nil, nil, nil
			if s.tmpDir != "" {
				_ = os.RemoveAll(s.tmpDir)
				s.tmpDir = ""
			}
			s.failed = true
		default:
			if s.refresh == nil {
				return nil
			}
			changed, err := s.refresh()
			if err != nil {
				return err
			}
			if !changed {
				return nil
			}
			if err := s.stopLocked(); err != nil {
				return err
			}
			s.failed = true
		}
	}
	if s.failed {
		s.recreated = true
		s.failed = false
	}
	if s.direct || runtime.GOOS == "darwin" {
		dir, err := makeSandboxTempDir("caretz-session-exec-*")
		if err != nil {
			return err
		}
		s.tmpDir = dir
	}
	supervisor, err := s.runner.startSessionSupervisor(cwd, s.direct, s.tmpDir)
	if err != nil {
		if s.tmpDir != "" {
			_ = os.RemoveAll(s.tmpDir)
			s.tmpDir = ""
		}
		return err
	}
	s.cmd = supervisor.cmd
	s.workspace = cwd
	s.in = supervisor.in
	s.out = bufio.NewReader(supervisor.out)
	s.connection = newSessionConnection(s.in, s.out)
	s.cleanup = supervisor.cleanup
	s.proxy = supervisor.proxy
	s.refresh = supervisor.refresh
	s.waitDone = make(chan error, 1)
	go func() {
		waitErr := supervisor.cmd.Wait()
		supervisor.cleanup()
		s.waitDone <- waitErr
	}()
	return nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.stopLocked()
	if s.tmpDir != "" {
		if removeErr := os.RemoveAll(s.tmpDir); err == nil {
			err = removeErr
		}
		s.tmpDir = ""
	}
	return err
}

func (s *Session) stopLocked() error {
	if s.in != nil {
		_ = s.in.Close()
	}
	var err error
	if s.cmd != nil {
		select {
		case waitErr := <-s.waitDone:
			if waitErr != nil {
				err = waitErr
			}
		case <-time.After(7 * time.Second):
			if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL); err != nil {
				_ = s.cmd.Process.Kill()
			}
			<-s.waitDone
		}
	}
	if s.cleanup != nil {
		s.cleanup()
		s.cleanup = nil
	}
	s.cmd, s.in, s.out, s.waitDone = nil, nil, nil, nil
	s.refresh = nil
	return err
}

func writeSessionFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxSessionFrame {
		return fmt.Errorf("session frame too large")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if err = writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func readSessionFrame(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > maxSessionFrame {
		return fmt.Errorf("invalid session frame size %d", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8 session frame")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid trailing session frame data")
	}
	return nil
}

func init() {
	if os.Getenv(sessionSupervisorEnv) == "1" {
		runSessionSupervisor()
		os.Exit(0)
	}
}

func runSessionSupervisor() {
	cleanupPrepare, err := prepareSessionSupervisor()
	if err != nil {
		fmt.Fprintf(os.Stderr, "session supervisor prepare failed: %v\n", err)
		os.Exit(1)
	}
	defer cleanupPrepare()
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
	tmpDir := os.Getenv("TMPDIR")
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	jobs := map[int]*exec.Cmd{}
	var jobsMu sync.Mutex
	groups := map[int]struct{}{}
	var groupsMu sync.Mutex
	trackGroup := func(cmd *exec.Cmd) {
		if cmd.Process == nil {
			return
		}
		groupsMu.Lock()
		groups[cmd.Process.Pid] = struct{}{}
		groupsMu.Unlock()
	}
	forgetEmptyGroup := func(pgid int) {
		if err := syscall.Kill(-pgid, 0); err != syscall.ESRCH {
			return
		}
		groupsMu.Lock()
		delete(groups, pgid)
		groupsMu.Unlock()
	}
	active := map[uint64]*exec.Cmd{}
	requestGroups := map[uint64]int{}
	outputs := map[uint64][2]*synchronizedOutput{}
	canceled := map[uint64]bool{}
	var activeMu sync.Mutex
	var responseMu sync.Mutex
	respond := func(resp sessionResponse) {
		responseMu.Lock()
		_ = writeSessionFrame(os.Stdout, resp)
		responseMu.Unlock()
	}
	reaperCleanup := startPlatformReaper(func(pid int) bool {
		jobsMu.Lock()
		_, job := jobs[pid]
		jobsMu.Unlock()
		activeMu.Lock()
		defer activeMu.Unlock()
		for _, cmd := range active {
			if cmd.Process != nil && cmd.Process.Pid == pid {
				return true
			}
		}
		return job
	})
	defer reaperCleanup()
	cleanup := func() {
		groupsMu.Lock()
		list := make([]int, 0, len(groups))
		for pgid := range groups {
			list = append(list, pgid)
		}
		groupsMu.Unlock()
		if len(list) == 0 {
			return
		}
		for _, pgid := range list {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			remaining := 0
			for _, pgid := range list {
				if err := syscall.Kill(-pgid, 0); err != syscall.ESRCH {
					remaining++
				}
			}
			if remaining == 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, pgid := range list {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
	defer cleanup()
	r := bufio.NewReader(os.Stdin)
	for {
		var req sessionRequest
		if err := readSessionFrame(r, &req); err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				return
			}
			return
		}
		if req.CancelID != 0 {
			ack := sessionResponse{ID: req.ID, Type: "cancelled"}
			activeMu.Lock()
			cmd := active[req.CancelID]
			pgid := requestGroups[req.CancelID]
			if cmd == nil && pgid == 0 {
				canceled[req.CancelID] = true
			}
			if pgid != 0 {
				if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
					ack.Error = err.Error()
				} else {
					ack.PID = pgid
				}
			}
			if buffers, ok := outputs[req.CancelID]; ok {
				ack.Stdout = buffers[0].String()
				ack.Stderr = buffers[1].String()
			}
			activeMu.Unlock()
			respond(ack)
			continue
		}
		resp := sessionResponse{ID: req.ID, Type: "result", ExitCode: 0}
		if req.ID == 0 || req.Command == "" || req.CWD == "" {
			resp.Error = "invalid execution request"
			respond(resp)
			continue
		}
		cmd := exec.Command("/bin/bash", "-c", req.Command)
		cmd.Dir = req.CWD
		allowDocker := os.Getenv(sessionAllowDockerEnv) == "1"
		baseEnv := os.Environ()
		if os.Getenv(sessionDirectEnv) == "1" {
			baseEnv = sanitizeSessionEnv(baseEnv, allowDocker)
		} else {
			baseEnv = sanitizeSessionCommandEnv(baseEnv, allowDocker)
		}
		cmd.Env = mergeEnv(baseEnv, req.Env)
		cmd.Env = mergeEnv(cmd.Env, map[string]string{"TMPDIR": tmpDir})
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdin = nil
		cmd.ExtraFiles = nil
		if req.Background {
			jobsMu.Lock()
			active := len(jobs)
			jobsMu.Unlock()
			if active >= maxBackgroundJobs {
				resp.Error = "at most 8 background jobs may run concurrently"
				respond(resp)
				continue
			}
			processDir := filepath.Join(tmpDir, "caretz", "processes")
			if err := os.MkdirAll(processDir, 0700); err != nil {
				resp.Error = err.Error()
				respond(resp)
				continue
			}
			provisional, err := os.CreateTemp(processDir, ".starting-*")
			if err != nil {
				resp.Error = err.Error()
				respond(resp)
				continue
			}
			logWriter := &tailLogWriter{file: provisional, limit: backgroundLogLimit}
			cmd.Stdout, cmd.Stderr = logWriter, logWriter
			jobsMu.Lock()
			if err = cmd.Start(); err != nil {
				jobsMu.Unlock()
				_ = provisional.Close()
				_ = os.Remove(provisional.Name())
				resp.Error = err.Error()
				respond(resp)
				continue
			}
			trackGroup(cmd)
			pid := cmd.Process.Pid
			activeMu.Lock()
			requestGroups[req.ID] = pid
			activeMu.Unlock()
			logPath := filepath.Join(processDir, strconv.Itoa(pid)+".log")
			_ = os.Rename(provisional.Name(), logPath)
			jobs[pid] = cmd
			jobsMu.Unlock()
			go func(requestID uint64) {
				_ = cmd.Wait()
				forgetEmptyGroup(pid)
				activeMu.Lock()
				if syscall.Kill(-pid, 0) == syscall.ESRCH {
					delete(requestGroups, requestID)
				}
				activeMu.Unlock()
				_ = provisional.Close()
				jobsMu.Lock()
				delete(jobs, pid)
				jobsMu.Unlock()
			}(req.ID)
			resp.Stdout = fmt.Sprintf("Background command started.\nPID: %d\nLog: $TMPDIR/caretz/processes/%d.log\nStop: kill -TERM -- -%d", pid, pid, pid)
			respond(resp)
			continue
		}
		go func(req sessionRequest, cmd *exec.Cmd, resp sessionResponse) {
			var stdout, stderr synchronizedOutput
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			activeMu.Lock()
			if canceled[req.ID] {
				delete(canceled, req.ID)
				activeMu.Unlock()
				resp.Error = "command cancelled before starting"
				respond(resp)
				return
			}
			if err := cmd.Start(); err != nil {
				activeMu.Unlock()
				resp.Error = err.Error()
				respond(resp)
				return
			}
			trackGroup(cmd)
			active[req.ID] = cmd
			requestGroups[req.ID] = cmd.Process.Pid
			outputs[req.ID] = [2]*synchronizedOutput{&stdout, &stderr}
			wasCanceled := canceled[req.ID]
			delete(canceled, req.ID)
			activeMu.Unlock()
			if wasCanceled {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			timer := time.AfterFunc(time.Duration(req.TimeoutSeconds)*time.Second, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
			err := cmd.Wait()
			forgetEmptyGroup(cmd.Process.Pid)
			timedOut := !timer.Stop()
			activeMu.Lock()
			delete(active, req.ID)
			delete(outputs, req.ID)
			if syscall.Kill(-cmd.Process.Pid, 0) == syscall.ESRCH {
				delete(requestGroups, req.ID)
			}
			activeMu.Unlock()
			resp.Stdout, resp.Stderr = stdout.String(), stderr.String()
			if timedOut {
				resp.Error = fmt.Sprintf("command timed out after %d seconds", req.TimeoutSeconds)
			} else if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					resp.ExitCode = ee.ExitCode()
				} else {
					resp.Error = err.Error()
				}
			}
			respond(resp)
		}(req, cmd, resp)
	}
}

func sanitizeSessionEnv(env []string, allowDocker bool) []string {
	out := env[:0]
	for _, entry := range env {
		key, _, _ := bytes.Cut([]byte(entry), []byte("="))
		switch string(key) {
		case "DOCKER_HOST":
			if allowDocker {
				out = append(out, entry)
			}
			continue
		case sessionSupervisorEnv, sessionDirectEnv, sessionAllowDockerEnv, "_SANDBOX_CHILD", "_SANDBOX_CMD", "_SANDBOX_PROXY_SOCKET", "_SANDBOX_SOCKS_SOCKET",
			"OPENAI_API_KEY", "TAVILY_API_KEY", "BRAVE_SEARCH_API_KEY", "SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS",
			"DISPLAY", "WAYLAND_DISPLAY", "BASH_ENV", "ENV", "CDPATH", "IFS", "GLIBC_TUNABLES":
			continue
		}
		if bytes.HasPrefix(key, []byte("LD_")) || bytes.HasPrefix(key, []byte("DYLD_")) || bytes.HasPrefix(key, []byte("MALLOC_")) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// sanitizeSessionCommandEnv removes supervisor-only values from an environment
// that was already sanitized before the restricted supervisor started. Explicit
// grants must survive this second boundary so child commands can use them.
func sanitizeSessionCommandEnv(env []string, allowDocker bool) []string {
	out := env[:0]
	for _, entry := range env {
		key, _, _ := bytes.Cut([]byte(entry), []byte("="))
		switch string(key) {
		case "DOCKER_HOST":
			if allowDocker {
				out = append(out, entry)
			}
			continue
		case sessionSupervisorEnv, sessionDirectEnv, sessionAllowDockerEnv, "_SANDBOX_CHILD", "_SANDBOX_CMD", "_SANDBOX_PROXY_SOCKET", "_SANDBOX_SOCKS_SOCKET":
			continue
		}
		out = append(out, entry)
	}
	return out
}

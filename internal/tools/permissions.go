package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/makkalot/caretz/lib/llm"
)

const (
	PermissionPathRead  = "path_read"
	PermissionPathWrite = "path_write"
	PermissionNetwork   = "network"
	// PermissionSandboxOff lets one exact Bash command run outside the
	// sandbox. It is never added to the registry, so it cannot widen the
	// sandbox policy for other commands.
	PermissionSandboxOff = "sandbox_off"
)

var ErrPermissionApprovalUnavailable = errors.New("permission approval unavailable")

var permissionMutexInit sync.Mutex

// PermissionError identifies a policy denial that can be retried with a
// narrow, one-tool-call grant. Other errors must never be promoted to an
// approval request merely because their text contains "permission denied".
type PermissionError struct {
	Capability string
	Target     string
	Reason     string
	Command    string
	MayHaveRun bool
	Recursive  bool
	// Fallback, when set, is the tool's ordinary result. It is returned
	// instead of an error when the user declines or cannot be asked, so the
	// model sees the command's own outcome rather than the approval request.
	Fallback *llm.Message
}

func (e *PermissionError) Error() string {
	if e == nil {
		return "permission denied"
	}
	return fmt.Sprintf("access denied: %s (%s)", e.Target, e.Reason)
}

func AsPermissionError(err error) (*PermissionError, bool) {
	var permissionErr *PermissionError
	return permissionErr, errors.As(err, &permissionErr)
}

type permissionGrant struct {
	Capability string
	Target     string
	Recursive  bool
}

type permissionGrantsKey struct{}
type permissionRequesterKey struct{}
type sessionPermissionsKey struct{}

// SessionPermissionChecker reports whether the user already approved
// capability for target for the rest of the session.
type SessionPermissionChecker func(capability, target string) bool

func WithSessionPermissions(ctx context.Context, checker SessionPermissionChecker) context.Context {
	return context.WithValue(ctx, sessionPermissionsKey{}, checker)
}

func sessionPermissionGranted(ctx context.Context, capability, target string) bool {
	checker, _ := ctx.Value(sessionPermissionsKey{}).(SessionPermissionChecker)
	return checker != nil && checker(capability, target)
}

type PermissionRequester func(context.Context, llm.ToolCall, *PermissionError) (bool, error)

func WithPermissionRequester(ctx context.Context, requester PermissionRequester) context.Context {
	return context.WithValue(ctx, permissionRequesterKey{}, requester)
}

// PermissionRequesterAvailable reports whether someone can answer an approval
// request in ctx.
func PermissionRequesterAvailable(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	requester, _ := ctx.Value(permissionRequesterKey{}).(PermissionRequester)
	return requester != nil
}

func RequestPermission(ctx context.Context, call llm.ToolCall, permissionErr *PermissionError) (bool, error) {
	if ctx == nil {
		return false, nil
	}
	requester, _ := ctx.Value(permissionRequesterKey{}).(PermissionRequester)
	if requester == nil {
		return false, ErrPermissionApprovalUnavailable
	}
	return requester(ctx, call, permissionErr)
}

// networkAuthorizer keeps tool values while observing the proxy request's
// lifetime. HTTP server contexts do not inherit the tool's permission requester.
func networkAuthorizer(toolCtx context.Context, call llm.ToolCall, command string) func(context.Context, string) (bool, error) {
	return func(requestCtx context.Context, target string) (bool, error) {
		ctx, cancel := context.WithCancel(toolCtx)
		if deadline, ok := requestCtx.Deadline(); ok {
			cancel()
			ctx, cancel = context.WithDeadline(toolCtx, deadline)
		}
		stop := context.AfterFunc(requestCtx, cancel)
		defer func() { stop(); cancel() }()
		if err := requestCtx.Err(); err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return RequestPermission(ctx, call, &PermissionError{Capability: PermissionNetwork, Target: target, Reason: "sandbox network policy blocked destination", Command: command, MayHaveRun: true})
	}
}

// AddPermissionGrant adds a session-owned permission grant. It is safe to
// call while tools are executing. Clones are independent unless they opt in
// to live inheritance with WithLivePermissionsFrom.
func (r *Registry) AddPermissionGrant(capability, target string, recursive bool) error {
	if r == nil {
		return errors.New("nil tool registry")
	}
	capability = strings.TrimSpace(capability)
	target = strings.TrimSpace(target)
	if capability != PermissionPathRead && capability != PermissionPathWrite && capability != PermissionNetwork {
		return fmt.Errorf("invalid permission capability %q", capability)
	}
	if target == "" {
		return errors.New("permission target must not be empty")
	}
	if capability == PermissionNetwork {
		var err error
		target, err = canonicalNetworkTarget(target)
		if err != nil {
			return err
		}
	}
	mu := r.permissionMutex()
	mu.Lock()
	added := false
	if capability == PermissionNetwork {
		for _, existing := range r.NetworkAllowlist {
			if existing == target {
				mu.Unlock()
				return nil
			}
		}
		r.NetworkAllowlist = append(r.NetworkAllowlist, target)
		added = true
	} else if capability == PermissionPathRead {
		if !containsPath(r.ReadableRoots, target) {
			r.ReadableRoots = append(r.ReadableRoots, target)
			added = true
		}
	} else if !containsPath(r.WritableRoots, target) {
		r.WritableRoots = append(r.WritableRoots, target)
		added = true
		// Read-write access necessarily includes read access.
		if !containsPath(r.ReadableRoots, target) {
			r.ReadableRoots = append(r.ReadableRoots, target)
		}
	}
	mu.Unlock()
	if !added || capability == PermissionNetwork || r.executionState == nil {
		return nil
	}
	readable, writable, _ := r.permissionTargets()
	if _, err := r.executionState.addFilesystemRoots(readable, writable); err != nil {
		return fmt.Errorf("refresh persistent sandbox: %w", err)
	}
	return nil
}

func (r *Registry) permissionMutex() *sync.RWMutex {
	permissionMutexInit.Lock()
	defer permissionMutexInit.Unlock()
	if r.permissionMu == nil {
		r.permissionMu = &sync.RWMutex{}
	}
	return r.permissionMu
}

func canonicalNetworkTarget(target string) (string, error) {
	protocol := ""
	if strings.HasPrefix(strings.ToLower(target), "tcp://") {
		protocol = "tcp://"
		target = target[len(protocol):]
	}
	if strings.ContainsAny(target, " /\\") {
		return "", fmt.Errorf("invalid network target %q", target)
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("invalid network target %q: expected HOST:PORT", target)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid network target %q: port must be 1-65535", target)
	}
	return protocol + net.JoinHostPort(strings.ToLower(strings.TrimSuffix(host, ".")), strconv.Itoa(portNumber)), nil
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
		pathAbs, pathErr := filepath.Abs(path)
		targetAbs, targetErr := filepath.Abs(target)
		if pathErr == nil && targetErr == nil && canonicalApprovalPath(pathAbs) == canonicalApprovalPath(targetAbs) {
			return true
		}
	}
	return false
}

func (r *Registry) permissionGranted(capability, target string) bool {
	if r == nil {
		return false
	}
	read, write, network := r.permissionTargets()
	if capability == PermissionNetwork {
		for _, allowed := range network {
			if allowed == target {
				return true
			}
		}
		return false
	}
	roots := read
	if capability == PermissionPathWrite {
		roots = write
	}
	for _, root := range roots {
		if abs, err := filepath.Abs(root); err == nil {
			root = canonicalApprovalPath(abs)
		}
		if target == root || filepathHasPrefix(target, root) {
			return true
		}
	}
	return false
}

func (r *Registry) permissionTargets() (read, write, network []string) {
	if r == nil {
		return nil, nil, nil
	}
	mu := r.permissionMutex()
	mu.RLock()
	read = append([]string(nil), r.ReadableRoots...)
	write = append([]string(nil), r.WritableRoots...)
	network = append([]string(nil), r.NetworkAllowlist...)
	mu.RUnlock()
	if r.permissionSource != nil {
		parentRead, parentWrite, parentNetwork := r.permissionSource.permissionTargets()
		read = append(read, parentRead...)
		write = append(write, parentWrite...)
		network = append(network, parentNetwork...)
	}
	return read, write, network
}

func WithPermissionGrant(ctx context.Context, capability, target string, recursive bool) context.Context {
	grants, _ := ctx.Value(permissionGrantsKey{}).([]permissionGrant)
	copyOfGrants := append([]permissionGrant(nil), grants...)
	switch capability {
	case PermissionNetwork:
		if canonical, err := canonicalNetworkTarget(target); err == nil {
			target = canonical
		}
	case PermissionSandboxOff:
		recursive = false
	default:
		target = canonicalApprovalPath(target)
	}
	copyOfGrants = append(copyOfGrants, permissionGrant{Capability: capability, Target: target, Recursive: recursive})
	return context.WithValue(ctx, permissionGrantsKey{}, copyOfGrants)
}

func permissionGranted(ctx context.Context, capability, target string) bool {
	if ctx == nil {
		return false
	}
	if capability == PermissionNetwork {
		canonical, err := canonicalNetworkTarget(target)
		if err != nil {
			return false
		}
		target = canonical
	} else if capability != PermissionSandboxOff {
		target = canonicalApprovalPath(target)
	}
	grants, _ := ctx.Value(permissionGrantsKey{}).([]permissionGrant)
	for _, grant := range grants {
		if grant.Capability != capability {
			continue
		}
		root := grant.Target
		if capability != PermissionNetwork && capability != PermissionSandboxOff {
			root = filepath.Clean(root)
		}
		if target == root || grant.Recursive && filepathHasPrefix(target, root) {
			return true
		}
	}
	return false
}

func permissionTargets(ctx context.Context, capability string) []string {
	if ctx == nil {
		return nil
	}
	grants, _ := ctx.Value(permissionGrantsKey{}).([]permissionGrant)
	var targets []string
	for _, grant := range grants {
		if grant.Capability == capability {
			targets = append(targets, grant.Target)
		}
	}
	return targets
}

func filepathHasPrefix(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func outsideRootPermission(capability, path, reason string, recursive bool) error {
	if reason != "path outside allowed directory" {
		return fmt.Errorf("access denied: %s", reason)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("access denied: invalid path")
	}
	return &PermissionError{Capability: capability, Target: canonicalApprovalPath(abs), Reason: reason, Recursive: recursive}
}

func canonicalApprovalPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS != "darwin" {
		return path
	}
	for from, to := range map[string]string{"/var": "/private/var", "/tmp": "/private/tmp", "/etc": "/private/etc"} {
		if path == from {
			return to
		}
		if strings.HasPrefix(path, from+string(filepath.Separator)) {
			return to + strings.TrimPrefix(path, from)
		}
	}
	return path
}

func (r *Registry) outsideRootPermission(capability, path, reason string, recursive bool) error {
	abs, err := filepath.Abs(path)
	if err == nil {
		for _, hidden := range r.HiddenPaths {
			hiddenAbs, hiddenErr := filepath.Abs(hidden)
			if hiddenErr == nil && (abs == hiddenAbs || filepathHasPrefix(abs, hiddenAbs)) {
				return fmt.Errorf("access denied: protected path")
			}
		}
	}
	return outsideRootPermission(capability, path, reason, recursive)
}

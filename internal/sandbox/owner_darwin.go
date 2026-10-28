//go:build darwin

package sandbox

import (
	"sync"
	"syscall"
	"time"
)

// macOS hides other processes' environments from non-root callers, so one-shot
// commands are found by process group instead. A nohup or & job stays in its
// command's group after the command exits; a job that calls setsid leaves it
// and is not found.
var sandboxedGroups = struct {
	sync.Mutex
	pgids map[int]struct{}
}{pgids: map[int]struct{}{}}

// trackSandboxedGroup records the process group of a one-shot command that
// just started. Groups that have emptied are forgotten first, so a recycled
// group ID is rarely mistaken for one of ours.
func trackSandboxedGroup(pgid int) {
	sandboxedGroups.Lock()
	defer sandboxedGroups.Unlock()
	for known := range sandboxedGroups.pgids {
		if syscall.Kill(-known, 0) == syscall.ESRCH {
			delete(sandboxedGroups.pgids, known)
		}
	}
	sandboxedGroups.pgids[pgid] = struct{}{}
}

func terminateSandboxedProcesses(grace time.Duration) {
	live := func() []int {
		sandboxedGroups.Lock()
		defer sandboxedGroups.Unlock()
		var pgids []int
		for pgid := range sandboxedGroups.pgids {
			if syscall.Kill(-pgid, 0) == syscall.ESRCH {
				delete(sandboxedGroups.pgids, pgid)
				continue
			}
			pgids = append(pgids, pgid)
		}
		return pgids
	}
	terminateAll(live, func(pgid int, sig syscall.Signal) { _ = syscall.Kill(-pgid, sig) }, grace)
}

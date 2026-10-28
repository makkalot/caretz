package sandbox

import (
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// sandboxOwnerEnv tags sandboxed commands with the Caretz process that started
// them, where the platform can read it back (see owner_linux.go).
const sandboxOwnerEnv = "_CARETZ_SANDBOX_OWNER"

// oneShotLaunched records that this process started a one-shot sandboxed
// command, so exits that never ran one skip the cleanup scan.
var oneShotLaunched atomic.Bool

// TerminateSandboxedProcesses stops processes left running by one-shot
// sandboxed commands this Caretz process started, such as nohup jobs. Session
// supervisors clean up their own jobs when Caretz closes them or exits.
// Commands the user approved to run unsandboxed are left alone: they may
// outlive Caretz on purpose. It is best effort; see terminateSandboxedProcesses
// for what each platform can find.
func TerminateSandboxedProcesses() {
	if !oneShotLaunched.Load() {
		return
	}
	terminateSandboxedProcesses(2 * time.Second)
}

// TerminateSandboxedProcessesOnExit runs TerminateSandboxedProcesses when
// SIGHUP or SIGTERM ends Caretz, such as when its terminal is closed, and then
// lets the signal end the process as it would have. The returned function
// removes the handler and runs the cleanup for a normal exit.
func TerminateSandboxedProcessesOnExit() func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			TerminateSandboxedProcesses()
			signal.Reset(sig)
			_ = syscall.Kill(os.Getpid(), sig.(syscall.Signal))
		case <-done:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
		TerminateSandboxedProcesses()
	}
}

// terminateAll sends SIGTERM to every target found by list, waits up to grace
// for them to exit, then SIGKILLs what remains. list is rescanned between
// rounds so children forked while dying are caught too. An empty first scan is
// confirmed by a second one, since a process caught mid-exec can briefly hide
// what identifies it (on Linux, its environment reads back empty).
func terminateAll(list func() []int, kill func(int, syscall.Signal), grace time.Duration) {
	targets := list()
	if len(targets) == 0 {
		time.Sleep(50 * time.Millisecond)
		if targets = list(); len(targets) == 0 {
			return
		}
	}
	for _, target := range targets {
		kill(target, syscall.SIGTERM)
	}
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		if targets = list(); len(targets) == 0 {
			return
		}
	}
	for round := 0; round < 3 && len(targets) > 0; round++ {
		for _, target := range targets {
			kill(target, syscall.SIGKILL)
		}
		time.Sleep(20 * time.Millisecond)
		targets = list()
	}
}

//go:build linux

package sandbox

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"
)

// A one-shot bubblewrap command shares the host PID namespace, so a job it
// detaches with nohup or setsid outlives it with no process group or parent
// left to find it by. The inherited environment still identifies it.
var sandboxOwner = newSandboxOwner()

func newSandboxOwner() string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	return fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(nonce))
}

func sandboxOwnerEnvironment() map[string]string {
	return map[string]string{sandboxOwnerEnv: sandboxOwner}
}

// terminateSandboxedProcesses stops every process whose initial environment
// carries this Caretz's owner marker. A process that clears its environment
// before exec is no longer recognized.
func terminateSandboxedProcesses(grace time.Duration) {
	entry := sandboxOwnerEnv + "=" + sandboxOwner
	terminateAll(func() []int { return processesWithEnvironment(entry) }, func(pid int, sig syscall.Signal) { _ = syscall.Kill(pid, sig) }, grace)
}

// processesWithEnvironment lists processes whose initial environment contains
// entry. Processes Caretz may not inspect, and zombies, are skipped.
func processesWithEnvironment(entry string) []int {
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	want := []byte(entry)
	var pids []int
	for _, dir := range dirs {
		pid, err := strconv.Atoi(dir.Name())
		if err != nil || pid == self {
			continue
		}
		data, err := os.ReadFile("/proc/" + dir.Name() + "/environ")
		if err != nil {
			continue
		}
		for _, value := range bytes.Split(data, []byte{0}) {
			if bytes.Equal(value, want) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

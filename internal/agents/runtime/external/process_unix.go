//go:build !windows

package external

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// ConfigureProcess places an external CLI subprocess in its own process group
// so cancellation and teardown terminate grandchildren (MCP servers, shells).
func ConfigureProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			return KillProcessTree(cmd.Process)
		}
	}
}

// OwnProcessTree returns an idempotent cleanup function that terminates the
// started CLI process and its entire Unix process group.
func OwnProcessTree(process *os.Process) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = KillProcessTree(process)
		})
	}
}

// KillProcessTree terminates a started CLI process group on Unix.
func KillProcessTree(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	if err != nil {
		return process.Kill()
	}
	return nil
}

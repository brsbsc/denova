//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package builtin

import (
	"os"
	"os/exec"
	"syscall"
)

type processTree struct {
	pgid int
}

func configureProcessTree(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func startProcessTree(process *os.Process) *processTree {
	if process == nil || process.Pid <= 0 {
		return &processTree{}
	}
	return &processTree{pgid: process.Pid}
}

func (tree *processTree) terminate() {
	if tree != nil && tree.pgid > 0 {
		_ = syscall.Kill(-tree.pgid, syscall.SIGKILL)
	}
}

func (tree *processTree) cleanup() {
	tree.terminate()
}

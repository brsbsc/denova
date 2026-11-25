//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package builtin

import (
	"os"
	"os/exec"
)

type processTree struct {
	process *os.Process
}

func configureProcessTree(*exec.Cmd) {}

func startProcessTree(process *os.Process) *processTree {
	return &processTree{process: process}
}

func (tree *processTree) terminate() {
	if tree != nil && tree.process != nil {
		_ = tree.process.Kill()
	}
}

func (tree *processTree) cleanup() {
	tree.terminate()
}

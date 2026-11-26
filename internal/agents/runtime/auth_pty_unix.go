//go:build !windows

package agentruntime

import (
	"os/exec"
	"syscall"
)

// configureAuthPTYProcess places the interactive auth CLI in its own session
// with the slave PTY as its controlling terminal. Setting Setpgid alongside
// Setsid causes Linux forkExec to fail with EINVAL; Setsid already establishes
// pgid == pid so process-group teardown via external.OwnProcessTree works.
func configureAuthPTYProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}
}

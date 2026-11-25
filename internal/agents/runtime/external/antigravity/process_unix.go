//go:build !windows

package antigravity

import (
	"os/exec"

	"denova/internal/agents/runtime/external"
)

func configureProcess(cmd *exec.Cmd) {
	external.ConfigureProcess(cmd)
}

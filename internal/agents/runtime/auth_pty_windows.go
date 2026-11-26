//go:build windows

package agentruntime

import (
	"os/exec"

	"denova/internal/agents/runtime/external"
)

func configureAuthPTYProcess(cmd *exec.Cmd) {
	external.ConfigureProcess(cmd)
}

package codex

import (
	"os/exec"

	"denova/internal/agents/runtime/external"
)

func configureProcess(cmd *exec.Cmd) {
	external.ConfigureProcess(cmd)
}

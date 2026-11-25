//go:build !windows

package grok

import (
	"os/exec"

	"denova/internal/agents/runtime/external"
)

func configureProcess(cmd *exec.Cmd) {
	external.ConfigureProcess(cmd)
}

// verifyCommandLine is a no-op outside Windows: other platforms size their
// argument list per-argument rather than as one bounded command line.
func verifyCommandLine(string, []string) error { return nil }

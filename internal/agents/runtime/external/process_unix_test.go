//go:build !windows

package external

import "syscall"

func testProcessExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

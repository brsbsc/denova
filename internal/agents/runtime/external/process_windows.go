//go:build windows

package external

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createNoWindow = 0x08000000

// ConfigureProcess hides console windows, starts a new process group, and
// wires context-cancelled commands to tree termination.
func ConfigureProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			return KillProcessTree(cmd.Process)
		}
	}
}

// OwnProcessTree binds a started CLI process to a kill-on-close Windows Job
// Object so aborting or closing terminates all descendant processes even if
// the root CLI process has already exited.
func OwnProcessTree(process *os.Process) func() {
	if process == nil || process.Pid <= 0 {
		return func() {}
	}
	job, err := assignKillOnCloseJob(process)
	var once sync.Once
	return func() {
		once.Do(func() {
			if err == nil {
				_ = windows.TerminateJobObject(job, 1)
				_ = windows.CloseHandle(job)
				return
			}
			_ = KillProcessTree(process)
		})
	}
}

func assignKillOnCloseJob(process *os.Process) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// KillProcessTree terminates a started CLI process and any surviving children
// on Windows when a Job Object handle is not already owned by the caller.
func KillProcessTree(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return os.ErrProcessDone
	}
	kill := exec.Command("taskkill.exe", "/T", "/F", "/PID", fmt.Sprintf("%d", process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	kill.Stdout, kill.Stderr = io.Discard, io.Discard
	if kill.Run() != nil {
		return process.Kill()
	}
	return nil
}

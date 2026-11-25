//go:build windows

package builtin

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitBreakawayOK              = 0x00000800
	jobObjectLimitKillOnJobClose           = 0x00002000
	processTerminate                       = 0x0001
	processSetQuota                        = 0x0100
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW           = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject    = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject   = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject         = kernel32.NewProc("TerminateJobObject")
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type processTree struct {
	mu       sync.Mutex
	process  *os.Process
	job      syscall.Handle
	assigned bool
	closed   bool
}

func configureProcessTree(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// startProcessTree binds the freshly started command to a Windows Job Object
// configured with KILL_ON_JOB_CLOSE so surviving grandchildren are terminated
// by the kernel even after the direct shell process exits and is reaped.
func startProcessTree(process *os.Process) *processTree {
	tree := &processTree{process: process}
	if process == nil || process.Pid <= 0 {
		return tree
	}
	job, assigned := assignProcessToKillJob(uint32(process.Pid))
	tree.job = job
	tree.assigned = assigned
	return tree
}

func assignProcessToKillJob(pid uint32) (syscall.Handle, bool) {
	procHandle, err := syscall.OpenProcess(processSetQuota|processTerminate, false, pid)
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(procHandle)

	rawJob, _, _ := procCreateJobObjectW.Call(0, 0)
	if rawJob == 0 {
		return 0, false
	}
	job := syscall.Handle(rawJob)
	var limits jobObjectExtendedLimitInformation
	limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose | jobObjectLimitBreakawayOK
	ok, _, _ := procSetInformationJobObject.Call(
		uintptr(job),
		jobObjectExtendedLimitInformationClass,
		uintptr(unsafe.Pointer(&limits)),
		unsafe.Sizeof(limits),
	)
	if ok == 0 {
		_ = syscall.CloseHandle(job)
		return 0, false
	}
	ok, _, _ = procAssignProcessToJobObject.Call(uintptr(job), uintptr(procHandle))
	if ok == 0 {
		_ = syscall.CloseHandle(job)
		return 0, false
	}
	return job, true
}

func (tree *processTree) terminate() {
	if tree == nil {
		return
	}
	tree.mu.Lock()
	job := tree.job
	assigned := tree.assigned
	process := tree.process
	tree.mu.Unlock()

	if assigned && job != 0 {
		_, _, _ = procTerminateJobObject.Call(uintptr(job), 1)
	}
	if !assigned && process != nil {
		killProcessTreeByPID(process)
	} else if process != nil {
		_ = process.Kill()
	}
}

func (tree *processTree) cleanup() {
	if tree == nil {
		return
	}
	tree.mu.Lock()
	if tree.closed {
		tree.mu.Unlock()
		return
	}
	tree.closed = true
	job := tree.job
	assigned := tree.assigned
	process := tree.process
	tree.job = 0
	tree.mu.Unlock()

	if job != 0 {
		_, _, _ = procTerminateJobObject.Call(uintptr(job), 1)
		_ = syscall.CloseHandle(job)
		return
	}
	if !assigned && process != nil {
		killProcessTreeByPID(process)
	}
}

func killProcessTreeByPID(process *os.Process) {
	if process == nil {
		return
	}
	command := exec.Command("taskkill.exe", "/T", "/F", "/PID", fmt.Sprintf("%d", process.Pid))
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if command.Run() != nil {
		_ = process.Kill()
	}
}

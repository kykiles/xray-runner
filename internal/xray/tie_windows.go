package xray

import (
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareChild(*exec.Cmd) {}

// killJob is a job object that ends every process in it once its last handle
// closes. The handle is ours for the life of the process and never closed by
// hand: Windows closes it when we exit, however we exit, and takes the cores
// with it.
var killJob = sync.OnceValues(func() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { //nolint:gosec // G103: the API takes the struct by address and size
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
})

// adoptChild puts the core into killJob. Best-effort: a core outside the job
// still runs, and our own teardown still stops it on an orderly exit.
func adoptChild(p *os.Process) {
	job, err := killJob()
	if err != nil {
		slog.Warn("job object для ядра не создан: при аварийном выходе ядро останется", "error", err)
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		slog.Warn("ядро не привязано к программе", "pid", p.Pid, "error", err)
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		slog.Warn("ядро не привязано к программе", "pid", p.Pid, "error", err)
	}
}
